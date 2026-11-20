// Package testutil builds throwaway remotes and stores for tests.
package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/store"
)

// Lead is the actor that creates test stores; it holds the "lead" role.
var Lead = model.Actor{Email: "lead@example.com", Kind: model.KindHuman}

// BareRemote creates an empty bare repository and returns its path.
func BareRemote(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.Command("git", "init", "-q", "--bare", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return dir
}

// NewStore sets up a local store named project against remote. DUMEN_HOME must already
// point at a temporary directory (see Home).
func NewStore(t testing.TB, remote, project string) *store.Store {
	t.Helper()
	s, _, err := store.Setup(context.Background(), remote, store.DefaultBranch, project, true, Lead)
	if err != nil {
		t.Fatalf("store.Setup: %v", err)
	}
	return s
}

// Home points DUMEN_HOME at a fresh temporary directory for the duration of the test.
func Home(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DUMEN_HOME", dir)
	return dir
}

// MoveAway renames path so the remote becomes unreachable, and returns a function that restores it.
func MoveAway(t testing.TB, path string) (restore func()) {
	t.Helper()
	hidden := path + ".offline"
	if err := os.Rename(path, hidden); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Rename(hidden, path); err != nil {
			t.Fatal(err)
		}
	}
}
