package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/nurettincoban/dumen/internal/cache"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/render"
)

type topicIn struct {
	Topic string `json:"topic,omitempty" jsonschema:"a topic name, such as payments or booking-flow"`
	Text  string `json:"text,omitempty" jsonschema:"or: a plan, a question or a task; decisions on the topics it mentions are listed"`
	All   bool   `json:"all,omitempty" jsonschema:"include decisions made for other services"`
}

// maxTopicList bounds one decisions_for_topic answer.
const maxTopicList = 20

func kindTag(kind string) string {
	if kind == "" {
		return ""
	}
	return " [" + render.Label(kind, 20) + "]"
}

// DecisionsForTopic lists approved decisions by topic, or by the topics a text mentions.
// Only approved decisions are listed: proposed ones are agent-written and unreviewed.
func (s *Server) DecisionsForTopic(ctx context.Context, in topicIn) (string, error) {
	topic := strings.ToLower(strings.TrimSpace(in.Topic))
	if topic == "" && strings.TrimSpace(in.Text) == "" {
		return "", fmt.Errorf("give a topic or a text")
	}
	p, err := s.project()
	if err != nil {
		return "", err
	}
	c, warn := s.snapshot(ctx, p)
	if c == nil {
		return "", fmt.Errorf("no local data yet")
	}
	service := p.Repo
	if in.All {
		service = ""
	}
	var found []cache.Decision
	if topic != "" {
		for _, d := range c.Decisions {
			if !c.Concerns(d, service) {
				continue
			}
			for _, t := range d.Topics() {
				if strings.EqualFold(t, topic) {
					found = append(found, d)
					break
				}
			}
		}
	} else {
		for _, m := range c.MatchText(in.Text, service) {
			found = append(found, m.Decision)
		}
	}
	what := "topic " + render.Label(topic, 40)
	if topic == "" {
		what = "the topics of that text"
	}
	if len(found) == 0 {
		known := map[string]bool{}
		for _, t := range model.SortedKeys(c.Config.Topics) {
			known[t] = true
		}
		for _, d := range c.Decisions {
			for _, t := range d.AppliesTo.Topics {
				known[t] = true
			}
		}
		msg := "No approved decision on " + what + "."
		if len(known) > 0 {
			msg += " Topics with decisions or keywords: " + render.Label(strings.Join(model.SortedKeys(known), ", "), 400) + "."
		}
		return msg + warn, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nApproved decisions on %s (%d):\n", notice, what, len(found))
	for i, d := range found {
		if i == maxTopicList {
			fmt.Fprintf(&b, "... and %d more; narrow the topic.\n", len(found)-i)
			break
		}
		scope := d.ScopeLabel()
		fmt.Fprintf(&b, "- %s%s %s (%s; topics: %s)\n", d.ID, kindTag(d.Kind), render.Label(d.Headline(), model.MaxRule),
			render.Label(scope, 120), render.Label(strings.Join(d.Topics(), ", "), 120))
	}
	b.WriteString("Follow them; do not ask your human again what they settle. get_decision has the reasons.")
	return b.String() + warn, nil
}
