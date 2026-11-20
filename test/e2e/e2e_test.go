//go:build e2e

// Package e2e holds acceptance test 3: real Claude Code in headless mode (`claude -p`)
// against the real dumen binary. It needs a logged-in `claude` on PATH and spends a few
// model calls, so it is behind the `e2e` build tag and never runs in CI:
//
//	go test -tags e2e ./test/e2e -count=1 -v
package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

type world struct {
	t    *testing.T
	bin  string // dumen binary
	repo string // the team's code repository (working tree)
	env  []string
}

func model() string {
	if m := os.Getenv("DUMEN_E2E_MODEL"); m != "" {
		return m
	}
	return "claude-haiku-5-5"
}

func setup(t *testing.T) *world {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Skip("e2e tests are local-only for now")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude is not on PATH")
	}
	tmp := t.TempDir()
	w := &world{t: t, repo: filepath.Join(tmp, "repo")}
	binDir := filepath.Join(tmp, "bin")
	w.bin = filepath.Join(binDir, "dumen")
	if runtime.GOOS == "windows" {
		w.bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", w.bin, "github.com/nurettincoban/comm-pro/cmd/dumen").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	// The human's environment: no CLAUDECODE marker, dumen on PATH, its own dumen home.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDECODE=") && !strings.HasPrefix(kv, "DUMEN_HOME=") && !strings.HasPrefix(kv, "PATH=") &&
			!strings.HasPrefix(kv, "CLAUDE_PROJECT_DIR=") {
			w.env = append(w.env, kv)
		}
	}
	w.env = append(w.env, "DUMEN_HOME="+filepath.Join(tmp, "dumen-home"),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	remote := filepath.Join(tmp, "remote.git")
	w.sh(tmp, "git", "init", "-q", "--bare", remote)
	w.sh(tmp, "git", "init", "-q", w.repo)
	w.sh(w.repo, "git", "config", "user.email", "lead@example.com")
	w.sh(w.repo, "git", "config", "user.name", "Lead")
	w.sh(w.repo, "git", "remote", "add", "origin", remote)
	w.write("src/auth/login.ts", "export const login = () => true;\n")
	w.write("src/api/orders.ts", "export const orders = () => [];\n")
	w.sh(w.repo, "git", "add", "-A")
	w.sh(w.repo, "git", "commit", "-q", "-m", "init")
	w.sh(w.repo, "git", "push", "-q", "origin", "HEAD")
	w.dumen("init")
	w.dumen("install")
	return w
}

func (w *world) sh(dir, name string, args ...string) string {
	w.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, w.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (w *world) dumen(args ...string) string { return w.sh(w.repo, w.bin, args...) }

func (w *world) write(rel, content string) {
	w.t.Helper()
	p := filepath.Join(w.repo, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) read(rel string) string {
	data, _ := os.ReadFile(filepath.Join(w.repo, filepath.FromSlash(rel)))
	return string(data)
}

// claude runs one headless session in the repository and returns its final text.
func (w *world) claude(prompt string, extra ...string) string {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	args := append([]string{"-p", prompt, "--model", model(), "--permission-mode", "acceptEdits"}, extra...)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir, cmd.Env = w.repo, w.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		w.t.Fatalf("claude -p: %v\nstdout: %s\nstderr: %s", err, out.String(), errb.String())
	}
	w.t.Logf("claude said: %s", strings.TrimSpace(out.String()))
	return out.String()
}

var idRe = regexp.MustCompile(`[DYR]-[0-9A-HJKMNP-TV-Z]{4}`)

func firstID(s string) string { return idRe.FindString(s) }

// activity returns the raw activity log from a fresh sync of the store.
func (w *world) activity() string {
	w.t.Helper()
	// Hooks push in a detached process; give it a moment, then sync explicitly.
	time.Sleep(2 * time.Second)
	w.dumen("sync")
	var b strings.Builder
	home := ""
	for _, kv := range w.env {
		if strings.HasPrefix(kv, "DUMEN_HOME=") {
			home = strings.TrimPrefix(kv, "DUMEN_HOME=")
		}
	}
	filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".jsonl") && strings.Contains(filepath.ToSlash(p), "/store/activity/") {
			data, _ := os.ReadFile(p)
			b.Write(data)
		}
		return nil
	})
	return b.String()
}

// (a) An active deny directive blocks an Edit.
func TestDenyDirectiveBlocksEdit(t *testing.T) {
	w := setup(t)
	id := firstID(w.dumen("direct", "--deny", "--paths", "src/auth/**", "--for", "1d", "Release week: do not touch src/auth"))
	if id == "" {
		t.Fatal("no directive id")
	}
	before := w.read("src/auth/login.ts")
	out := w.claude("Use the Edit tool to append the line `// touched` to the end of src/auth/login.ts. " +
		"Try exactly once. Do not use Bash or any other tool to change the file. " +
		"Then tell me in one sentence what happened, quoting any directive ID you were given.")
	if got := w.read("src/auth/login.ts"); got != before {
		t.Fatalf("the file under the deny directive was changed:\n%s", got)
	}
	if !strings.Contains(out, id) {
		t.Errorf("the agent did not report the blocking directive %s", id)
	}
	log := w.activity()
	if !strings.Contains(log, `"kind":"blocked"`) || !strings.Contains(log, id) {
		t.Errorf("no blocked event for %s in the activity log:\n%s", id, log)
	}
	for _, kind := range []string{"started", "finished"} {
		if !strings.Contains(log, `"kind":"`+kind+`"`) {
			t.Errorf("no %s event in the activity log:\n%s", kind, log)
		}
	}

	// The same edit outside the frozen path goes through.
	w.claude("Use the Edit tool to append the line `// touched` to the end of src/api/orders.ts. Then say done.")
	if !strings.Contains(w.read("src/api/orders.ts"), "// touched") {
		t.Error("an edit outside the deny scope should have succeeded")
	}
}

// (d) SessionStart injects directives, and they come first.
func TestSessionStartInjectsDirectivesFirst(t *testing.T) {
	w := setup(t)
	a := firstID(w.dumen("direct", "--all", "--for", "1d", "All new endpoints use requireAuth"))
	b := firstID(w.dumen("direct", "--deny", "--paths", "db/migrations", "--for", "1d", "Migration freeze"))
	out := w.claude("Do not use any tools. At the start of this session you were given a block of dumen team records. " +
		"Reply with the heading of the FIRST section in that block (the line in capital letters), " +
		"then list every directive ID from it, one per line. Nothing else.")
	for _, id := range []string{a, b} {
		if !strings.Contains(out, id) {
			t.Errorf("directive %s did not reach the session", id)
		}
	}
	if !strings.Contains(out, "ACTIVE DIRECTIVES") {
		t.Errorf("the first section of the injected block should be ACTIVE DIRECTIVES")
	}
}
