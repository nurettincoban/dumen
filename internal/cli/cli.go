// Package cli implements the dumen command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/nurettincoban/comm-pro/internal/gitx"
	"github.com/nurettincoban/comm-pro/internal/hook"
	"github.com/nurettincoban/comm-pro/internal/install"
	"github.com/nurettincoban/comm-pro/internal/model"
	"github.com/nurettincoban/comm-pro/internal/op"
	"github.com/nurettincoban/comm-pro/internal/project"
	"github.com/nurettincoban/comm-pro/internal/store"
)

// Version is set at build time by goreleaser.
var Version = "dev"

type app struct {
	in       io.Reader
	out, err io.Writer
}

type command struct {
	name, usage, short string
	run                func(a *app, args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"init", "init [--store <remote-url>#<branch>]", "create the dumen store for this repository", cmdInit},
		{"join", "join", "connect this machine to the repository's dumen store", cmdJoin},
		{"status", "status", "show sync state, role and queued operations", cmdStatus},
		{"direct", `direct [--deny] (--all | --role R | --area A | --paths P1,P2) [--for 7d | --until DATE] "text"`, "publish a directive", cmdDirect},
		{"directives", "directives [--all]", "list directives", cmdDirectives},
		{"withdraw", "withdraw <id>", "withdraw a directive", cmdWithdraw},
		{"activity", "activity [--since today|2h|7d|DATE] [--person P] [--kind K] [--events] [--json]", "what agents did: counts, sessions, who is stuck", cmdActivity},
		{"watch", "watch [--interval 5s]", "live tail of agent activity", cmdWatch},
		{"private", "private [on|off]", "stop or resume monitoring of your new sessions (the fact stays visible)", cmdPrivate},
		{"gc", "gc", "archive stale drafts and compact activity older than 30 days", cmdGC},
		{"sync", "sync [--project NAME] [--quiet]", "push queued operations and refresh the local cache", cmdSync},
		{"install", "install [--binary PATH]", "write the Claude Code hook configuration into this repository", cmdInstall},
		{"hook", "hook <event>", "Claude Code hook entry point (reads JSON on stdin)", cmdHook},
		{"version", "version", "print the version", func(a *app, _ []string) error { fmt.Fprintln(a.out, "dumen", Version); return nil }},
	}
}

// Main runs the CLI and returns the process exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{in: stdin, out: stdout, err: stderr}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		a.help()
		return 0
	}
	for _, c := range commands {
		if c.name == args[0] {
			if err := c.run(a, args[1:]); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					return 0
				}
				fmt.Fprintln(stderr, "dumen:", err)
				return 1
			}
			return 0
		}
	}
	fmt.Fprintf(stderr, "dumen: unknown command %q (run `dumen help`)\n", args[0])
	return 2
}

func (a *app) help() {
	fmt.Fprintln(a.out, "dumen steers and monitors your team's Claude Code agents through your git remote.")
	fmt.Fprintln(a.out, "\nCommands:")
	for _, c := range commands {
		fmt.Fprintf(a.out, "  %-11s %s\n", c.name, c.short)
	}
}

func (a *app) flags(c string) *flag.FlagSet {
	fs := flag.NewFlagSet("dumen "+c, flag.ContinueOnError)
	fs.SetOutput(a.err)
	return fs
}

// ---- shared helpers ----

func longCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

