package store_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/store"
	"github.com/nurettincoban/dumen/internal/testutil"
)

func tryLock(t *testing.T, lock func(context.Context) (func(), error), wait time.Duration) (func(), bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	unlock, err := lock(ctx)
	return unlock, err == nil
}

// Gauntlet O2 and L1: the store lock is an operating system lock. A lock file left by a
// process that died holds nothing, a held lock is never handed out twice, and a git lock
// file left by a killed git is cleared.
func TestLockIsHeldByTheKernelAndClearsStaleGitLocks(t *testing.T) {
	testutil.Home(t)
	s := testutil.NewStore(t, testutil.BareRemote(t), "proj")
	path := filepath.Join(s.Home, "lock")

	// What a killed dumen leaves: the file with its pid, and nobody holding it. Even a pid
	// that is alive (reused by another program) must not matter.
	os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
	index := filepath.Join(s.Root(), ".git", "index.lock")
	os.WriteFile(index, nil, 0o644) // as a killed git leaves it
	start := time.Now()
	unlock, ok := tryLock(t, s.Lock, 5*time.Second)
	if !ok || time.Since(start) > time.Second {
		t.Fatalf("a lock file nobody holds must be taken at once (ok=%v after %v)", ok, time.Since(start))
	}
	if _, err := os.Stat(index); err == nil {
		t.Error("a stale index.lock must be removed by whoever takes the store lock")
	}
	if _, err := s.Sync(context.Background()); err != nil {
		t.Errorf("the store must work after a stale index.lock: %v", err)
	}
	if data, _ := os.ReadFile(path); strings.TrimSpace(string(data)) != fmt.Sprint(os.Getpid()) {
		t.Errorf("the lock file should name its holder, got %q", data)
	}

	// While it is held nobody else gets it, however old the file looks.
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	if _, ok := tryLock(t, s.Lock, 200*time.Millisecond); ok {
		t.Fatal("a held lock was handed out twice")
	}
	unlock()
	unlock() // a second unlock is harmless
	unlock, ok = tryLock(t, s.Lock, time.Second)
	if !ok {
		t.Fatal("a released lock could not be taken again")
	}
	unlock()
}

// Gauntlet L1: the lock dies with its holder. A child process takes the lock and is
// killed; the parent gets it at once.
func TestLockIsReleasedWhenTheHolderIsKilled(t *testing.T) {
	if os.Getenv("DUMEN_TEST_HOLD_LOCK") != "" {
		s, err := store.Open("proj")
		if err != nil {
			os.Exit(3)
		}
		if _, err := s.Lock(context.Background()); err != nil {
			os.Exit(4)
		}
		fmt.Println("held")
		time.Sleep(time.Minute)
		return
	}
	home := testutil.Home(t)
	s := testutil.NewStore(t, testutil.BareRemote(t), "proj")
	child := exec.Command(os.Args[0], "-test.run", "^TestLockIsReleasedWhenTheHolderIsKilled$")
	child.Env = append(os.Environ(), "DUMEN_TEST_HOLD_LOCK=1")
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(out, buf); err != nil || string(buf[:4]) != "held" {
		t.Fatalf("child did not take the lock (home %s): %q %v", home, buf, err)
	}
	if _, ok := tryLock(t, s.Lock, 200*time.Millisecond); ok {
		t.Fatal("the lock of a live process was handed out")
	}
	child.Process.Kill()
	child.Wait()
	unlock, ok := tryLock(t, s.Lock, 2*time.Second)
	if !ok {
		t.Fatal("the lock of a killed process was not released")
	}
	unlock()
}

// Gauntlet L2: setting a store up again (dumen join in a second repository) waits for
// whoever is working in the clone.
func TestSetupWaitsForTheStoreLock(t *testing.T) {
	testutil.Home(t)
	remote := testutil.BareRemote(t)
	s := testutil.NewStore(t, remote, "proj")
	unlock, ok := tryLock(t, s.Lock, time.Second)
	if !ok {
		t.Fatal("setup: lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, _, err := store.Setup(ctx, remote, s.Branch, "proj", false, model.Actor{Email: "dev@example.com"}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("Setup must wait for the store lock, got %v", err)
	}
	unlock()
	if _, _, err := store.Setup(context.Background(), remote, s.Branch, "proj", false, model.Actor{Email: "dev@example.com"}); err != nil {
		t.Fatalf("Setup after the lock is free: %v", err)
	}
}

// Gauntlet O3: the clone's loose objects are packed, at most once a day.
func TestMaintainPacksLooseObjects(t *testing.T) {
	testutil.Home(t)
	s := testutil.NewStore(t, testutil.BareRemote(t), "proj")
	for i := 0; i < 40; i++ { // every record dumen writes is a commit
		cmd := exec.Command("git", "-c", "user.email=dev@example.com", "-c", "user.name=Dev", "commit", "-q", "--allow-empty", "-m", fmt.Sprintf("event %d", i))
		cmd.Dir = s.Root()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %v\n%s", err, out)
		}
	}
	loose := func() int {
		out, _ := exec.Command("git", "-C", s.Root(), "count-objects").Output()
		var n int
		fmt.Sscanf(string(out), "%d objects", &n)
		return n
	}
	before := loose()
	if before < 40 {
		t.Fatalf("setup: %d loose objects", before)
	}
	if err := s.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := loose(); after != 0 {
		t.Errorf("loose objects: %d before, %d after", before, after)
	}
	marker := filepath.Join(s.Home, "maintained")
	st, err := os.Stat(marker)
	if err != nil {
		t.Fatal("no marker after maintenance")
	}
	if err := s.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(marker); !st2.ModTime().Equal(st.ModTime()) {
		t.Error("maintenance ran twice within a day")
	}
	if _, err := s.Sync(context.Background()); err != nil {
		t.Errorf("the store must work after maintenance: %v", err)
	}
}
