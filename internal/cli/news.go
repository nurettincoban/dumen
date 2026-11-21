package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/activity"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/policy"
	"github.com/nurettincoban/dumen/internal/store"
)

func init() {
	commands = append(commands, command{"news", "news [--since 7d] [--repo NAME] [--all]",
		"list decisions that came into force or were replaced recently, newest first", cmdNews})
}

func cmdNews(a *app, args []string) error {
	fs := a.flags("news")
	since := fs.String("since", "7d", "today, yesterday, 2h, 7d, a date, or an RFC 3339 time")
	repo := fs.String("repo", "", "only decisions that concern this repository (default: the current one)")
	all := fs.Bool("all", false, "every repository of the project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	from, err := activity.ParseSince(*since, time.Now())
	if err != nil {
		return err
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	service := strings.ToLower(*repo)
	if service == "" && !*all {
		service = p.Repo
	}
	cut := model.FormatTime(from)
	return locked(a, p, func(root string) error {
		decs, err := store.ListDecisions(root)
		if err != nil {
			return err
		}
		cfg, err := store.ReadConfig(root)
		if err != nil {
			return err
		}
		byID := map[string]model.Decision{}
		for _, d := range decs {
			byID[d.ID] = d
		}
		var recent []model.Decision
		for _, d := range decs {
			if d.Status != model.StatusApproved || d.ApprovedAt < cut {
				continue
			}
			if service != "" && !(d.ForService(service) && policy.RelevantTo(cfg, d.Paths, d.Area, service)) {
				continue
			}
			recent = append(recent, d)
		}
		sort.SliceStable(recent, func(i, j int) bool { return recent[i].ApprovedAt > recent[j].ApprovedAt })
		where := "this project"
		if service != "" {
			where = service
		}
		if len(recent) == 0 {
			fmt.Fprintf(a.out, "No decision came into force for %s since %s.\n", where, from.Local().Format("2006-01-02 15:04"))
			return nil
		}
		fmt.Fprintf(a.out, "Decisions in force for %s since %s (%d), newest first\n\n", where, from.Local().Format("2006-01-02 15:04"), len(recent))
		for _, d := range recent {
			at, _ := model.ParseTime(d.ApprovedAt)
			fmt.Fprintf(a.out, "%s  %s  %-12s %s\n", at.Local().Format("01-02 15:04"), d.ID, clip(orDash(d.Kind), 12), clip(d.Headline(), 100))
			made := "approved by " + orDash(d.ApprovedBy)
			if d.Repo != "" {
				made += " in " + d.Repo
			}
			fmt.Fprintf(a.out, "                    %s; applies to %s\n", made, services(d))
			for _, old := range d.Supersedes {
				fmt.Fprintf(a.out, "                    replaces %s: %s\n", old, clip(byID[old].Headline(), 90))
			}
		}
		fmt.Fprintln(a.out, "\n`dumen show <id>` has the reasons. To change a decision: `dumen supersede <id>`.")
		return nil
	})
}
