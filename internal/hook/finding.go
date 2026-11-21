package hook

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nurettincoban/dumen/internal/cache"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/render"
	"github.com/nurettincoban/dumen/internal/session"
)

// PlanTool is Claude Code's tool for leaving plan mode with a plan.
const PlanTool = "ExitPlanMode"

// maxNote is how many decisions one topic note names.
const maxNote = 3

// startDecisions returns the approved decisions that concern this repository: first
// those made for this service, then the product-wide ones, each newest first.
func (r *runCtx) startDecisions() []cache.Decision {
	var mine, wide []cache.Decision
	for _, d := range r.c.DecisionsForRepo(r.proj.Repo) {
		switch {
		case r.forThisRepo(d):
			mine = append(mine, d)
		default:
			wide = append(wide, d)
		}
	}
	return append(mine, wide...)
}

// forThisRepo reports whether a decision was made for this service in particular: it
// names the service, or is linked to paths (which DecisionsForRepo found to be here).
func (r *runCtx) forThisRepo(d cache.Decision) bool {
	return r.proj.Repo != "" && (!d.ProductWide() || len(d.Paths) > 0)
}

// decisionsBlock renders the first n of the decisions from startDecisions, grouped.
func (r *runCtx) decisionsBlock(ds []cache.Decision, n int) string {
	if len(ds) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nAPPROVED DECISIONS (%d of %d; follow them, and do not ask your human again what they settle)\n", n, len(ds))
	group := ""
	for _, d := range ds[:n] {
		g := "Product-wide:"
		if r.forThisRepo(d) {
			g = "For " + r.proj.Repo + ":"
		}
		if g != group {
			group = g
			b.WriteString(g + "\n")
		}
		b.WriteString(decisionLine(d) + "\n")
	}
	if topics := r.topicNames(); len(topics) > 0 {
		fmt.Fprintf(&b, "Decision topics: %s (decisions_for_topic lists a topic's decisions)\n", render.Label(strings.Join(topics, ", "), 400))
	}
	return b.String()
}

// topicNames lists the topics the team declared or used, sorted.
func (r *runCtx) topicNames() []string {
	seen := map[string]bool{}
	for _, t := range model.SortedKeys(r.c.Config.Topics) {
		seen[t] = true
	}
	for _, d := range r.c.Decisions {
		for _, t := range d.AppliesTo.Topics {
			seen[t] = true
		}
	}
	return model.SortedKeys(seen)
}

// topicNote names the approved decisions whose topics text mentions and that the agent
// has not been shown in this session. The text itself is never stored or logged.
func (r *runCtx) topicNote(text, where string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var picked []cache.Match
	for _, m := range r.c.MatchText(text, r.proj.Repo) {
		if session.Has(r.sess.Noted, m.Decision.ID) {
			continue
		}
		picked = append(picked, m)
		if len(picked) == maxNote {
			break
		}
	}
	if len(picked) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Approved team decisions on topics in %s:\n", where)
	for _, m := range picked {
		r.sess.Noted = session.Add(r.sess.Noted, m.Decision.ID)
		b.WriteString(decisionLine(m.Decision) + "\n")
	}
	b.WriteString("Follow them where they apply; get_decision has the full text.")
	// Not logged: the ids would tell the team which topics a person's message touched.
	return b.String()
}

// planNote names, when the agent leaves plan mode, approved decisions on the plan's
// topics, so that the agent can check its plan against them before it starts. It never
// blocks the plan.
func (r *runCtx) planNote() string {
	var in struct {
		Plan string `json:"plan"`
	}
	json.Unmarshal(r.in.ToolInput, &in)
	if strings.TrimSpace(in.Plan) == "" {
		return ""
	}
	var b strings.Builder
	var ids []string
	for _, m := range r.c.MatchText(in.Plan, r.proj.Repo) {
		if len(ids) == 5 {
			break
		}
		ids = append(ids, m.Decision.ID)
		if r.in.AgentID == "" { // a subagent's context is its own; the main agent still gets the note
			r.sess.Noted = session.Add(r.sess.Noted, m.Decision.ID)
		}
		b.WriteString(decisionLine(m.Decision) + "\n")
	}
	if len(ids) == 0 {
		return ""
	}
	r.log(model.EventDecisionRead, "decisions noted for the plan", ids...)
	return "Approved team decisions on the topics of your plan:\n" + b.String() +
		"Check the plan against them. If a step goes against one, change the step, or tell your human which decision it conflicts with; " +
		"a decision changes only by supersede, which a human approves."
}
