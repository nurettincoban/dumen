package cli

import (
	"os"
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
