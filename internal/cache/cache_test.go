package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/store"
)

// Gauntlet N2. A broken dumen.yml must not switch the deny guard off: the cache is still
// built, without roles and areas, and the directives in force stay enforced.
func TestBrokenConfigKeepsDirectivesEnforced(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	d := model.Directive{ID: "Y-4HTR", Kind: model.DirectiveDeny, Status: model.DirectiveActive,
		Scope: model.Scope{Paths: []string{"src/auth/**"}}, Text: "Auth is frozen",
		Starts: model.FormatTime(now.Add(-time.Minute)), Expires: model.FormatTime(now.Add(time.Hour))}
	if _, err := store.WriteDirective(root, d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, store.ConfigFile), []byte("roles: [this is not a map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Build(root, "head", now)
	if err != nil {
		t.Fatalf("a broken dumen.yml must not fail the cache: %v", err)
	}
	if c.ConfigError == "" {
		t.Error("the config error must be reported")
	}
	if _, blocked := c.DenyFor("src/auth/login.go", now); !blocked {
		t.Error("the deny directive is no longer enforced")
	}
}
