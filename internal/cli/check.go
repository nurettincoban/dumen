package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/check"
	"github.com/nurettincoban/dumen/internal/render"
)

// violationsError is returned by `dumen check` when it found changes under a deny directive.
type violationsError struct{ msg string }

func (e *violationsError) Error() string { return e.msg }

// cmdCheck fails when a change touches a path under an active deny directive. With a
// commit range it checks what head changed since it forked from base (for CI or a
// pre-push hook); without one it checks the work tree against HEAD. It exits with 1 when
// it found such a change and with 2 when the check itself could not be made.
func cmdCheck(a *app, args []string) error {
	err := runCheck(a, args)
	var found *violationsError
	if err != nil && !errors.As(err, &found) && !errors.Is(err, flag.ErrHelp) {
		return &exitError{code: 2, err: err}
	}
	return err
}

func runCheck(a *app, args []string) error {
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
		head = strings.TrimPrefix(head, ".") // a..b and a...b mean the same here
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
		fmt.Fprintf(a.out, "OK: %s in %s, none under a deny directive.\n", countFiles(len(files)), what)
		return nil
	}
	for _, v := range vs {
		fmt.Fprintf(a.out, "%s  %s  %s (until %s)\n", v.File, v.Directive.ID,
			render.Label(v.Directive.Text, 70), render.Until(v.Directive.Expires))
	}
	verb := "are"
	if len(vs) == 1 {
		verb = "is"
	}
	return &violationsError{fmt.Sprintf("%d of %s in %s %s under a deny directive", len(vs), countFiles(len(files)), what, verb)}
}

func countFiles(n int) string {
	if n == 1 {
		return "1 changed file"
	}
	return fmt.Sprintf("%d changed files", n)
}
