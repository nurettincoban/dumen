package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
)

func TestProductAndQuestions(t *testing.T) {
	_, p := repo(t)
	run(t, "product", "--name", "Online reservations", "--service", "booking-svc=Creates bookings",
		"--topic", "booking-flow=booking page, time picker, slot", "--kind", "security=lead")
	out := run(t, "product")
	for _, want := range []string{"Online reservations", "booking-svc", "time picker", "security", "ux "} {
		if !strings.Contains(out, want) {
			t.Fatalf("product output lacks %q:\n%s", want, out)
		}
	}
	run(t, "product", "--topic", "booking-flow=")
	if out := run(t, "product"); strings.Contains(out, "time picker") {
		t.Fatalf("an empty value did not remove the topic:\n%s", out)
	}
	// An answer an agent saved waits in the queue of whoever may approve its kind.
	agent := model.Actor{Email: p.Store.Email, Kind: model.KindAgent}
	d := model.Decision{ID: model.NewID(model.PrefixDecision), Title: "Time selection", Rule: "Slot buttons", Kind: model.KindUI,
		Origin: &model.Origin{Question: "How do guests pick a time?", Answer: "Slot buttons", AnsweredBy: "ayse@example.com", Verified: true}}
	if _, err := p.Engine().Submit(context.Background(), agent, &op.CreateDecision{Decision: d, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	q := run(t, "questions")
	if !strings.Contains(q, "Slot buttons") || !strings.Contains(q, "How do guests pick a time?") || !strings.Contains(q, "seen in the question tool") {
		t.Fatalf("questions:\n%s", q)
	}
	// Inside Claude Code the command is an agent's and is refused.
	t.Setenv("CLAUDECODE", "1")
	if out, code := runErr("product", "--name", "x"); code == 0 {
		t.Fatalf("an agent changed the product: %s", out)
	}
}
