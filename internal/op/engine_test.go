package op

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nurettincoban/comm-pro/internal/model"
	"github.com/nurettincoban/comm-pro/internal/outbox"
	"github.com/nurettincoban/comm-pro/internal/store"
	"github.com/nurettincoban/comm-pro/internal/testutil"
)

var (
	ctx   = context.Background()
	agent = model.Actor{Email: "dev@example.com", Kind: model.KindAgent}
	dev   = model.Actor{Email: "dev@example.com", Kind: model.KindHuman}
)

func draft(title string) *CreateDecision {
	return &CreateDecision{Decision: model.Decision{ID: model.NewID(model.PrefixDecision), Title: title}, Body: "Decision.\n\nWhy."}
}

func event(session, kind string, at time.Time) *AppendEvent {
	return &AppendEvent{Event: model.Event{TS: model.FormatTime(at), Session: session, Person: "dev@example.com", Kind: kind, Text: kind}}
}

func setup(t *testing.T) (*Engine, *Engine, string) {
	t.Helper()
	testutil.Home(t)
	remote := testutil.BareRemote(t)
	a := &Engine{Store: testutil.NewStore(t, remote, "a")}
	b := &Engine{Store: testutil.NewStore(t, remote, "b")}
	return a, b, remote
}

func TestSubmitAndRoles(t *testing.T) {
	a, b, _ := setup(t)

	out, err := a.Submit(ctx, agent, draft("Use opaque cursors"))
	if err != nil || out.Status != Applied {
		t.Fatalf("draft: %+v %v", out, err)
	}
	id := out.RecordID

	// An agent cannot approve, even with the lead's email.
	_, err = b.Submit(ctx, model.Actor{Email: testutil.Lead.Email, Kind: model.KindAgent}, &ResolveDecision{ID: id, Status: model.StatusApproved})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent approve: want forbidden, got %v", err)
	}
	// A human without a publisher role cannot approve or publish.
	if _, err = b.Submit(ctx, dev, &ResolveDecision{ID: id, Status: model.StatusApproved}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("dev approve: want forbidden, got %v", err)
	}
	now := time.Now().UTC()
	dir := &CreateDirective{Directive: model.Directive{ID: model.NewID(model.PrefixDirective), Kind: model.DirectiveContext,
		Scope: model.Scope{All: true}, Expires: model.FormatTime(now.Add(time.Hour))}, Text: "All new endpoints use requireAuth"}
	if _, err = b.Submit(ctx, dev, dir); !errors.Is(err, ErrForbidden) {
		t.Fatalf("dev publish: want forbidden, got %v", err)
	}
	if n := outbox.Count(b.Store.FailedDir()); n != 3 {
		t.Fatalf("want 3 failed entries, got %d", n)
	}
	// Anyone may propose.
	dir.Propose = true
	if out, err = b.Submit(ctx, agent, dir); err != nil || out.Status != Applied {
		t.Fatalf("propose: %+v %v", out, err)
	}
	// The lead approves from the other clone, which has not seen the draft yet.
	if out, err = b.Submit(ctx, testutil.Lead, &ResolveDecision{ID: id, Status: model.StatusApproved}); err != nil {
		t.Fatalf("lead approve: %v", err)
	}
	if _, err := a.Store.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	d, _, err := store.ReadDecision(a.Store.Root(), id)
	if err != nil || d.Status != model.StatusApproved || d.ApprovedBy != testutil.Lead.Email || d.AuthorKind != model.KindAgent {
		t.Fatalf("decision after approve: %+v %v", d, err)
	}
	// Approved decisions are immutable.
	if _, err = a.Submit(ctx, testutil.Lead, &ResolveDecision{ID: id, Status: model.StatusRejected}); err == nil {
		t.Fatal("re-resolving an approved decision must fail")
	}
}

func TestSecretRejectedBeforeQueue(t *testing.T) {
	a, _, _ := setup(t)
	d := draft("Rotate keys")
	d.Body = "use AKIAIOSFODNN7EXAMPLE for now"
	if _, err := a.Submit(ctx, agent, d); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("want secret rejection, got %v", err)
	}
	if n := outbox.Count(a.Store.OutboxDir()) + outbox.Count(a.Store.FailedDir()); n != 0 {
		t.Fatalf("a secret must never be written to disk, found %d entries", n)
	}
}

func TestIDCollisionRegenerates(t *testing.T) {
	a, b, _ := setup(t)
	d1, d2 := draft("First"), draft("Second")
	d2.Decision.ID = d1.Decision.ID
	o1, err := a.Submit(ctx, agent, d1)
	if err != nil {
		t.Fatal(err)
	}
	o2, err := b.Submit(ctx, agent, d2)
	if err != nil {
		t.Fatal(err)
	}
	if o1.RecordID == o2.RecordID || len(o2.Notes) == 0 {
		t.Fatalf("collision not resolved: %s vs %s, notes %v", o1.RecordID, o2.RecordID, o2.Notes)
	}
	all, _ := store.ListDecisions(b.Store.Root())
	if len(all) != 2 {
		t.Fatalf("want 2 decisions, got %d", len(all))
	}
}

