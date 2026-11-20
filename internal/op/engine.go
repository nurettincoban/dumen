package op

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/nurettincoban/comm-pro/internal/gitx"
	"github.com/nurettincoban/comm-pro/internal/model"
	"github.com/nurettincoban/comm-pro/internal/outbox"
	"github.com/nurettincoban/comm-pro/internal/store"
)

// Outcome statuses.
const (
	Applied  = "applied"  // on the remote
	Queued   = "queued"   // waiting in the outbox (offline, or the remote was too busy)
	Rejected = "rejected" // refused when applied; moved to the failed directory
)

// Outcome is what happened to one operation.
type Outcome struct {
	OpID     string
	Kind     string
	Status   string
	RecordID string
	Notes    []string
	Err      error // set when Rejected
}

// Report summarises one flush.
type Report struct {
	Outcomes  []Outcome
	Offline   bool  // the remote could not be reached
	Contended bool  // every push attempt lost the race; operations stay queued
	Err       error // the error behind Offline
	Head      string
}

// Engine applies operations to a store.
type Engine struct {
	Store      *store.Store
	MaxRetries int                           // re-applies after a rejected push (default 5)
	Now        func() time.Time              // defaults to time.Now
	Regenerate func(root string) error       // rewrites generated files after operations are applied
	AfterSync  func(root, head string)       // called whenever the checkout matches a fetched head
	pushHook   func(attempt int) (skip bool) // test hook
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// Enqueue validates an operation and puts it in the outbox. It does no network work,
// which is why hooks use it.
func (e *Engine) Enqueue(actor model.Actor, o Op) (string, error) {
	if err := o.Check(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	entry := &outbox.Entry{Kind: o.Kind(), Actor: actor, Created: model.FormatTime(e.now()), Payload: payload}
	if err := outbox.Add(e.Store.OutboxDir(), entry); err != nil {
		return "", err
	}
	return entry.ID, nil
}

// Submit enqueues an operation and flushes the outbox. The returned outcome is Applied,
// Queued (it will be replayed by a later flush) or Rejected.
func (e *Engine) Submit(ctx context.Context, actor model.Actor, o Op) (Outcome, error) {
	id, err := e.Enqueue(actor, o)
	if err != nil {
		return Outcome{Kind: o.Kind(), Status: Rejected, Err: err}, err
	}
	rep, err := e.Flush(ctx)
	if err != nil {
		return Outcome{OpID: id, Kind: o.Kind(), Status: Queued}, err
	}
	for _, out := range rep.Outcomes {
		if out.OpID == id {
			if out.Status == Rejected {
				return out, out.Err
			}
			return out, nil
		}
	}
	// Another process flushed our entry between Enqueue and Flush.
	return Outcome{OpID: id, Kind: o.Kind(), Status: Applied}, nil
}

type pending struct {
	entry *outbox.Entry
	op    Op
	out   Outcome
}

// Flush pushes everything in the outbox: fetch, apply to the fresh head, commit, push
// fast-forward only; on rejection reset and re-apply, up to MaxRetries times.
func (e *Engine) Flush(ctx context.Context) (Report, error) {
	var rep Report
	unlock, err := e.Store.Lock(ctx)
	if err != nil {
		return rep, err
	}
	defer unlock()

	entries, err := outbox.List(e.Store.OutboxDir())
	if err != nil {
		return rep, err
	}
	if len(entries) == 0 {
		return rep, nil
	}
	max := e.MaxRetries
	if max <= 0 {
		max = 5
	}
	queued := func() {
		for _, en := range entries {
			rep.Outcomes = append(rep.Outcomes, Outcome{OpID: en.ID, Kind: en.Kind, Status: Queued})
		}
	}

	for attempt := 0; attempt <= max; attempt++ {
		if attempt > 0 {
			// Jitter spreads out writers that lost the same race.
			d := time.Duration(5+rand.IntN(40*attempt)) * time.Millisecond
			select {
			case <-ctx.Done():
				queued()
				return rep, ctx.Err()
			case <-time.After(d):
			}
		}
		head, err := e.Store.Sync(ctx)
		if err != nil {
			rep.Offline, rep.Err = true, err
			queued()
			return rep, nil
		}
		rep.Head = head
		if e.AfterSync != nil {
			e.AfterSync(e.Store.Root(), head)
		}

		// A previous push with an unknown outcome may have landed: do not apply those twice.
		var flagged []string
		for _, en := range entries {
			if en.MaybePushed {
				flagged = append(flagged, en.ID)
			}
		}
		if len(flagged) > 0 {
			done, err := e.Store.AppliedOps(ctx, flagged)
			if err != nil {
				return rep, err
			}
			rest := entries[:0]
			for _, en := range entries {
				if done[en.ID] {
					rep.Outcomes = append(rep.Outcomes, Outcome{OpID: en.ID, Kind: en.Kind, Status: Applied})
					if err := en.Remove(); err != nil {
						return rep, err
					}
					continue
				}
				rest = append(rest, en)
			}
			entries = rest
		}

		// Apply every queued operation, oldest first, to the fresh head.
		now := e.now()
		var good []*pending
		rest := entries[:0]
		for _, en := range entries {
			p := &pending{entry: en, out: Outcome{OpID: en.ID, Kind: en.Kind}}
			o, err := Decode(en.Kind, en.Payload)
			if err == nil {
				p.op = o
				tx := &Tx{Root: e.Store.Root(), Now: now, Actor: en.Actor}
				if err = o.Apply(tx); err == nil {
					p.out.RecordID, p.out.Notes = tx.RecordID, tx.Notes
				}
			}
			if err != nil {
				p.out.Status, p.out.Err = Rejected, err
				rep.Outcomes = append(rep.Outcomes, p.out)
				if ferr := en.Fail(e.Store.FailedDir(), err); ferr != nil {
					return rep, ferr
				}
				// A rejected operation may have left partial writes behind.
				if _, rerr := e.Store.ResetToFetched(ctx); rerr != nil {
					return rep, rerr
				}
				for _, g := range good {
					tx := &Tx{Root: e.Store.Root(), Now: now, Actor: g.entry.Actor}
					if aerr := g.op.Apply(tx); aerr != nil {
						return rep, fmt.Errorf("re-applying %s: %w", g.entry.Kind, aerr)
					}
				}
				continue
			}
			good = append(good, p)
			rest = append(rest, en)
		}
		entries = rest
		if len(good) == 0 {
			return rep, nil
		}
		if e.Regenerate != nil {
			if err := e.Regenerate(e.Store.Root()); err != nil {
				return rep, err
			}
		}

		committed, err := e.Store.Commit(ctx, commitMessage(good), good[0].entry.Actor.Email)
		if err != nil {
			return rep, err
		}
		if !committed { // nothing changed on this head; the operations are satisfied
			return rep, e.finish(&rep, good)
		}
		for _, p := range good {
			// Persist the applied form (an ID may have been regenerated) and the push marker.
			if payload, err := json.Marshal(p.op); err == nil {
				p.entry.Payload = payload
			}
			p.entry.MaybePushed = true
			if err := p.entry.Save(); err != nil {
				return rep, err
			}
		}
		if e.pushHook != nil && e.pushHook(attempt) {
			rep.Offline = true
			queued()
			return rep, nil
		}
		res, perr := e.Store.Push(ctx)
		switch res {
		case gitx.PushOK:
			if head, err := e.Store.Head(ctx); err == nil {
				rep.Head = head
				if e.AfterSync != nil {
					e.AfterSync(e.Store.Root(), head)
				}
			}
			return rep, e.finish(&rep, good)
		case gitx.PushRejected:
			for _, p := range good {
				p.entry.MaybePushed = false
				if err := p.entry.Save(); err != nil {
					return rep, err
				}
			}
			continue
		default:
			rep.Offline, rep.Err = true, perr
			queued()
			return rep, nil
		}
	}
	rep.Contended = true
	queued()
	return rep, nil
}

func (e *Engine) finish(rep *Report, good []*pending) error {
	for _, p := range good {
		p.out.Status = Applied
		rep.Outcomes = append(rep.Outcomes, p.out)
		if err := p.entry.Remove(); err != nil {
			return err
		}
	}
	return nil
}

func commitMessage(good []*pending) string {
	var b strings.Builder
	if len(good) == 1 {
		b.WriteString("dumen: " + describe(good[0]))
	} else {
		fmt.Fprintf(&b, "dumen: %d operations", len(good))
	}
	b.WriteString("\n\n")
	if len(good) > 1 {
		for _, p := range good {
			b.WriteString("- " + describe(p) + "\n")
		}
		b.WriteString("\n")
	}
	for _, p := range good {
		b.WriteString("Dumen-Op: " + p.entry.ID + "\n")
	}
	return b.String()
}

func describe(p *pending) string {
	if p.out.RecordID != "" {
		return p.entry.Kind + " " + p.out.RecordID
	}
	return p.entry.Kind
}
