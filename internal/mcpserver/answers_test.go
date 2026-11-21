package mcpserver

import (
	"strings"
	"testing"

	"github.com/nurettincoban/dumen/internal/hook"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/store"
)

const slotQ = "How should guests pick a time on the booking page?"

func (w *world) askSlot(answer any) {
	w.hook(hook.PostToolUse, map[string]any{"tool_name": hook.QuestionTool, "tool_use_id": "toolu_1",
		"tool_input": map[string]any{"questions": []any{map[string]any{"question": slotQ, "header": "Time",
			"options": []any{map[string]any{"label": "Slot buttons"}, map[string]any{"label": "Calendar"}}}}},
		"tool_response": answer})
}

func (w *world) saved(text string) model.Decision {
	w.t.Helper()
	i := strings.Index(text, "Saved as D-")
	if i < 0 {
		w.t.Fatalf("not saved: %s", text)
	}
	id := strings.Fields(text[i+len("Saved as "):])[0]
	d, _, err := store.ReadDecision(w.proj.Store.Root(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return d
}

// An answer the hook saw in the question tool is saved as a proposed decision that
// records the question, the options and the pick.
func TestRecordAnswerVerified(t *testing.T) {
	w := setup(t)
	w.askSlot(map[string]any{"answers": map[string]any{slotQ: "Slot buttons"}})
	text := w.ok("record_answer", map[string]any{"question": slotQ, "answer": "Slot buttons", "kind": "ui",
		"rule": "Guests pick a time from slot buttons grouped by part of day", "topics": []string{"booking-flow"}})
	d := w.saved(text)
	if d.Status != model.StatusProposed || d.Kind != model.KindUI || d.Origin == nil || !d.Origin.Verified ||
		len(d.Origin.Options) != 2 || !strings.Contains(d.Body, "- Slot buttons (picked)") {
		t.Fatalf("saved decision: %+v\n%s", d, d.Body)
	}
	if !strings.Contains(text, "may approve ui decisions") {
		t.Fatalf("the lead should be told they can approve it: %s", text)
	}
	// Saving the same choice again lists it instead.
	again := w.ok("record_answer", map[string]any{"question": "Time picker on the staff console?", "answer": "Slot buttons", "kind": "ui",
		"rule": "Staff pick times from slot buttons", "topics": []string{"booking-flow"}})
	if !strings.Contains(again, "Nothing was saved") || !strings.Contains(again, d.ID) {
		t.Fatalf("a similar decision was not listed: %s", again)
	}
	forced := w.ok("record_answer", map[string]any{"question": "Time picker on the staff console?", "answer": "Slot buttons", "kind": "ui",
		"rule": "Staff pick times from slot buttons", "topics": []string{"booking-flow"}, "new": true})
	if w.saved(forced).Origin.Verified {
		t.Fatal("an answer the hook never saw was marked verified")
	}
}

// What the agent reports is not proof: an answer missing from the question tool's
// response, or a response that echoes every option, is saved unverified.
func TestRecordAnswerUnverified(t *testing.T) {
	w := setup(t)
	w.askSlot(map[string]any{"answers": map[string]any{slotQ: "Calendar"}})
	d := w.saved(w.ok("record_answer", map[string]any{"question": slotQ, "answer": "Slot buttons", "kind": "ui", "rule": "r"}))
	if d.Origin.Verified {
		t.Fatal("an answer the human did not pick was marked verified")
	}
	w2 := setup(t)
	w2.askSlot(map[string]any{"options": []string{"Slot buttons", "Calendar"}})
	d = w2.saved(w2.ok("record_answer", map[string]any{"question": slotQ, "answer": "Calendar", "kind": "ui", "rule": "r"}))
	if d.Origin.Verified {
		t.Fatal("a response that lists every option verified an answer")
	}
}

// An agent cannot approve through record_answer, and a rule and kind are required.
func TestRecordAnswerNeverApproves(t *testing.T) {
	w := setup(t)
	for _, args := range []map[string]any{
		{"question": slotQ, "answer": "Slot buttons", "kind": "ui"},
		{"question": slotQ, "answer": "Slot buttons", "rule": "r"},
		{"question": slotQ, "rule": "r", "kind": "ui"},
	} {
		if text, isErr := w.call("record_answer", args); !isErr {
			t.Fatalf("missing field accepted: %v -> %s", args, text)
		}
	}
	w.askSlot(map[string]any{"answers": map[string]any{slotQ: "Slot buttons"}})
	d := w.saved(w.ok("record_answer", map[string]any{"question": slotQ, "answer": "Slot buttons", "kind": "ui", "rule": "r"}))
	if d.Status != model.StatusProposed || d.ApprovedBy != "" || d.AuthorKind != model.KindAgent {
		t.Fatalf("record_answer wrote %+v", d)
	}
}