func TestRejectedPushReapplies(t *testing.T) {
	a, b, _ := setup(t)
	// b prepares a commit, and a pushes first while b is between commit and push.
	b.pushHook = func(attempt int) bool {
		if attempt == 0 {
			if _, err := a.Submit(ctx, agent, draft("From a")); err != nil {
				t.Errorf("a submit: %v", err)
			}
		}
		return false
	}
	out, err := b.Submit(ctx, agent, draft("From b"))
	if err != nil || out.Status != Applied {
		t.Fatalf("b submit: %+v %v", out, err)
	}
	all, _ := store.ListDecisions(b.Store.Root())
	if len(all) != 2 {
		t.Fatalf("want both decisions after re-apply, got %d", len(all))
	}
	if merges, _ := b.Store.HasMergeCommits(ctx); merges {
		t.Fatal("history contains a merge commit")
	}
}

func TestOfflineQueueReplays(t *testing.T) {
	a, b, remote := setup(t)
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

	restore := testutil.MoveAway(t, remote)
	var queuedDraft string
	for i, o := range []Op{event("s1", model.EventStarted, base), draft("Offline draft"), event("s1", model.EventFinished, base.Add(time.Minute))} {
		out, err := a.Submit(ctx, agent, o)
		if err != nil || out.Status != Queued {
			t.Fatalf("op %d while offline: %+v %v", i, out, err)
		}
		if c, ok := o.(*CreateDecision); ok {
			queuedDraft = c.Decision.ID
		}
	}
	if n := outbox.Count(a.Store.OutboxDir()); n != 3 {
		t.Fatalf("want 3 queued, got %d", n)
	}
	restore()

	// Meanwhile someone else wrote to the remote.
	if _, err := b.Submit(ctx, agent, draft("Online draft")); err != nil {
		t.Fatal(err)
	}
	rep, err := a.Flush(ctx)
	if err != nil || rep.Offline || len(rep.Outcomes) != 3 {
		t.Fatalf("replay: %+v %v", rep, err)
	}
	for _, o := range rep.Outcomes {
		if o.Status != Applied {
			t.Fatalf("replayed op not applied: %+v", o)
		}
	}
	if n := outbox.Count(a.Store.OutboxDir()); n != 0 {
		t.Fatalf("outbox not empty after replay: %d", n)
	}
	if _, err := b.Store.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	evs, _ := store.ReadEvents(b.Store.Root(), "2026-11-20")
	if len(evs) != 2 || evs[0].Kind != model.EventStarted || evs[1].Kind != model.EventFinished {
		t.Fatalf("events after replay: %+v", evs)
	}
	if _, _, err := store.ReadDecision(b.Store.Root(), queuedDraft); err != nil {
		t.Fatalf("queued draft missing after replay: %v", err)
	}
	all, _ := store.ListDecisions(b.Store.Root())
	if len(all) != 2 {
		t.Fatalf("want 2 decisions, got %d", len(all))
	}
}

// A push whose outcome is unknown (the connection dropped after the remote accepted it)
// must not be applied a second time on replay.
func TestUnknownPushOutcomeIsNotDuplicated(t *testing.T) {
	a, _, _ := setup(t)
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	a.pushHook = func(int) bool {
		// The push really happens, but the engine is told the outcome is unknown.
		if _, err := a.Store.Push(ctx); err != nil {
			t.Errorf("push: %v", err)
		}
		return true
	}
	out, err := a.Submit(ctx, agent, event("s1", model.EventStarted, base))
	if err != nil || out.Status != Queued {
		t.Fatalf("submit: %+v %v", out, err)
	}
	a.pushHook = nil
	rep, err := a.Flush(ctx)
	if err != nil || len(rep.Outcomes) != 1 || rep.Outcomes[0].Status != Applied {
		t.Fatalf("flush: %+v %v", rep, err)
	}
	evs, _ := store.ReadEvents(a.Store.Root(), "2026-11-20")
	if len(evs) != 1 {
		t.Fatalf("event duplicated: got %d lines", len(evs))
	}
}

func TestSupersedeRules(t *testing.T) {
	a, _, _ := setup(t)
	o1, err := a.Submit(ctx, testutil.Lead, &CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "REST"}, Body: "b", Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	// Superseding a decision that does not exist fails.
	bad := draft("GraphQL")
	bad.Decision.Supersedes = []string{"D-0000"}
	if _, err := a.Submit(ctx, agent, bad); err == nil {
		t.Fatal("superseding a missing decision must fail")
	}
	// Two approved decisions superseding the same one is allowed and derived as contested.
	for _, title := range []string{"GraphQL", "gRPC"} {
		d := draft(title)
		d.Decision.Supersedes = []string{o1.RecordID}
		d.Approve = true
		if _, err := a.Submit(ctx, testutil.Lead, d); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := store.ListDecisions(a.Store.Root())
	if len(all) != 3 {
		t.Fatalf("want 3 decisions, got %d", len(all))
	}
}
