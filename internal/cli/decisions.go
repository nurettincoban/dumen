package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/policy"
	"github.com/nurettincoban/dumen/internal/project"
	"github.com/nurettincoban/dumen/internal/store"
)

func init() {
	commands = append(commands,
		command{"decide", `decide --title T --rule R --kind K [--services S1,S2] [--topics T1,T2] [--owner EMAIL] [-m BODY | --file F] [--paths P1,P2] [--sources R-1,R-2] [--approve]`, "record a decision (proposed unless --approve)", cmdDecide},
		command{"decisions", "decisions [path] [--service S] [--topic T] [--kind K] [--proposed] [--all]", "list approved decisions, optionally filtered", cmdDecisions},
		command{"questions", "questions [--all]", "list proposed decisions you may approve, such as answers agents saved", cmdQuestions},
		command{"show", "show <id>", "show a decision, directive or ref in full", cmdShow},
		command{"approve", "approve <id>", "approve a proposed decision", func(a *app, args []string) error { return resolve(a, args, model.StatusApproved) }},
		command{"reject", "reject <id>", "reject a proposed decision", func(a *app, args []string) error { return resolve(a, args, model.StatusRejected) }},
		command{"supersede", `supersede <id> --title T --rule R [-m BODY | --file F]`, "replace an approved decision with a new one", cmdSupersede},
		command{"ref", `ref add <url> --kind design|artifact|mr|doc --title T [--governs G] [--area A] [--paths P] | ref check <id> | ref list`, "manage links to sources of truth", cmdRef},
	)
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// Anchor returns the sha of the code repository's main line, recorded on new decisions.
func Anchor(root string) string {
	for _, ref := range []string{"origin/HEAD", "origin/main", "origin/master", "HEAD"} {
		if sha := gitOut(root, "rev-parse", "--verify", "-q", ref); sha != "" {
			return sha
		}
	}
	return ""
}

type decisionFlags struct {
	title, rule, kind, services, topics, owner, body, file, area, paths, sources *string
	needs                                                                        pairs
}

func addDecisionFlags(fs *flag.FlagSet) decisionFlags {
	needs := pairs{}
	fs.Var(needs, "needs", "SERVICE=WORK: work this decision needs from another service, which hears about it; repeatable")
	return decisionFlags{
		needs:    needs,
		title:    fs.String("title", "", "one-line title"),
		rule:     fs.String("rule", "", "the rule agents see first, one sentence (at most 300 characters)"),
		kind:     fs.String("kind", "", "product, ux, ui, architecture, technical, or a kind from dumen.yml"),
		services: fs.String("services", "", "comma-separated services it applies to (default: the whole product)"),
		topics:   fs.String("topics", "", "comma-separated topics, e.g. payments,capacity"),
		owner:    fs.String("owner", "", "git email of the person to ask about it"),
		body:     fs.String("m", "", "body: why, what is allowed and not allowed, rejected alternatives"),
		file:     fs.String("file", "", "read the body from a file (\"-\" for stdin)"),
		area:     fs.String("area", "", "area from dumen.yml"),
		paths:    fs.String("paths", "", "comma-separated paths the decision governs"),
		sources:  fs.String("sources", "", "comma-separated ref ids"),
	}
}

func (f decisionFlags) build(a *app, p *project.Project) (model.Decision, string, error) {
	root := p.Root
	body := *f.body
	if *f.file == "-" {
		data, err := io.ReadAll(a.in)
		if err != nil {
			return model.Decision{}, "", err
		}
		body = string(data)
	} else if *f.file != "" {
		data, err := os.ReadFile(*f.file)
		if err != nil {
			return model.Decision{}, "", err
		}
		body = string(data)
	}
	if strings.TrimSpace(*f.title) == "" {
		return model.Decision{}, "", fmt.Errorf("--title is required")
	}
	if strings.TrimSpace(*f.rule) == "" {
		return model.Decision{}, "", fmt.Errorf("--rule is required: the one sentence agents see first")
	}
	if strings.TrimSpace(body) == "" {
		return model.Decision{}, "", fmt.Errorf("a body is required (-m or --file): why, what is allowed and not allowed, and rejected alternatives")
	}
	d := model.Decision{ID: model.NewID(model.PrefixDecision), Title: strings.TrimSpace(*f.title),
		Rule: strings.Join(strings.Fields(*f.rule), " "), Kind: strings.ToLower(strings.TrimSpace(*f.kind)),
		AppliesTo: model.AppliesTo{Services: splitList(strings.ToLower(*f.services)), Topics: splitList(strings.ToLower(*f.topics))},
		Owner:     strings.ToLower(strings.TrimSpace(*f.owner)), Area: *f.area,
		Sources: splitList(strings.ToUpper(*f.sources)), AnchoredAt: Anchor(root)}
	for svc, ask := range f.needs {
		if ask = strings.Join(strings.Fields(ask), " "); ask != "" {
			if d.Needs == nil {
				d.Needs = map[string]string{}
			}
			d.Needs[svc] = ask
		}
	}
	for _, x := range splitList(*f.paths) {
		d.Paths = append(d.Paths, policy.Qualify(p.Repo, x)) // paths typed inside a repository belong to it
	}
	if d.AnchoredAt != "" && p.Repo != "" {
		d.AnchoredAt = p.Repo + "@" + d.AnchoredAt
	}
	return d, body, nil
}

func cmdDecide(a *app, args []string) error {
	fs := a.flags("decide")
	df := addDecisionFlags(fs)
	approve := fs.Bool("approve", false, "record it as approved (needs approval rights)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	d, body, err := df.build(a, p)
	if err != nil {
		return err
	}
	if d.Kind == "" {
		return fmt.Errorf("--kind is required: product, ux, ui, architecture, technical, or a kind from dumen.yml")
	}
	d.Repo = p.Repo
	out, err := a.submit(p, &op.CreateDecision{Decision: d, Body: body, Approve: *approve})
	if err != nil {
		return err
	}
	state := "proposed"
	if *approve {
		state = "approved"
	}
	id := out.RecordID
	if id == "" {
		id = "(id not reported; `dumen decisions` lists it)"
	}
	fmt.Fprintf(a.out, "Recorded %s (%s): %s\n", id, state, d.Title)
	return nil
}

func cmdSupersede(a *app, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: dumen supersede <id> --title T [-m BODY | --file F]")
	}
	old := strings.ToUpper(args[0])
	fs := a.flags("supersede")
	df := addDecisionFlags(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	d, body, err := df.build(a, p)
	if err != nil {
		return err
	}
	d.Supersedes = []string{old}
	d.Repo = p.Repo
	out, err := a.submit(p, &op.CreateDecision{Decision: d, Body: body, Approve: true})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Recorded %s (approved), superseding %s\n", out.RecordID, old)
	return nil
}

func resolve(a *app, args []string, status string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: dumen approve|reject <id>")
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	out, err := a.submit(p, &op.ResolveDecision{ID: strings.ToUpper(args[0]), Status: status})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s is now %s\n", out.RecordID, status)
	return nil
}

// locked refreshes the store (best effort) and runs f with the checkout locked.
func locked(a *app, p *project.Project, f func(root string) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := p.Refresh(ctx); err != nil {
		fmt.Fprintln(a.err, "warning: remote unreachable, showing the last synced state")
	}
	unlock, err := p.Store.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return f(p.Store.Root())
}

func cmdDecisions(a *app, args []string) error {
	fs := a.flags("decisions")
	drafts := fs.Bool("proposed", false, "list proposed decisions instead of approved ones")
	fs.BoolVar(drafts, "drafts", false, "same as --proposed")
	all := fs.Bool("all", false, "include proposed, rejected and superseded decisions")
	service := fs.String("service", "", "only decisions that apply to this service")
	topic := fs.String("topic", "", "only decisions on this topic")
	kind := fs.String("kind", "", "only decisions of this kind")
	// Allow the path before or after the flags.
	var path string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		path, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if path == "" && fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	return locked(a, p, func(root string) error {
		decs, err := store.ListDecisions(root)
		if err != nil {
			return err
		}
		cfg, err := store.ReadConfig(root)
		if err != nil {
			return err
		}
		derived := policy.Derive(decs)
		rel := ""
		if path != "" {
			rel = policy.Qualify(p.Repo, relPath(p.Root, path))
		}
		n := 0
		for _, d := range decs {
			state := d.Status
			if d.Status == model.StatusApproved {
				switch by := derived.SupersededBy[d.ID]; {
				case len(by) == 1:
					state = "superseded"
				case len(by) > 1:
					state = "contested"
				}
			}
			switch {
			case *all:
			case *drafts:
				if d.Status != model.StatusProposed {
					continue
				}
			case state != model.StatusApproved && state != "contested":
				continue
			}
			if rel != "" && !policy.MatchAny(d.Paths, rel) && !(d.Area != "" && policy.InArea(cfg, d.Area, rel)) {
				continue
			}
			if (*service != "" && !d.ForService(*service)) || (*kind != "" && d.Kind != *kind) ||
				(*topic != "" && !contains(d.Topics(), *topic)) {
				continue
			}
			n++
			fmt.Fprintf(a.out, "%s  %-10s %-12s %s\n", d.ID, state, clip(orDash(d.Kind), 12), clip(d.Headline(), 110))
		}
		if n == 0 {
			fmt.Fprintln(a.out, "No matching decisions.")
		}
		return nil
	})
}

// relPath turns a user-supplied path into a repo-relative one.
func relPath(root, path string) string {
	if r, _ := policy.SplitRepo(path); r != "" {
		return path // already written as <repo>:<path>
	}
	if !strings.HasPrefix(path, "/") && !strings.Contains(path, ":") {
		wd, _ := os.Getwd()
		if r, err := relTo(root, wd, path); err == nil {
			return r
		}
	}
	return policy.CleanRel(path)
}

func cmdShow(a *app, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: dumen show <id>")
	}
	id := strings.ToUpper(args[0])
	p, err := openProject()
	if err != nil {
		return err
	}
	return locked(a, p, func(root string) error {
		switch {
		case model.ValidID(id, model.PrefixDecision):
			d, rel, err := store.ReadDecision(root, id)
			if err != nil {
				return err
			}
			decs, _ := store.ListDecisions(root)
			derived := policy.Derive(decs)
			fmt.Fprintf(a.out, "%s  %s\nstatus      %s\n", d.ID, d.Title, d.Status)
			if by := derived.SupersededBy[d.ID]; len(by) == 1 {
				fmt.Fprintf(a.out, "superseded  by %s\n", by[0])
			} else if len(by) > 1 {
				fmt.Fprintf(a.out, "contested   superseded by %s\n", strings.Join(by, " and "))
			}
			fmt.Fprintf(a.out, "rule        %s\nkind        %s\nservices    %s\ntopics      %s\nowner       %s\n",
				orDash(d.Rule), orDash(d.Kind), services(d), orDash(strings.Join(d.Topics(), ", ")), orDash(d.Owner))
			if o := d.Origin; o != nil {
				fmt.Fprintf(a.out, "asked       %s\npicked      %s (by %s, verified %v)\n", o.Question, o.Answer, o.AnsweredBy, o.Verified)
			}
			fmt.Fprintf(a.out, "area        %s\npaths       %s\nsources     %s\nauthor      %s (%s)\napproved by %s\nsupersedes  %s\nanchored at %s\ncreated     %s\nfile        %s\n\n%s\n",
				orDash(d.Area), orDash(strings.Join(d.Paths, ", ")), orDash(strings.Join(d.Sources, ", ")), d.Author, d.AuthorKind,
				orDash(d.ApprovedBy), orDash(strings.Join(d.Supersedes, ", ")), orDash(shortAnchor(d.AnchoredAt)), d.Created, rel, d.Body)
		case model.ValidID(id, model.PrefixDirective):
			d, _, err := store.ReadDirective(root, id)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s  [%s] %s\nstatus       %s\nscope        %s\nstarts       %s\nexpires      %s\npublished by %s\nproposed by  %s\n\n%s\n",
				d.ID, d.Kind, clip(strings.ReplaceAll(d.Text, "\n", " "), 80), d.Status, d.Scope, d.Starts, d.Expires,
				orDash(d.PublishedBy), orDash(d.ProposedBy), d.Text)
		case model.ValidID(id, model.PrefixRef):
			r, err := store.ReadRef(root, id)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s  [%s] %s\nurl        %s\ngoverns    %s\nowner      %s\narea       %s\npaths      %s\nchecked at %s\n",
				r.ID, r.Kind, r.Title, r.URL, orDash(r.Governs), orDash(r.Owner), orDash(r.Area), orDash(strings.Join(r.Paths, ", ")), r.CheckedAt)
		default:
			return fmt.Errorf("%q is not a record id (D-XXXX, Y-XXXX or R-XXXX)", args[0])
		}
		return nil
	})
}

