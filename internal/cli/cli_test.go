package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/nurettincoban/dumen/internal/gitx"
	"github.com/nurettincoban/dumen/internal/outbox"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/activity"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/project"
	"github.com/nurettincoban/dumen/internal/render"
	"github.com/nurettincoban/dumen/internal/store"
	"github.com/nurettincoban/dumen/internal/testutil"
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
	if code := Main(append([]string{"--no-terminal"}, args...), strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("dumen %s: exit %d\n%s%s", strings.Join(args, " "), code, out.String(), errb.String())
	}
	return out.String()
}

func runErr(args ...string) (string, int) {
	var out, errb bytes.Buffer
	code := Main(append([]string{"--no-terminal"}, args...), strings.NewReader(""), &out, &errb)
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
	if st := run(t, "status"); !strings.Contains(st, "role: lead") || !strings.Contains(st, "0 queued, 0 rejected") {
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
	// Who has not received the release directive: everyone except ayse (acked) and the lead who published it.
	if len(got.Unacknowledged) != 1 || got.Unacknowledged[0].Directive != release ||
		strings.Join(got.Unacknowledged[0].Missing, ",") != "can@example.com,deniz@example.com" {
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

func TestPublishProposal(t *testing.T) {
	repo(t)
	out := run(t, "direct", "--propose", "--paths", "src/api/**", "Keep the API stable")
	id := out[len("Proposed ") : len("Proposed ")+6]
	if list := run(t, "directives"); strings.Contains(list, id) {
		t.Fatalf("a proposal must not be listed as active: %q", list)
	}
	if out := run(t, "direct", "--publish", strings.ToLower(id)); !strings.Contains(out, "Published "+id) {
		t.Fatalf("publish: %q", out)
	}
	if list := run(t, "directives"); !strings.Contains(list, id) || !strings.Contains(list, "active") {
		t.Fatalf("published proposal not active: %q", list)
	}
}

// Gauntlet K5 and K6. One record that cannot be parsed (edited by hand, or written by a
// newer dumen) must not stop the team: reads skip it, writes go on, status names it.
// And a write that is reported as failed is never published later.
func TestOneBadRecordDoesNotStopTheTeam(t *testing.T) {
	_, p := repo(t)
	good := run(t, "direct", "--deny", "--paths", "src/auth/**", "Auth is frozen")
	ctx := context.Background()
	unlock, err := p.Store.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"decisions/D-ZZZZ-bad.md":  "---\nid: [unclosed\n---\nbody\n",
		"directives/Y-ZZZZ-bad.md": "no front matter at all\n",
		"refs/R-ZZZZ.yml":          "id: {{{\n",
	} {
		path := filepath.Join(p.Store.Root(), filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Store.Commit(ctx, "a colleague edits records by hand", "colleague@example.com"); err != nil {
		t.Fatal(err)
	}
	if res, err := p.Store.Push(ctx); err != nil || res != gitx.PushOK {
		t.Fatalf("push: %v %v", res, err)
	}
	unlock()

	// Writes still work, and the directive that was there is still enforced.
	out := run(t, "direct", "--all", "can we still publish?")
	if !strings.Contains(out, "Published Y-") {
		t.Fatalf("publishing with a bad record in the store: %q", out)
	}
	list := run(t, "directives")
	if !strings.Contains(list, good[len("Published "):len("Published ")+6]) || !strings.Contains(list, "can we still publish?") {
		t.Fatalf("directives: %q", list)
	}
	st := run(t, "status")
	if strings.Contains(st, "OFFLINE") || !strings.Contains(st, "3 unreadable") || !strings.Contains(st, "decisions/D-ZZZZ-bad.md") {
		t.Fatalf("status must name the bad records and not claim to be offline: %q", st)
	}
	if !strings.Contains(st, "0 queued") {
		t.Fatalf("nothing may be left queued: %q", st)
	}
	run(t, "decisions")
	run(t, "ref", "list")
}

// Gauntlet K6. When a write fails for a reason other than the network, it is gone: the
// command says so and a later sync publishes nothing.
func TestFailedWriteIsNotPublishedLater(t *testing.T) {
	_, p := repo(t)
	// Make every flush fail in a way that is not a network error.
	cfg := filepath.Join(p.Store.Root(), "dumen.yml")
	ctx := context.Background()
	unlock, _ := p.Store.Lock(ctx)
	p.Store.Sync(ctx)
	os.WriteFile(cfg, []byte("roles: [this is not a map\n"), 0o644)
	p.Store.Commit(ctx, "break dumen.yml", "colleague@example.com")
	if res, err := p.Store.Push(ctx); err != nil || res != gitx.PushOK {
		t.Fatalf("push: %v %v", res, err)
	}
	unlock()

	msg, code := runErr("direct", "--all", "published by accident later?")
	if code == 0 {
		t.Fatalf("the write must fail while dumen.yml is broken: %q", msg)
	}
	if n := outbox.Count(p.Store.OutboxDir()); n != 0 {
		t.Fatalf("a write reported as failed is still queued (%d): %q", n, msg)
	}
}

// Gauntlet T4. A store written by a newer dumen is read but never written.
func TestNewerSchemaIsReadOnly(t *testing.T) {
	_, p := repo(t)
	run(t, "direct", "--all", "written before the upgrade")
	ctx := context.Background()
	unlock, _ := p.Store.Lock(ctx)
	p.Store.Sync(ctx)
	cfg, err := store.ReadConfig(p.Store.Root())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Version = model.SchemaVersion + 1
	if err := store.WriteConfig(p.Store.Root(), cfg); err != nil {
		t.Fatal(err)
	}
	p.Store.Commit(ctx, "a newer dumen upgrades the store", "colleague@example.com")
	if res, err := p.Store.Push(ctx); err != nil || res != gitx.PushOK {
		t.Fatalf("push: %v %v", res, err)
	}
	unlock()

	msg, code := runErr("direct", "--all", "written by an old client")
	if code == 0 || !strings.Contains(msg, "newer dumen format") || !strings.Contains(msg, "upgrade dumen") {
		t.Fatalf("an old client must refuse to write: %d %q", code, msg)
	}
	if n := outbox.Count(p.Store.OutboxDir()); n != 0 {
		t.Fatalf("the refused write is still queued (%d)", n)
	}
	if list := run(t, "directives"); !strings.Contains(list, "written before the upgrade") || strings.Contains(list, "old client") {
		t.Fatalf("reading must still work: %q", list)
	}
}

// Gauntlet O1 and T8. When the git host turns a push down (protected branch, missing
// permission, unsigned commits), the user sees the host's own words, not "unreachable".
func TestHostRefusalIsShownAsIs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell hook on the remote")
	}
	_, p := repo(t)
	hook := filepath.Join(p.Store.Remote, "hooks", "pre-receive")
	os.MkdirAll(filepath.Dir(hook), 0o755)
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'GH006: Protected branch update failed. Commits must have verified signatures.' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	msg, code := runErr("direct", "--all", "this cannot be pushed")
	if code == 0 || !strings.Contains(msg, "the git host refused the push") || !strings.Contains(msg, "verified signatures") {
		t.Fatalf("want the host's message, got %d %q", code, msg)
	}
	if strings.Contains(msg, "could not be reached") {
		t.Fatalf("a refusal is not an unreachable remote: %q", msg)
	}
	if n := outbox.Count(p.Store.OutboxDir()); n != 0 {
		t.Fatalf("the refused write is still queued (%d)", n)
	}
	os.Remove(hook)
	if out := run(t, "direct", "--all", "now it works"); !strings.Contains(out, "Published Y-") {
		t.Fatalf("after the host allows it: %q", out)
	}
	if list := run(t, "directives"); strings.Contains(list, "cannot be pushed") {
		t.Fatalf("the refused directive was published later: %q", list)
	}
}

// `dumen init` works when origin is a relative local path: the store's clone lives
// elsewhere, so the path is resolved against the repository first.
func TestInitResolvesARelativeOrigin(t *testing.T) {
	testutil.Home(t)
	t.Setenv("CLAUDECODE", "")
	remote := testutil.BareRemote(t)
	dir := filepath.Join(filepath.Dir(remote), "code")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", testutil.Lead.Email}, {"config", "user.name", "Lead"},
		{"remote", "add", "origin", "../" + filepath.Base(remote)},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
	run(t, "init")
	if out := run(t, "status"); !strings.Contains(out, "up to date") {
		t.Fatalf("status after init with a relative origin:\n%s", out)
	}
}

// `dumen join <url>` names the project after the store's own dumen.yml, not after the
// URL, so a workspace shares local state with the code repositories of the project.
func TestJoinWorkspaceUsesTheStoreProjectName(t *testing.T) {
	_, p := repo(t)
	run(t, "role", "set", testutil.PM.Email, "pm")
	remote := p.Store.Remote
	want := p.Name

	// The PM's machine: its own dumen home, a global git identity, no code.
	testutil.Home(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\temail = "+testutil.PM.Email+"\n\tname = PM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	spaces := t.TempDir()
	t.Setenv("DUMEN_WORKSPACES", spaces)
	// A URL whose last element differs from the project name.
	alias := filepath.Join(t.TempDir(), "team-store.git")
	if err := os.Symlink(remote, alias); err != nil {
		t.Skip("symlinks are not available")
	}
	t.Chdir(t.TempDir())
	out := run(t, "join", alias)
	if !strings.Contains(out, "Joined project \""+want+"\"") {
		t.Fatalf("join should use the project name from the store (%q):\n%s", want, out)
	}
	if _, err := os.Stat(filepath.Join(spaces, want, project.File)); err != nil {
		t.Fatalf("workspace %s was not created: %v", want, err)
	}
}

// Gauntlet S1: a store remote from a `.dumen` file somebody else wrote must never reach
// git as an option or as a remote-helper command.
func TestJoinRefusesARemoteThatGitWouldReadAsAnOption(t *testing.T) {
	testutil.Home(t)
	t.Setenv("CLAUDECODE", "")
	spaces := t.TempDir()
	t.Setenv("DUMEN_WORKSPACES", spaces)
	defer func() {
		if left, _ := os.ReadDir(spaces); len(left) > 0 {
			t.Errorf("a refused join left a workspace behind: %v", left)
		}
	}()
	marker := filepath.Join(t.TempDir(), "PWNED")
	evil := "--upload-pack=touch " + marker + ";"
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", testutil.Lead.Email}, {"config", "user.name", "Lead"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
	for _, remote := range []string{evil, "ext::sh -c touch% " + marker} {
		if err := os.WriteFile(filepath.Join(dir, project.File), []byte("project = demo\nstore = "+remote+"#dumen\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"join"}, {"status"}, {"sync"}, {"join", "--", remote}} {
			if out, code := runErr(args...); code == 0 {
				t.Errorf("dumen %v with remote %q should fail:\n%s", args, remote, out)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatalf("dumen %v ran a command taken from the remote %q", args, remote)
			}
		}
	}
	os.Remove(filepath.Join(dir, project.File))
	for _, remote := range []string{evil, "ext::sh -c touch% " + marker} {
		if out, code := runErr("init", "--store", remote+"#dumen"); code == 0 || !strings.Contains(out, "refusing store remote") {
			t.Errorf("init with remote %q should be refused:\n%s", remote, out)
		}
	}
	if out, code := runErr("init", "--store", testutil.BareRemote(t)+"#-x"); code == 0 || !strings.Contains(out, "refusing store branch") {
		t.Errorf("a branch that starts with \"-\" should be refused:\n%s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("dumen init ran a command taken from the remote")
	}
}

// Gauntlet S2: a project or repo name from a `.dumen` file is one plain path element, so
// local state can never be created, or removed, outside the dumen home.
func TestProjectNameCannotLeaveTheDumenHome(t *testing.T) {
	_, p := repo(t)
	remote := p.Store.Remote
	home := testutil.Home(t) // another machine
	victim := filepath.Join(filepath.Dir(home), "victim", "store")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(victim, "keep.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", testutil.Lead.Email}, {"config", "user.name", "Lead"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
	for _, body := range []string{
		"project = ../victim\nstore = " + remote + "#dumen\n",
		"project = a/b\nstore = " + remote + "#dumen\n",
		"project = ..\nstore = " + remote + "#dumen\n",
		"project = ok\nrepo = ../x\nstore = " + remote + "#dumen\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, project.File), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := runErr("join"); code == 0 || !strings.Contains(out, "invalid") {
			t.Errorf("join should refuse %q:\n%s", body, out)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("dumen join removed a directory outside the dumen home")
	}
	os.Remove(filepath.Join(dir, project.File))
	for _, args := range [][]string{{"init", "--project", "../victim"}, {"init", "--repo", "../x"}, {"sync", "--project", "../victim"}} {
		if out, code := runErr(args...); code == 0 {
			t.Errorf("dumen %v should fail:\n%s", args, out)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("dumen removed a directory outside the dumen home")
	}
}

// `dumen check` fails when a change, however it was made, touches a path under a deny
// directive: in the work tree, or in a commit range for CI.
func TestCheckFindsChangesUnderADenyDirective(t *testing.T) {
	dir, _ := repo(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	put := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("src/auth/login.go", "one\n")
	put("src/api/orders.go", "one\n")
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	base := git("rev-parse", "HEAD")
	out := run(t, "direct", "--deny", "--paths", "src/auth/**", "Auth is frozen")
	id := out[strings.Index(out, "Y-") : strings.Index(out, "Y-")+6]

	put("src/api/orders.go", "two\n")
	if out := run(t, "check"); !strings.Contains(out, "OK: 1 changed file in") {
		t.Fatalf("a change outside the frozen path is fine:\n%s", out)
	}
	put("src/auth/login.go", "two\n") // as `sed -i` would
	out, code := runErr("check")
	if code != 1 || !strings.Contains(out, "src/auth/login.go") || !strings.Contains(out, id) {
		t.Fatalf("check must fail and name the file and the directive (exit %d):\n%s", code, out)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "two")
	if out := run(t, "check"); !strings.Contains(out, "OK: 0 changed files") {
		t.Fatalf("a clean work tree passes:\n%s", out)
	}
	for _, args := range [][]string{{"check", base + "..HEAD"}, {"check", base + ".."}, {"check", base, "HEAD"}} {
		out, code := runErr(args...)
		if code != 1 || !strings.Contains(out, "src/auth/login.go") || !strings.Contains(out, "1 of 2 changed files") {
			t.Errorf("dumen %v must fail on the committed change (exit %d):\n%s", args, code, out)
		}
	}
	run(t, "withdraw", id)
	if out := run(t, "check", base+"..HEAD"); !strings.Contains(out, "OK: 2 changed files") {
		t.Fatalf("after the directive is withdrawn the range passes:\n%s", out)
	}
	// A check that could not be made exits with 2, so CI can tell it from a finding.
	for _, args := range [][]string{{"check", "a", "b", "c"}, {"check", "nosuchrev..HEAD"}, {"check", "--output=x..HEAD"}} {
		if out, code := runErr(args...); code != 2 {
			t.Errorf("dumen %v: want exit 2, got %d:\n%s", args, code, out)
		}
	}
}

// Gauntlet F1: a range is checked as a pull request shows it, from the point where the
// branch forked. A commit that the base gained under the frozen path afterwards is not
// the branch's change.
func TestCheckIgnoresWhatTheBaseGainedAfterTheFork(t *testing.T) {
	dir, _ := repo(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	put := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("src/auth/login.go", "one\n")
	put("src/web/app.go", "one\n")
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	git("branch", "-M", "main")
	git("checkout", "-q", "-b", "feature")
	put("src/web/app.go", "two\n")
	git("commit", "-q", "-am", "feature work")
	git("checkout", "-q", "main")
	put("src/auth/login.go", "two\n") // somebody with the right to do so, before the freeze
	git("commit", "-q", "-am", "auth work on main")
	git("checkout", "-q", "feature")
	run(t, "direct", "--deny", "--paths", "src/auth/**", "Auth is frozen")
	for _, r := range []string{"main..HEAD", "main...HEAD", "main..feature"} {
		if out, code := runErr("check", r); code != 0 || !strings.Contains(out, "OK: 1 changed file in") {
			t.Errorf("dumen check %s must pass: the branch never touched src/auth (exit %d):\n%s", r, code, out)
		}
	}
	put("src/auth/login.go", "feature\n")
	git("commit", "-q", "-am", "touch auth")
	if out, code := runErr("check", "main..HEAD"); code != 1 || !strings.Contains(out, "src/auth/login.go") {
		t.Errorf("a branch that does touch src/auth must fail (exit %d):\n%s", code, out)
	}
	git("checkout", "-q", "--orphan", "island")
	git("commit", "-q", "-m", "unrelated history")
	if out, code := runErr("check", "main..HEAD"); code != 2 || !strings.Contains(out, "no common ancestor") {
		t.Errorf("without a merge base the check must say so (exit %d):\n%s", code, out)
	}
}

// A local state file that was tampered with is refused with the reason, not with the
// advice to join.
func TestTamperedLocalStateIsReportedAsSuch(t *testing.T) {
	_, p := repo(t)
	local := filepath.Join(p.Store.Home, "local.json")
	if err := os.WriteFile(local, []byte(`{"project":"x","remote":"--upload-pack=x","branch":"dumen"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runErr("status")
	if code == 0 || !strings.Contains(out, "refusing store remote") || strings.Contains(out, "has not joined") {
		t.Fatalf("status with a tampered local.json (exit %d):\n%s", code, out)
	}
}

// Gauntlet O4: an unrelated repository whose `.dumen` carries the name of a project this
// machine has joined gets none of the team's records and sends nothing to the team's
// store, until somebody runs `dumen join` there.
func TestAnotherRepositoryWithTheSameProjectNameIsNotTheProject(t *testing.T) {
	dir, p := repo(t)
	run(t, "direct", "--all", "Team only: use the structured logger")
	body, err := os.ReadFile(filepath.Join(dir, project.File))
	if err != nil {
		t.Fatal(err)
	}

	other := t.TempDir() // somebody else's repository, cloned from anywhere
	if err := os.WriteFile(filepath.Join(other, project.File), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := project.Find(other); !errors.Is(err, project.ErrNotJoinedHere) {
		t.Fatalf("project.Find in the other repository: %v", err)
	}
	t.Chdir(other)
	for _, args := range [][]string{{"status"}, {"directives"}, {"activity"}, {"decide", "--title", "x", "--rule", "r", "--kind", "technical", "-m", "y"}} {
		if out, code := runErr(args...); code == 0 || !strings.Contains(out, "has not joined project") || strings.Contains(out, "structured logger") {
			t.Errorf("dumen %v in the other repository (exit %d):\n%s", args, code, out)
		}
	}
	// A hook there injects nothing and logs nothing.
	var out bytes.Buffer
	in := fmt.Sprintf(`{"session_id":"s9","cwd":%q,"source":"startup"}`, other)
	Main([]string{"hook", "session-start"}, strings.NewReader(in), &out, &out)
	if strings.Contains(out.String(), "structured logger") || strings.Contains(out.String(), "ACTIVE DIRECTIVES") {
		t.Fatalf("team records were injected into an unrelated repository:\n%s", out.String())
	}
	if entries, _ := os.ReadDir(p.Store.OutboxDir()); len(entries) != 0 {
		t.Fatalf("the unrelated session queued %d operations for the team's store", len(entries))
	}

	// Local state that lists no directory at all, as an earlier build wrote it, is not a
	// licence for every directory: it counts as not joined.
	local := filepath.Join(p.Store.Home, "local.json")
	saved, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	json.Unmarshal(saved, &state)
	delete(state, "roots")
	bare, _ := json.Marshal(state)
	os.WriteFile(local, bare, 0o644)
	if out, code := runErr("status"); code == 0 || !strings.Contains(out, "has not joined project") {
		t.Errorf("local state without roots must fail closed (exit %d):\n%s", code, out)
	}
	os.WriteFile(local, saved, 0o644)

	// The first repository still works, also through a symlink to it.
	t.Chdir(dir)
	if out := run(t, "directives"); !strings.Contains(out, "structured logger") {
		t.Fatalf("the joined repository lost its records:\n%s", out)
	}
	link := filepath.Join(t.TempDir(), "link")
	if os.Symlink(dir, link) == nil {
		t.Chdir(link)
		run(t, "status")
	}
}

// Gauntlet S3: a human action from a command without a terminal is an agent's, whatever
// the environment says, so an agent's shell cannot pose as a person.
func TestHumanActionsNeedATerminal(t *testing.T) {
	repo(t)
	t.Setenv("CLAUDECODE", "")
	out := run(t, "decide", "--title", "x; y", "--rule", "r", "--kind", "product", "-m", "b")
	id := out[strings.Index(out, "D-") : strings.Index(out, "D-")+6]
	for _, args := range [][]string{
		{"approve", id}, {"reject", id},
		{"decide", "--title", "t", "--rule", "r", "--kind", "product", "-m", "b", "--approve"},
		{"direct", "--all", "x"},
		{"role", "set", "someone@example.com", "dev"},
		{"product", "--name", "x"},
	} {
		var o, e bytes.Buffer
		if code := Main(args, strings.NewReader(""), &o, &e); code == 0 || !strings.Contains(e.String(), "no terminal") {
			t.Errorf("%v without a terminal: exit %d\n%s%s", args, code, o.String(), e.String())
		}
	}
}
