package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/activity"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/project"
	"github.com/nurettincoban/dumen/internal/store"
)

// queryLocked refreshes (best effort) and reads events while holding the store lock, so a
// concurrent sync cannot reset the checkout mid-read.
func queryLocked(a *app, p *project.Project, f activity.Filter, refresh bool) ([]model.Event, []model.Directive, model.Config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if refresh {
		if _, err := p.Refresh(ctx); err != nil {
			fmt.Fprintln(a.err, "warning: remote unreachable, showing the last synced state")
		}
	}
	unlock, err := p.Store.Lock(ctx)
	if err != nil {
		return nil, nil, model.Config{}, err
	}
	defer unlock()
	evs, err := activity.Query(p.Store.Root(), f)
	if err != nil {
		return nil, nil, model.Config{}, err
	}
	dirs, err := store.ListDirectives(p.Store.Root())
	if err != nil {
		return nil, nil, model.Config{}, err
	}
	cfg, err := store.ReadConfig(p.Store.Root())
	return evs, dirs, cfg, err
}

func eventLine(e model.Event) string {
	ts, _ := model.ParseTime(e.TS)
	ids := ""
	if len(e.IDs) > 0 {
		ids = " [" + strings.Join(e.IDs, ", ") + "]"
	}
	who := e.Person
	if e.Repo != "" {
		who += " @" + e.Repo
	}
	return fmt.Sprintf("%s  %-14s %-34s %-10s %s%s", ts.Local().Format("01-02 15:04:05"), e.Kind, clip(who, 34), clip(e.Session, 10), e.Text, ids)
}

func cmdActivity(a *app, args []string) error {
	fs := a.flags("activity")
	since := fs.String("since", "today", "today, yesterday, 2h, 7d, a date, or an RFC 3339 time")
	person := fs.String("person", "", "only this person (email or part of it)")
	kind := fs.String("kind", "", "only this event kind: "+strings.Join(model.EventKinds, ", "))
	repo := fs.String("repo", "", "only sessions working in this repository")
	events := fs.Bool("events", false, "list every event instead of the summary")
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	now := time.Now()
	from, err := activity.ParseSince(*since, now)
	if err != nil {
		return err
	}
	evs, dirs, cfg, err := queryLocked(a, p, activity.Filter{Since: from, Person: *person, Kind: *kind, Repo: *repo}, true)
	if err != nil {
		return err
	}
	sum := activity.Summarize(evs)
	type unacked struct {
		Directive string   `json:"directive"`
		Text      string   `json:"text"`
		Missing   []string `json:"missing"`
	}
	var missing []unacked
	if *person == "" && *kind == "" && *repo == "" {
		// Acknowledgements are looked up over all time, not only inside --since.
		all, _, _, err := queryLocked(a, p, activity.Filter{Kind: model.EventDirectiveAck}, false)
		if err != nil {
			return err
		}
		allSum := activity.Summarize(all)
		for _, d := range dirs {
			if d.ActiveAt(now.UTC()) {
				if m := activity.Unacked(cfg, d, allSum); len(m) > 0 {
					missing = append(missing, unacked{d.ID, d.Text, m})
				}
			}
		}
	}
	if *asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"since": model.FormatTime(from), "summary": sum, "unacknowledged": missing})
	}
	if *events {
		for _, e := range evs {
			fmt.Fprintln(a.out, eventLine(e))
		}
		return nil
	}
	fmt.Fprintf(a.out, "Since %s\n", from.Local().Format("2006-01-02 15:04"))
	fmt.Fprintf(a.out, "started %d  finished %d  blocked %d  asking_human %d  progress %d  directive_ack %d  draft_written %d  decision_read %d  private %d\n",
		sum.Counts[model.EventStarted], sum.Counts[model.EventFinished], sum.Counts[model.EventBlocked],
		sum.Counts[model.EventAskingHuman], sum.Counts[model.EventProgress], sum.Counts[model.EventDirectiveAck],
		sum.Counts[model.EventDraftWritten], sum.Counts[model.EventDecisionRead], sum.Counts[model.EventPrivate])
	if len(sum.ByRepo) > 0 {
		fmt.Fprint(a.out, "By repository:")
		for _, r := range model.SortedKeys(sum.ByRepo) {
			fmt.Fprintf(a.out, "  %s %d", r, sum.ByRepo[r])
		}
		fmt.Fprintln(a.out, " sessions")
	}
	if len(sum.Sessions) > 0 {
		fmt.Fprintf(a.out, "\nSessions (%d)\n", len(sum.Sessions))
		for _, s := range sum.Sessions {
			first, _ := model.ParseTime(s.First)
			last, _ := model.ParseTime(s.Last)
			who := s.Person
			if s.Repo != "" {
				who += " @" + s.Repo
			}
			fmt.Fprintf(a.out, "  %-34s %-10s %-13s %s-%s  %s\n", clip(who, 34), clip(s.Session, 10), s.State,
				first.Local().Format("15:04"), last.Local().Format("15:04"), clip(s.LastText, 90))
		}
	}
	if len(sum.Stuck) > 0 {
		fmt.Fprintf(a.out, "\nStuck (%d)\n", len(sum.Stuck))
		for _, s := range sum.Stuck {
			fmt.Fprintf(a.out, "  %-24s %-10s %-13s %s\n", clip(s.Person, 24), clip(s.Session, 10), s.State, clip(s.LastText, 100))
		}
	}
	if len(missing) > 0 {
		fmt.Fprintln(a.out, "\nNot yet acknowledged")
		for _, m := range missing {
			fmt.Fprintf(a.out, "  %s %s\n    %s\n", m.Directive, clip(strings.ReplaceAll(m.Text, "\n", " "), 70), strings.Join(m.Missing, ", "))
		}
	}
	return nil
}

