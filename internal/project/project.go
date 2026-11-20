// Package project ties a working directory to its dumen store.
package project

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nurettincoban/comm-pro/internal/cache"
	"github.com/nurettincoban/comm-pro/internal/op"
	"github.com/nurettincoban/comm-pro/internal/render"
	"github.com/nurettincoban/comm-pro/internal/store"
)

// File is the project file committed at the root of the main repository (and written
// into PM/designer workspaces). It holds no secrets.
const File = ".dumen"

// ErrNoProject means no .dumen file was found in dir or its parents.
var ErrNoProject = errors.New("not a dumen project (no .dumen file found; run `dumen init`)")

// Project is a located project.
type Project struct {
	Root  string // directory containing the .dumen file
	Name  string
	Spec  string // "<remote-url>#<branch>" or "origin#<branch>"
	Store *store.Store
}

// ReadFile parses a .dumen file: "key = value" lines, "#" comments.
func ReadFile(path string) (name, spec string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "project":
			name = v
		case "store":
			spec = v
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("%s: missing project name", path)
	}
	return name, spec, sc.Err()
}

// WriteFile writes a .dumen file.
func WriteFile(dir, name, spec string) error {
	body := "# dumen project file. Commit this file. It holds no secrets.\n" +
		"# store = <remote-url>#<branch>; \"origin\" means this repository's origin remote.\n" +
		"project = " + name + "\n" +
		"store = " + spec + "\n"
	return os.WriteFile(filepath.Join(dir, File), []byte(body), 0o644)
}

// Locate walks up from dir to find the .dumen file. It does not open the store.
func Locate(dir string) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for {
		p := filepath.Join(dir, File)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			name, spec, err := ReadFile(p)
			if err != nil {
				return nil, err
			}
			return &Project{Root: dir, Name: name, Spec: spec}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNoProject
		}
		dir = parent
	}
}

// Find locates the project and opens its local store.
func Find(dir string) (*Project, error) {
	p, err := Locate(dir)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(p.Name)
	if err != nil {
		return p, err
	}
	p.Store = s
	return p, nil
}

// Engine returns the write engine for this project, wired to keep the cache fresh.
func (p *Project) Engine() *op.Engine {
	return &op.Engine{
		Store:      p.Store,
		Regenerate: render.Regenerate,
		AfterSync: func(root, head string) {
			if c, err := cache.Build(root, head, time.Now().UTC()); err == nil {
				c.Write(p.Store.CachePath())
			}
		},
	}
}

// Refresh fetches the store and rebuilds the cache. The caller bounds it with ctx.
func (p *Project) Refresh(ctx context.Context) (*cache.Cache, error) {
	unlock, err := p.Store.Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	head, err := p.Store.Sync(ctx)
	if err != nil {
		return nil, err
	}
	c, err := cache.Build(p.Store.Root(), head, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return c, c.Write(p.Store.CachePath())
}

// Poll is the cheap periodic check: one ls-remote, and a fetch only if the sha changed.
// It reports whether the cache content changed.
func (p *Project) Poll(ctx context.Context) (changed bool, err error) {
	sha, err := p.Store.RemoteHead(ctx)
	if err != nil {
		return false, err
	}
	c, cerr := cache.Read(p.Store.CachePath())
	if cerr == nil && c.Head == sha {
		c.SyncedAt = time.Now().UTC().Format("2006-01-02T15:04:05Z")
		return false, c.Write(p.Store.CachePath())
	}
	_, err = p.Refresh(ctx)
	return err == nil, err
}

// PrivateMarker is the file whose presence marks new sessions on this machine as not monitored.
func (p *Project) PrivateMarker() string { return filepath.Join(p.Store.Home, "private") }
