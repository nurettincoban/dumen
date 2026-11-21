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

	"github.com/nurettincoban/dumen/internal/cache"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/render"
	"github.com/nurettincoban/dumen/internal/store"
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
	Repo  string // this repository's name within the project; "" for a single-repo project or a workspace
	Spec  string // "<remote-url>#<branch>" or "origin#<branch>"
	Store *store.Store
}

// ReadFile parses a .dumen file: "key = value" lines, "#" comments.
func ReadFile(path string) (name, spec, repo string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
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
		case "repo":
			repo = strings.ToLower(v)
		}
	}
	if name == "" {
		return "", "", "", fmt.Errorf("%s: missing project name", path)
	}
	if err := model.CheckName("project", name); err != nil {
		return "", "", "", fmt.Errorf("%s: %v", path, err)
	}
	if repo != "" {
		if err := model.CheckName("repo", repo); err != nil {
			return "", "", "", fmt.Errorf("%s: %v", path, err)
		}
	}
	return name, spec, repo, sc.Err()
}

// WriteFile writes a .dumen file.
func WriteFile(dir, name, spec string, repo ...string) error {
	body := "# dumen project file. Commit this file. It holds no secrets.\n" +
		"# store = <remote-url>#<branch>; \"origin\" means this repository's origin remote.\n" +
		"project = " + name + "\n" +
		"store = " + spec + "\n"
	if len(repo) > 0 && repo[0] != "" {
		body += "# repo names this repository when several repositories share one store.\n" +
			"# Paths in directives and decisions can then be written as " + repo[0] + ":src/...\n" +
			"repo = " + strings.ToLower(repo[0]) + "\n"
	}
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
			name, spec, repo, err := ReadFile(p)
			if err != nil {
				return nil, err
			}
			return &Project{Root: dir, Name: name, Spec: spec, Repo: repo}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNoProject
		}
		dir = parent
	}
}

// ErrNotJoinedHere means the directory names a project this machine knows, but did not
// join it: an unrelated repository whose `.dumen` carries the same project name, or a
// repository that was moved. Nothing is read from or written to the store for it.
var ErrNotJoinedHere = errors.New("this directory has not joined the project on this machine")

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
	if !s.Knows(p.Root) {
		return p, ErrNotJoinedHere
	}
	p.Store = s
	return p, nil
}

// FindBelow is for a session opened in a directory that holds several service
// repositories side by side, none of which is the directory itself. It looks one level
// down for .dumen files and returns a project rooted at dir with no repository name, so
// that the session receives the records of every repository of that project. When the
// subdirectories belong to different projects, the first one by name is used.
func FindBelow(dir string) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, ErrNoProject
	}
	const maxEntries = 500 // a hook must stay fast in a huge directory
	for i, e := range entries {
		if i >= maxEntries {
			break
		}
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		name, spec, _, err := ReadFile(filepath.Join(dir, e.Name(), File))
		if err != nil {
			continue
		}
		p := &Project{Root: dir, Name: name, Spec: spec}
		s, err := store.Open(name)
		if err != nil {
			return p, err
		}
		if !s.Knows(filepath.Join(dir, e.Name())) {
			continue
		}
		p.Store = s
		return p, nil
	}
	return nil, ErrNoProject
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
