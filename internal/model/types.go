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

// Decision statuses. A proposed decision is in use by the session that wrote it and
// waits for a person to approve it. Stores written before version 2 call it "draft";
// the store reads that spelling as StatusProposed.
const (
	StatusProposed = "proposed"
	StatusApproved = "approved"
	StatusRejected = "rejected"

	LegacyStatusDraft = "draft"
)

// Default decision kinds and the role that owns each. A team can add its own kinds,
// or change the owner role, under `kinds` in dumen.yml.
const (
	KindProduct      = "product"
	KindUX           = "ux"
	KindUI           = "ui"
	KindArchitecture = "architecture"
	KindTechnical    = "technical"
)

// DefaultKinds is used when dumen.yml declares no kinds.
func DefaultKinds() map[string]KindConfig {
	return map[string]KindConfig{
		KindProduct:      {OwnerRole: "pm"},
		KindUX:           {OwnerRole: "designer"},
		KindUI:           {OwnerRole: "designer"},
		KindArchitecture: {OwnerRole: "lead"},
		KindTechnical:    {OwnerRole: "lead"},
	}
}

// MaxRule is the maximum length of a decision's rule, in characters.
const MaxRule = 300

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
	Version int               `yaml:"version"`
	Project string            `yaml:"project"`
	Product string            `yaml:"product,omitempty"` // what the team builds, one line
	Roles   map[string]string `yaml:"roles"`             // git email -> role
	// Services names the repositories of the product, each with a one-line description.
	// Decisions apply to services by these names (the `repo` of each .dumen file).
	Services map[string]string `yaml:"services,omitempty"`
	// Topics are words from the product's language. Each maps to the keywords that
	// bring its decisions to an agent's attention when a request mentions them.
	Topics map[string][]string `yaml:"topics,omitempty"`
	// Kinds are extra kinds of decision the team records, or a different owner role for
	// a default kind. The default kinds are always known.
	Kinds    map[string]KindConfig `yaml:"kinds,omitempty"`
	Areas    map[string]Area       `yaml:"areas,omitempty"`
	Settings Settings              `yaml:"settings"`
}

// KindConfig says who owns a kind of decision.
type KindConfig struct {
	OwnerRole string `yaml:"owner_role"`
}

// KindsOrDefault returns the default kinds together with the ones dumen.yml declares. A
// declared kind with the name of a default one changes its owner role.
func (c Config) KindsOrDefault() map[string]KindConfig {
	out := DefaultKinds()
	for k, v := range c.Kinds {
		out[k] = v
	}
	return out
}

// KnownKind reports whether kind is one of the team's decision kinds.
func (c Config) KnownKind(kind string) bool {
	_, ok := c.KindsOrDefault()[kind]
	return ok
}

// OwnsKind reports whether email holds the role that owns decisions of this kind.
func (c Config) OwnsKind(email, kind string) bool {
	k, ok := c.KindsOrDefault()[kind]
	role := c.Role(email)
	return ok && role != "" && k.OwnerRole == role
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
		Version: SchemaVersion,
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

// CanApprove reports whether email may approve a decision of the given kind in the
// given area: publisher roles, the role that owns the kind, and the owners of the area.
func (c Config) CanApprove(email, kind, area string) bool {
	if c.CanPublish(email) {
		return true
	}
	if kind != "" && c.OwnsKind(email, kind) {
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

// Decision is a team decision about the product, its UX and UI, its architecture or its
// technology. Rule is the one sentence agents see first. The body holds why, what is
// allowed and not allowed (with examples), and the alternatives that were not taken.
type Decision struct {
	ID        string    `yaml:"id"`
	Title     string    `yaml:"title"`
	Rule      string    `yaml:"rule,omitempty"`
	Kind      string    `yaml:"kind,omitempty"`
	Status    string    `yaml:"status"`
	AppliesTo AppliesTo `yaml:"applies_to,omitempty"`
	Owner     string    `yaml:"owner,omitempty"` // git email of the person to ask about it
	// Area and Paths are the version 1 scope. An area also counts as a topic; paths
	// link the decision to files and act as its path guard.
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

// AppliesTo scopes a decision to services and topics. No services, or "*", means the
// whole product.
type AppliesTo struct {
	Services []string `yaml:"services,omitempty"`
	Topics   []string `yaml:"topics,omitempty"`
}

// IsZero lets yaml omit an empty scope.
func (a AppliesTo) IsZero() bool { return len(a.Services) == 0 && len(a.Topics) == 0 }

// ProductWide reports whether the decision applies to every service.
func (d Decision) ProductWide() bool {
	if len(d.AppliesTo.Services) == 0 {
		return true
	}
	for _, s := range d.AppliesTo.Services {
		if s == "*" {
			return true
		}
	}
	return false
}

// ForService reports whether the decision applies to the named service (a repository
// name from .dumen). An empty name, as in a single-repository project, matches all.
func (d Decision) ForService(service string) bool {
	if service == "" || d.ProductWide() {
		return true
	}
	for _, s := range d.AppliesTo.Services {
		if s == service {
			return true
		}
	}
	return false
}

// Topics returns the decision's topics, counting its version 1 area as one.
func (d Decision) Topics() []string {
	out := append([]string(nil), d.AppliesTo.Topics...)
	if d.Area != "" {
		dup := false
		for _, t := range out {
			dup = dup || t == d.Area
		}
		if !dup {
			out = append(out, d.Area)
		}
	}
	return out
}

// Headline is what agents see first: the rule, or the title for decisions without one.
func (d Decision) Headline() string {
	if d.Rule != "" {
		return d.Rule
	}
	return d.Title
}

// ValidName reports whether s is a usable kind, topic or service name: lower-case
// letters, digits, '-' and '_', at most 40 characters.
func ValidName(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// ValidService reports whether s is a usable service name: a repository name as `dumen
// init --repo` accepts it, in lower case, since .dumen stores repository names that way.
func ValidService(s string) bool {
	return CheckName("service", s) == nil && s == strings.ToLower(s)
}

// Validate checks field-level rules.
func (d Decision) Validate() error {
	if !ValidID(d.ID, PrefixDecision) {
		return fmt.Errorf("decision: invalid id %q", d.ID)
	}
	if strings.TrimSpace(d.Title) == "" {
		return fmt.Errorf("decision %s: title is required", d.ID)
	}
	if utf8.RuneCountInString(d.Rule) > MaxRule {
		return fmt.Errorf("decision %s: rule is longer than %d characters", d.ID, MaxRule)
	}
	if d.Kind != "" && !ValidName(d.Kind) {
		return fmt.Errorf("decision %s: invalid kind %q", d.ID, d.Kind)
	}
	for _, s := range d.AppliesTo.Services {
		if s != "*" && !ValidService(s) {
			return fmt.Errorf("decision %s: invalid service %q: use the repository name from .dumen", d.ID, s)
		}
	}
	if d.Owner != "" && (!strings.Contains(d.Owner, "@") || strings.ContainsAny(d.Owner, " \t\r\n") || len(d.Owner) > 254) {
		return fmt.Errorf("decision %s: owner %q is not an email address", d.ID, d.Owner)
	}
	for _, t := range d.AppliesTo.Topics {
		if !ValidName(t) {
			return fmt.Errorf("decision %s: invalid topic %q", d.ID, t)
		}
	}
	switch d.Status {
	case StatusProposed, StatusApproved, StatusRejected:
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
//
// Version 2 added decision rules, kinds, services and topics, and renamed the "draft"
// status to "proposed".
const SchemaVersion = 2

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
