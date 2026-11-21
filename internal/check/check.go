// Package check finds changed files that lie under an active deny directive. A hook can
// only stop Claude Code's own file tools; a diff shows every change, whatever made it.
package check

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/cache"
	"github.com/nurettincoban/dumen/internal/gitx"
	"github.com/nurettincoban/dumen/internal/policy"
)

// Violation is one changed file under a deny directive.
type Violation struct {
	File      string // path relative to the project root, with forward slashes
	Directive cache.Directive
}

// prefix returns the path of root inside its git work tree ("" at the top, else "sub/dir/").
func prefix(ctx context.Context, root string) (string, error) {
	out, err := gitx.Git{Dir: root}.Run(ctx, "rev-parse", "--show-prefix")
	return strings.TrimSpace(out), err
}

// DiffFiles lists the files head changed since it forked from base, relative to root:
// the diff from the merge base of the two to head, as a pull request shows it. Commits
// that base gained in the meantime are not the branch's changes. Both sides of a rename
// are listed.
func DiffFiles(ctx context.Context, root, base, head string) ([]string, error) {
	pre, err := prefix(ctx, root)
	if err != nil {
		return nil, err
	}
	g := gitx.Git{Dir: root}
	var shas [2]string
	for i, rev := range []string{base, head} {
		out, err := g.Run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
		if err != nil {
			return nil, fmt.Errorf("%q is not a commit in this repository", rev)
		}
		shas[i] = strings.TrimSpace(out)
	}
	mb, err := g.Run(ctx, "merge-base", shas[0], shas[1])
	if err != nil || strings.TrimSpace(mb) == "" {
		return nil, fmt.Errorf("%s and %s have no common ancestor here; in a shallow clone, fetch the full history first (git fetch --unshallow)", base, head)
	}
	out, err := g.Run(ctx, "diff", "--name-only", "--no-renames", "-z", strings.TrimSpace(mb), shas[1], "--")
	if err != nil {
		return nil, err
	}
	return under(pre, strings.Split(out, "\x00")), nil
}

// WorktreeFiles lists the files that differ from HEAD in the work tree or the index,
// and untracked files, relative to root.
func WorktreeFiles(ctx context.Context, root string) ([]string, error) {
	pre, err := prefix(ctx, root)
	if err != nil {
		return nil, err
	}
	out, err := gitx.Git{Dir: root}.Run(ctx, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range strings.Split(out, "\x00") {
		if len(entry) > 3 {
			files = append(files, entry[3:]) // "XY path"
		}
	}
	return under(pre, files), nil
}

// under keeps the paths below prefix and makes them relative to it.
func under(prefix string, files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		f = filepath.ToSlash(f)
		if f == "" || !strings.HasPrefix(f, prefix) {
			continue
		}
		if f = strings.TrimPrefix(f, prefix); f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// Violations returns the files that an active deny directive covers. repo is the
// repository's name within the project ("" for a single-repository project).
func Violations(c *cache.Cache, repo string, files []string, now time.Time) []Violation {
	var out []Violation
	for _, f := range files {
		if d, ok := c.DenyFor(policy.Qualify(repo, f), now); ok {
			out = append(out, Violation{File: f, Directive: d})
		}
	}
	return out
}

// ChangedSince reports whether the file at root/rel was changed at or after t. For a
// file that no longer exists, the directory it was in tells when it went away.
func ChangedSince(root, rel string, t time.Time) bool {
	p := filepath.Join(root, filepath.FromSlash(rel))
	st, err := os.Lstat(p)
	if err != nil {
		if st, err = os.Stat(filepath.Dir(p)); err != nil {
			return true
		}
	}
	return !st.ModTime().Before(t.Truncate(time.Second))
}
