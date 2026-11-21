package store_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/testutil"
)

func tryLock(t *testing.T, lock func(context.Context) (func(), error), wait time.Duration) (func(), bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	unlock, err := lock(ctx)
	return unlock, err == nil
}

// Gauntlet O2: a lock whose holder died is taken over at once, a lock a live process
// keeps fresh is respected, and a git lock file left by a killed git is cleared.
func TestLockTakeoverAndStaleGitLocks(t *testing.T) {
	testutil.Home(t)
	s := testutil.NewStore(t, testutil.BareRemote(t), "proj")
	path := filepath.Join(s.Home, "lock")

	// A process that has exited: its pid is free.
	dead := exec.Command("git", "version")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(fmt.Sprintf("%d L-DEAD", dead.Process.Pid)), 0o644)
	index := filepath.Join(s.Root(), ".git", "index.lock")
	os.WriteFile(index, nil, 0o644) // as a killed git leaves it
	start := time.Now()
	unlock, ok := tryLock(t, s.Lock, 5*time.Second)
	if !ok || time.Since(start) > 2*time.Second {
		t.Fatalf("the lock of a dead process must be taken over at once (ok=%v after %v)", ok, time.Since(start))
	}
	if _, err := os.Stat(index); err == nil {
		t.Error("a stale index.lock must be removed by whoever takes the store lock")
	}
	if _, err := s.Sync(context.Background()); err != nil {
		t.Errorf("the store must work after a stale index.lock: %v", err)
	}

	// While it is held by this (live) process, nobody else gets it.
	if _, ok := tryLock(t, s.Lock, 200*time.Millisecond); ok {
		t.Fatal("a held lock was handed out twice")
	}
	unlock()
	unlock() // a second unlock is harmless
	if _, err := os.Stat(path); err == nil {
		t.Fatal("unlock left the lock file behind")
	}

	// A live pid with a fresh lock is respected; the same lock gone old is not (the pid
	// was reused by some other program, which never touches the file).
	os.WriteFile(path, []byte(fmt.Sprintf("%d L-LIVE", os.Getpid())), 0o644)
	if _, ok := tryLock(t, s.Lock, 200*time.Millisecond); ok {
		t.Fatal("a fresh lock of a live process was taken over")
	}
	old := time.Now().Add(-3 * time.Minute)
	os.Chtimes(path, old, old)
	unlock, ok = tryLock(t, s.Lock, 2*time.Second)
	if !ok {
		t.Fatal("a lock nobody has touched for minutes must be taken over")
	}
	// Unlock removes only the caller's own lock.
	os.WriteFile(path, []byte("1 L-OTHER"), 0o644)
	unlock()
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "L-OTHER") {
		t.Error("unlock removed a lock that belongs to another holder")
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
