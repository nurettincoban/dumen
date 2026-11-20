package model

import (
	"testing"
	"time"
)

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		id := NewID(PrefixDecision)
		if !ValidID(id, PrefixDecision) {
			t.Fatalf("invalid id %q", id)
		}
		seen[id] = true
	}
	if len(seen) < 1900 {
		t.Fatalf("ids are not random enough: %d unique of 2000", len(seen))
	}
	if ValidID("D-ILOU", "") || ValidID("D-12345", "") || ValidID("X-1234", "") || ValidID("D-1234", PrefixRef) {
		t.Fatal("ValidID accepted a bad id")
	}
}

func TestSlug(t *testing.T) {
	if got := Slug("  Pagination uses Opaque cursors!  "); got != "pagination-uses-opaque-cursors" {
		t.Fatalf("got %q", got)
	}
}

func TestDirectiveValidateAndActive(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	d := Directive{ID: "Y-7K2M", Kind: DirectiveDeny, Scope: Scope{Paths: []string{"src/auth/**"}},
		PublishedBy: "pm@x.io", Starts: FormatTime(now), Expires: FormatTime(now.Add(time.Hour)),
		Status: DirectiveActive, Text: "do not touch auth"}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if !d.ActiveAt(now) || d.ActiveAt(now.Add(time.Hour)) || d.ActiveAt(now.Add(-time.Second)) {
		t.Fatal("ActiveAt window is wrong")
	}
	d.Scope = Scope{All: true}
	if d.Validate() == nil {
		t.Fatal("deny directive with scope all must be rejected")
	}
	d.Kind = DirectiveContext
	d.Scope = Scope{All: true, Role: "backend"}
	if d.Validate() == nil {
		t.Fatal("two scope selectors must be rejected")
	}
}

func TestEventValidate(t *testing.T) {
	e := Event{TS: "2026-11-20T12:00:00Z", Session: "abc-123", Person: "a@x.io", Kind: EventStarted}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Session = "../etc"
	if e.Validate() == nil {
		t.Fatal("unsafe session accepted")
	}
}
