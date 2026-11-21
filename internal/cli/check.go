package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/check"
	"github.com/nurettincoban/dumen/internal/render"
)

// cmdCheck fails when a change touches a path under an active deny directive. With a
// commit range it checks that diff (for CI or a pre-push hook); without one it checks the
// work tree against HEAD.
func cmdCheck(a *app, args []string) error {
	fs := a.flags("check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var base, head string
	switch fs.NArg() {
	case 0:
	case 1:
		var ok bool
		if base, head, ok = strings.Cut(fs.Arg(0), ".."); !ok || base == "" {
			return fmt.Errorf("usage: dumen check [<base>..<head>]")
		}
		head = strings.TrimPrefix(head, ".") // a...b is accepted and read as a..b
		if head == "" {
			head = "HEAD"
		}
	case 2:
		base, head = fs.Arg(0), fs.Arg(1)
	default:
		return fmt.Errorf("usage: dumen check [<base>..<head>]")
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := p.Refresh(ctx)
	if err != nil {
		if c, err = readCache(p); err != nil {
			return fmt.Errorf("no local cache and the remote is unreachable")
		}
		fmt.Fprintln(a.err, "warning: remote unreachable, checking against the last synced directives")
	}
	var files []string
	what := "the work tree"
	if base != "" {
		what = base + ".." + head
		files, err = check.DiffFiles(ctx, p.Root, base, head)
	} else {
		files, err = check.WorktreeFiles(ctx, p.Root)
	}
	if err != nil {
		return err
	}
	vs := check.Violations(c, p.Repo, files, time.Now().UTC())
	if len(vs) == 0 {
		fmt.Fprintf(a.out, "OK: %d changed files in %s, none under a deny directive.\n", len(files), what)
		return nil
	}
	for _, v := range vs {
		fmt.Fprintf(a.out, "%s  %s  %s (until %s)\n", v.File, v.Directive.ID,
			render.Label(v.Directive.Text, 70), render.Until(v.Directive.Expires))
	}
	return fmt.Errorf("%d of %d changed files in %s are under a deny directive", len(vs), len(files), what)
}
