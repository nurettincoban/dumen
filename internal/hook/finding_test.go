package hook

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurettincoban/dumen/internal/op"
)

func ctxOf(out *Output) string {
	if out == nil || out.HookSpecificOutput == nil {
		return ""
	}
	return out.HookSpecificOutput.AdditionalContext
}

// A prompt that mentions a topic gets the approved decisions on it, once per session,
// and never the ones the session-start block already showed.
func TestPromptGetsDecisionsOnItsTopics(t *testing.T) {
	f := newFixture(t)
	f.submit(&op.SetProduct{Topics: map[string][]string{"booking-flow": {"booking page", "time picker"}}})
	prompt := map[string]any{"prompt": "Redo the time picker on the booking page"}
	// Shown at session start: no note.
	id := f.slotDecision(true)
	f.run(SessionStart, map[string]any{"source": "startup"})
	if got := ctxOf(f.run(UserPromptSubmit, prompt)); strings.Contains(got, id) {
		t.Fatalf("a decision already shown at session start was noted again: %s", got)
	}
	// A new session that was not shown it, for example after /clear and a later decision.
	in := map[string]any{"session_id": "sess-2", "prompt": prompt["prompt"]}
	got := ctxOf(f.run(UserPromptSubmit, in))
	if !strings.Contains(got, id) || !strings.Contains(got, "[ui] Guests pick a time from slot buttons") {
		t.Fatalf("no topic note for the prompt: %q", got)
	}
	if again := ctxOf(f.run(UserPromptSubmit, map[string]any{"session_id": "sess-2", "prompt": prompt["prompt"]})); strings.Contains(again, id) {
		t.Fatalf("the same decision was noted twice: %s", again)
	}
	// The prompt itself is never kept.
	for _, dir := range []string{f.proj.Store.SessionsDir(), f.proj.Store.OutboxDir()} {
		filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() {
				if data, _ := os.ReadFile(path); strings.Contains(string(data), "Redo the time picker") {
					t.Errorf("the prompt was stored in %s", path)
				}
			}
			return nil
		})
	}
	if other := ctxOf(f.run(UserPromptSubmit, map[string]any{"session_id": "sess-3", "prompt": "Fix the logout button"})); strings.Contains(other, id) {
		t.Fatalf("an unrelated prompt got the note: %s", other)
	}
}

// Proposed decisions never reach a session through a note.
func TestProposedDecisionIsNeverNoted(t *testing.T) {
	f := newFixture(t)
	f.submit(&op.SetProduct{Topics: map[string][]string{"booking-flow": {"booking page", "time picker"}}})
	id := f.slotDecision(false)
	if got := ctxOf(f.run(UserPromptSubmit, map[string]any{"prompt": "Redo the time picker on the booking page"})); strings.Contains(got, id) || strings.Contains(got, "slot buttons") {
		t.Fatalf("a proposed decision was noted: %s", got)
	}
	plan := map[string]any{"tool_name": PlanTool, "permission_mode": "plan", "tool_input": map[string]any{"plan": "1. Rebuild the time picker on the booking page"}}
	if got := ctxOf(f.run(PreToolUse, plan)); strings.Contains(got, id) {
		t.Fatalf("a proposed decision was noted for a plan: %s", got)
	}
}

// Leaving plan mode shows the decisions on the plan's topics, without blocking it.
func TestPlanIsCheckedAgainstDecisions(t *testing.T) {
	f := newFixture(t)
	f.submit(&op.SetProduct{Topics: map[string][]string{"booking-flow": {"booking page", "time picker"}}})
	id := f.slotDecision(true)
	out := f.run(PreToolUse, map[string]any{"tool_name": PlanTool, "permission_mode": "plan",
		"tool_input": map[string]any{"plan": "1. Replace the time picker on the booking page with a calendar"}})
	if decision(out) != "" {
		t.Fatalf("the plan was blocked: %+v", out)
	}
	if got := ctxOf(out); !strings.Contains(got, id) || !strings.Contains(got, "Check the plan against them") {
		t.Fatalf("no decisions for the plan: %q", got)
	}
}
