package mcpserver

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/activity"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/render"
)

// The tools in this file let a person do in Claude Code what the CLI also offers: the
// agent prepares the call, and the human-only ones go through the permission prompt.

type roleIn struct {
	sessionOpt
	Email string `json:"email" jsonschema:"the teammate's git user.email"`
	Role  string `json:"role,omitempty" jsonschema:"pm, lead, designer, dev or another short word; empty removes the person"`
}

type productIn struct {
	sessionOpt
	Name     string            `json:"name,omitempty" jsonschema:"one line: what the team builds"`
	Services map[string]string `json:"services,omitempty" jsonschema:"service (repository name) -> one-line description; an empty description removes it"`
	Topics   map[string]string `json:"topics,omitempty" jsonschema:"topic -> comma-separated keywords that bring its decisions to an agent's attention; empty removes it"`
	Kinds    map[string]string `json:"kinds,omitempty" jsonschema:"kind -> the only role that may approve it; empty lets any team member approve"`
}

type newsIn struct {
	sessionOpt
	Since string `json:"since,omitempty" jsonschema:"today, yesterday, 2h, 7d (default), a date"`
	All   bool   `json:"all,omitempty" jsonschema:"every repository, not only this one"`
}

// Reject rejects a proposed decision. Human-confirmed only.
func (s *Server) Reject(ctx context.Context, in idIn) (string, error) {
	p, err := s.project()
	if err != nil {
		return "", err
	}
	out, note, err := s.submit(ctx, p, s.human(p), &op.ResolveDecision{ID: strings.ToUpper(strings.TrimSpace(in.ID)), Status: model.StatusRejected})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s is now rejected.%s", out.RecordID, note), nil
}

// WithdrawDirective ends an active directive early. Human-confirmed only.
func (s *Server) WithdrawDirective(ctx context.Context, in idIn) (string, error) {
	p, err := s.project()
	if err != nil {
		return "", err
	}
	out, note, err := s.submit(ctx, p, s.human(p), &op.SetDirectiveStatus{ID: strings.ToUpper(strings.TrimSpace(in.ID)), Status: model.DirectiveWithdrawn})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s is withdrawn.%s", out.RecordID, note), nil
}

// SetRole adds a teammate, changes their role, or removes them. Human-confirmed only.
func (s *Server) SetRole(ctx context.Context, in roleIn) (string, error) {
	p, err := s.project()
	if err != nil {
		return "", err
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	_, note, err := s.submit(ctx, p, s.human(p), &op.SetRole{Email: email, Role: strings.ToLower(strings.TrimSpace(in.Role))})
	if err != nil {
		return "", err
	}
	if in.Role == "" {
		return fmt.Sprintf("%s is removed from the team.%s", email, note), nil
	}
	return fmt.Sprintf("%s now has the role %s.%s", email, in.Role, note), nil
}

// SetProduct names the product, its services, topics and kind owners. Human-confirmed only.
func (s *Server) SetProduct(ctx context.Context, in productIn) (string, error) {
	p, err := s.project()
	if err != nil {
		return "", err
	}
	o := &op.SetProduct{Services: in.Services, Kinds: in.Kinds}
	if in.Name != "" {
		o.Product = &in.Name
	}
	if len(in.Topics) > 0 {
		o.Topics = map[string][]string{}
		for t, kws := range in.Topics {
			o.Topics[t] = strings.Split(kws, ",")
		}
	}
	_, note, err := s.submit(ctx, p, s.human(p), o)
	if err != nil {
		return "", err
	}
	return "dumen.yml is updated." + note, nil
}

// CheckRef marks a link an agent added as checked by a person. Human-confirmed only.
func (s *Server) CheckRef(ctx context.Context, in idIn) (string, error) {
	p, err := s.project()
	if err != nil {
		return "", err
	}
	out, note, err := s.submit(ctx, p, s.human(p), &op.CheckRef{ID: strings.ToUpper(strings.TrimSpace(in.ID))})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s is checked; other agents see it from now on.%s", out.RecordID, note), nil
}

// News lists the decisions that came into force recently, with what they replaced and
// what they need. Approved decisions only.
func (s *Server) News(ctx context.Context, in newsIn) (string, error) {
	p, err := s.project()
	if err != nil {
		return "", err
	}
	since := in.Since
	if since == "" {
		since = "7d"
	}
	from, err := activity.ParseSince(since, s.now())
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
	cut := model.FormatTime(from)
	var recent []int
	for i, d := range c.Decisions {
		if d.ApprovedAt >= cut && c.Concerns(d, service) {
			recent = append(recent, i)
		}
	}
	sort.SliceStable(recent, func(a, b int) bool { return c.Decisions[recent[a]].ApprovedAt > c.Decisions[recent[b]].ApprovedAt })
	if len(recent) == 0 {
		return "No decision came into force since " + from.Format(time.RFC3339) + "." + warn, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nDecisions in force since %s (%d), newest first:\n", notice, from.Format("2006-01-02 15:04 MST"), len(recent))
	for n, i := range recent {
		if n == maxTopicList {
			fmt.Fprintf(&b, "... and %d more\n", len(recent)-n)
			break
		}
		d := c.Decisions[i]
		fmt.Fprintf(&b, "- %s%s %s (approved by %s", d.ID, kindTag(d.Kind), render.Label(d.Headline(), model.MaxRule), dash(d.ApprovedBy))
		if d.Repo != "" {
			fmt.Fprintf(&b, " in %s", d.Repo)
		}
		if len(d.Supersedes) > 0 {
			fmt.Fprintf(&b, "; replaces %s", strings.Join(d.Supersedes, ", "))
		}
		b.WriteString(")\n")
		for _, svc := range model.SortedKeys(d.Needs) {
			fmt.Fprintf(&b, "  needs from %s: %s\n", svc, render.Label(d.Needs[svc], model.MaxNeed))
		}
	}
	return b.String() + warn, nil
}

type privateIn struct {
	On      bool   `json:"on" jsonschema:"true stops monitoring new sessions on this machine, false resumes it"`
	Session string `json:"session,omitempty" jsonschema:"the dumen session id from the session-start block; only needed if dumen asks for it"`
}

// SetPrivate turns monitoring of new sessions on this machine off or back on. It is the
// person's choice, so it is human-confirmed.
func (s *Server) SetPrivate(_ context.Context, in privateIn) (string, error) {
	p, err := s.project()
	if err != nil {
		return "", err
	}
	marker := p.PrivateMarker()
	if !in.On {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return "Monitoring resumed for new sessions on this machine.", nil
	}
	if err := os.WriteFile(marker, []byte(model.FormatTime(s.now())+"\n"), 0o644); err != nil {
		return "", err
	}
	return "New Claude Code sessions on this machine are not monitored: no activity is logged except one line saying the session is private. Directives and decisions are still delivered.", nil
}