func cmdRef(a *app, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dumen ref add <url> ... | ref check <id> | ref list")
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("usage: dumen ref add <url> --kind design|artifact|mr|doc --title T [--governs G]")
		}
		fs := a.flags("ref add")
		kind := fs.String("kind", "doc", "design, artifact, mr or doc")
		title := fs.String("title", "", "one-line label")
		governs := fs.String("governs", "", "one line: what this link is the source of truth for")
		area := fs.String("area", "", "area from dumen.yml")
		paths := fs.String("paths", "", "comma-separated paths it governs")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		r := model.Ref{ID: model.NewID(model.PrefixRef), URL: args[1], Kind: *kind, Title: *title, Governs: *governs, Area: *area, Paths: splitList(*paths)}
		out, err := a.submit(p, &op.AddRef{Ref: r})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Recorded %s [%s] %s\n", out.RecordID, r.Kind, r.Title)
		return nil
	case "check":
		if len(args) != 2 {
			return fmt.Errorf("usage: dumen ref check <id>")
		}
		out, err := a.submit(p, &op.CheckRef{ID: strings.ToUpper(args[1])})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s marked as checked today\n", out.RecordID)
		return nil
	case "list":
		return locked(a, p, func(root string) error {
			refs, err := store.ListRefs(root)
			if err != nil {
				return err
			}
			cfg, err := store.ReadConfig(root)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			for _, r := range refs {
				flag := ""
				if r.Status == model.RefUnverified {
					flag = "  UNVERIFIED: added by an agent, not shown to agents until `dumen ref check " + r.ID + "`"
				} else if t, err := model.ParseTime(r.CheckedAt); err == nil && now.Sub(t) > time.Duration(cfg.Settings.RefStaleDays)*24*time.Hour {
					flag = fmt.Sprintf("  UNCHECKED for %d days", int(now.Sub(t).Hours()/24))
				}
				fmt.Fprintf(a.out, "%s  %-8s %s  %s%s\n", r.ID, r.Kind, clip(r.Title, 50), r.URL, flag)
			}
			if len(refs) == 0 {
				fmt.Fprintln(a.out, "No refs recorded.")
			}
			return nil
		})
	}
	return fmt.Errorf("usage: dumen ref add <url> ... | ref check <id> | ref list")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// services renders the services a decision applies to.
