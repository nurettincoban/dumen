package check

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/cache"
	"github.com/nurettincoban/dumen/internal/model"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=dev@example.com", "-c", "user.name=Dev"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(rel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Changed files are reported relative to the project root, also when the project is a
// subdirectory of the git work tree, and every kind of change is seen.
func TestChangedFiles(t *testing.T) {
	top := t.TempDir()
	git(t, top, "init", "-q")
	for _, f := range []string{"svc/src/auth/login.go", "svc/src/auth/gone.go", "svc/src/api/a.go", "other/x.go"} {
		write(t, top, f)
	}
	git(t, top, "add", "-A")
	git(t, top, "commit", "-q", "-m", "one")
	root := filepath.Join(top, "svc")
	ctx := context.Background()

	write(t, top, "svc/src/auth/login.go ") // a new untracked file with a space in its name
	os.WriteFile(filepath.Join(root, "src/api/a.go"), []byte("changed\n"), 0o644)
	os.Remove(filepath.Join(root, "src/auth/gone.go"))
	write(t, top, "other/y.go") // outside the project root
	got, err := WorktreeFiles(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"src/api/a.go", "src/auth/gone.go", "src/auth/login.go "}; !reflect.DeepEqual(got, want) {
		t.Errorf("work tree: got %q, want %q", got, want)
	}

	git(t, top, "mv", "svc/src/auth/login.go", "svc/src/auth/signin.go")
	git(t, top, "add", "-A")
	git(t, top, "commit", "-q", "-m", "two")
	got, err = DiffFiles(ctx, root, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// Both sides of the rename are listed, so moving a file out of a frozen path is seen.
	if want := []string{"src/api/a.go", "src/auth/gone.go", "src/auth/login.go", "src/auth/login.go ", "src/auth/signin.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("diff: got %q, want %q", got, want)
	}
	if _, err := DiffFiles(ctx, root, "--output=/tmp/x", "HEAD"); err == nil {
		t.Error("a revision that looks like an option must be an error")
	}
}

// Gauntlet P1: a file whose name contains a newline is still found under a glob.
func TestViolationsWithANewlineInTheName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file names cannot contain a newline on Windows")
	}
	top := t.TempDir()
	git(t, top, "init", "-q")
	write(t, top, "src/auth/a\nb.go")
	files, err := WorktreeFiles(context.Background(), top)
	if err != nil {
		t.Fatal(err)
	}
	c := &cache.Cache{Directives: []cache.Directive{{Directive: model.Directive{ID: "Y-AAAA", Kind: model.DirectiveDeny,
		Status: model.DirectiveActive, Scope: model.Scope{Paths: []string{"src/auth/**"}},
		Starts: model.FormatTime(time.Now().Add(-time.Hour)), Expires: model.FormatTime(time.Now().Add(time.Hour))}}}}
	if vs := Violations(c, "", files, time.Now()); len(vs) != 1 || vs[0].File != "src/auth/a\nb.go" {
		t.Fatalf("files %q, violations %+v", files, vs)
	}
}
