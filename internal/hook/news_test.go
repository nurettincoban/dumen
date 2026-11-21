package hook

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
)

func (f *fixture) approved(title, rule string, supersedes ...string) string {
	f.t.Helper()
	id := f.submit(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: title, Rule: rule, Kind: model.KindProduct,
		Repo: "booking-svc", Supersedes: supersedes, AppliesTo: model.AppliesTo{Topics: []string{"payments"}}}, Body: "Why.", Approve: true})
	if _, err := f.proj.Refresh(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// A decision approved while a session runs reaches it on its next step, once, with who
// approved it, where, and what it replaced.
func TestDecisionNewsReachesRunningSessions(t *testing.T) {
	f := newFixture(t)
	old := f.approved("Refunds", "Cancelled bookings are refunded to the card")
	f.now = f.now.Add(-time.Hour) // the session started before the changes below
	start := ctxOf(f.run(SessionStart, map[string]any{"source": "startup"}))
	if strings.Contains(start, "CHANGED SINCE") {
		t.Fatalf("the first session here has nothing to compare with: %s", start)
	}
	edit := map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "README.md"}}
	if got := ctxOf(f.run(PreToolUse, edit)); strings.Contains(got, "DECISION NEWS") {
		t.Fatalf("a decision in force at session start came as news: %s", got)
	}
	nw := f.approved("Refunds", "Cancelled bookings are refunded as store credit", old)
	got := ctxOf(f.run(PreToolUse, edit))
	for _, want := range []string{"DECISION NEWS (1)", nw, "refunded as store credit", "replaces " + old, "in booking-svc"} {
		if !strings.Contains(got, want) {
			t.Errorf("news lacks %q: %s", want, got)
		}
	}
	if again := ctxOf(f.run(UserPromptSubmit, map[string]any{"prompt": "carry on"})); strings.Contains(again, nw) {
		t.Fatalf("news was given twice: %s", again)
	}
	// The next session in this repository says what changed since the previous one.
	later := f.approved("Deposits", "Bookings over 8 guests take a 20% deposit")
	next := ctxOf(f.run(SessionStart, map[string]any{"session_id": "sess-9", "source": "startup"}))
	if !strings.Contains(next, "CHANGED SINCE THE LAST SESSION HERE (2)") || !strings.Contains(next, later) || !strings.Contains(next, "replaces "+old) {
		t.Fatalf("session start after a change:\n%s", next)
	}
}

// Proposed decisions are never news.
func TestProposedDecisionIsNotNews(t *testing.T) {
	f := newFixture(t)
	f.run(SessionStart, map[string]any{"source": "startup"})
	id := f.slotDecision(false)
	if got := ctxOf(f.run(UserPromptSubmit, map[string]any{"prompt": "go on"})); strings.Contains(got, id) {
		t.Fatalf("a proposed decision came as news: %s", got)
	}
}

// News that arrives while the agent ends its turn stops it once, so it does not report
// "done" on work a teammate's decision just changed.
func TestStopHandsOverNewsOnce(t *testing.T) {
	f := newFixture(t)
	f.now = f.now.Add(-time.Hour)
	f.run(SessionStart, map[string]any{"source": "startup"})
	id := f.approved("Holds", "A booking is held for 10 minutes while the guest pays")
	out := f.run(Stop, map[string]any{"last_assistant_message": "Done."})
	if out == nil || out.Decision != "block" || !strings.Contains(out.Reason, id) {
		t.Fatalf("stop did not hand over the news: %+v", out)
	}
	if again := f.run(Stop, map[string]any{"last_assistant_message": "Done.", "stop_hook_active": true}); again != nil && again.Decision == "block" {
		t.Fatalf("the agent was stopped twice for the same news: %+v", again)
	}
}

// Gauntlet N1: news given to a subagent still reaches the main agent, and the main
// agent's Stop still hands it over.
func TestSubagentDoesNotUseUpNews(t *testing.T) {
	f := newFixture(t)
	f.now = f.now.Add(-time.Hour)
	f.run(SessionStart, map[string]any{"source": "startup"})
	id := f.approved("Holds", "A booking is held for 10 minutes while the guest pays")
	read := func(agent string) map[string]any {
		in := map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "README.md"}}
		if agent != "" {
			in["agent_id"] = agent
		}
		return in
	}
	if got := ctxOf(f.run(PreToolUse, read("sub1"))); !strings.Contains(got, id) {
		t.Fatalf("the subagent did not get the news: %s", got)
	}
	if got := ctxOf(f.run(PreToolUse, read("sub1"))); strings.Contains(got, id) {
		t.Fatalf("the subagent got the news twice: %s", got)
	}
	if got := ctxOf(f.run(PreToolUse, read(""))); !strings.Contains(got, id) {
		t.Fatalf("the main agent lost the news to its subagent: %s", got)
	}
}

// A decision the person approved outside this session, in the same repository, is
// still news here: they may run two sessions (gauntlet N2). Only the session whose own
// server approved it is spared; see mcpserver.TestOwnApprovalIsNewsOnlyElsewhere.
func TestSamePersonOtherSessionGetsNews(t *testing.T) {
	f := newFixture(t)
	f.now = f.now.Add(-time.Hour)
	f.run(SessionStart, map[string]any{"source": "startup"})
	f.now = f.now.Add(time.Hour)
	mine := f.submit(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "Mine", Rule: "Cancelled bookings are refunded as store credit",
		Kind: model.KindProduct}, Body: "Why.", Approve: true})
	if _, err := f.proj.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ctxOf(f.run(PreToolUse, map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "x"}})); !strings.Contains(got, mine) || !strings.Contains(got, "approved by you") {
		t.Errorf("the same person's decision from another session must be news here:\n%s", got)
	}
}
