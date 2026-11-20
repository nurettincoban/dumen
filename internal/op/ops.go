// Package op defines dumen's write operations and the protocol that applies them:
// fetch, apply to the fresh head, commit, fast-forward-only push, and on rejection
// reset and re-apply. Operations are data, so they can wait in the outbox while offline.
package op

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nurettincoban/comm-pro/internal/model"
	"github.com/nurettincoban/comm-pro/internal/policy"
	"github.com/nurettincoban/comm-pro/internal/secrets"
	"github.com/nurettincoban/comm-pro/internal/store"
)

// Tx is what an operation sees while it is applied to a fresh checkout.
type Tx struct {
	Root  string
	Now   time.Time
	Actor model.Actor

	// RecordID is set by the operation to the ID of the record it wrote.
	RecordID string
	// Notes are warnings for the person who ran the operation.
	Notes []string

	cfg *model.Config
}

// Config loads dumen.yml once per transaction.
func (tx *Tx) Config() (model.Config, error) {
	if tx.cfg == nil {
		c, err := store.ReadConfig(tx.Root)
		if err != nil {
			return c, err
		}
		tx.cfg = &c
	}
	return *tx.cfg, nil
}

// Op is one write. Apply must validate everything before it writes anything, because it
// runs again from scratch on a newer head whenever a push is rejected.
type Op interface {
	Kind() string
	// Check validates the operation on its own (no store access) and scans it for secrets.
	Check() error
	// Apply writes the operation's effect into tx.Root.
	Apply(tx *Tx) error
}

var registry = map[string]func() Op{}

func register(f func() Op) { registry[f().Kind()] = f }

// Decode rebuilds an operation from its kind and JSON payload.
func Decode(kind string, payload json.RawMessage) (Op, error) {
	f, ok := registry[kind]
	if !ok {
		return nil, fmt.Errorf("unknown operation kind %q", kind)
	}
	o := f()
	if err := json.Unmarshal(payload, o); err != nil {
		return nil, fmt.Errorf("operation %s: %v", kind, err)
	}
	return o, nil
}

func init() {
	register(func() Op { return &CreateDecision{} })
	register(func() Op { return &ResolveDecision{} })
	register(func() Op { return &ArchiveStaleDrafts{} })
	register(func() Op { return &CreateDirective{} })
	register(func() Op { return &SetDirectiveStatus{} })
	register(func() Op { return &AppendEvent{} })
	register(func() Op { return &AddRef{} })
	register(func() Op { return &CheckRef{} })
	register(func() Op { return &UpdateConfig{} })
	register(func() Op { return &SetRole{} })
	register(func() Op { return &CompactActivity{} })
}

// ErrForbidden marks a role or actor-kind violation.
var ErrForbidden = errors.New("forbidden")

func requireHuman(tx *Tx, what string) error {
	if tx.Actor.Kind != model.KindHuman {
		return fmt.Errorf("%w: agents cannot %s; ask your human", ErrForbidden, what)
	}
	return nil
}

// freshID keeps id if it is free on this head and otherwise draws a new one.
func freshID(tx *Tx, id, prefix string) string {
	for store.Exists(tx.Root, id) {
		old := id
		id = model.NewID(prefix)
		tx.Notes = append(tx.Notes, fmt.Sprintf("id %s was taken, using %s", old, id))
	}
	return id
}

// ---- decisions ----

// CreateDecision writes a new decision. Agents and humans without approval rights always
// create drafts; a human with approval rights may create it approved (used by supersede).
type CreateDecision struct {
	Decision model.Decision `json:"decision"`
	Body     string         `json:"body"`
	Approve  bool           `json:"approve,omitempty"`
}

func (o *CreateDecision) Kind() string { return "decision.create" }

func (o *CreateDecision) Check() error {
	d := o.Decision
	if !model.ValidID(d.ID, model.PrefixDecision) {
		return fmt.Errorf("decision: invalid id %q", d.ID)
	}
	if strings.TrimSpace(d.Title) == "" {
		return fmt.Errorf("decision: title is required")
	}
	return secrets.Scan(d.Title, o.Body, d.Area, strings.Join(d.Paths, "\n"))
}

