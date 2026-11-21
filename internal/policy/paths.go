package policy

import (
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
)

var (
	globMu    sync.Mutex
	globCache = map[string]*regexp.Regexp{}
)

// CleanRel normalises a repo-relative path: forward slashes, no leading "./" or "/".
func CleanRel(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean("/" + p)
	return strings.TrimPrefix(p, "/")
}

// CaseInsensitive makes path matching ignore case. Hooks turn it on for file systems that
// are case-insensitive by default (Windows, macOS), so "SRC/Auth" cannot slip past "src/auth".
var CaseInsensitive bool

var repoPrefix = regexp.MustCompile(`^(\*|[A-Za-z0-9][A-Za-z0-9._-]*):`)

// SplitRepo splits "billing-svc:src/auth/**" into its repository and path parts. A value
// without a repository prefix returns repo "". The prefix "*" means any repository.
func SplitRepo(s string) (repo, rest string) {
	if m := repoPrefix.FindString(s); m != "" {
		return strings.ToLower(m[:len(m)-1]), s[len(m):]
	}
	return "", s
}

// Qualify prefixes a repo-relative path with its repository name ("" leaves it as is).
func Qualify(repo, rel string) string {
	if repo == "" {
		return rel
	}
	if r, _ := SplitRepo(rel); r != "" {
		return rel
	}
	return strings.ToLower(repo) + ":" + rel
}

// PatternRepo returns the repository a pattern is limited to ("" when it covers every repository).
func PatternRepo(pattern string) string {
	repo, _ := SplitRepo(pattern)
	if repo == "*" {
		return ""
	}
	return repo
}

// MatchPath reports whether file matches pattern. Both may carry a repository prefix
// ("billing-svc:src/auth/**"). A pattern without one covers every repository; a pattern
// with one only matches files of that repository.
//
// In the path part, a pattern without wildcards is a prefix ("src/auth" matches
// everything under it); otherwise "*" matches within one segment and "**" across segments.
func MatchPath(pattern, file string) bool {
	prepo, pattern := SplitRepo(pattern)
	frepo, file := SplitRepo(file)
	if prepo != "" && prepo != "*" && prepo != frepo {
		return false
	}
	pattern = strings.TrimSuffix(CleanRelKeepGlob(pattern), "/")
	file = CleanRel(file)
	if CaseInsensitive {
		pattern, file = strings.ToLower(pattern), strings.ToLower(file)
	}
	if pattern == "" || pattern == "**" {
		return true
	}
	if !strings.ContainsAny(pattern, "*?") {
		return file == pattern || strings.HasPrefix(file, pattern+"/")
	}
	globMu.Lock()
	re, ok := globCache[pattern]
	if !ok {
		re = compileGlob(pattern)
		globCache[pattern] = re
	}
	globMu.Unlock()
	return re.MatchString(file)
}

// CleanRelKeepGlob normalises separators without collapsing glob characters.
func CleanRelKeepGlob(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	return strings.TrimPrefix(p, "/")
}

func compileGlob(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?s)^") // (?s): a file name may contain a newline, and "." must match it
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?") // "**/" also matches zero directories
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	// A pattern that names a directory also covers what is inside it.
	b.WriteString("(?:/.*)?$")
	return regexp.MustCompile(b.String())
}

// MatchAny reports whether file matches any of the patterns.
func MatchAny(patterns []string, file string) bool {
	for _, p := range patterns {
		if MatchPath(p, file) {
			return true
		}
	}
	return false
}

// InArea reports whether file belongs to the named area.
func InArea(cfg model.Config, area, file string) bool {
	a, ok := cfg.Areas[area]
	return ok && MatchAny(a.Paths, file)
}

// touchesRepo reports whether any of the patterns can match a file in repo. A session
// without a repository (a PM workspace) is concerned with every pattern.
func touchesRepo(patterns []string, repo string) bool {
	if repo == "" || len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if r := PatternRepo(p); r == "" || r == strings.ToLower(repo) {
			return true
		}
	}
	return false
}

// Delivered reports whether an active directive should be shown to a session of the given
// role working in the given repository. Role-scoped directives go to that role only.
// Path and area directives go to every session whose repository they can touch, because
// any such session may end up editing those paths.
func Delivered(cfg model.Config, d model.Directive, role, repo string, now time.Time) bool {
	if !d.ActiveAt(now) {
		return false
	}
	switch {
	case d.Scope.Role != "":
		return d.Scope.Role == role
	case len(d.Scope.Paths) > 0:
		return touchesRepo(d.Scope.Paths, repo)
	case d.Scope.Area != "":
		return touchesRepo(cfg.Areas[d.Scope.Area].Paths, repo)
	}
	return true
}

// RelevantTo reports whether a record with these paths and area concerns the repository.
// Records with no paths and no area concern everyone.
func RelevantTo(cfg model.Config, paths []string, area, repo string) bool {
	if repo == "" || (len(paths) == 0 && area == "") {
		return true
	}
	if len(paths) > 0 && touchesRepo(paths, repo) {
		return true
	}
	if a, ok := cfg.Areas[area]; ok && touchesRepo(a.Paths, repo) {
		return true
	}
	return len(paths) == 0 && area != "" && len(cfg.Areas[area].Paths) == 0
}

// CoversPath reports whether a directive's scope covers the repo-relative file.
func CoversPath(cfg model.Config, d model.Directive, file string) bool {
	switch {
	case len(d.Scope.Paths) > 0:
		return MatchAny(d.Scope.Paths, file)
	case d.Scope.Area != "":
		return InArea(cfg, d.Scope.Area, file)
	}
	return false
}

// DenyFor returns the active deny directive that blocks edits to file, if any.
func DenyFor(cfg model.Config, directives []model.Directive, file string, now time.Time) (model.Directive, bool) {
	for _, d := range directives {
		if d.Kind == model.DirectiveDeny && d.ActiveAt(now) && CoversPath(cfg, d, file) {
			return d, true
		}
	}
	return model.Directive{}, false
}
