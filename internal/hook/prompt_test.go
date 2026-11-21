package hook

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/session"
	"github.com/nurettincoban/dumen/internal/testutil"
)

// Gauntlet P2: the permission prompt of every human action says what will happen, so
// the person knows what they say yes to.
func TestPromptSaysWhatHappens(t *testing.T) {
	f := newFixture(t)
	f.run(SessionStart, map[string]any{"source": "startup"})
	session.RegisterServer(f.proj.Store.SessionsDir(), 4242, os.Getpid())
	dec := f.submit(&op.CreateDecision{Decision: model.Decision{ID: model.NewID("D"), Title: "Refunds", Rule: "Cancelled bookings are refunded as store credit",
		Kind: model.KindProduct}, Body: "Why."})
	dir := f.directive(model.DirectiveContext, model.Scope{All: true}, "Use the structured logger")
	prop, err := f.eng.Submit(context.Background(), testutil.Lead, &op.CreateDirective{Directive: model.Directive{ID: model.NewID("Y"), Kind: model.DirectiveDeny,
		Scope: model.Scope{Paths: []string{"db/migrations/**"}}, Expires: model.FormatTime(time.Now().Add(48 * time.Hour))}, Text: "Migration freeze", Propose: true})
	if err != nil {
		t.Fatal(err)
	}
	ref := f.submit(&op.AddRef{Ref: model.Ref{ID: model.NewID("R"), URL: "https://design.example.com/slots", Kind: "design", Title: "Slot picker design"}})
	if _, err := f.proj.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tool string
		in   map[string]any
		want string
	}{
		{"reject", map[string]any{"id": dec}, `It rejects ` + dec + ` [product] "Cancelled bookings are refunded as store credit"`},
		{"withdraw_directive", map[string]any{"id": dir}, `It ends directive ` + dir + ` [context] "Use the structured logger"`},
		{"publish_directive", map[string]any{"text": "Freeze billing auth", "kind": "deny", "paths": []string{"billing-svc:src/auth/**"}}, `It tells paths billing-svc:src/auth/** [deny] for 7 days: "Freeze billing auth"`},
		{"publish_directive", map[string]any{"id": prop.RecordID}, `It tells paths db/migrations/** [deny] until `},
		{"publish_directive", map[string]any{"id": prop.RecordID}, `: "Migration freeze"`},
		{"set_role", map[string]any{"email": "ayse@example.com", "role": "lead"}, `It gives ayse@example.com the role "lead"`},
		{"set_role", map[string]any{"email": "ayse@example.com"}, `It removes ayse@example.com from the team`},
		{"set_private", map[string]any{"on": true}, `turns monitoring off`},
		{"set_private", map[string]any{"on": false}, `turns monitoring back on`},
		{"check_ref", map[string]any{"id": ref}, `It confirms link ` + ref + ` "Slot picker design" (https://design.example.com/slots)`},
		{"set_product", map[string]any{"name": "Table reservations", "services": map[string]string{"booking-svc": "Bookings", "old-svc": ""}, "kinds": map[string]string{"ui": "designer"}},
			`It names the product "Table reservations". It sets service booking-svc to "Bookings". It removes service old-svc. It sets the approver role of kind ui to "designer".`},
	} {
		o := f.run(PreToolUse, map[string]any{"tool_name": "mcp__dumen__" + c.tool, "permission_mode": "default", "tool_input": c.in})
		if decision(o) != "ask" {
			t.Errorf("%s: want ask, got %+v", c.tool, o)
			continue
		}
		if r := o.HookSpecificOutput.PermissionDecisionReason; !strings.Contains(r, c.want) {
			t.Errorf("%s prompt:\n%s\nwant it to contain:\n%s", c.tool, r, c.want)
		}
	}

	many := map[string]string{}
	for i := 0; i < 40; i++ {
		many[fmt.Sprintf("t%02d", i)] = "k"
	}
	o := f.run(PreToolUse, map[string]any{"tool_name": "mcp__dumen__set_product", "permission_mode": "default", "tool_input": map[string]any{"topics": many}})
	if r := o.HookSpecificOutput.PermissionDecisionReason; !strings.Contains(r, "and 28 more changes") || len(r) > 2000 {
		t.Errorf("a long set_product prompt must be capped:\n%s", r)
	}
}
