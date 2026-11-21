package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/session"
)

func slotQuestion(extra string) map[string]any {
	return map[string]any{"tool_name": QuestionTool, "permission_mode": "default", "tool_use_id": "toolu_1",
		"tool_input": map[string]any{"questions": []any{map[string]any{
			"question": "How should guests pick a time on the booking page?" + extra, "header": "Time picker",
			"options": []any{
				map[string]any{"label": "Slot buttons", "description": "Grouped by morning, afternoon and evening"},
				map[string]any{"label": "Calendar", "description": "A calendar with a free time picker"},
			}}}}}
}

func (f *fixture) slotDecision(approve bool) string {
	f.t.Helper()
	id := f.submit(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "Time selection",
		Rule: "Guests pick a time from slot buttons grouped by part of day", Kind: model.KindUI,
		AppliesTo: model.AppliesTo{Topics: []string{"booking-flow"}}}, Body: "Why.", Approve: approve})
	if _, err := f.proj.Refresh(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// A question an approved decision already answers is held back once; asking again goes
// through, so a wrong match costs one turn.
func TestQuestionHeldBackOnce(t *testing.T) {
	f := newFixture(t)
	f.submit(&op.SetProduct{Topics: map[string][]string{"booking-flow": {"booking page", "time picker", "slot"}}})
	id := f.slotDecision(true)

	out := f.run(PreToolUse, slotQuestion(""))
	if decision(out) != "deny" || !strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, id) ||
		!strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "slot buttons grouped") {
		t.Fatalf("question not held back with the decision: %+v", out)
	}
	if out := f.run(PreToolUse, slotQuestion("")); decision(out) == "deny" {
		t.Fatalf("the same question was held back twice: %+v", out)
	}
	// One keyword of a product-wide decision is not enough.
	weak := slotQuestion("")
	weak["tool_input"] = map[string]any{"questions": []any{map[string]any{"question": "Which slot size should the cache use?",
		"options": []any{map[string]any{"label": "Small"}, map[string]any{"label": "Large"}}}}}
	if out := f.run(PreToolUse, weak); decision(out) == "deny" {
		t.Fatalf("a weak match held a question back: %+v", out)
	}
}

// A proposed decision is an agent's wording that no owner has read: it holds nothing back.
func TestProposedDecisionDoesNotHoldBack(t *testing.T) {
	f := newFixture(t)
	f.submit(&op.SetProduct{Topics: map[string][]string{"booking-flow": {"booking page", "time picker", "slot"}}})
	f.slotDecision(false)
	if out := f.run(PreToolUse, slotQuestion("")); decision(out) == "deny" {
		t.Fatalf("a proposed decision held a question back: %+v", out)
	}
}

// The PostToolUse hook keeps the question and what came back, without the options.
func TestAnswerIsCaptured(t *testing.T) {
	f := newFixture(t)
	in := slotQuestion("")
	in["tool_response"] = map[string]any{"answers": map[string]any{"How should guests pick a time on the booking page?": "Slot buttons"}}
	out := f.run(PostToolUse, in)
	if out == nil || !strings.Contains(out.HookSpecificOutput.AdditionalContext, "record_answer") {
		t.Fatalf("no record_answer hint: %+v", out)
	}
	st := session.Load(f.proj.Store.SessionsDir(), "sess-1")
	if len(st.Answers) != 1 || len(st.Answers[0].Questions) != 1 || len(st.Answers[0].Questions[0].Options) != 2 {
		t.Fatalf("captured: %+v", st.Answers)
	}
	if raw := st.Answers[0].Raw; !strings.Contains(raw, "Slot buttons") || strings.Contains(raw, "Calendar") {
		t.Fatalf("raw should hold the answer and not the options: %q", raw)
	}
	other := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}
	if out := f.run(PostToolUse, other); out != nil {
		t.Fatalf("PostToolUse on another tool printed %+v", out)
	}
}

// The PostToolUse input Claude Code 2.1.330 sends for AskUserQuestion, captured from a
// live interactive session: the picked label is in tool_response.answers, and the
// response echoes every option.
func TestRealAnswerFixture(t *testing.T) {
	f := newFixture(t)
	data, err := os.ReadFile(filepath.Join("testdata", "askuserquestion-post-2.1.330.json"))
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatal(err)
	}
	if out := f.run(PostToolUse, in); out == nil {
		t.Fatal("no output for the real payload")
	}
	st := session.Load(f.proj.Store.SessionsDir(), "sess-1")
	if len(st.Answers) != 1 {
		t.Fatalf("captured: %+v", st.Answers)
	}
	a := st.Answers[0]
	if got := a.Picked["Which queue should orders use?"]; len(got) != 1 || got[0] != "Kafka" {
		t.Fatalf("picked: %+v", a.Picked)
	}
	if len(a.Questions) != 1 || len(a.Questions[0].Options) != 2 {
		t.Fatalf("questions: %+v", a.Questions)
	}
}
