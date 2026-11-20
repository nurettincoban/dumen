package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nurettincoban/comm-pro/internal/activity"
	"github.com/nurettincoban/comm-pro/internal/model"
	"github.com/nurettincoban/comm-pro/internal/op"
	"github.com/nurettincoban/comm-pro/internal/project"
	"github.com/nurettincoban/comm-pro/internal/render"
	"github.com/nurettincoban/comm-pro/internal/store"
	"github.com/nurettincoban/comm-pro/internal/testutil"
)

// repo creates a code repository with an origin remote, as a human lead, and runs `dumen init`.
func repo(t *testing.T) (dir string, p *project.Project) {
	t.Helper()
	testutil.Home(t)
	t.Setenv("CLAUDECODE", "") // the test acts as a human at a terminal
	remote := testutil.BareRemote(t)
	dir = t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", testutil.Lead.Email}, {"config", "user.name", "Lead"},
		{"remote", "add", "origin", remote},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
	run(t, "init")
	p, err := project.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, p
}

func run(t *testing.T, args ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	if code := Main(args, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("dumen %s: exit %d\n%s%s", strings.Join(args, " "), code, out.String(), errb.String())
	}
	return out.String()
}

func runErr(args ...string) (string, int) {
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(""), &out, &errb)
	return out.String() + errb.String(), code
}

func TestInitDirectWithdraw(t *testing.T) {
	dir, p := repo(t)
	if _, err := os.Stat(filepath.Join(dir, project.File)); err != nil {
		t.Fatal("init did not write the project file")
	}
	if strings.HasPrefix(p.Store.Home, dir) {
		t.Fatal("local state must never be inside the working tree")
	}
	out := run(t, "direct", "--deny", "--paths", "src/auth/**", "--for", "2d", "Release", "week:", "do not touch src/auth")
	id := model.Slug(out) // only to make sure something was printed
	if id == "" || !strings.Contains(out, "Published Y-") {
		t.Fatalf("direct: %q", out)
	}
	yid := out[len("Published ") : len("Published ")+6]
	if list := run(t, "directives"); !strings.Contains(list, yid) || !strings.Contains(list, "deny") {
		t.Fatalf("directives: %q", list)
	}
	// Inside Claude Code the CLI acts as an agent and cannot publish or withdraw.
	t.Setenv("CLAUDECODE", "1")
	if msg, code := runErr("direct", "--all", "agents rule"); code == 0 || !strings.Contains(msg, "forbidden") {
		t.Fatalf("agent publish must fail: %d %q", code, msg)
	}
	if msg, code := runErr("withdraw", yid); code == 0 || !strings.Contains(msg, "forbidden") {
		t.Fatalf("agent withdraw must fail: %d %q", code, msg)
	}
	if msg, code := runErr("private", "on"); code == 0 || !strings.Contains(msg, "forbidden") {
		t.Fatalf("agent private must fail: %d %q", code, msg)
	}
	t.Setenv("CLAUDECODE", "")
	run(t, "withdraw", yid)
	if list := run(t, "directives"); strings.Contains(list, yid) {
		t.Fatalf("withdrawn directive still listed: %q", list)
	}
	if st := run(t, "status"); !strings.Contains(st, "role: lead") || !strings.Contains(st, "0 queued, 2 rejected") {
		t.Fatalf("status: %q", st)
	}
}