func (o *CreateDecision) Apply(tx *Tx) error {
	d := o.Decision
	d.Body = o.Body
	d.Author = tx.Actor.Email
	d.AuthorKind = tx.Actor.Kind
	d.Status = model.StatusDraft
	d.ApprovedBy = ""
	if d.Created == "" {
		d.Created = model.FormatTime(tx.Now)
	}
	all, err := store.ListDecisions(tx.Root)
	if err != nil {
		return err
	}
	byID := map[string]model.Decision{}
	for _, x := range all {
		byID[x.ID] = x
	}
	for _, s := range d.Supersedes {
		if _, ok := byID[s]; !ok {
			return fmt.Errorf("decision: cannot supersede %s: not found", s)
		}
	}
	for _, s := range d.Sources {
		if !store.Exists(tx.Root, s) {
			return fmt.Errorf("decision: source %s not found", s)
		}
	}
	if o.Approve {
		if err := requireHuman(tx, "approve decisions"); err != nil {
			return err
		}
		cfg, err := tx.Config()
		if err != nil {
			return err
		}
		if !cfg.CanApprove(tx.Actor.Email, d.Area) {
			return fmt.Errorf("%w: %s may not approve decisions in area %q", ErrForbidden, tx.Actor.Email, d.Area)
		}
		for _, s := range d.Supersedes {
			if byID[s].Status != model.StatusApproved {
				return fmt.Errorf("decision: %s is not approved, so it cannot be superseded", s)
			}
		}
		d.Status = model.StatusApproved
		d.ApprovedBy = tx.Actor.Email
	}
	d.ID = freshID(tx, d.ID, model.PrefixDecision)
	o.Decision.ID = d.ID
	if err := d.Validate(); err != nil {
		return err
	}
	if err := policy.SupersedeCycle(append(all, d)); err != nil {
		return err
	}
	tx.RecordID = d.ID
	_, err = store.WriteDecision(tx.Root, d)
	return err
}

// ResolveDecision approves or rejects a draft. Approved bodies are immutable afterwards.
type ResolveDecision struct {
	ID     string `json:"id"`
	Status string `json:"status"` // approved | rejected
}

func (o *ResolveDecision) Kind() string { return "decision.resolve" }

func (o *ResolveDecision) Check() error {
	if !model.ValidID(o.ID, model.PrefixDecision) {
		return fmt.Errorf("invalid decision id %q", o.ID)
	}
	if o.Status != model.StatusApproved && o.Status != model.StatusRejected {
		return fmt.Errorf("invalid status %q", o.Status)
	}
	return nil
}

func (o *ResolveDecision) Apply(tx *Tx) error {
	if err := requireHuman(tx, "approve or reject decisions"); err != nil {
		return err
	}
	d, _, err := store.ReadDecision(tx.Root, o.ID)
	if err != nil {
		return err
	}
	cfg, err := tx.Config()
	if err != nil {
		return err
	}
	if !cfg.CanApprove(tx.Actor.Email, d.Area) {
		return fmt.Errorf("%w: %s may not approve or reject decisions in area %q", ErrForbidden, tx.Actor.Email, d.Area)
	}
	if d.Status != model.StatusDraft {
		return fmt.Errorf("decision %s is already %s", d.ID, d.Status)
	}
	all, err := store.ListDecisions(tx.Root)
	if err != nil {
		return err
	}
	if o.Status == model.StatusApproved {
		byID := map[string]model.Decision{}
		for _, x := range all {
			byID[x.ID] = x
		}
		for _, s := range d.Supersedes {
			if byID[s].Status != model.StatusApproved {
				return fmt.Errorf("decision %s supersedes %s, which is not approved", d.ID, s)
			}
		}
		if err := policy.SupersedeCycle(all); err != nil {
			return err
		}
		d.ApprovedBy = tx.Actor.Email
	}
	d.Status = o.Status
	if err := d.Validate(); err != nil {
		return err
	}
	tx.RecordID = d.ID
	_, err = store.WriteDecision(tx.Root, d)
	return err
}

