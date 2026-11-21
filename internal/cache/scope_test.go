package cache

import (
	"testing"

	"github.com/nurettincoban/dumen/internal/model"
)

func TestScopeLabelAndConcerns(t *testing.T) {
	c := &Cache{}
	paths := Decision{ID: "D-1", Paths: []string{"billing-svc:src/**"}}
	svc := Decision{ID: "D-2", AppliesTo: model.AppliesTo{Services: []string{"orders-svc"}}}
	wide := Decision{ID: "D-3"}
	for _, x := range []struct {
		d            Decision
		label        string
		billing, ord bool
	}{
		{paths, "paths billing-svc:src/**", true, false},
		{svc, "orders-svc", false, true},
		{wide, "product-wide", true, true},
	} {
		if got := x.d.ScopeLabel(); got != x.label {
			t.Errorf("%s label: %q", x.d.ID, got)
		}
		if c.Concerns(x.d, "billing-svc") != x.billing || c.Concerns(x.d, "orders-svc") != x.ord {
			t.Errorf("%s concerns: billing %v orders %v", x.d.ID, c.Concerns(x.d, "billing-svc"), c.Concerns(x.d, "orders-svc"))
		}
	}
}
