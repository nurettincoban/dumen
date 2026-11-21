// Package cache is the local JSON snapshot hooks read. Hooks never touch git on the fast
// path: they read this one file, which is rebuilt after every fetch.
package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/policy"
	"github.com/nurettincoban/dumen/internal/store"
)

// StaleAfter is how old the last successful remote check may be before a hook tries one
// synchronous fetch.
const StaleAfter = 90 * time.Second

// Decision is the part of an approved decision that hooks need. Bodies stay in the store.
type Decision struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Rule      string          `json:"rule,omitempty"`
	Kind      string          `json:"kind,omitempty"`
	AppliesTo model.AppliesTo `json:"applies_to,omitempty"`
	Owner     string          `json:"owner,omitempty"`
	Area      string          `json:"area,omitempty"`
	Paths     []string        `json:"paths,omitempty"`
	Sources   []string        `json:"sources,omitempty"`
	Created   string          `json:"created"`
	Contested bool            `json:"contested,omitempty"`
	// For news: when and by whom it came into force, where it was made, what it retired.
	ApprovedAt string   `json:"approved_at,omitempty"`
	ApprovedBy string   `json:"approved_by,omitempty"`
	Repo       string   `json:"repo,omitempty"`
	Supersedes []string `json:"supersedes,omitempty"`
	// Needs is the work the decision asks of other services, by service.
	Needs map[string]string `json:"needs,omitempty"`
}

// Directive mirrors model.Directive with its text.
type Directive struct {
	model.Directive
	Text string `json:"text"`
}

// Cache is the snapshot.
type Cache struct {
	Version    int          `json:"version"`
	Head       string       `json:"head"`
	BuiltAt    string       `json:"built_at"`
	SyncedAt   string       `json:"synced_at"` // last time the remote was known to equal Head
	Config     model.Config `json:"config"`
	Directives []Directive  `json:"directives"` // status active; the time window is checked on read
	Decisions  []Decision   `json:"decisions"`  // approved and not superseded, newest first
	Refs       []model.Ref  `json:"refs"`       // links a person added or checked

	ConfigError string `json:"config_error,omitempty"` // why dumen.yml could not be read, if so

	UnverifiedRefs int `json:"unverified_refs,omitempty"` // links agents added, waiting for a person
	Drafts         int `json:"drafts"`
	Proposed       int `json:"proposed"` // proposed directives waiting for a publisher
}

const version = 4

// Build reads the checkout at root into a snapshot.
func Build(root, head string, now time.Time) (*Cache, error) {
	c := &Cache{Version: version, Head: head, BuiltAt: model.FormatTime(now), SyncedAt: model.FormatTime(now)}
	var err error
	if c.Config, err = store.ReadConfig(root); err != nil {
		// A broken dumen.yml must not switch the guard off. Without roles and areas
		// nobody can publish and area scopes match nothing, but every directive that is
		// already in force keeps being delivered and enforced.
		c.Config = model.DefaultConfig("")
		c.ConfigError = strings.Join(strings.Fields(err.Error()), " ")
	}
	dirs, err := store.ListDirectives(root)
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		switch d.Status {
		case model.DirectiveActive:
			if ex, err := model.ParseTime(d.Expires); err == nil && ex.After(now.Add(-24*time.Hour)) {
				c.Directives = append(c.Directives, Directive{Directive: d, Text: d.Text})
			}
		case model.DirectiveProposed:
			c.Proposed++
		}
	}
	decs, err := store.ListDecisions(root)
	if err != nil {
		return nil, err
	}
	derived := policy.Derive(decs)
	for _, d := range decs {
		switch d.Status {
		case model.StatusProposed:
			c.Drafts++
		case model.StatusApproved:
			if len(derived.SupersededBy[d.ID]) == 1 {
				continue // cleanly superseded: no longer in force
			}
			c.Decisions = append(c.Decisions, Decision{ID: d.ID, Title: d.Title, Rule: d.Rule, Kind: d.Kind,
				AppliesTo: d.AppliesTo, Owner: d.Owner, Area: d.Area, Paths: d.Paths,
				Sources: d.Sources, Created: d.Created, Contested: derived.Contested[d.ID],
				ApprovedAt: d.ApprovedAt, ApprovedBy: d.ApprovedBy, Repo: d.Repo, Supersedes: d.Supersedes, Needs: d.Needs})
		}
	}
	sort.SliceStable(c.Decisions, func(i, j int) bool {
		if c.Decisions[i].Created != c.Decisions[j].Created {
			return c.Decisions[i].Created > c.Decisions[j].Created
		}
		return c.Decisions[i].ID < c.Decisions[j].ID
	})
	refs, err := store.ListRefs(root)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		if r.Status == model.RefUnverified {
			c.UnverifiedRefs++ // never injected: no person has seen its text yet
			continue
		}
		c.Refs = append(c.Refs, r)
	}
	return c, nil
}