// ArchiveStaleDrafts moves drafts older than settings.draft_archive_days to decisions/archive.
type ArchiveStaleDrafts struct{}

func (o *ArchiveStaleDrafts) Kind() string { return "decision.archive_stale" }
func (o *ArchiveStaleDrafts) Check() error { return nil }

func (o *ArchiveStaleDrafts) Apply(tx *Tx) error {
	cfg, err := tx.Config()
	if err != nil {
		return err
	}
	all, err := store.ListDecisions(tx.Root)
	if err != nil {
		return err
	}
	cutoff := tx.Now.Add(-time.Duration(cfg.Settings.DraftArchiveDays) * 24 * time.Hour)
	for _, d := range all {
		if d.Status != model.StatusDraft {
			continue
		}
		created, err := model.ParseTime(d.Created)
		if err != nil || !created.Before(cutoff) {
			continue
		}
		if err := store.ArchiveDecision(tx.Root, d.ID); err != nil {
			return err
		}
	}
	return nil
}

// ---- directives ----

// CreateDirective publishes a directive (humans with a publisher role) or proposes one (anyone).
type CreateDirective struct {
	Directive model.Directive `json:"directive"`
	Text      string          `json:"text"`
	Propose   bool            `json:"propose,omitempty"`
}

func (o *CreateDirective) Kind() string { return "directive.create" }

func (o *CreateDirective) Check() error {
	if !model.ValidID(o.Directive.ID, model.PrefixDirective) {
		return fmt.Errorf("directive: invalid id %q", o.Directive.ID)
	}
	if strings.TrimSpace(o.Text) == "" {
		return fmt.Errorf("directive: text is required")
	}
	return secrets.Scan(o.Text, strings.Join(o.Directive.Scope.Paths, "\n"))
}

func (o *CreateDirective) Apply(tx *Tx) error {
	d := o.Directive
	d.Text = o.Text
	cfg, err := tx.Config()
	if err != nil {
		return err
	}
	if d.Starts == "" {
		// Start a minute in the past so a machine whose clock runs slightly behind the
		// publisher's still sees the directive as active right away.
		d.Starts = model.FormatTime(tx.Now.Add(-time.Minute))
	}
	if o.Propose {
		d.Status = model.DirectiveProposed
		d.ProposedBy = tx.Actor.Email
		d.PublishedBy = ""
	} else {
		if err := requirePublisher(tx, cfg); err != nil {
			return err
		}
		d.Status = model.DirectiveActive
		d.PublishedBy = tx.Actor.Email
	}
	if d.Scope.Area != "" {
		if _, ok := cfg.Areas[d.Scope.Area]; !ok {
			return fmt.Errorf("directive: unknown area %q", d.Scope.Area)
		}
	}
	d.ID = freshID(tx, d.ID, model.PrefixDirective)
	o.Directive.ID = d.ID
	if err := d.Validate(); err != nil {
		return err
	}
	if !o.Propose {
		if err := noteDirectiveLimit(tx, cfg, 1); err != nil {
			return err
		}
	}
	tx.RecordID = d.ID
	_, err = store.WriteDirective(tx.Root, d)
	return err
}

func requirePublisher(tx *Tx, cfg model.Config) error {
	if err := requireHuman(tx, "publish or withdraw directives"); err != nil {
		return err
	}
	if !cfg.CanPublish(tx.Actor.Email) {
		return fmt.Errorf("%w: %s does not hold a role that may publish directives (%s)",
			ErrForbidden, tx.Actor.Email, strings.Join(cfg.Settings.Publishers, ", "))
	}
	return nil
}

func noteDirectiveLimit(tx *Tx, cfg model.Config, adding int) error {
	all, err := store.ListDirectives(tx.Root)
	if err != nil {
		return err
	}
	n := adding
	for _, x := range all {
		if x.ActiveAt(tx.Now) {
			n++
		}
	}
	if n > cfg.Settings.MaxActiveDirectives {
		tx.Notes = append(tx.Notes, fmt.Sprintf("there are now %d active directives (recommended maximum %d); agents follow a short list better",
			n, cfg.Settings.MaxActiveDirectives))
	}
	return nil
}

