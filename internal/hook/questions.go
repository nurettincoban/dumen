package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nurettincoban/dumen/internal/cache"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/render"
	"github.com/nurettincoban/dumen/internal/session"
	"github.com/nurettincoban/dumen/internal/store"
)

// QuestionTool is Claude Code's tool for asking the user a multiple-choice question.
const QuestionTool = "AskUserQuestion"

// maxRaw bounds what a session keeps of one question-tool call.
const maxRaw = 16 << 10

type questionInput struct {
	Questions []struct {
		Question string `json:"question"`
		Header   string `json:"header"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

func parseQuestions(raw json.RawMessage) []session.Question {
	var in questionInput
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	var out []session.Question
	for _, q := range in.Questions {
		sq := session.Question{Question: strings.TrimSpace(q.Question)}
		for _, o := range q.Options {
			if l := strings.TrimSpace(o.Label); l != "" {
				sq.Options = append(sq.Options, l)
			}
		}
		if sq.Question != "" {
			out = append(out, sq)
		}
	}
	return out
}

// questionText is what a question is matched on: the question, its header and its options.
func questionText(raw json.RawMessage) string {
	var in questionInput
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	var b strings.Builder
	for _, q := range in.Questions {
		b.WriteString(q.Question + "\n" + q.Header + "\n")
		for _, o := range q.Options {
			b.WriteString(o.Label + "\n" + o.Description + "\n")
		}
	}
	return b.String()
}

// holdBack answers the question tool. When an approved team decision may already answer
// the question, the question is held back once with that decision, so the agent follows
// it instead of asking its human again. Asking the same question again goes through, so
// a wrong match costs one turn. Only approved decisions hold questions back.
func (r *runCtx) holdBack() string {
	text := questionText(r.in.ToolInput)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	key := hex.EncodeToString(sum[:8])
	if session.Has(r.sess.Held, key) {
		return ""
	}
	var strong []cache.Match
	for _, m := range r.c.MatchText(text, r.proj.Repo) {
		if m.Strong() {
			strong = append(strong, m)
		}
	}
	if len(strong) == 0 {
		return ""
	}
	if len(strong) > 3 {
		strong = strong[:3]
	}
	r.sess.Held = append(r.sess.Held, key)
	var b strings.Builder
	b.WriteString("dumen: the team may already have decided this. ")
	if len(strong) == 1 {
		b.WriteString("This approved decision looks related:\n")
	} else {
		b.WriteString("These approved decisions look related:\n")
	}
	ids := make([]string, 0, len(strong))
	for _, m := range strong {
		d := m.Decision
		ids = append(ids, d.ID)
		kind := ""
		if d.Kind != "" {
			kind = " [" + render.Label(d.Kind, 20) + "]"
		}
		fmt.Fprintf(&b, "- %s%s %s\n", d.ID, kind, render.Label(d.Headline(), model.MaxRule))
	}
	b.WriteString("If one of them answers your question, follow it and tell your human which decision you followed; do not ask. " +
		"If your human wants it changed, that is a supersede, which every agent then gets as news. " +
		"If none does, ask your question again unchanged and it will go through. Use get_decision for the full text.")
	r.log(model.EventDecisionRead, "question held back: a team decision may answer it", ids...)
	return b.String()
}

// postToolUse keeps what the agent asked its human and what came back, so that
// record_answer can tell an answer the human gave from one the agent only reports.
func (r *runCtx) postToolUse() *Output {
	if r.in.ToolName != QuestionTool {
		return nil
	}
	qs := parseQuestions(r.in.ToolInput)
	if len(qs) == 0 {
		return nil
	}
	// What came back: the tool's response, and the input's "answers" field, which is where
	// some versions put the picked labels. The options themselves are left out, or every
	// option would look picked.
	var withAnswers struct {
		Answers json.RawMessage `json:"answers"`
	}
	json.Unmarshal(r.in.ToolInput, &withAnswers)
	raw := string(r.in.ToolResponse) + "\n" + string(withAnswers.Answers)
	if len(raw) > maxRaw {
		raw = raw[:maxRaw]
	}
	picked := pickedAnswers(r.in.ToolResponse)
	for q, v := range pickedAnswers(r.in.ToolInput) {
		if _, ok := picked[q]; !ok {
			picked[q] = v
		}
	}
	if len(picked) == 0 {
		picked = nil
	}
	r.sess.AddAnswer(session.Answer{At: model.FormatTime(r.now), ToolUseID: r.in.ToolUseID, Questions: qs, Picked: picked, Raw: raw})
	r.sess.Save()
	return &Output{HookSpecificOutput: &Specific{HookEventName: "PostToolUse", AdditionalContext: "[dumen] Your human answered. " +
		"If this was a lasting choice that teammates or other agents will meet again (product, UX, UI, architecture or technical), " +
		"save it for the team with the dumen record_answer tool, once per question. Skip choices local to one file or function."}}
}

// whatIsDecided describes, for the permission prompt of approve and supersede, what the
// person is about to put in force and what stops being in force, since the prompt is the
// one place a person reads before saying yes.
func (r *runCtx) whatIsDecided(tool string) string {
	var in struct {
		ID   string `json:"id"`
		Rule string `json:"rule"`
		Kind string `json:"kind"`
	}
	json.Unmarshal(r.in.ToolInput, &in)
	root := r.proj.Store.Root()
	describe := func(id string) string {
		d, _, err := store.ReadDecision(root, strings.ToUpper(strings.TrimSpace(id)))
		if err != nil {
			return render.Label(id, 12) + " (not found)"
		}
		return fmt.Sprintf("%s [%s] %q", d.ID, render.Label(orDefault(d.Kind, "no kind"), 20), render.Label(d.Headline(), 200))
	}
	switch {
	case strings.HasSuffix(tool, "__approve") && in.ID != "":
		d, _, err := store.ReadDecision(root, strings.ToUpper(strings.TrimSpace(in.ID)))
		if err != nil {
			return ""
		}
		var b strings.Builder
		b.WriteString(" It puts in force " + describe(d.ID) + ".")
		for _, old := range d.Supersedes {
			b.WriteString(" It retires " + describe(old) + ".")
		}
		return b.String()
	case strings.HasSuffix(tool, "__supersede") && in.ID != "":
		kind := in.Kind
		if kind == "" {
			kind = "same kind"
		}
		return fmt.Sprintf(" It retires %s and puts in force [%s] %q.", describe(in.ID), render.Label(kind, 20), render.Label(in.Rule, 200))
	}
	return ""
}

// pickedAnswers reads a structured "answers" field, question text to the picked label
// or labels, from a tool input or response. Anything else yields an empty map.
func pickedAnswers(raw json.RawMessage) map[string][]string {
	out := map[string][]string{}
	var v struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return out
	}
	for q, a := range v.Answers {
		var one string
		var many []string
		switch {
		case json.Unmarshal(a, &one) == nil:
			many = []string{one}
		case json.Unmarshal(a, &many) == nil:
		default:
			continue
		}
		if len(out) < 16 {
			out[render.Label(q, 500)] = many
		}
	}
	return out
}
