// Package activity answers questions about the activity log: what happened, what is
// done, who is stuck, and who has not acknowledged a directive.
package activity

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/policy"
	"github.com/nurettincoban/dumen/internal/store"
)

// Filter selects events. Zero values mean "any".
type Filter struct {
	Since   time.Time
	Until   time.Time
	Person  string // exact email, or a substring of it
	Kind    string
	Session string
	Repo    string
}

func (f Filter) match(e model.Event) bool {
	if f.Kind != "" && e.Kind != f.Kind {
		return false
	}
	if f.Person != "" && !strings.Contains(strings.ToLower(e.Person), strings.ToLower(f.Person)) {
		return false
	}
	if f.Repo != "" && !strings.EqualFold(e.Repo, f.Repo) {
		return false
	}
	if f.Session != "" && !strings.HasPrefix(e.Session, f.Session) {
		return false
	}
	if f.Since.IsZero() && f.Until.IsZero() {
		return true
	}
	ts, err := model.ParseTime(e.TS)
	if err != nil {
		return false
	}
	if !f.Since.IsZero() && ts.Before(f.Since) {
		return false
	}
	return f.Until.IsZero() || ts.Before(f.Until)
}

// Query returns the matching events in time order.
func Query(root string, f Filter) ([]model.Event, error) {
	days, err := store.ListDays(root)
	if err != nil {
		return nil, err
	}
	var out []model.Event
	for _, d := range days {
		if !f.Since.IsZero() && d < f.Since.UTC().Format("2006-01-02") {
			continue
		}
		if !f.Until.IsZero() && d > f.Until.UTC().Format("2006-01-02") {
			continue
		}
		evs, err := store.ReadEvents(root, d)
		if err != nil {
			return nil, err
		}
		for _, e := range evs {
			if f.match(e) {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// Session states.
const (
	StateActive   = "active"
	StateFinished = "finished"
	StateBlocked  = "blocked"
	StateAsking   = "asking human"
	StatePrivate  = "not monitored"
)

// Session is the summary of one agent session.
type Session struct {
	Session  string   `json:"session"`
	Person   string   `json:"person"`
	Repo     string   `json:"repo,omitempty"`
	First    string   `json:"first"`
	Last     string   `json:"last"`
	State    string   `json:"state"`
	LastText string   `json:"last_text,omitempty"`
	Events   int      `json:"events"`
	Acked    []string `json:"acked,omitempty"` // directive ids acknowledged
}

// Summary aggregates a set of events.
type Summary struct {
	Counts   map[string]int      `json:"counts"`            // events by kind
	ByRepo   map[string]int      `json:"by_repo,omitempty"` // sessions per repository, when the project spans several
	Sessions []Session           `json:"sessions"`          // ordered by first event, then session id
	Stuck    []Session           `json:"stuck"`             // sessions whose current state is blocked or asking a human
	Acks     map[string][]string `json:"acks"`              // directive id -> people whose agents acknowledged it
	// Violations are the events that report changes left under a deny directive.
	Violations []model.Event `json:"violations,omitempty"`
}

// Summarize computes the summary of events (which must be in time order).
func Summarize(events []model.Event) Summary {
	sum := Summary{Counts: map[string]int{}, Acks: map[string][]string{}}
	idx := map[string]*Session{}
	var order []string
	acks := map[string]map[string]bool{}
	for _, e := range events {
		sum.Counts[e.Kind]++
		s := idx[e.Session]
		if s == nil {
			s = &Session{Session: e.Session, Person: e.Person, Repo: e.Repo, First: e.TS, State: StateActive}
			idx[e.Session] = s
			order = append(order, e.Session)
		}
		s.Last = e.TS
		s.Events++
		// A resumed session logs "session resume"; that must not replace the last real summary.
		if e.Kind == model.EventViolation {
			sum.Violations = append(sum.Violations, e)
			continue // a finding about the session, not a step of it
		}
		if e.Text != "" && (e.Kind != model.EventStarted || s.LastText == "") {
			s.LastText = e.Text
		}
		switch e.Kind {
		case model.EventFinished:
			s.State = StateFinished
		case model.EventBlocked:
			s.State = StateBlocked
		case model.EventAskingHuman:
			s.State = StateAsking
		case model.EventPrivate:
			s.State = StatePrivate
		case model.EventStarted, model.EventProgress, model.EventDraftWritten:
			if s.State != StatePrivate {
				s.State = StateActive // the session moved on after being blocked or asking
			}
		case model.EventDirectiveAck:
			for _, id := range e.IDs {
				if acks[id] == nil {
					acks[id] = map[string]bool{}
				}
				acks[id][e.Person] = true
				if !contains(s.Acked, id) {
					s.Acked = append(s.Acked, id)
				}
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := idx[order[i]], idx[order[j]]
		if a.First != b.First {
			return a.First < b.First
		}
		return a.Session < b.Session
	})
	for _, id := range order {
		s := *idx[id]
		if s.Repo != "" {
			if sum.ByRepo == nil {
				sum.ByRepo = map[string]int{}
			}
			sum.ByRepo[s.Repo]++
		}
		sum.Sessions = append(sum.Sessions, s)
		if s.State == StateBlocked || s.State == StateAsking {
			sum.Stuck = append(sum.Stuck, s)
		}
	}
	for id, people := range acks {
		sum.Acks[id] = model.SortedKeys(people)
	}
	return sum
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Unacked returns, for a directive, the people in the roster whose agents have not
// acknowledged it. Only people the directive is delivered to are counted.
func Unacked(cfg model.Config, d model.Directive, sum Summary) []string {
	acked := map[string]bool{}
	for _, p := range sum.Acks[d.ID] {
		acked[strings.ToLower(p)] = true
	}
	// A directive limited to some repositories is only delivered to sessions working
	// there, so only the people seen there are expected to acknowledge it.
	repos := scopeRepos(cfg, d.Scope)
	seen := map[string]bool{}
	for _, s := range sum.Sessions {
		if repos[strings.ToLower(s.Repo)] {
			seen[strings.ToLower(s.Person)] = true
		}
	}
	var out []string
	for _, email := range model.SortedKeys(cfg.Roles) {
		if d.Scope.Role != "" && cfg.Roles[email] != d.Scope.Role {
			continue
		}
		if repos != nil && !seen[email] {
			continue
		}
		if email == strings.ToLower(d.PublishedBy) {
			continue // the publisher knows about their own directive
		}
		if !acked[email] {
			out = append(out, email)
		}
	}
	return out
}

// scopeRepos returns the repositories a path or area scope is limited to, or nil when
// the scope can touch every repository.
func scopeRepos(cfg model.Config, sc model.Scope) map[string]bool {
	patterns := sc.Paths
	if sc.Area != "" {
		patterns = cfg.Areas[sc.Area].Paths
	}
	if len(patterns) == 0 {
		return nil
	}
	repos := map[string]bool{}
	for _, p := range patterns {
		r := policy.PatternRepo(p)
		if r == "" {
			return nil
		}
		repos[r] = true
	}
	return repos
}

// ParseSince understands "today", "yesterday", a span such as "2h" or "7d", a date
// (2026-11-20, local midnight) or an RFC 3339 time.
func ParseSince(s string, now time.Time) (time.Time, error) {
	local := now.Local()
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "today":
		return midnight.UTC(), nil
	case "yesterday":
		return midnight.AddDate(0, 0, -1).UTC(), nil
	case "week":
		return midnight.AddDate(0, 0, -7).UTC(), nil
	}
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour).UTC(), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d).UTC(), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, local.Location()); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid --since %q (use today, yesterday, 2h, 7d, 2026-11-20)", s)
}
