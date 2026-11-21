package op

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/store"
	"github.com/nurettincoban/dumen/internal/testutil"
)

func decision(title, rule, kind string, services ...string) *CreateDecision {
	return &CreateDecision{Decision: model.Decision{ID: model.NewID(model.PrefixDecision), Title: title, Rule: rule,
		Kind: kind, AppliesTo: model.AppliesTo{Services: services}}, Body: "Why.\n\n## Allowed\n- x\n\n## Not allowed\n- y"}
}

// A team that sets an owner role for a kind limits approval of that kind to the role,
// and publishers still approve any kind.
func TestKindOwnerApproves(t *testing.T) {
	a, _, _ := setup(t)
	designer := model.Actor{Email: "sofia@example.com", Kind: model.KindHuman}
	if _, err := a.Submit(ctx, testutil.Lead, &SetRole{Email: designer.Email, Role: "designer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Submit(ctx, testutil.Lead, &SetRole{Email: dev.Email, Role: "backend"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Submit(ctx, testutil.Lead, &SetProduct{Kinds: map[string]string{"ui": "designer", "ux": "designer", "architecture": "lead"}}); err != nil {
		t.Fatal(err)
	}
	ui, err := a.Submit(ctx, agent, decision("Time slots", "Guests pick a time from slot buttons", model.KindUI, "web-app"))
	if err != nil {
		t.Fatal(err)
	}
	arch, err := a.Submit(ctx, agent, decision("Payments", "Only billing-svc talks to the payment provider", model.KindArchitecture))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Submit(ctx, dev, &ResolveDecision{ID: ui.RecordID, Status: model.StatusApproved}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a developer approved a UI decision: %v", err)
	}
	if _, err := a.Submit(ctx, designer, &ResolveDecision{ID: arch.RecordID, Status: model.StatusApproved}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the designer approved an architecture decision: %v", err)
	}
	if _, err := a.Submit(ctx, designer, &ResolveDecision{ID: ui.RecordID, Status: model.StatusApproved}); err != nil {
		t.Fatalf("the designer could not approve a UI decision: %v", err)
	}
	if _, err := a.Submit(ctx, testutil.Lead, &ResolveDecision{ID: arch.RecordID, Status: model.StatusApproved}); err != nil {
		t.Fatalf("the lead could not approve an architecture decision: %v", err)
	}
	d, _, err := store.ReadDecision(a.Store.Root(), ui.RecordID)
	if err != nil || d.Status != model.StatusApproved || d.ApprovedBy != designer.Email || d.Kind != model.KindUI {
		t.Fatalf("approved UI decision: %+v %v", d, err)
	}
}

func TestDecisionScopeIsChecked(t *testing.T) {
	a, _, _ := setup(t)
	if _, err := a.Submit(ctx, agent, decision("x", "r", "marketing")); err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("unknown kind: %v", err)
	}
	long := decision("x", strings.Repeat("a", model.MaxRule+1), model.KindProduct)
	if _, err := a.Submit(ctx, agent, long); err == nil {
		t.Fatal("a rule longer than the limit was accepted")
	}
	if _, err := a.Submit(ctx, agent, decision("x", "r", model.KindProduct, "Booking SVC")); err == nil {
		t.Fatal("an invalid service name was accepted")
	}
	// Once dumen.yml names the services, a decision may only name those.
	if _, err := a.Submit(ctx, testutil.Lead, &SetProduct{Services: map[string]string{"booking-svc": "Bookings", "web-app": "Site"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Submit(ctx, agent, decision("x", "r", model.KindProduct, "billing-svc")); err == nil || !strings.Contains(err.Error(), "unknown service") {
		t.Fatalf("unknown service: %v", err)
	}
	if _, err := a.Submit(ctx, agent, decision("x", "r", model.KindProduct, "booking-svc", "*")); err != nil {
		t.Fatalf("known service: %v", err)
	}
}

// A replacement keeps the kind, scope and owner of what it replaces.
func TestSupersedeInheritsScope(t *testing.T) {
	a, _, _ := setup(t)
	old := decision("No overbooking", "No overbooking; holds take capacity under a row lock", model.KindProduct, "inventory-svc")
	old.Decision.AppliesTo.Topics = []string{"capacity"}
	old.Decision.Owner = "maya@example.com"
	old.Approve = true
	out, err := a.Submit(ctx, testutil.Lead, old)
	if err != nil {
		t.Fatal(err)
	}
	repl := &CreateDecision{Decision: model.Decision{ID: model.NewID(model.PrefixDecision), Title: "Flexible fares may overbook 2%",
		Rule: "No overbooking, except flexible-fare rooms up to 102%", Supersedes: []string{out.RecordID}}, Body: "Why.", Approve: true}
	got, err := a.Submit(ctx, testutil.Lead, repl)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := store.ReadDecision(a.Store.Root(), got.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != model.KindProduct || d.Owner != "maya@example.com" || !d.ForService("inventory-svc") || d.ForService("web-app") ||
		strings.Join(d.Topics(), ",") != "capacity" {
		t.Fatalf("replacement did not inherit the scope: %+v", d)
	}
}

// A store written by version 1 is raised to version 2 by the first decision this build
// writes, and its "draft" decisions read as proposed.
func TestVersion1StoreIsUpgraded(t *testing.T) {
	a, _, _ := setup(t)
	out, err := a.Submit(ctx, agent, draft("Old draft"))
	if err != nil {
		t.Fatal(err)
	}
	root := a.Store.Root()
	// Turn the store back into what version 1 wrote.
	_, rel, err := store.ReadDecision(root, out.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, filepath.FromSlash(rel))
	data, _ := os.ReadFile(file)
	os.WriteFile(file, []byte(strings.Replace(string(data), "status: proposed", "status: draft", 1)), 0o644)
	cfg, _ := store.ReadConfig(root)
	cfg.Version = 1
	if err := store.WriteConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	d, _, err := store.ReadDecision(root, out.RecordID)
	if err != nil || d.Status != model.StatusProposed {
		t.Fatalf("a version 1 draft did not read as proposed: %+v %v", d, err)
	}
	tx := &Tx{Root: root, Now: time.Now(), Actor: agent}
	if err := (&CreateDecision{Decision: model.Decision{ID: model.NewID(model.PrefixDecision), Title: "New", Rule: "r",
		Kind: model.KindTechnical}, Body: "b"}).Apply(tx); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.ReadConfig(root); cfg.Version != model.SchemaVersion {
		t.Fatalf("store version after a write: %d", cfg.Version)
	}
}

// Gauntlet A1: approving a decision also retires what it supersedes, so the approver
// must be allowed to approve those too.
func TestSupersedeNeedsRightsOverTheRetiredDecision(t *testing.T) {
	a, _, _ := setup(t)
	designer := model.Actor{Email: "sofia@example.com", Kind: model.KindHuman}
	if _, err := a.Submit(ctx, testutil.Lead, &SetRole{Email: designer.Email, Role: "designer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Submit(ctx, testutil.Lead, &SetProduct{Kinds: map[string]string{"ui": "designer", "ux": "designer", "architecture": "lead"}}); err != nil {
		t.Fatal(err)
	}
	arch := decision("Payments", "Only billing-svc talks to the payment provider", model.KindArchitecture)
	arch.Approve = true
	out, err := a.Submit(ctx, testutil.Lead, arch)
	if err != nil {
		t.Fatal(err)
	}
	sneak := decision("Checkout UX", "Checkout charges the card directly", model.KindUX)
	sneak.Decision.Supersedes = []string{out.RecordID}
	prop, err := a.Submit(ctx, agent, sneak)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Submit(ctx, designer, &ResolveDecision{ID: prop.RecordID, Status: model.StatusApproved}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the designer retired an architecture decision through a UX one: %v", err)
	}
	direct := decision("Checkout UX", "Checkout charges the card directly", model.KindUX)
	direct.Decision.Supersedes, direct.Approve = []string{out.RecordID}, true
	if _, err := a.Submit(ctx, designer, direct); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the designer superseded an architecture decision directly: %v", err)
	}
	got, err := a.Submit(ctx, testutil.Lead, &ResolveDecision{ID: prop.RecordID, Status: model.StatusApproved})
	if err != nil || len(got.Notes) == 0 || !strings.Contains(got.Notes[0], "[architecture]") {
		t.Fatalf("the lead's approval should name what it retires: %+v %v", got, err)
	}
}

// Gauntlet B1: a repository name with a dot is a valid service.
func TestServiceNamesFollowRepositoryNames(t *testing.T) {
	a, _, _ := setup(t)
	if _, err := a.Submit(ctx, agent, decision("x", "r", model.KindProduct, "billing.svc")); err != nil {
		t.Fatalf("dotted service: %v", err)
	}
	// Gauntlet B2: dumen product takes the same names.
	if _, err := a.Submit(ctx, testutil.Lead, &SetProduct{Services: map[string]string{"billing.svc": "Payments"}}); err != nil {
		t.Fatalf("dotted service in dumen product: %v", err)
	}
	if _, err := a.Submit(ctx, testutil.Lead, &SetProduct{Services: map[string]string{"Billing.Svc": "Payments"}}); err == nil {
		t.Fatal("dumen product accepted an upper-case service name")
	}
	if _, err := a.Submit(ctx, agent, decision("x", "r", model.KindProduct, "Billing.Svc")); err == nil {
		t.Fatal("an upper-case service name was accepted; .dumen stores repository names in lower case")
	}
}

// Decisions are binding but changeable: by default any team member approves, and an
// approval says when and where the decision came into force.
func TestAnyMemberApprovesByDefault(t *testing.T) {
	a, _, _ := setup(t)
	dev := model.Actor{Email: "ayse@example.com", Kind: model.KindHuman}
	if _, err := a.Submit(ctx, testutil.Lead, &SetRole{Email: dev.Email, Role: "backend"}); err != nil {
		t.Fatal(err)
	}
	d := decision("Refunds", "Cancelled bookings are refunded as store credit", model.KindProduct, "booking-svc", "billing-svc")
	d.Decision.Repo = "booking-svc"
	prop, err := a.Submit(ctx, agent, d)
	if err != nil {
		t.Fatal(err)
	}
	stranger := model.Actor{Email: "someone@example.com", Kind: model.KindHuman}
	if _, err := a.Submit(ctx, stranger, &ResolveDecision{ID: prop.RecordID, Status: model.StatusApproved}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a person with no role approved: %v", err)
	}
	if _, err := a.Submit(ctx, dev, &ResolveDecision{ID: prop.RecordID, Status: model.StatusApproved}); err != nil {
		t.Fatalf("a developer could not approve: %v", err)
	}
	got, _, err := store.ReadDecision(a.Store.Root(), prop.RecordID)
	if err != nil || got.ApprovedBy != dev.Email || got.ApprovedAt == "" || got.Repo != "booking-svc" {
		t.Fatalf("approved decision: %+v %v", got, err)
	}
	// Anyone on the team may change it again.
	change := decision("Refunds", "Cancelled bookings are refunded to the card", model.KindProduct)
	change.Decision.Supersedes, change.Approve = []string{prop.RecordID}, true
	if _, err := a.Submit(ctx, model.Actor{Email: testutil.Lead.Email, Kind: model.KindHuman}, change); err != nil {
		t.Fatalf("supersede by another member: %v", err)
	}
}