// SetDirectiveStatus publishes a proposal, withdraws a directive, or marks it promoted.
type SetDirectiveStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"` // active | withdrawn | promoted
}

func (o *SetDirectiveStatus) Kind() string { return "directive.status" }

func (o *SetDirectiveStatus) Check() error {
	if !model.ValidID(o.ID, model.PrefixDirective) {
		return fmt.Errorf("invalid directive id %q", o.ID)
	}
	switch o.Status {
	case model.DirectiveActive, model.DirectiveWithdrawn, model.DirectivePromoted:
		return nil
	}
	return fmt.Errorf("invalid status %q", o.Status)
}

func (o *SetDirectiveStatus) Apply(tx *Tx) error {
	cfg, err := tx.Config()
	if err != nil {
		return err
	}
	if err := requirePublisher(tx, cfg); err != nil {
		return err
	}
	d, _, err := store.ReadDirective(tx.Root, o.ID)
	if err != nil {
		return err
	}
	switch {
	case o.Status == model.DirectiveActive && d.Status == model.DirectiveProposed:
		d.PublishedBy = tx.Actor.Email
		if err := noteDirectiveLimit(tx, cfg, 1); err != nil {
			return err
		}
	case o.Status != model.DirectiveActive && (d.Status == model.DirectiveActive || d.Status == model.DirectiveProposed):
		if d.PublishedBy == "" {
			d.PublishedBy = tx.Actor.Email
		}
	default:
		return fmt.Errorf("directive %s is %s and cannot become %s", d.ID, d.Status, o.Status)
	}
	d.Status = o.Status
	if err := d.Validate(); err != nil {
		return err
	}
	tx.RecordID = d.ID
	_, err = store.WriteDirective(tx.Root, d)
	return err
}

// ---- activity ----

// AppendEvent appends one event to its session's activity file.
type AppendEvent struct {
	Event model.Event `json:"event"`
}

func (o *AppendEvent) Kind() string { return "event.append" }

func (o *AppendEvent) Check() error {
	if err := o.Event.Validate(); err != nil {
		return err
	}
	return secrets.Scan(o.Event.Text)
}

func (o *AppendEvent) Apply(tx *Tx) error {
	if err := o.Event.Validate(); err != nil {
		return err
	}
	_, err := store.AppendEvent(tx.Root, o.Event)
	return err
}

// ---- refs ----

// AddRef records a link to a source of truth.
type AddRef struct {
	Ref model.Ref `json:"ref"`
}

func (o *AddRef) Kind() string { return "ref.add" }

func (o *AddRef) Check() error {
	if !model.ValidID(o.Ref.ID, model.PrefixRef) {
		return fmt.Errorf("ref: invalid id %q", o.Ref.ID)
	}
	return secrets.Scan(o.Ref.URL, o.Ref.Title, o.Ref.Governs)
}

func (o *AddRef) Apply(tx *Tx) error {
	r := o.Ref
	if r.CheckedAt == "" {
		r.CheckedAt = model.FormatTime(tx.Now)
	}
	if r.Owner == "" {
		r.Owner = tx.Actor.Email
	}
	r.ID = freshID(tx, r.ID, model.PrefixRef)
	o.Ref.ID = r.ID
	if err := r.Validate(); err != nil {
		return err
	}
	tx.RecordID = r.ID
	_, err := store.WriteRef(tx.Root, r)
	return err
}

// CheckRef records that a human confirmed the link is still the source of truth.
type CheckRef struct {
	ID string `json:"id"`
}

func (o *CheckRef) Kind() string { return "ref.check" }

func (o *CheckRef) Check() error {
	if !model.ValidID(o.ID, model.PrefixRef) {
		return fmt.Errorf("invalid ref id %q", o.ID)
	}
	return nil
}

func (o *CheckRef) Apply(tx *Tx) error {
	r, err := store.ReadRef(tx.Root, o.ID)
	if err != nil {
		return err
	}
	r.CheckedAt = model.FormatTime(tx.Now)
	tx.RecordID = r.ID
	_, err = store.WriteRef(tx.Root, r)
	return err
}

