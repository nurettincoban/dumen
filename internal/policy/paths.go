package policy

import (
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nurettincoban/comm-pro/internal/model"
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

// MatchPath reports whether the repo-relative file matches pattern.
// A pattern without wildcards is a path prefix ("src/auth" matches everything under it).
// Otherwise "*" matches within one path segment and "**" matches across segments.
func MatchPath(pattern, file string) bool {
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
	b.WriteString("^")
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

// Delivered reports whether an active directive should be shown to a session of the given
// role. Role-scoped directives go to that role only; path and area directives go to
// everyone, because any session may end up touching those paths.
func Delivered(d model.Directive, role string, now time.Time) bool {
	if !d.ActiveAt(now) {
		return false
	}
	if d.Scope.Role != "" {
		return d.Scope.Role == role
	}
	return true
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
