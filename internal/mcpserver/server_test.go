package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nurettincoban/dumen/internal/hook"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/project"
	"github.com/nurettincoban/dumen/internal/store"
	"github.com/nurettincoban/dumen/internal/testutil"
)

type world struct {
	t    *testing.T
	proj *project.Project
	cs   *mcp.ClientSession
}

const parentPID = 777

func setup(t *testing.T) *world {
	t.Helper()
	testutil.Home(t)
	remote := testutil.BareRemote(t)
	s := testutil.NewStore(t, remote, "proj")
	root := t.TempDir()
	if err := project.WriteFile(root, "proj", remote+"#dumen"); err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, proj: &project.Project{Root: root, Name: "proj", Store: s}}
	if _, err := w.proj.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	impl := &Server{Dir: root, ParentPID: parentPID, Version: "test"}
	t.Cleanup(impl.Wait) // registered first, so it runs last: no flush may outlive the temp dirs
	srv := New(impl)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	w.cs = cs
	// A session started through the hook, so the server can find its id by parent pid.
	w.hook(hook.SessionStart, map[string]any{"source": "startup"})
	return w
}

func (w *world) hook(event string, in map[string]any) string {
	in["session_id"], in["cwd"] = "sess-1", w.proj.Root
	data, _ := json.Marshal(in)
	var out bytes.Buffer
	hook.Run(event, bytes.NewReader(data), &out, hook.Env{ParentPID: parentPID})
	return out.String()
}

// call invokes a tool the way Claude Code does and returns its text and error flag.
func (w *world) call(name string, args map[string]any) (string, bool) {
	w.t.Helper()
	res, err := w.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		w.t.Fatalf("%s: protocol error: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func (w *world) ok(name string, args map[string]any) string {
	w.t.Helper()
	text, isErr := w.call(name, args)
	if isErr {
		w.t.Fatalf("%s failed: %s", name, text)
	}
	return text
}

// confirmed simulates the human path: the hook sends the call to the permission prompt
// (leaving a ticket), the person says yes, and Claude Code runs the tool.
func (w *world) confirmed(name string, args map[string]any) (string, bool) {
	out := w.hook(hook.PreToolUse, map[string]any{"tool_name": "mcp__dumen__" + name, "permission_mode": "default", "tool_input": args})
	if !strings.Contains(out, `"permissionDecision":"ask"`) {
		w.t.Fatalf("hook did not ask for %s: %s", name, out)
	}
	return w.call(name, args)
}

func (w *world) events() []model.Event {
	w.t.Helper()
	// Tool calls push in the background; flush whatever is left and read the store.
	deadline := time.Now().Add(10 * time.Second)
	for {
		rep, err := w.proj.Engine().Flush(context.Background())
		if err == nil && !rep.Offline && !rep.Contended {
			break
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("flush: %+v %v", rep, err)
		}
	}
	unlock, _ := w.proj.Store.Lock(context.Background())
	defer unlock()
	w.proj.Store.Sync(context.Background())
	evs, _ := store.ReadEvents(w.proj.Store.Root(), time.Now().UTC().Format("2006-01-02"))
	return evs
}

func TestToolList(t *testing.T) {
	w := setup(t)
	res, err := w.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
	}
	sort.Strings(names)
	want := "ack_directive activity add_ref approve decisions_for_path directives draft_decision get_decision log_event propose_directive publish_directive search_decisions supersede"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("tools:\n got %s\nwant %s", got, want)
	}
}