// Write stores the snapshot atomically.
func (c *Cache) Write(path string) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid()) // one name per process: two writers never share a temp file
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read loads the snapshot.
func Read(path string) (*Cache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Cache{}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Age returns how long ago the remote was last known to match the snapshot.
func (c *Cache) Age(now time.Time) time.Duration {
	t, err := model.ParseTime(c.SyncedAt)
	if err != nil {
		return time.Duration(1<<62 - 1)
	}
	return now.Sub(t)
}

// ActiveDirectives returns the directives in force at now that a session of the given
// role, working in the given repository ("" for none), receives.
func (c *Cache) ActiveDirectives(role, repo string, now time.Time) []Directive {
	var out []Directive
	for _, d := range c.Directives {
		full := d.Directive
		full.Text = d.Text
		if policy.Delivered(c.Config, full, role, repo, now) {
			out = append(out, d)
		}
	}
	return out
}

// DenyFor returns the active deny directive covering the repo-relative file, if any.
func (c *Cache) DenyFor(file string, now time.Time) (Directive, bool) {
	for _, d := range c.Directives {
		full := d.Directive
		full.Text = d.Text
		if full.Kind == model.DirectiveDeny && full.ActiveAt(now) && policy.CoversPath(c.Config, full, file) {
			return d, true
		}
	}
	return Directive{}, false
}

// DecisionsFor returns approved decisions linked to the repo-relative file by path or area.
func (c *Cache) DecisionsFor(file string) []Decision {
	var out []Decision
	for _, d := range c.Decisions {
		if policy.MatchAny(d.Paths, file) || (d.Area != "" && policy.InArea(c.Config, d.Area, file)) {
			out = append(out, d)
		}
	}
	return out
}

// RefsFor returns refs linked to the repo-relative file by path or area.
func (c *Cache) RefsFor(file string) []model.Ref {
	var out []model.Ref
	for _, r := range c.Refs {
		if policy.MatchAny(r.Paths, file) || (r.Area != "" && policy.InArea(c.Config, r.Area, file)) {
			out = append(out, r)
		}
	}
	return out
}

// DecisionsForRepo returns approved decisions that concern the repository, newest first.
func (c *Cache) DecisionsForRepo(repo string) []Decision {
	if repo == "" {
		return c.Decisions
	}
	var out []Decision
	for _, d := range c.Decisions {
		if c.Concerns(d, repo) {
			out = append(out, d)
		}
	}
	return out
}

// Concerns reports whether a decision applies in the repository: it names the service or
// no service, and its paths, if any, include paths of this repository. Every place that
// shows decisions to an agent uses this one rule.
func (c *Cache) Concerns(d Decision, repo string) bool {
	if repo == "" {
		return true
	}
	return d.ForService(repo) && policy.RelevantTo(c.Config, d.Paths, d.Area, repo)
}

// RefsForRepo returns refs that concern the repository.
func (c *Cache) RefsForRepo(repo string) []model.Ref {
	if repo == "" {
		return c.Refs
	}
	var out []model.Ref
	for _, r := range c.Refs {
		if policy.RelevantTo(c.Config, r.Paths, r.Area, repo) {
			out = append(out, r)
		}
	}
	return out
}