func cmdWatch(a *app, args []string) error {
	fs := a.flags("watch")
	interval := fs.Duration("interval", 5*time.Second, "how often to check the remote")
	once := fs.Bool("once", false, "print what is new and exit (for scripts)")
	since := fs.String("since", "10m", "how far back to start")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	from, err := activity.ParseSince(*since, time.Now())
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, perr := p.Poll(ctx)
		cancel()
		if perr != nil && *once {
			fmt.Fprintln(a.err, "warning: remote unreachable")
		}
		evs, _, _, err := queryLocked(a, p, activity.Filter{Since: from}, false)
		if err != nil {
			return err
		}
		for _, e := range evs {
			key := e.TS + "|" + e.Session + "|" + e.Kind + "|" + e.Text + "|" + strings.Join(e.IDs, ",")
			if !seen[key] {
				seen[key] = true
				fmt.Fprintln(a.out, eventLine(e))
			}
		}
		if *once {
			return nil
		}
		time.Sleep(*interval)
	}
}

func cmdPrivate(a *app, args []string) error {
	p, err := openProject()
	if err != nil {
		return err
	}
	if os.Getenv("CLAUDECODE") != "" {
		return fmt.Errorf("forbidden: only a human can turn monitoring off; ask your human to run this in their own terminal")
	}
	marker := p.PrivateMarker()
	mode := "on"
	if len(args) > 0 {
		mode = args[0]
	}
	switch mode {
	case "on":
		if err := os.WriteFile(marker, []byte(model.FormatTime(time.Now())+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "New Claude Code sessions on this machine are not monitored: no activity is logged except one line saying the session is private.")
		fmt.Fprintln(a.out, "Directives are still delivered and enforced. Run `dumen private off` to resume.")
	case "off":
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintln(a.out, "Monitoring resumed for new sessions.")
	default:
		return fmt.Errorf("usage: dumen private [on|off]")
	}
	return nil
}

func cmdGC(a *app, args []string) error {
	p, err := openProject()
	if err != nil {
		return err
	}
	for _, o := range []op.Op{&op.ArchiveStaleDrafts{}, &op.CompactActivity{Days: 30}} {
		out, err := a.submit(p, o)
		if err != nil {
			return err
		}
		_ = out
	}
	gitOut(p.Store.Root(), "gc", "-q")
	fmt.Fprintln(a.out, "Done: stale drafts archived, activity older than 30 days compacted.")
	return nil
}