func TestAgentCannotApproveOrPublishWithoutHumanConfirmation(t *testing.T) {
	w := setup(t)
	text := w.ok("draft_decision", map[string]any{"title": "Use opaque cursors", "body": "Decision.\n\nWhy.\n\nRejected: offsets.", "paths": []string{"src/api"}})
	id := model.NewID("D")
	if i := strings.Index(text, "D-"); i >= 0 {
		id = text[i : i+6]
	}
	// Straight calls, as an agent would make them with no hook involved: refused.
	for name, args := range map[string]map[string]any{
		"approve":           {"id": id},
		"publish_directive": {"text": "do x", "all": true},
		"supersede":         {"id": id, "title": "t", "body": "b"},
	} {
		if text, isErr := w.call(name, args); !isErr || !strings.Contains(text, "human action") {
			t.Errorf("%s without confirmation must be refused, got %q (error=%v)", name, text, isErr)
		}
	}
	// A ticket for one call does not cover a different call.
	w.hook(hook.PreToolUse, map[string]any{"tool_name": "mcp__dumen__approve", "permission_mode": "default", "tool_input": map[string]any{"id": "D-0000"}})
	if _, isErr := w.call("approve", map[string]any{"id": id}); !isErr {
		t.Fatal("a ticket must be bound to the exact arguments")
	}
	// With bypassPermissions the hook denies and leaves no ticket.
	out := w.hook(hook.PreToolUse, map[string]any{"tool_name": "mcp__dumen__approve", "permission_mode": "bypassPermissions", "tool_input": map[string]any{"id": id}})
	if !strings.Contains(out, `"deny"`) {
		t.Fatalf("want deny under bypassPermissions: %s", out)
	}
	if _, isErr := w.call("approve", map[string]any{"id": id}); !isErr {
		t.Fatal("no ticket may exist after a deny")
	}

	// The confirmed path works, once.
	if text, isErr := w.confirmed("approve", map[string]any{"id": id}); isErr || !strings.Contains(text, "approved") {
		t.Fatalf("confirmed approve failed: %s", text)
	}
	if _, isErr := w.call("approve", map[string]any{"id": id}); !isErr {
		t.Fatal("a ticket is single use")
	}
	if text, isErr := w.confirmed("publish_directive", map[string]any{"text": "Release week: do not touch src/auth", "kind": "deny", "paths": []string{"src/auth/**"}, "days": 2}); isErr || !strings.Contains(text, "Published Y-") {
		t.Fatalf("confirmed publish failed: %s", text)
	}
	text = w.ok("get_decision", map[string]any{"id": id})
	if !strings.Contains(text, "status: approved") || !strings.Contains(text, "Rejected: offsets.") || !strings.Contains(text, "author: "+testutil.Lead.Email+" (agent)") {
		t.Fatalf("get_decision: %s", text)
	}
}