func gitOut(dir string, args ...string) string {
	out, err := gitx.Git{Dir: dir}.Run(context.Background(), args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// actor is the person running the command. Inside a Claude Code session (CLAUDECODE is
// set in the shells Claude Code starts) the actor is an agent, so human-only operations
// are refused even if the Bash hook is bypassed.
func actor(dir string, s *store.Store) model.Actor {
	email := strings.ToLower(gitOut(dir, "config", "user.email"))
	if email == "" && s != nil {
		email = s.Email
	}
	kind := model.KindHuman
	if os.Getenv("CLAUDECODE") != "" {
		kind = model.KindAgent
	}
	return model.Actor{Email: email, Kind: kind}
}

func openProject() (*project.Project, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	p, err := project.Find(wd)
	if err != nil {
		if p != nil {
			return nil, fmt.Errorf("this machine has not joined project %q yet: run `dumen join`", p.Name)
		}
		return nil, err
	}
	return p, nil
}

// submit runs one operation and prints what happened.
func (a *app) submit(p *project.Project, o op.Op) (op.Outcome, error) {
	ctx, cancel := longCtx()
	defer cancel()
	wd, _ := os.Getwd()
	out, err := p.Engine().Submit(ctx, actor(wd, p.Store), o)
	if err != nil {
		return out, err
	}
	for _, n := range out.Notes {
		fmt.Fprintln(a.err, "note:", n)
	}
	if out.Status == op.Queued {
		fmt.Fprintln(a.err, "note: the remote could not be reached; the operation is queued and will be pushed by the next `dumen sync`.")
	}
	return out, nil
}

// SpawnSync starts a detached `dumen sync` for the project so a hook can return at once.
func SpawnSync(projectName string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "sync", "--quiet", "--project", projectName)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}

// ---- init / join ----

func resolveSpec(repoRoot, spec string) (remote, branch string, err error) {
	remote, branch = store.ParseSpec(spec)
	if remote == "" || remote == "origin" {
		remote = gitOut(repoRoot, "remote", "get-url", "origin")
		if remote == "" {
			return "", "", fmt.Errorf("this repository has no `origin` remote; pass --store <remote-url>#<branch>")
		}
	}
	if branch == "" {
		branch = store.DefaultBranch
	}
	return remote, branch, nil
}

func cmdInit(a *app, args []string) error {
	fs := a.flags("init")
	spec := fs.String("store", "origin#"+store.DefaultBranch, "where records live: <remote-url>#<branch>")
	name := fs.String("project", "", "project name (default: derived from the remote)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wd, _ := os.Getwd()
	root := gitOut(wd, "rev-parse", "--show-toplevel")
	if root == "" {
		return fmt.Errorf("run `dumen init` inside the git repository you want to steer")
	}
	if existing, err := project.Locate(root); err == nil {
		return fmt.Errorf("this repository already has a %s file (project %q); run `dumen join` instead", project.File, existing.Name)
	}
	remote, branch, err := resolveSpec(root, *spec)
	if err != nil {
		return err
	}
	me := actor(root, nil)
	if me.Email == "" {
		return fmt.Errorf("git user.email is not set; dumen uses it to identify you")
	}
	if *name == "" {
		*name = store.ProjectName(remote)
	}
	ctx, cancel := longCtx()
	defer cancel()
	s, res, err := store.Setup(ctx, remote, branch, *name, true, me)
	if err != nil {
		return err
	}
	if err := project.WriteFile(root, *name, *spec); err != nil {
		return err
	}
	p := &project.Project{Root: root, Name: *name, Spec: *spec, Store: s}
	if _, err := p.Refresh(ctx); err != nil {
		return err
	}
	if res.Created {
		fmt.Fprintf(a.out, "Created branch %q on %s with you (%s) as lead.\n", branch, remote, me.Email)
	} else {
		fmt.Fprintf(a.out, "Branch %q already existed on %s; joined it.\n", branch, remote)
	}
	fmt.Fprintf(a.out, "Wrote %s. Commit it so teammates can run `dumen join`.\n", project.File)
	fmt.Fprintf(a.out, "Local state: %s (never inside your working tree).\n", s.Home)
	for _, w := range res.Warnings {
		fmt.Fprintln(a.err, "warning:", w)
	}
	return nil
}

func cmdJoin(a *app, args []string) error {
	fs := a.flags("join")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wd, _ := os.Getwd()
	loc, err := project.Locate(wd)
	if err != nil {
		return err
	}
	remote, branch, err := resolveSpec(loc.Root, loc.Spec)
	if err != nil {
		return err
	}
	me := actor(loc.Root, nil)
	if me.Email == "" {
		return fmt.Errorf("git user.email is not set; dumen uses it to identify you")
	}
	ctx, cancel := longCtx()
	defer cancel()
	s, res, err := store.Setup(ctx, remote, branch, loc.Name, false, me)
	if err != nil {
		return err
	}
	loc.Store = s
	c, err := loc.Refresh(ctx)
	if err != nil {
		return err
	}
	role := c.Config.Role(me.Email)
	if role == "" {
		role = "unassigned (ask a lead to add you to dumen.yml)"
	}
	fmt.Fprintf(a.out, "Joined project %q as %s, role: %s.\n", loc.Name, me.Email, role)
	for _, w := range res.Warnings {
		fmt.Fprintln(a.err, "warning:", w)
	}
	return nil
}

// ---- status / sync ----

func cmdStatus(a *app, args []string) error {
	p, err := openProject()
	if err != nil {
		return err
	}
	wd, _ := os.Getwd()
	me := actor(wd, p.Store)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := p.Refresh(ctx)
	online := err == nil
	if err != nil {
		if c, err = readCache(p); err != nil {
			return fmt.Errorf("no local cache and the remote is unreachable")
		}
	}
	now := time.Now().UTC()
	fmt.Fprintf(a.out, "project   %s\nstore     %s#%s\n", p.Name, p.Store.Remote, p.Store.Branch)
	fmt.Fprintf(a.out, "you       %s (%s, role: %s)\n", me.Email, me.Kind, orDash(c.Config.Role(me.Email)))
	if online {
		fmt.Fprintf(a.out, "sync      up to date (%s)\n", short(c.Head))
	} else {
		fmt.Fprintf(a.out, "sync      OFFLINE, last sync %s ago\n", c.Age(now).Round(time.Second))
	}
	fmt.Fprintf(a.out, "active    %d directives, %d approved decisions, %d drafts, %d proposed directives\n",
		len(c.ActiveDirectives(c.Config.Role(me.Email), now)), len(c.Decisions), c.Drafts, c.Proposed)
	queued, failed := countJSON(p.Store.OutboxDir()), countJSON(p.Store.FailedDir())
	fmt.Fprintf(a.out, "outbox    %d queued, %d rejected\n", queued, failed)
	return nil
}

func cmdSync(a *app, args []string) error {
	fs := a.flags("sync")
	name := fs.String("project", "", "project name (default: from the .dumen file)")
	quiet := fs.Bool("quiet", false, "print nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var p *project.Project
	if *name != "" {
		s, err := store.Open(*name)
		if err != nil {
			return err
		}
		p = &project.Project{Name: *name, Store: s}
	} else {
		var err error
		if p, err = openProject(); err != nil {
			return err
		}
	}
	ctx, cancel := longCtx()
	defer cancel()
	rep, err := p.Engine().Flush(ctx)
	if err != nil {
		return err
	}
	if len(rep.Outcomes) == 0 && !rep.Offline {
		if _, err := p.Refresh(ctx); err != nil {
			return err
		}
	}
	if *quiet {
		return nil
	}
	applied, queued, rejected := 0, 0, 0
	for _, o := range rep.Outcomes {
		switch o.Status {
		case op.Applied:
			applied++
		case op.Queued:
			queued++
		case op.Rejected:
			rejected++
			fmt.Fprintf(a.err, "rejected: %s: %v\n", o.Kind, o.Err)
		}
	}
	state := "up to date"
	if rep.Offline {
		state = "remote unreachable"
	} else if rep.Contended {
		state = "remote busy, try again"
	}
	fmt.Fprintf(a.out, "%s: %d pushed, %d still queued, %d rejected\n", state, applied, queued, rejected)
	return nil
}

// ---- directives ----

func parseSpan(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q (use 7d, 12h, 30m)", s)
	}
	return d, nil
}

func parseWhen(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			if layout == "2006-01-02" {
				t = t.Add(24*time.Hour - time.Second) // end of that day
			}
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date %q (use 2026-11-27 or RFC 3339)", s)
}

func cmdDirect(a *app, args []string) error {
	fs := a.flags("direct")
	deny := fs.Bool("deny", false, "block edits under the scope (needs --paths or --area)")
	all := fs.Bool("all", false, "every agent")
	role := fs.String("role", "", "agents of people with this role")
	area := fs.String("area", "", "an area from dumen.yml")
	paths := fs.String("paths", "", "comma-separated path patterns, e.g. src/auth/**")
	span := fs.String("for", "7d", "how long it stays active (7d, 12h)")
	until := fs.String("until", "", "expiry date instead of --for")
	propose := fs.Bool("propose", false, "propose instead of publish (any role)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return fmt.Errorf("usage: dumen %s", `direct [--deny] (--all | --role R | --area A | --paths P1,P2) [--for 7d | --until DATE] "text"`)
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expires := now
	if *until != "" {
		if expires, err = parseWhen(*until); err != nil {
			return err
		}
	} else {
		d, err := parseSpan(*span)
		if err != nil {
			return err
		}
		expires = now.Add(d)
	}
	d := model.Directive{ID: model.NewID(model.PrefixDirective), Kind: model.DirectiveContext,
		Scope:  model.Scope{All: *all, Role: *role, Area: *area},
		Starts: model.FormatTime(now), Expires: model.FormatTime(expires)}
	if *deny {
		d.Kind = model.DirectiveDeny
	}
	for _, x := range strings.Split(*paths, ",") {
		if x = strings.TrimSpace(x); x != "" {
			d.Scope.Paths = append(d.Scope.Paths, x)
		}
	}
	if err := d.Scope.Validate(); err != nil {
		return fmt.Errorf("%v: pass exactly one of --all, --role, --area, --paths", err)
	}
	out, err := a.submit(p, &op.CreateDirective{Directive: d, Text: text, Propose: *propose})
	if err != nil {
		return err
	}
	verb := "Published"
	if *propose {
		verb = "Proposed"
	}
	fmt.Fprintf(a.out, "%s %s [%s] %s, until %s\n", verb, out.RecordID, d.Kind, d.Scope, expires.Local().Format("2006-01-02 15:04"))
	return nil
}

func cmdDirectives(a *app, args []string) error {
	fs := a.flags("directives")
	all := fs.Bool("all", false, "include expired, withdrawn and proposed directives")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := p.Refresh(ctx); err != nil {
		fmt.Fprintln(a.err, "warning: remote unreachable, showing the last synced state")
	}
	unlock, err := p.Store.Lock(ctx)
	if err != nil {
		return err
	}
	ds, err := store.ListDirectives(p.Store.Root())
	unlock()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Expires < ds[j].Expires })
	n := 0
	for _, d := range ds {
		state := d.Status
		if d.Status == model.DirectiveActive && !d.ActiveAt(now) {
			state = "expired"
			if st, _ := model.ParseTime(d.Starts); st.After(now) {
				state = "scheduled"
			}
		}
		if !*all && state != model.DirectiveActive && state != "scheduled" {
			continue
		}
		n++
		ex, _ := model.ParseTime(d.Expires)
		fmt.Fprintf(a.out, "%s  %-9s %-7s %-28s until %s  %s\n", d.ID, state, d.Kind, clip(d.Scope.String(), 28),
			ex.Local().Format("2006-01-02 15:04"), clip(strings.ReplaceAll(d.Text, "\n", " "), 80))
	}
	if n == 0 {
		fmt.Fprintln(a.out, "No active directives.")
	}
	return nil
}

func cmdWithdraw(a *app, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: dumen withdraw <id>")
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	out, err := a.submit(p, &op.SetDirectiveStatus{ID: strings.ToUpper(args[0]), Status: model.DirectiveWithdrawn})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Withdrew %s\n", out.RecordID)
	return nil
}

// ---- install ----

func cmdInstall(a *app, args []string) error {
	fs := a.flags("install")
	binary := fs.String("binary", "dumen", "command Claude Code runs for hooks (a name on PATH or an absolute path)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wd, _ := os.Getwd()
	loc, err := project.Locate(wd)
	if err != nil {
		return err
	}
	path, err := install.WriteSettings(loc.Root, *binary)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Wrote dumen hooks to %s. Commit it so every teammate's Claude Code picks them up.\n", path)
	return nil
}

// ---- hook ----

func cmdHook(a *app, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: dumen hook <session-start|user-prompt-submit|pre-tool-use|stop|session-end>")
	}
	hook.Run(args[0], a.in, a.out, hook.Env{
		ProjectDir: os.Getenv("CLAUDE_PROJECT_DIR"),
		ParentPID:  os.Getppid(),
		SpawnSync:  SpawnSync,
	})
	return nil
}
