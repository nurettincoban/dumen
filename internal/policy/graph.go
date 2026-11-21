// Package policy holds the rules that are derived from records rather than stored in them.
package policy

import (
	"fmt"
	"sort"

	"github.com/nurettincoban/dumen/internal/model"
)

// SupersedeCycle returns an error if the supersedes links among the decisions form a cycle.
func SupersedeCycle(decisions []model.Decision) error {
	next := map[string][]string{}
	for _, d := range decisions {
		next[d.ID] = d.Supersedes
	}
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(id string) error
	visit = func(id string) error {
		color[id] = grey
		for _, n := range next[id] {
			switch color[n] {
			case grey:
				return fmt.Errorf("supersede cycle through %s and %s", id, n)
			case white:
				if err := visit(n); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}
	ids := make([]string, 0, len(next))
	for id := range next {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if color[id] == white {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// Derived holds the statuses that are computed, never written.
type Derived struct {
	SupersededBy map[string][]string // decision id -> approved decisions that supersede it
	Contested    map[string]bool     // superseded by two or more approved decisions
}

// Derive computes superseded and contested decisions from the approved ones.
func Derive(decisions []model.Decision) Derived {
	d := Derived{SupersededBy: map[string][]string{}, Contested: map[string]bool{}}
	for _, dec := range decisions {
		if dec.Status != model.StatusApproved {
			continue
		}
		for _, s := range dec.Supersedes {
			d.SupersededBy[s] = append(d.SupersededBy[s], dec.ID)
		}
	}
	for id, by := range d.SupersededBy {
		sort.Strings(by)
		d.SupersededBy[id] = by
		if len(by) > 1 {
			d.Contested[id] = true
		}
	}
	return d
}
