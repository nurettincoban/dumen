package policy

import (
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
)

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"src/auth/**", "src/auth/login.ts", true},
		// Gauntlet P1: a newline in a file name must not take the file out of a glob.
		{"src/auth/**", "src/auth/a\nb.go", true},
		{"src/**/secrets.*", "src/x\ny/secrets.a\nb", true},
		{"src/*.go", "src/a\nb.go", true},
		{"src/auth/**", "src/web/a\nsrc/auth/b.go", false},
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
		return model.Directive{ID: id, Kind: kind, Scope: sc, Status: status, PublishedBy: "pm@example.com",
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
	if !Delivered(cfg, role, "backend", "", now) || Delivered(cfg, role, "frontend", "", now) || !Delivered(cfg, ds[0], "frontend", "", now) || Delivered(cfg, ds[3], "pm", "", now) {
		t.Error("Delivered is wrong")
	}
}

func TestRepoQualifiedPaths(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"billing-svc:src/auth/**", "billing-svc:src/auth/login.go", true},
		{"billing-svc:src/auth/**", "orders-svc:src/auth/login.go", false},
		{"billing-svc:src/auth/**", "src/auth/login.go", false}, // a single-repo session is not that repository
		{"src/auth/**", "billing-svc:src/auth/login.go", true},  // no prefix: every repository
		{"*:src/auth", "orders-svc:src/auth/x.go", true},
		{"billing-svc:", "billing-svc:anything/at/all.go", true},
		{"billing-svc:**", "billing-svc:a.go", true},
		{"Billing-Svc:src", "billing-svc:src/a.go", true},
		{"billing-svc:src", "billing-svc:srcs/a.go", false},
	}
	for _, c := range cases {
		if got := MatchPath(c.pattern, c.file); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
	if got := Qualify("billing-svc", "src/a.go"); got != "billing-svc:src/a.go" {
		t.Errorf("Qualify = %q", got)
	}
	if got := Qualify("billing-svc", "orders-svc:src/a.go"); got != "orders-svc:src/a.go" {
		t.Errorf("Qualify must keep an explicit repository, got %q", got)
	}
	if got := Qualify("", "src/a.go"); got != "src/a.go" {
		t.Errorf("Qualify without a repository = %q", got)
	}

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	cfg := model.DefaultConfig("p")
	cfg.Areas = map[string]model.Area{"payments": {Paths: []string{"billing-svc:src/pay", "gateway:src/pay"}}}
	mk := func(sc model.Scope) model.Directive {
		return model.Directive{ID: "Y-0001", Kind: model.DirectiveDeny, Scope: sc, Status: model.DirectiveActive, PublishedBy: "pm@example.com",
			Starts: model.FormatTime(now.Add(-time.Hour)), Expires: model.FormatTime(now.Add(time.Hour)), Text: "t"}
	}
	freeze := mk(model.Scope{Paths: []string{"billing-svc:src/auth/**"}})
	if !Delivered(cfg, freeze, "backend", "billing-svc", now) {
		t.Error("the frozen repository must receive the directive")
	}
	if Delivered(cfg, freeze, "backend", "orders-svc", now) {
		t.Error("a directive limited to another repository is noise here")
	}
	if !Delivered(cfg, freeze, "pm", "", now) {
		t.Error("a workspace without a repository sees every directive")
	}
	area := mk(model.Scope{Area: "payments"})
	if !Delivered(cfg, area, "backend", "gateway", now) || Delivered(cfg, area, "backend", "orders-svc", now) {
		t.Error("area directives follow the repositories of the area's paths")
	}
	if _, ok := DenyFor(cfg, []model.Directive{area}, "gateway:src/pay/card.go", now); !ok {
		t.Error("area deny must block in a repository of the area")
	}
	if _, ok := DenyFor(cfg, []model.Directive{area}, "orders-svc:src/pay/card.go", now); ok {
		t.Error("area deny must not leak into another repository")
	}
}
