package policy

import (
	"testing"
	"time"

	"github.com/nurettincoban/comm-pro/internal/model"
)

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"src/auth/**", "src/auth/login.ts", true},
		{"src/auth/**", "src/auth/oauth/google.ts", true},
		{"src/auth/**", "src/authz/x.ts", false},
		{"src/auth", "src/auth/login.ts", true},
		{"src/auth/", "src/auth", true},
		{"src/auth", "src/authz/x.ts", false},
		{"**/*.sql", "db/migrations/001.sql", true},
		{"**/*.sql", "001.sql", true},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"src/*/api", "src/orders/api/handler.go", true},
		{"src/auth/**", "src\\auth\\login.ts", true},
		{"src/auth/**", "src/auth/../auth/login.ts", true},
		{"src/auth/**", "src/other/../auth/x", true},
		{"db/migrations", "db/migrations.md", false},
	}
	for _, c := range cases {
		if got := MatchPath(c.pattern, c.file); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
}

func TestDenyFor(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	cfg := model.DefaultConfig("p")
	cfg.Areas = map[string]model.Area{"billing": {Paths: []string{"src/billing"}}}
	mk := func(id, kind string, sc model.Scope, status string, exp time.Time) model.Directive {
		return model.Directive{ID: id, Kind: kind, Scope: sc, Status: status, PublishedBy: "pm@x.io",
			Starts: model.FormatTime(now.Add(-time.Hour)), Expires: model.FormatTime(exp), Text: id}
	}
	ds := []model.Directive{
		mk("Y-0001", model.DirectiveDeny, model.Scope{Paths: []string{"src/auth/**"}}, model.DirectiveActive, now.Add(time.Hour)),
		mk("Y-0002", model.DirectiveDeny, model.Scope{Area: "billing"}, model.DirectiveActive, now.Add(time.Hour)),
		mk("Y-0003", model.DirectiveDeny, model.Scope{Paths: []string{"docs"}}, model.DirectiveWithdrawn, now.Add(time.Hour)),
		mk("Y-0004", model.DirectiveDeny, model.Scope{Paths: []string{"old"}}, model.DirectiveActive, now.Add(-time.Minute)),
		mk("Y-0005", model.DirectiveContext, model.Scope{Paths: []string{"src/api"}}, model.DirectiveActive, now.Add(time.Hour)),
	}
	want := map[string]string{
		"src/auth/login.ts":  "Y-0001",
		"src/billing/tax.go": "Y-0002",
		"docs/x.md":          "",
		"old/x":              "",
		"src/api/handler.go": "",
		"src/other/thing.go": "",
	}
	for file, id := range want {
		d, ok := DenyFor(cfg, ds, file, now)
		if (id == "") == ok || d.ID != id {
			t.Errorf("DenyFor(%q) = %q %v, want %q", file, d.ID, ok, id)
		}
	}
	role := mk("Y-0006", model.DirectiveContext, model.Scope{Role: "backend"}, model.DirectiveActive, now.Add(time.Hour))
	if !Delivered(role, "backend", now) || Delivered(role, "frontend", now) || !Delivered(ds[0], "frontend", now) || Delivered(ds[3], "pm", now) {
		t.Error("Delivered is wrong")
	}
}
