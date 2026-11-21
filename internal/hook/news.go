package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nurettincoban/dumen/internal/cache"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/render"
	"github.com/nurettincoban/dumen/internal/session"
)

// maxNews bounds the decisions one news note names; the rest are counted.
const maxNews = 5

// newsLine is a decision line with who put it in force, where, and what it replaced.
func (r *runCtx) newsLine(d cache.Decision) string {
	var extra []string
	if d.ApprovedBy != "" {
		who := render.Label(d.ApprovedBy, 80)
		if strings.EqualFold(d.ApprovedBy, r.proj.Store.Email) {
			who = "you"
		}
		extra = append(extra, "approved by "+who)
	}
	if d.Repo != "" && d.Repo != r.proj.Repo {
		extra = append(extra, "in "+d.Repo)
	}
	if len(d.Supersedes) > 0 {
		extra = append(extra, "replaces "+strings.Join(d.Supersedes, ", "))
	}
	line := decisionLine(d)
	if len(extra) > 0 {
		line += " (" + strings.Join(extra, "; ") + ")"
	}
	return line + r.needsLine(d)
}

// needsLine says what a decision asks of this repository, or which services it asks.
func (r *runCtx) needsLine(d cache.Decision) string {
	if len(d.Needs) == 0 {
		return ""
	}
	if ask, ok := d.Needs[r.proj.Repo]; ok && r.proj.Repo != "" {
		return "\n  NEEDS FROM " + r.proj.Repo + ": " + render.Label(ask, model.MaxNeed)
	}
	return "\n  needs work from: " + strings.Join(model.SortedKeys(d.Needs), ", ")
}

// news returns the approved decisions that concern this repository and that the session
// has not been given yet, oldest first, so that a change reads in the order it happened.
func (r *runCtx) news() []cache.Decision {
	sub := r.in.AgentID
	if !r.sess.NewsBase {
		// No session start was seen (dumen was installed mid-session): what is in force now
		// is the baseline, and only later changes are news.
		for _, d := range r.c.Decisions {
			r.sess.Known = session.Add(r.sess.Known, d.ID)
		}
		r.sess.NewsBase = true
		r.sess.Save()
		return nil
	}
	own := session.Approved(r.proj.Store.SessionsDir(), r.sess.Session)
	var out []cache.Decision
	for i := len(r.c.Decisions) - 1; i >= 0; i-- {
		d := r.c.Decisions[i]
		// A subagent has its own context: what it is told does not count for the main agent.
		if session.Has(r.sess.Known, d.ID) || (sub != "" && session.Has(r.sess.SubKnown[sub], d.ID)) || !r.c.Concerns(d, r.proj.Repo) {
			continue
		}
		// This session's own dumen server put it in force: the agent called approve or
		// supersede itself, so it is not news here. Every other session, the same
		// person's included, still gets it.
		if sub == "" && session.Has(own, d.ID) {
			r.sess.Known = session.Add(r.sess.Known, d.ID)
			continue
		}
		out = append(out, d)
	}
	return out
}

// newsBlock hands over decisions that came into force while the session was running.
// Every decision named is marked as given, so it is shown once.
func (r *runCtx) newsBlock() string {
	ds := r.news()
	if len(ds) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DECISION NEWS (%d): put in force since this session started; binding from now on\n", len(ds))
	ids := make([]string, 0, len(ds))
	for i, d := range ds {
		if sub := r.in.AgentID; sub != "" {
			if r.sess.SubKnown == nil {
				r.sess.SubKnown = map[string][]string{}
			}
			r.sess.SubKnown[sub] = session.Add(r.sess.SubKnown[sub], d.ID)
		} else {
			r.sess.Known = session.Add(r.sess.Known, d.ID)
			r.sess.Noted = session.Add(r.sess.Noted, d.ID)
		}
		ids = append(ids, d.ID)
		if i < maxNews {
			b.WriteString(r.newsLine(d) + "\n")
		}
	}
	if len(ds) > maxNews {
		fmt.Fprintf(&b, "... and %d more; decisions_for_topic and get_decision have them.\n", len(ds)-maxNews)
	}
	b.WriteString("Follow them from now on. If one changes what you are doing, or NEEDS work from this service, tell your human in one line; " +
		"a decision replaced by another is no longer in force. Anyone on the team can change a decision with supersede.")
	r.log(model.EventDecisionRead, "decision news delivered", ids...)
	return b.String()
}

// seenFile keeps, per repository on this machine, when its last session started, so
// that the next one can say what changed in between.
func (r *runCtx) seenFile() string {
	name := r.proj.Repo
	if name == "" {
		name = "_"
	}
	return filepath.Join(r.proj.Store.SessionsDir(), "seen-"+name)
}

// sinceLastSession lists approved decisions that came into force after the previous
// session in this repository started, and records this session's start.
func (r *runCtx) sinceLastSession() string {
	path := r.seenFile()
	data, _ := os.ReadFile(path)
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(model.FormatTime(r.now)), 0o600)
	last := strings.TrimSpace(string(data))
	if _, err := model.ParseTime(last); err != nil {
		return "" // first session here: everything is new, and the list below has it
	}
	var ds []cache.Decision
	for i := len(r.c.Decisions) - 1; i >= 0; i-- {
		d := r.c.Decisions[i]
		// At one-second resolution, a decision approved in the second the previous
		// session started may be shown again; that is better than missing it.
		if d.ApprovedAt >= last && r.c.Concerns(d, r.proj.Repo) {
			ds = append(ds, d)
		}
	}
	if len(ds) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nCHANGED SINCE THE LAST SESSION HERE (%d)\n", len(ds))
	for i, d := range ds {
		if i == maxNews {
			fmt.Fprintf(&b, "... and %d more\n", len(ds)-maxNews)
			break
		}
		b.WriteString(r.newsLine(d) + "\n")
	}
	return b.String()
}