func services(d model.Decision) string {
	if d.ProductWide() {
		return "all (product-wide)"
	}
	return strings.Join(d.AppliesTo.Services, ", ")
}

func cmdQuestions(a *app, args []string) error {
	fs := a.flags("questions")
	all := fs.Bool("all", false, "list every proposed decision, not only those you may approve")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	return locked(a, p, func(root string) error {
		decs, err := store.ListDecisions(root)
		if err != nil {
			return err
		}
		cfg, err := store.ReadConfig(root)
		if err != nil {
			return err
		}
		n := 0
		for _, d := range decs {
			if d.Status != model.StatusProposed || (!*all && !cfg.CanApprove(p.Store.Email, d.Kind, d.Area)) {
				continue
			}
			n++
			fmt.Fprintf(a.out, "%s  %-12s %s\n", d.ID, clip(orDash(d.Kind), 12), clip(d.Headline(), 110))
			if o := d.Origin; o != nil {
				how := "reported by the agent"
				if o.Verified {
					how = "seen in the question tool"
				}
				fmt.Fprintf(a.out, "        asked:  %s\n        picked: %s (by %s, %s)\n", clip(o.Question, 100), clip(o.Answer, 80), o.AnsweredBy, how)
			}
		}
		switch {
		case n == 0 && *all:
			fmt.Fprintln(a.out, "No proposed decisions.")
		case n == 0:
			fmt.Fprintln(a.out, "Nothing waits for you. `dumen questions --all` lists every proposed decision.")
		default:
			fmt.Fprintln(a.out, "Approve with `dumen approve <id>`, or reject with `dumen reject <id>`; `dumen show <id>` has the full text.")
		}
		return nil
	})
}
