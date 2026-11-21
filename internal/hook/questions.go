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
		ID       string            `json:"id"`
		Rule     string            `json:"rule"`
		Kind     string            `json:"kind"`
		Needs    map[string]string `json:"needs"`
		Email    string            `json:"email"`
		Role     string            `json:"role"`
		On       bool              `json:"on"`
		Name     string            `json:"name"`
		Services map[string]string `json:"services"`
		Topics   map[string]string `json:"topics"`
		Kinds    map[string]string `json:"kinds"`
		Text     string            `json:"text"`
		Days     float64           `json:"days"`
		All      bool              `json:"all"`
		Area     string            `json:"area"`
		Paths    []string          `json:"paths"`
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
		for _, svc := range model.SortedKeys(d.Needs) {
			fmt.Fprintf(&b, " It asks %s to: %q.", render.Label(svc, 40), render.Label(d.Needs[svc], model.MaxNeed))
		}
		for _, old := range d.Supersedes {
			b.WriteString(" It retires " + describe(old) + ".")
		}
		return b.String()
	case strings.HasSuffix(tool, "__supersede") && in.ID != "":
		kind := in.Kind
		if kind == "" {
			kind = "same kind"
		}
		out := fmt.Sprintf(" It retires %s and puts in force [%s] %q.", describe(in.ID), render.Label(kind, 20), render.Label(in.Rule, 200))
		for _, svc := range model.SortedKeys(in.Needs) {
			out += fmt.Sprintf(" It asks %s to: %q.", render.Label(svc, 40), render.Label(in.Needs[svc], model.MaxNeed))
		}
		return out
	case strings.HasSuffix(tool, "__reject") && in.ID != "":
		return " It rejects " + describe(in.ID) + "."
	case strings.HasSuffix(tool, "__withdraw_directive") && in.ID != "":
		d, _, err := store.ReadDirective(root, strings.ToUpper(strings.TrimSpace(in.ID)))
		if err != nil {
			return " It ends directive " + render.Label(in.ID, 12) + " (not found)."
		}
		return fmt.Sprintf(" It ends directive %s [%s] %q for every agent.", d.ID, render.Label(d.Kind, 10), render.Label(d.Text, 200))
	case strings.HasSuffix(tool, "__publish_directive"):
		// By id the proposal is published as stored: its text, kind, scope and expiry.
		if in.ID != "" {
			d, _, err := store.ReadDirective(root, strings.ToUpper(strings.TrimSpace(in.ID)))
			if err != nil {
				return " It publishes directive " + render.Label(in.ID, 12) + " (not found)."
			}
			return fmt.Sprintf(" It tells %s [%s] until %s: %q.", scopeText(d.Scope), render.Label(d.Kind, 10), render.Until(d.Expires), render.Label(d.Text, 200))
		}
		days := in.Days
		if days <= 0 {
			days = 7
		}
		scope := model.Scope{All: in.All, Role: in.Role, Area: in.Area, Paths: in.Paths}
		if scope.Role == "" && scope.Area == "" && len(scope.Paths) == 0 {
			scope.All = true
		}
		return fmt.Sprintf(" It tells %s [%s] for %g days: %q.", scopeText(scope), render.Label(orDefault(in.Kind, "context"), 10), days, render.Label(in.Text, 200))
	case strings.HasSuffix(tool, "__set_role") && in.Email != "":
		if strings.TrimSpace(in.Role) == "" {
			return fmt.Sprintf(" It removes %s from the team.", render.Label(in.Email, 80))
		}
		return fmt.Sprintf(" It gives %s the role %q.", render.Label(in.Email, 80), render.Label(in.Role, 30))
	case strings.HasSuffix(tool, "__set_private"):
		if in.On {
			return " It turns monitoring off for new sessions on this machine (activity is no longer logged)."
		}
		return " It turns monitoring back on for new sessions on this machine."
	case strings.HasSuffix(tool, "__check_ref") && in.ID != "":
		ref, err := store.ReadRef(root, strings.ToUpper(strings.TrimSpace(in.ID)))
		if err != nil {
			return " It confirms link " + render.Label(in.ID, 12) + " (not found)."
		}
		return fmt.Sprintf(" It confirms link %s %q (%s) as a source of truth.", ref.ID, render.Label(ref.Title, 120), render.Label(ref.URL, 200))
	case strings.HasSuffix(tool, "__set_product"):
		var b strings.Builder
		if in.Name != "" {
			fmt.Fprintf(&b, " It names the product %q.", render.Label(in.Name, 120))
		}
		shown := 0
		change := func(what string, m map[string]string, max int) {
			for _, k := range model.SortedKeys(m) {
				if shown++; shown > maxPromptChanges {
					continue
				}
				if strings.TrimSpace(m[k]) == "" {
					fmt.Fprintf(&b, " It removes %s %s.", what, render.Label(k, 40))
				} else {
					fmt.Fprintf(&b, " It sets %s %s to %q.", what, render.Label(k, 40), render.Label(m[k], max))
				}
			}
		}
		change("service", in.Services, 120)
		change("topic", in.Topics, 120)
		change("the approver role of kind", in.Kinds, 30)
		if shown > maxPromptChanges {
			fmt.Fprintf(&b, " ... and %d more changes.", shown-maxPromptChanges)
		}
		return b.String()
	}
	return ""
}

// maxPromptChanges caps the changes a set_product prompt lists one by one.
const maxPromptChanges = 12

// scopeText says whom a directive scope reaches, in bounded labels.
func scopeText(sc model.Scope) string {
	switch {
	case sc.Role != "":
		return "agents of role " + render.Label(sc.Role, 30)
	case sc.Area != "":
		return "area " + render.Label(sc.Area, 40)
	case len(sc.Paths) > 0:
		return "paths " + render.Label(strings.Join(sc.Paths, ", "), 120)
	}
	return "every agent"
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