func TestDirectiveAckAndActivity(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	eng := w.proj.Engine()
	cfg, _ := store.ReadConfig(w.proj.Store.Root())
	cfg.Roles["dev@example.com"] = "backend"
	if _, err := eng.Submit(ctx, testutil.Lead, &op.UpdateConfig{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	out, err := eng.Submit(ctx, testutil.Lead, &op.CreateDirective{Directive: model.Directive{ID: model.NewID("Y"), Kind: model.DirectiveContext,
		Scope: model.Scope{All: true}, Expires: model.FormatTime(time.Now().Add(time.Hour))}, Text: "All new endpoints use requireAuth"})
	if err != nil {
		t.Fatal(err)
	}
	id := out.RecordID

	if text := w.ok("directives", map[string]any{}); !strings.Contains(text, id) || !strings.Contains(text, "requireAuth") {
		t.Fatalf("directives: %s", text)
	}
	if text, isErr := w.call("ack_directive", map[string]any{"id": "Y-0000"}); !isErr {
		t.Fatalf("acknowledging an unknown directive must fail: %s", text)
	}
	w.ok("ack_directive", map[string]any{"id": strings.ToLower(id)})
	if text := w.ok("ack_directive", map[string]any{"id": id}); !strings.Contains(text, "already") {
		t.Fatalf("second ack: %s", text)
	}
	w.ok("log_event", map[string]any{"kind": "asking_human", "text": "Which auth provider should the gateway use?"})
	if _, isErr := w.call("log_event", map[string]any{"kind": "started"}); !isErr {
		t.Fatal("agents may not log lifecycle kinds that hooks own")
	}
	if text, isErr := w.call("log_event", map[string]any{"kind": "progress", "text": "token ghp_abcdefghijklmnopqrstuvwxyz0123456789"}); !isErr || !strings.Contains(text, "secret") {
		t.Fatalf("a secret in an event must be rejected: %s", text)
	}

	count := map[string]int{}
	for _, e := range w.events() {
		count[e.Kind]++
		if e.Session != "sess-1" {
			t.Errorf("event not attributed to the hook's session: %+v", e)
		}
	}
	if count[model.EventDirectiveAck] != 1 || count[model.EventAskingHuman] != 1 || count[model.EventStarted] != 1 {
		t.Fatalf("events: %v", count)
	}

	text := w.ok("activity", map[string]any{"since": "today"})
	var res struct {
		Counts     map[string]int `json:"counts"`
		Stuck      []struct{ Session, State string }
		Directives []struct {
			Directive string   `json:"directive"`
			Missing   []string `json:"not_acknowledged_by"`
			Acked     []string `json:"acknowledged_by"`
		} `json:"directives"`
	}
	if err := json.Unmarshal([]byte(text[strings.Index(text, "{"):]), &res); err != nil {
		t.Fatalf("activity is not JSON: %v\n%s", err, text)
	}
	if res.Counts["started"] != 1 || len(res.Stuck) != 1 || res.Stuck[0].State != "asking human" {
		t.Fatalf("activity summary: %s", text)
	}
	if len(res.Directives) != 1 || res.Directives[0].Directive != id ||
		strings.Join(res.Directives[0].Missing, ",") != "dev@example.com" || strings.Join(res.Directives[0].Acked, ",") != testutil.Lead.Email {
		t.Fatalf("who has not acknowledged: %s", text)
	}
}

func TestRefsAndPathLookup(t *testing.T) {
	w := setup(t)
	text := w.ok("add_ref", map[string]any{"url": "https://claude.ai/design/abc", "kind": "design", "title": "Checkout flow", "governs": "checkout screens", "paths": []string{"src/ui/checkout"}})
	ref := text[strings.Index(text, "R-") : strings.Index(text, "R-")+6]
	if _, isErr := w.call("add_ref", map[string]any{"url": "not a url", "kind": "design", "title": "x"}); !isErr {
		t.Fatal("a ref needs an http(s) link")
	}
	w.ok("draft_decision", map[string]any{"title": "Checkout follows the design", "body": "Pointer decision: the design governs the checkout screens.", "sources": []string{ref}, "paths": []string{"src/ui/checkout"}})
	if text := w.ok("search_decisions", map[string]any{"query": "checkout"}); !strings.Contains(text, "No matching") {
		t.Fatalf("drafts must not show up as approved: %s", text)
	}
	if text := w.ok("search_decisions", map[string]any{"query": "checkout", "status": "draft"}); !strings.Contains(text, "Checkout follows the design") {
		t.Fatalf("draft search: %s", text)
	}
	// Gauntlet K4: a link an agent added reaches no agent until a person has checked it.
	path := map[string]any{"path": "src/ui/checkout/Cart.tsx"}
	if text := w.ok("decisions_for_path", path); strings.Contains(text, ref) || strings.Contains(text, "claude.ai/design") {
		t.Fatalf("an unverified ref was shown to an agent: %s", text)
	}
	if out := w.hook(hook.SessionStart, map[string]any{"source": "startup"}); strings.Contains(out, "Checkout flow") || !strings.Contains(out, "awaiting a human check") {
		t.Fatalf("an unverified ref must not be injected, only counted: %s", out)
	}
	ctx := context.Background()
	if _, err := w.proj.Engine().Submit(ctx, model.Actor{Email: testutil.Lead.Email, Kind: model.KindAgent}, &op.CheckRef{ID: ref}); !errors.Is(err, op.ErrForbidden) {
		t.Fatalf("an agent must not confirm a ref, got %v", err)
	}
	if _, err := w.proj.Engine().Submit(ctx, testutil.Lead, &op.CheckRef{ID: ref}); err != nil {
		t.Fatal(err)
	}
	if text := w.ok("decisions_for_path", path); !strings.Contains(text, ref) || !strings.Contains(text, "https://claude.ai/design/abc") {
		t.Fatalf("decisions_for_path after the human check: %s", text)
	}
	if text := w.ok("propose_directive", map[string]any{"text": "Use the new logger", "role": "backend"}); !strings.Contains(text, "not in force") {
		t.Fatalf("propose: %s", text)
	}
	if text := w.ok("directives", map[string]any{}); !strings.Contains(text, "waiting for a publisher: 1") || strings.Contains(text, "new logger") {
		t.Fatalf("a proposal must not appear as an active directive: %s", text)
	}
}

// connect starts another dumen MCP server for the same project, as an agent could from a
// shell, and returns a client for it.
func (w *world) connect(impl *Server) *mcp.ClientSession {
	w.t.Helper()
	w.t.Cleanup(impl.Wait)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := New(impl).Connect(ctx, st, nil); err != nil {
		w.t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "rogue", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { cs.Close() })
	return cs
}

// Gauntlet K1. The hook writes the ticket when it answers "ask", before the person
// decides. After a declined prompt, an agent must not be able to spend that ticket by
// piping the same call into a `dumen mcp` it starts itself.
func TestDeclinedConfirmationCannotBeSpentByAnotherServer(t *testing.T) {
	w := setup(t)
	text := w.ok("draft_decision", map[string]any{"title": "Agents approve their own drafts", "body": "b"})
	id := text[strings.Index(text, "D-") : strings.Index(text, "D-")+6]
	args := map[string]any{"id": id}
	ask := func() {
		t.Helper()
		out := w.hook(hook.PreToolUse, map[string]any{"tool_name": "mcp__dumen__approve", "permission_mode": "default", "tool_input": args})
		if !strings.Contains(out, `"permissionDecision":"ask"`) {
			t.Fatalf("hook did not ask: %s", out)
		}
	}
	refused := func(cs *mcp.ClientSession, who string) {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "approve", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("%s spent a ticket the person never confirmed", who)
		}
	}
	// The person declines: Claude Code never calls the tool, the ticket stays behind.
	ask()
	// A server started from a shell under some other process.
	refused(w.connect(&Server{Dir: w.proj.Root, ParentPID: parentPID + 1, PID: 700001, Version: "test"}), "a server with another parent")
	ask()
	// A server started with `exec`, so that it has the same parent as the real one.
	refused(w.connect(&Server{Dir: w.proj.Root, ParentPID: parentPID, PID: 700002, Version: "test"}), "a second server with the same parent")

	if got := w.ok("get_decision", args); !strings.Contains(got, "status: draft") {
		t.Fatalf("the draft was approved without a person: %s", got)
	}
	// The real server still works when the person does confirm.
	if text, isErr := w.confirmed("approve", args); isErr {
		t.Fatalf("confirmed approve failed: %s", text)
	}
}

// Only modes where a person answers the prompt may get "ask".
func TestConfirmationOnlyInModesWithAPerson(t *testing.T) {
	w := setup(t)
	args := map[string]any{"id": "D-0000"}
	for _, mode := range []string{"auto", "dontAsk", "bypassPermissions", "", "someFutureMode"} {
		out := w.hook(hook.PreToolUse, map[string]any{"tool_name": "mcp__dumen__approve", "permission_mode": mode, "tool_input": args})
		if !strings.Contains(out, `"permissionDecision":"deny"`) {
			t.Errorf("mode %q: want deny, got %s", mode, out)
		}
		if _, isErr := w.call("approve", args); !isErr {
			t.Errorf("mode %q left a usable ticket", mode)
		}
	}
	for _, mode := range []string{"default", "acceptEdits", "plan"} {
		out := w.hook(hook.PreToolUse, map[string]any{"tool_name": "mcp__dumen__approve", "permission_mode": mode, "tool_input": args})
		if !strings.Contains(out, `"permissionDecision":"ask"`) {
			t.Errorf("mode %q: want ask, got %s", mode, out)
		}
	}
}
