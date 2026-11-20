// Package concurrency holds acceptance test 1: six writers, 1,000 random operations
// against a local bare repository, zero lost writes and no merge commits.
package concurrency

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nurettincoban/comm-pro/internal/model"
	"github.com/nurettincoban/comm-pro/internal/op"
	"github.com/nurettincoban/comm-pro/internal/outbox"
	"github.com/nurettincoban/comm-pro/internal/render"
	"github.com/nurettincoban/comm-pro/internal/store"
	"github.com/nurettincoban/comm-pro/internal/testutil"
)

const writers = 6

type expect struct {
	mu        sync.Mutex
	events    map[string]bool // unique event text
	drafts    map[string]bool // unique decision title
	refs      map[string]bool // unique ref title
	dirs      map[string]bool // unique directive text
	approved  map[string]bool // decision ids approved by the lead
	submitted int
}

func TestSixWritersThousandOperations(t *testing.T) {
	total := 1000
	if v := os.Getenv("DUMEN_CONCURRENCY_OPS"); v != "" {
		total, _ = strconv.Atoi(v)
	}
	if testing.Short() {
		total = 120
	}
	ctx := context.Background()
	testutil.Home(t)
	remote := testutil.BareRemote(t)

	engines := make([]*op.Engine, writers)
	for i := range engines {
		engines[i] = &op.Engine{Store: testutil.NewStore(t, remote, fmt.Sprintf("writer%d", i)), Regenerate: render.Regenerate}
	}
	exp := &expect{events: map[string]bool{}, drafts: map[string]bool{}, refs: map[string]bool{},
		dirs: map[string]bool{}, approved: map[string]bool{}}
	base := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			e := engines[w]
			rng := rand.New(rand.NewPCG(uint64(w)+1, 42))
			actor := model.Actor{Email: fmt.Sprintf("dev%d@example.com", w), Kind: model.KindAgent}
			if w == 0 { // writer 0 is the lead, working as a human
				actor = testutil.Lead
			}
			var myDrafts []string // ids of this writer's drafts that reached the remote
			n := total / writers
			if w < total%writers {
				n++
			}
			for i := 0; i < n; i++ {
				tag := fmt.Sprintf("w%d-%04d", w, i)
				var o op.Op
				var record func(out op.Outcome)
				switch r := rng.IntN(100); {
				case r < 55:
					text := "event " + tag
					o = &op.AppendEvent{Event: model.Event{TS: model.FormatTime(base.Add(time.Duration(i) * time.Second)),
						Session: fmt.Sprintf("session-%d-%d", w, i%3), Person: actor.Email, Kind: model.EventProgress, Text: text}}
					record = func(op.Outcome) { exp.events[text] = true }
				case r < 80:
					title := "decision " + tag
					o = &op.CreateDecision{Decision: model.Decision{ID: model.NewID(model.PrefixDecision), Title: title,
						Paths: []string{"src/" + tag}}, Body: "Body of " + tag}
					record = func(op.Outcome) { exp.drafts[title] = true }
				case r < 90:
					title := "ref " + tag
					o = &op.AddRef{Ref: model.Ref{ID: model.NewID(model.PrefixRef), URL: "https://example.com/" + tag,
						Kind: "doc", Title: title}}
					record = func(op.Outcome) { exp.refs[title] = true }
				case w == 0 && len(myDrafts) > 0 && r < 95:
					id := myDrafts[0]
					myDrafts = myDrafts[1:]
					o = &op.ResolveDecision{ID: id, Status: model.StatusApproved}
					record = func(op.Outcome) { exp.approved[id] = true }
				case w == 0:
					text := "directive " + tag
					o = &op.CreateDirective{Directive: model.Directive{ID: model.NewID(model.PrefixDirective), Kind: model.DirectiveContext,
						Scope: model.Scope{All: true}, Expires: model.FormatTime(time.Now().Add(time.Hour))}, Text: text}
					record = func(op.Outcome) { exp.dirs[text] = true }
				default:
					text := "event " + tag
					o = &op.AppendEvent{Event: model.Event{TS: model.FormatTime(base.Add(time.Duration(i) * time.Second)),
						Session: fmt.Sprintf("session-%d-x", w), Person: actor.Email, Kind: model.EventBlocked, Text: text}}
					record = func(op.Outcome) { exp.events[text] = true }
				}
				out, err := e.Submit(ctx, actor, o)
				if err != nil {
					t.Errorf("writer %d op %d (%s): %v", w, i, o.Kind(), err)
					continue
				}
				exp.mu.Lock()
				exp.submitted++
				record(out)
				exp.mu.Unlock()
				if c, ok := o.(*op.CreateDecision); ok && w == 0 && out.Status == op.Applied {
					myDrafts = append(myDrafts, c.Decision.ID)
				}
			}
			// Drain whatever lost every race during the run.
			for tries := 0; outbox.Count(e.Store.OutboxDir()) > 0; tries++ {
				if tries > 200 {
					t.Errorf("writer %d could not drain its outbox", w)
					return
				}
				if _, err := e.Flush(ctx); err != nil {
					t.Errorf("writer %d flush: %v", w, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if exp.submitted != total {
		t.Fatalf("submitted %d operations, want %d", exp.submitted, total)
	}

	// Verify from a brand-new clone, so nothing local can hide a lost write.
	v := testutil.NewStore(t, remote, "verifier")
	root := v.Root()
	for _, e := range engines {
		if n := outbox.Count(e.Store.FailedDir()); n != 0 {
			t.Errorf("%s has %d rejected operations", e.Store.Project, n)
		}
	}
	if merges, err := v.HasMergeCommits(ctx); err != nil || merges {
		t.Errorf("merge commits on the dumen branch: %v %v", merges, err)
	}

	seen := map[string]int{}
	days, _ := store.ListDays(root)
	for _, day := range days {
		evs, err := store.ReadEvents(root, day)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			seen[e.Text]++
		}
	}
	for text := range exp.events {
		if seen[text] != 1 {
			t.Errorf("event %q appears %d times, want 1", text, seen[text])
		}
	}
	if len(seen) != len(exp.events) {
		t.Errorf("found %d distinct events, want %d", len(seen), len(exp.events))
	}

	decs, err := store.ListDecisions(root)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]int{}
	approved := 0
	for _, d := range decs {
		titles[d.Title]++
		if d.Status == model.StatusApproved {
			approved++
			if !exp.approved[d.ID] {
				t.Errorf("decision %s is approved but nobody approved it", d.ID)
			}
		}
	}
	for title := range exp.drafts {
		if titles[title] != 1 {
			t.Errorf("decision %q appears %d times, want 1", title, titles[title])
		}
	}
	if len(decs) != len(exp.drafts) || approved != len(exp.approved) {
		t.Errorf("decisions: %d (want %d), approved %d (want %d)", len(decs), len(exp.drafts), approved, len(exp.approved))
	}

	refs, _ := store.ListRefs(root)
	if len(refs) != len(exp.refs) {
		t.Errorf("refs: %d, want %d", len(refs), len(exp.refs))
	}
	dirs, _ := store.ListDirectives(root)
	if len(dirs) != len(exp.dirs) {
		t.Errorf("directives: %d, want %d", len(dirs), len(exp.dirs))
	}
	t.Logf("%d operations: %d events, %d decisions (%d approved), %d refs, %d directives",
		total, len(exp.events), len(decs), approved, len(refs), len(dirs))
}