// ---- config ----

// UpdateConfig replaces roles, areas and settings in dumen.yml. Publishers only.
type UpdateConfig struct {
	Config model.Config `json:"config"`
}

func (o *UpdateConfig) Kind() string { return "config.update" }
func (o *UpdateConfig) Check() error { return nil }

func (o *UpdateConfig) Apply(tx *Tx) error {
	cfg, err := tx.Config()
	if err != nil {
		return err
	}
	if err := requireHuman(tx, "change dumen.yml"); err != nil {
		return err
	}
	if !cfg.CanPublish(tx.Actor.Email) {
		return fmt.Errorf("%w: %s may not change dumen.yml", ErrForbidden, tx.Actor.Email)
	}
	next := o.Config
	next.Version = cfg.Version
	next.Project = cfg.Project
	if !next.CanPublish(tx.Actor.Email) {
		return fmt.Errorf("the new configuration would remove your own publisher role; ask another publisher to do that")
	}
	tx.cfg = nil
	return store.WriteConfig(tx.Root, next)
}

// SetRole gives one person a role in dumen.yml, or removes them when Role is empty.
// Unlike UpdateConfig it changes a single entry, so two leads adding people at the
// same time never overwrite each other. Publishers only.
type SetRole struct {
	Email string `json:"email"`
	Role  string `json:"role,omitempty"`
}

func (o *SetRole) Kind() string { return "config.role" }

func (o *SetRole) Check() error {
	o.Email = strings.ToLower(strings.TrimSpace(o.Email))
	o.Role = strings.ToLower(strings.TrimSpace(o.Role))
	if !strings.Contains(o.Email, "@") || strings.ContainsAny(o.Email, " \t\n") {
		return fmt.Errorf("%q is not an email address (use the person's git user.email)", o.Email)
	}
	if o.Role != "" && model.Slug(o.Role) != o.Role {
		return fmt.Errorf("role %q must be a short lowercase word such as dev, lead or pm", o.Role)
	}
	return nil
}

func (o *SetRole) Apply(tx *Tx) error {
	cfg, err := tx.Config()
	if err != nil {
		return err
	}
	if err := requireHuman(tx, "change roles"); err != nil {
		return err
	}
	if !cfg.CanPublish(tx.Actor.Email) {
		return fmt.Errorf("%w: %s may not change roles", ErrForbidden, tx.Actor.Email)
	}
	roles := make(map[string]string, len(cfg.Roles)+1) // a copy: tx.Config shares its map
	for e, r := range cfg.Roles {
		roles[e] = r
	}
	cfg.Roles = roles
	if o.Role == "" {
		delete(cfg.Roles, o.Email)
	} else {
		cfg.Roles[o.Email] = o.Role
	}
	if !cfg.CanPublish(tx.Actor.Email) {
		return fmt.Errorf("this would remove your own publisher role; ask another publisher to do that")
	}
	tx.cfg = nil
	tx.RecordID = o.Email
	return store.WriteConfig(tx.Root, cfg)
}

// ---- maintenance ----

// CompactActivity merges the per-session files of days older than Days into one file per day.
type CompactActivity struct {
	Days int `json:"days"`
}

func (o *CompactActivity) Kind() string { return "activity.compact" }

func (o *CompactActivity) Check() error {
	if o.Days < 1 {
		return fmt.Errorf("days must be at least 1")
	}
	return nil
}

func (o *CompactActivity) Apply(tx *Tx) error {
	days, err := store.ListDays(tx.Root)
	if err != nil {
		return err
	}
	cutoff := tx.Now.Add(-time.Duration(o.Days) * 24 * time.Hour).Format("2006-01-02")
	n := 0
	for _, d := range days {
		if d >= cutoff {
			continue
		}
		changed, err := store.CompactDay(tx.Root, d)
		if err != nil {
			return err
		}
		if changed {
			n++
		}
	}
	if n > 0 {
		tx.Notes = append(tx.Notes, fmt.Sprintf("compacted %d days of activity", n))
	}
	return nil
}
