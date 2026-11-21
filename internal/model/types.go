package model

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// TimeFormat is the layout of every timestamp dumen writes (UTC, second precision).
const TimeFormat = "2006-01-02T15:04:05Z"

// FormatTime renders t in the canonical layout.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// ParseTime parses a canonical timestamp (any RFC 3339 value is accepted).
func ParseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// Actor kinds.
const (
	KindHuman = "human"
	KindAgent = "agent"
)

// Decision statuses.
const (
	StatusDraft    = "draft"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// Directive kinds and statuses.
const (
	DirectiveContext = "context"
	DirectiveDeny    = "deny"

	DirectiveProposed  = "proposed"
	DirectiveActive    = "active"
	DirectiveWithdrawn = "withdrawn"
	DirectivePromoted  = "promoted"
)

// Activity event kinds.
const (
	EventStarted      = "started"
	EventDirectiveAck = "directive_ack"
	EventDecisionRead = "decision_read"
	EventDraftWritten = "draft_written"
	EventBlocked      = "blocked"
	EventAskingHuman  = "asking_human"
	EventProgress     = "progress"
	EventFinished     = "finished"
	EventPrivate      = "private" // session marked as not monitored; the fact itself is visible
	// EventViolation records that a session left changes under a deny directive, whatever
	// tool made them. Only the Stop hook writes it.
	EventViolation = "violation"
)

// EventKinds lists every valid event kind.
var EventKinds = []string{EventStarted, EventDirectiveAck, EventDecisionRead, EventDraftWritten,
	EventBlocked, EventAskingHuman, EventProgress, EventFinished, EventPrivate, EventViolation}

// MaxEventText is the maximum length of an event's text in characters.
const MaxEventText = 200

// Config is dumen.yml.
type Config struct {
	Version  int               `yaml:"version"`
	Project  string            `yaml:"project"`
	Roles    map[string]string `yaml:"roles"` // git email -> role
	Areas    map[string]Area   `yaml:"areas,omitempty"`
	Settings Settings          `yaml:"settings"`
}

// Area maps a named part of the product to path prefixes and owners.
type Area struct {
	Paths  []string `yaml:"paths"`
	Owners []string `yaml:"owners,omitempty"` // git emails
}

// Settings are team-wide tunables.
type Settings struct {
	Publishers          []string `yaml:"publishers"` // roles that may publish directives and approve decisions
	MaxActiveDirectives int      `yaml:"max_active_directives"`
	DraftArchiveDays    int      `yaml:"draft_archive_days"`
	RefStaleDays        int      `yaml:"ref_stale_days"`
	Claims              bool     `yaml:"claims"`
}

// DefaultConfig returns the configuration written by `dumen init`.
func DefaultConfig(project string) Config {
	return Config{
		Version: 1,
		Project: project,
		Roles:   map[string]string{},
		Settings: Settings{
			Publishers:          []string{"pm", "lead"},
			MaxActiveDirectives: 10,
			DraftArchiveDays:    7,
			RefStaleDays:        30,
		},
	}
}

// Role returns the role of the person with the given git email ("" when unknown).
func (c Config) Role(email string) string {
	return c.Roles[strings.ToLower(strings.TrimSpace(email))]
}

// CanPublish reports whether email holds a role allowed to publish directives.
func (c Config) CanPublish(email string) bool {
	role := c.Role(email)
	if role == "" {
		return false
	}
	for _, p := range c.Settings.Publishers {
		if p == role {
			return true
		}
	}
	return false
}

// CanApprove reports whether email may approve a decision in the given area:
// publisher roles, plus the owners of that area.
func (c Config) CanApprove(email, area string) bool {
	if c.CanPublish(email) {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if a, ok := c.Areas[area]; ok {
		for _, o := range a.Owners {
			if strings.ToLower(o) == email {
				return true
			}
		}
	}
	return false
}

// Actor is who performs an operation.
type Actor struct {
	Email string `json:"email" yaml:"email"`
	Kind  string `json:"kind" yaml:"kind"` // human | agent
}

// Decision is a team decision. The body holds: decision, why, rejected alternatives.
type Decision struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Status     string   `yaml:"status"`
	Area       string   `yaml:"area,omitempty"`
	Paths      []string `yaml:"paths,omitempty"`
	Sources    []string `yaml:"sources,omitempty"` // ref ids
	Author     string   `yaml:"author"`
	AuthorKind string   `yaml:"author_kind"`
	ApprovedBy string   `yaml:"approved_by,omitempty"`
	Supersedes []string `yaml:"supersedes,omitempty"`
	AnchoredAt string   `yaml:"anchored_at,omitempty"` // main sha
	Created    string   `yaml:"created"`
	Body       string   `yaml:"-"`
}

// Validate checks field-level rules.
func (d Decision) Validate() error {
	if !ValidID(d.ID, PrefixDecision) {
		return fmt.Errorf("decision: invalid id %q", d.ID)
	}
	if strings.TrimSpace(d.Title) == "" {
		return fmt.Errorf("decision %s: title is required", d.ID)
	}
	switch d.Status {
	case StatusDraft, StatusApproved, StatusRejected:
	default:
		return fmt.Errorf("decision %s: invalid status %q", d.ID, d.Status)
	}
	if d.AuthorKind != KindHuman && d.AuthorKind != KindAgent {
		return fmt.Errorf("decision %s: invalid author_kind %q", d.ID, d.AuthorKind)
	}
	if d.Author == "" {
		return fmt.Errorf("decision %s: author is required", d.ID)
	}
	if d.Status == StatusApproved && d.ApprovedBy == "" {
		return fmt.Errorf("decision %s: approved without approved_by", d.ID)
	}
	for _, s := range d.Supersedes {
		if !ValidID(s, PrefixDecision) {
			return fmt.Errorf("decision %s: invalid supersedes id %q", d.ID, s)
		}
		if s == d.ID {
			return fmt.Errorf("decision %s: cannot supersede itself", d.ID)
		}
	}
	for _, s := range d.Sources {
		if !ValidID(s, PrefixRef) {
			return fmt.Errorf("decision %s: invalid source ref id %q", d.ID, s)
		}
	}
	if _, err := ParseTime(d.Created); err != nil {
		return fmt.Errorf("decision %s: invalid created time: %v", d.ID, err)
	}
	return validatePaths(d.Paths)
}

// Scope says which sessions a directive applies to. Exactly one field is set.
type Scope struct {
	All   bool     `yaml:"all,omitempty" json:"all,omitempty"`
	Role  string   `yaml:"role,omitempty" json:"role,omitempty"`
	Area  string   `yaml:"area,omitempty" json:"area,omitempty"`
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
}

// Validate checks that exactly one scope selector is set.
func (s Scope) Validate() error {
	n := 0
	if s.All {
		n++
	}
	if s.Role != "" {
		n++
	}
	if s.Area != "" {
		n++
	}
	if len(s.Paths) > 0 {
		n++
	}
	if n != 1 {
		return fmt.Errorf("scope must be exactly one of all, role, area, paths")
	}
	return validatePaths(s.Paths)
}

// String renders the scope for one-line display.
func (s Scope) String() string {
	switch {
	case s.All:
		return "all"
	case s.Role != "":
		return "role " + s.Role
	case s.Area != "":
		return "area " + s.Area
	default:
		return "paths " + strings.Join(s.Paths, ", ")
	}
}

// Directive is a time-boxed instruction to agents.
type Directive struct {
	ID          string `yaml:"id"`
	Kind        string `yaml:"kind"`
	Scope       Scope  `yaml:"scope"`
	PublishedBy string `yaml:"published_by,omitempty"`
	ProposedBy  string `yaml:"proposed_by,omitempty"`
	Starts      string `yaml:"starts"`
	Expires     string `yaml:"expires"`
	Status      string `yaml:"status"`
	Text        string `yaml:"-"`
}

// Validate checks field-level rules.
func (d Directive) Validate() error {
	if !ValidID(d.ID, PrefixDirective) {
		return fmt.Errorf("directive: invalid id %q", d.ID)
	}
	if strings.TrimSpace(d.Text) == "" {
		return fmt.Errorf("directive %s: text is required", d.ID)
	}
	if d.Kind != DirectiveContext && d.Kind != DirectiveDeny {
		return fmt.Errorf("directive %s: invalid kind %q", d.ID, d.Kind)
	}
	switch d.Status {
	case DirectiveProposed:
		if d.ProposedBy == "" {
			return fmt.Errorf("directive %s: proposed without proposed_by", d.ID)
		}
	case DirectiveActive, DirectiveWithdrawn, DirectivePromoted:
		if d.PublishedBy == "" {
			return fmt.Errorf("directive %s: published_by is required", d.ID)
		}
	default:
		return fmt.Errorf("directive %s: invalid status %q", d.ID, d.Status)
	}
	if err := d.Scope.Validate(); err != nil {
		return fmt.Errorf("directive %s: %v", d.ID, err)
	}
	if d.Kind == DirectiveDeny && len(d.Scope.Paths) == 0 && d.Scope.Area == "" {
		return fmt.Errorf("directive %s: a deny directive needs a paths or area scope", d.ID)
	}
	st, err := ParseTime(d.Starts)
	if err != nil {
		return fmt.Errorf("directive %s: invalid starts: %v", d.ID, err)
	}
	ex, err := ParseTime(d.Expires)
	if err != nil {
		return fmt.Errorf("directive %s: invalid expires: %v", d.ID, err)
	}
	if !ex.After(st) {
		return fmt.Errorf("directive %s: expires must be after starts", d.ID)
	}
	return nil
}

// ActiveAt reports whether the directive is in force at t.
func (d Directive) ActiveAt(t time.Time) bool {
	if d.Status != DirectiveActive {
		return false
	}
	st, err1 := ParseTime(d.Starts)
	ex, err2 := ParseTime(d.Expires)
	if err1 != nil || err2 != nil {
		return false
	}
	return !t.Before(st) && t.Before(ex)
}

// Event is one line of a session's activity log.
type Event struct {
	TS      string   `json:"ts"`
	Session string   `json:"session"`
	Person  string   `json:"person"`
	Repo    string   `json:"repo,omitempty"` // repository the session works in, when the project spans several
	Kind    string   `json:"kind"`
	Text    string   `json:"text,omitempty"`
	IDs     []string `json:"ids,omitempty"`
}

// Validate checks field-level rules.
func (e Event) Validate() error {
	if _, err := ParseTime(e.TS); err != nil {
		return fmt.Errorf("event: invalid ts: %v", err)
	}
	if !validSession(e.Session) {
		return fmt.Errorf("event: invalid session %q", e.Session)
	}
	if e.Person == "" {
		return fmt.Errorf("event: person is required")
	}
	ok := false
	for _, k := range EventKinds {
		if k == e.Kind {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("event: invalid kind %q", e.Kind)
	}
	if utf8.RuneCountInString(e.Text) > MaxEventText {
		return fmt.Errorf("event: text longer than %d characters", MaxEventText)
	}
	if strings.ContainsAny(e.Text, "\n\r") {
		return fmt.Errorf("event: text must be a single line")
	}
	for _, id := range e.IDs {
		if !ValidID(id, "") {
			return fmt.Errorf("event: invalid id %q", id)
		}
	}
	return nil
}

// validSession allows only characters that are safe in a file name.
func validSession(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// Ref kinds.
var RefKinds = []string{"design", "artifact", "mr", "doc"}

// Ref is a link to a source of truth. dumen stores the link and a one-line label, never content.
// SchemaVersion is the newest dumen.yml `version` this build understands. A store with a
// higher version was written by a newer dumen: this build still reads it, skipping what
// it cannot parse, but refuses to write, because rewriting a record would silently drop
// the fields it does not know.
const SchemaVersion = 1

type Ref struct {
	ID        string   `yaml:"id"`
	URL       string   `yaml:"url"`
	Kind      string   `yaml:"kind"`
	Title     string   `yaml:"title"`
	Owner     string   `yaml:"owner,omitempty"`
	Area      string   `yaml:"area,omitempty"`
	Paths     []string `yaml:"paths,omitempty"`
	Governs   string   `yaml:"governs,omitempty"` // one line
	CheckedAt string   `yaml:"checked_at"`
	CheckedBy string   `yaml:"checked_by,omitempty"`
	// Status is RefUnverified for a link an agent added that no person has looked at yet.
	// Such a link is stored and listed but never injected into agent context.
	Status string `yaml:"status,omitempty"`
}

// RefUnverified marks a ref added by an agent and not yet checked by a person.
const RefUnverified = "unverified"

// Validate checks field-level rules.
func (r Ref) Validate() error {
	if !ValidID(r.ID, PrefixRef) {
		return fmt.Errorf("ref: invalid id %q", r.ID)
	}
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("ref %s: url must be an http(s) link", r.ID)
	}
	ok := false
	for _, k := range RefKinds {
		if k == r.Kind {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("ref %s: invalid kind %q", r.ID, r.Kind)
	}
	if strings.TrimSpace(r.Title) == "" {
		return fmt.Errorf("ref %s: title is required", r.ID)
	}
	if strings.ContainsAny(r.Title+r.Governs, "\n\r") {
		return fmt.Errorf("ref %s: title and governs must each be one line", r.ID)
	}
	if utf8.RuneCountInString(r.Governs) > 200 || utf8.RuneCountInString(r.Title) > 200 {
		return fmt.Errorf("ref %s: title and governs are one-line labels (max 200 characters)", r.ID)
	}
	if _, err := ParseTime(r.CheckedAt); err != nil {
		return fmt.Errorf("ref %s: invalid checked_at: %v", r.ID, err)
	}
	return validatePaths(r.Paths)
}

func validatePaths(paths []string) error {
	for _, p := range paths {
		// An optional "<repo>:" prefix limits the pattern to one repository.
		if i := strings.Index(p, ":"); i > 0 && !strings.ContainsAny(p[:i], "/\\") {
			p = p[i+1:]
			if p == "" {
				p = "**"
			}
		}
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "..") {
			return fmt.Errorf("invalid path %q: use repo-relative paths with forward slashes", p)
		}
	}
	return nil
}

// SortedKeys returns the keys of m in sorted order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