// Acceptance 5: `dumen activity --since today` returns correct started/blocked/finished
// counts for a scripted multi-session scenario.
func TestActivitySinceTodayScenario(t *testing.T) {
	_, p := repo(t)
	eng := p.Engine()
	now := time.Now().UTC()
	ctx := context.Background()

	cfg, _ := store.ReadConfig(p.Store.Root())
	cfg.Roles["ayse@example.com"] = "backend"
	cfg.Roles["can@example.com"] = "frontend"
	cfg.Roles["deniz@example.com"] = "pm"
	if _, err := eng.Submit(ctx, testutil.Lead, &op.UpdateConfig{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	out, err := eng.Submit(ctx, testutil.Lead, &op.CreateDirective{Directive: model.Directive{ID: model.NewID("Y"), Kind: model.DirectiveContext,
		Scope: model.Scope{All: true}, Expires: model.FormatTime(now.Add(48 * time.Hour))}, Text: "Release directive"})
	if err != nil {
		t.Fatal(err)
	}
	release := out.RecordID

	type ev struct {
		session, person, kind, text string
		at                          time.Time
		ids                         []string
	}
	old := now.Add(-30 * time.Hour) // yesterday or earlier in every time zone: must not be counted
	script := []ev{
		{"old-1", "ayse@example.com", model.EventStarted, "old session", old, nil},
		{"old-1", "ayse@example.com", model.EventBlocked, "old block", old.Add(time.Minute), nil},
		{"old-1", "ayse@example.com", model.EventFinished, "old done", old.Add(2 * time.Minute), nil},

		// Session A: starts, acknowledges, finishes.
		{"sess-a", "ayse@example.com", model.EventStarted, "session startup", now, nil},
		{"sess-a", "ayse@example.com", model.EventDirectiveAck, "", now, []string{release}},
		{"sess-a", "ayse@example.com", model.EventProgress, "endpoint added", now, nil},
		{"sess-a", "ayse@example.com", model.EventFinished, "orders endpoint done", now, nil},
		// Session B: blocked twice, still blocked.
		{"sess-b", "can@example.com", model.EventStarted, "session startup", now, nil},
		{"sess-b", "can@example.com", model.EventBlocked, "edit blocked: src/auth/a.ts", now, nil},
		{"sess-b", "can@example.com", model.EventBlocked, "edit blocked: src/auth/b.ts", now, nil},
		// Session C: blocked, then moved on and finished.
		{"sess-c", "can@example.com", model.EventStarted, "session startup", now, nil},
		{"sess-c", "can@example.com", model.EventBlocked, "edit blocked: db/x.sql", now, nil},
		{"sess-c", "can@example.com", model.EventProgress, "worked around it properly", now, nil},
		{"sess-c", "can@example.com", model.EventFinished, "ui done", now, nil},
		// Session D: waiting for its human.
		{"sess-d", "ayse@example.com", model.EventStarted, "session startup", now, nil},
		{"sess-d", "ayse@example.com", model.EventAskingHuman, "which auth provider?", now, nil},
		// Session E: finished without a problem.
		{"sess-e", testutil.Lead.Email, model.EventStarted, "session startup", now, nil},
		{"sess-e", testutil.Lead.Email, model.EventFinished, "docs done", now, nil},
	}
	for _, e := range script {
		o := &op.AppendEvent{Event: model.Event{TS: model.FormatTime(e.at), Session: e.session, Person: e.person, Kind: e.kind, Text: e.text, IDs: e.ids}}
		if _, err := eng.Enqueue(model.Actor{Email: e.person, Kind: model.KindAgent}, o); err != nil {
			t.Fatal(err)
		}
	}
	if rep, err := eng.Flush(ctx); err != nil || rep.Offline {
		t.Fatalf("flush: %+v %v", rep, err)
	}

	text := run(t, "activity", "--since", "today")
	if !strings.Contains(text, "started 5  finished 3  blocked 3  asking_human 1  progress 2  directive_ack 1") {
		t.Fatalf("wrong counts:\n%s", text)
	}
	var got struct {
		Summary        activity.Summary `json:"summary"`
		Unacknowledged []struct {
			Directive string   `json:"directive"`
			Missing   []string `json:"missing"`
		} `json:"unacknowledged"`
	}
	if err := json.Unmarshal([]byte(run(t, "activity", "--since", "today", "--json")), &got); err != nil {
		t.Fatal(err)
	}
	s := got.Summary
	if s.Counts[model.EventStarted] != 5 || s.Counts[model.EventBlocked] != 3 || s.Counts[model.EventFinished] != 3 || len(s.Sessions) != 5 {
		t.Fatalf("json counts: %+v", s.Counts)
	}
	stuck := map[string]string{}
	for _, x := range s.Stuck {
		stuck[x.Session] = x.State
	}
	if len(stuck) != 2 || stuck["sess-b"] != activity.StateBlocked || stuck["sess-d"] != activity.StateAsking {
		t.Fatalf("stuck sessions: %v", stuck)
	}
	// Who has not received the release directive: everyone on the roster except ayse.
	if len(got.Unacknowledged) != 1 || got.Unacknowledged[0].Directive != release ||
		strings.Join(got.Unacknowledged[0].Missing, ",") != "can@example.com,deniz@example.com,lead@example.com" {
		t.Fatalf("unacknowledged: %+v", got.Unacknowledged)
	}
	// Filters.
	if text := run(t, "activity", "--since", "today", "--person", "can@", "--kind", "blocked", "--events"); strings.Count(text, "\n") != 3 {
		t.Fatalf("filtered events:\n%s", text)
	}
	if text := run(t, "activity", "--since", "3d"); !strings.Contains(text, "started 6  finished 4  blocked 4") {
		t.Fatalf("3d counts:\n%s", text)
	}
	if text := run(t, "watch", "--once", "--since", "1h"); strings.Count(text, "\n") != 15 {
		t.Fatalf("watch --once should print today's 15 events:\n%s", text)
	}
}

// Acceptance 6: DECISIONS.md and ACTIVITY.md are byte-identical when regenerated from the same state.
func TestGeneratedFilesAreDeterministic(t *testing.T) {
	_, p := repo(t)
	eng := p.Engine()
	ctx := context.Background()
	now := time.Now().UTC()
	must := func(o op.Op) string {
		t.Helper()
		out, err := eng.Submit(ctx, testutil.Lead, o)
		if err != nil {
			t.Fatal(err)
		}
		return out.RecordID
	}
	cfg, _ := store.ReadConfig(p.Store.Root())
	cfg.Areas = map[string]model.Area{"api": {Paths: []string{"src/api"}}, "ui": {Paths: []string{"src/ui"}}}
	must(&op.UpdateConfig{Config: cfg})
	ref := must(&op.AddRef{Ref: model.Ref{ID: model.NewID("R"), URL: "https://design.example.com/a b|c", Kind: "design", Title: "Checkout | flow", Governs: "checkout screens", Area: "ui"}})
	d1 := must(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "REST for the public API", Area: "api", Paths: []string{"src/api"}}, Body: "b", Approve: true})
	must(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "GraphQL instead", Area: "api", Supersedes: []string{d1}}, Body: "b", Approve: true})
	d3 := must(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "Buttons use the design tokens", Area: "ui", Sources: []string{ref}}, Body: "b", Approve: true})
	must(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "Token set A", Supersedes: []string{d3}}, Body: "b", Approve: true})
	must(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "Token set B", Supersedes: []string{d3}}, Body: "b", Approve: true})
	must(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "A draft <script>alert(1)</script>"}, Body: "b"})
	must(&op.CreateDirective{Directive: model.Directive{ID: model.NewID("Y"), Kind: model.DirectiveDeny, Scope: model.Scope{Paths: []string{"src/auth/**"}},
		Expires: model.FormatTime(now.Add(time.Hour))}, Text: "Release week:\ndo not touch src/auth"})
	for i, kind := range []string{model.EventStarted, model.EventBlocked, model.EventAskingHuman, model.EventFinished} {
		must(&op.AppendEvent{Event: model.Event{TS: model.FormatTime(now.Add(time.Duration(i) * time.Second)), Session: "s1", Person: "a@example.com", Kind: kind, Text: "note | " + kind}})
	}

	root := p.Store.Root()
	committedD, _ := os.ReadFile(filepath.Join(root, store.DecisionsMD))
	committedA, _ := os.ReadFile(filepath.Join(root, store.ActivityMD))
	if len(committedD) == 0 || len(committedA) == 0 {
		t.Fatal("generated files were not committed")
	}
	// Regenerate from an independent clone of the same state, twice.
	other := testutil.NewStore(t, p.Store.Remote, "other")
	for i := 0; i < 2; i++ {
		if err := render.Regenerate(other.Root()); err != nil {
			t.Fatal(err)
		}
		gotD, _ := os.ReadFile(filepath.Join(other.Root(), store.DecisionsMD))
		gotA, _ := os.ReadFile(filepath.Join(other.Root(), store.ActivityMD))
		if !bytes.Equal(gotD, committedD) {
			t.Fatalf("DECISIONS.md differs on regeneration %d:\n%s\n---\n%s", i, committedD, gotD)
		}
		if !bytes.Equal(gotA, committedA) {
			t.Fatalf("ACTIVITY.md differs on regeneration %d:\n%s\n---\n%s", i, committedA, gotA)
		}
	}
	text := string(committedD)
	for _, want := range []string{"### api", "### ui", "CONTESTED", "## Superseded", d1, "## Drafts (1)", "## Sources of truth", "Checkout \\| flow", "## Directives"} {
		if !strings.Contains(text, want) {
			t.Errorf("DECISIONS.md is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<script>") {
		t.Error("DECISIONS.md must not carry raw HTML from record titles")
	}
	if a := string(committedA); !strings.Contains(a, "asking_human") || !strings.Contains(a, "Blocked or waiting on a person") {
		t.Errorf("ACTIVITY.md:\n%s", a)
	}
}

func TestGCCompactsOldActivity(t *testing.T) {
	_, p := repo(t)
	eng := p.Engine()
	ctx := context.Background()
	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	for i, s := range []string{"s1", "s2", "s1"} {
		o := &op.AppendEvent{Event: model.Event{TS: model.FormatTime(old.Add(time.Duration(i) * time.Minute)), Session: s, Person: "a@example.com", Kind: model.EventProgress, Text: "old"}}
		if _, err := eng.Submit(ctx, testutil.Lead, o); err != nil {
			t.Fatal(err)
		}
	}
	oldDraft := &op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "Stale draft", Created: model.FormatTime(old)}, Body: "b"}
	if _, err := eng.Submit(ctx, testutil.Lead, oldDraft); err != nil {
		t.Fatal(err)
	}
	run(t, "gc")
	day := old.Format("2006-01-02")
	root := p.Store.Root()
	if _, err := os.Stat(filepath.Join(root, "activity", day)); err == nil {
		t.Error("old day directory still exists")
	}
	evs, err := store.ReadEvents(root, day)
	if err != nil || len(evs) != 3 {
		t.Fatalf("events after compaction: %d %v", len(evs), err)
	}
	if ds, _ := store.ListDecisions(root); len(ds) != 0 {
		t.Errorf("stale draft not archived: %+v", ds)
	}
	if !store.Exists(root, oldDraft.Decision.ID) {
		t.Error("archived draft must keep reserving its id")
	}
}

func TestRoleSetListRemove(t *testing.T) {
	repo(t)
	if out := run(t, "role", "set", "Ayse@Example.com", "dev"); !strings.Contains(out, "ayse@example.com is now dev") {
		t.Fatalf("role set: %q", out)
	}
	if out := run(t, "role", "list"); !strings.Contains(out, "dev      ayse@example.com") || !strings.Contains(out, "lead") {
		t.Fatalf("role list: %q", out)
	}
	if msg, code := runErr("role", "set", "not-an-email", "dev"); code == 0 || !strings.Contains(msg, "not an email") {
		t.Fatalf("bad email must fail: %d %q", code, msg)
	}
	// Agents cannot change roles.
	t.Setenv("CLAUDECODE", "1")
	if msg, code := runErr("role", "set", "mallory@example.com", "lead"); code == 0 || !strings.Contains(msg, "forbidden") {
		t.Fatalf("agent role set must fail: %d %q", code, msg)
	}
	t.Setenv("CLAUDECODE", "")
	run(t, "role", "rm", "ayse@example.com")
	if out := run(t, "role", "list"); strings.Contains(out, "ayse@example.com") {
		t.Fatalf("removed person still listed: %q", out)
	}
}
