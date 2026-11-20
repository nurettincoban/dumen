package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nurettincoban/comm-pro/internal/cache"
	"github.com/nurettincoban/comm-pro/internal/project"
)

func readCache(p *project.Project) (*cache.Cache, error) { return cache.Read(p.Store.CachePath()) }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func countJSON(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// relTo resolves path (relative to wd) into a path relative to root, with forward slashes.
func relTo(root, wd, path string) (string, error) {
	abs := filepath.Join(wd, path)
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("outside the repository")
	}
	return filepath.ToSlash(rel), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clip(strings.TrimSpace(s), 200)
}

// lastLine returns the last non-empty line: git puts the real reason at the end.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
