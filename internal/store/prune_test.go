package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneSessions(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, n := range []string{"a.json", "a.approved", "b.json", "seen-web", "keep.approved"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	for _, n := range []string{"a.json", "a.approved", "seen-web"} {
		os.Chtimes(filepath.Join(dir, n), old, old)
	}
	pruneSessions(dir, time.Now().Add(-sessionKeep))
	for n, want := range map[string]bool{"a.json": false, "a.approved": false, "b.json": true, "seen-web": true, "keep.approved": true} {
		if _, err := os.Stat(filepath.Join(dir, n)); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", n, err == nil, want)
		}
	}
}
