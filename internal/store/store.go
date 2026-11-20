package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nurettincoban/comm-pro/internal/gitx"
	"github.com/nurettincoban/comm-pro/internal/model"
)

// DefaultBranch is the branch dumen records live on.
const DefaultBranch = "dumen"

// BaseDir returns the directory holding all local dumen state: $DUMEN_HOME or ~/.dumen.
// It is never inside a working tree.
func BaseDir() (string, error) {
	if d := os.Getenv("DUMEN_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dumen"), nil
}

// ParseSpec splits "<remote-url>#<branch>". The branch defaults to "dumen".
func ParseSpec(spec string) (remote, branch string) {
	if i := strings.LastIndex(spec, "#"); i > 0 {
		return spec[:i], spec[i+1:]
	}
	return spec, DefaultBranch
}

// ProjectName derives a local project name from a remote URL.
func ProjectName(remote string) string {
	s := strings.TrimRight(strings.ReplaceAll(remote, "\\", "/"), "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	s = model.Slug(s)
	if s == "" {
		s = "project"
	}
	return s
}

// Local is the per-machine description of a store (~/.dumen/<project>/local.json).
type Local struct {
	Project string `json:"project"`
	Remote  string `json:"remote"`
	Branch  string `json:"branch"`
}

// Store is a local single-branch clone of the dumen branch plus its side files.
type Store struct {
	Home string // ~/.dumen/<project>
	Local
}

// Root is the checkout of the dumen branch.
func (s *Store) Root() string { return filepath.Join(s.Home, "store") }

// OutboxDir holds operations waiting to be pushed.
func (s *Store) OutboxDir() string { return filepath.Join(s.Home, "outbox") }

// FailedDir holds operations that were rejected when applied.
func (s *Store) FailedDir() string { return filepath.Join(s.Home, "outbox-failed") }

// CachePath is the JSON cache hooks read.
func (s *Store) CachePath() string { return filepath.Join(s.Home, "cache.json") }

// SessionsDir holds per-session local state.
func (s *Store) SessionsDir() string { return filepath.Join(s.Home, "sessions") }

func (s *Store) git() gitx.Git { return gitx.Git{Dir: s.Root()} }

// Open loads an existing local store by project name.
func Open(project string) (*Store, error) {
	base, err := BaseDir()
	if err != nil {
		return nil, err
	}
	home := filepath.Join(base, project)
	data, err := os.ReadFile(filepath.Join(home, "local.json"))
	if err != nil {
		return nil, fmt.Errorf("no dumen store for project %q (run `dumen init` or `dumen join`): %w", project, err)
	}
	s := &Store{Home: home}
	if err := json.Unmarshal(data, &s.Local); err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.Join(home, "local.json"), err)
	}
	return s, nil
}

// SetupResult describes what Setup did.
type SetupResult struct {
	Created  bool     // the dumen branch was created on the remote
	Warnings []string // things the user should look at
}

// Setup creates the local clone for remote#branch. If the branch does not exist on the
// remote and create is true, it is created as an orphan branch holding dumen.yml.
func Setup(ctx context.Context, remote, branch, project string, create bool, founder model.Actor) (*Store, SetupResult, error) {
	var res SetupResult
	base, err := BaseDir()
	if err != nil {
		return nil, res, err
	}
	if project == "" {
		project = ProjectName(remote)
	}
	s := &Store{Home: filepath.Join(base, project), Local: Local{Project: project, Remote: remote, Branch: branch}}
	if prev, err := Open(project); err == nil && (prev.Remote != remote || prev.Branch != branch) {
		return nil, res, fmt.Errorf("project %q already points at %s#%s; pass a different project name", project, prev.Remote, prev.Branch)
	}
	sha, err := gitx.LsRemote(ctx, remote, branch)
	if err != nil {
		return nil, res, fmt.Errorf("cannot reach %s: %w", remote, err)
	}
	if sha == "" && !create {
		return nil, res, fmt.Errorf("branch %q does not exist on %s; run `dumen init` first", branch, remote)
	}
	if err := os.RemoveAll(s.Root()); err != nil {
		return nil, res, err
	}
	for _, d := range []string{s.Root(), s.OutboxDir(), s.FailedDir(), s.SessionsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, res, err
		}
	}
	g := s.git()
	steps := [][]string{
		{"init", "-q"},
		{"symbolic-ref", "HEAD", "refs/heads/" + branch},
		{"remote", "add", "origin", remote},
		{"config", "core.autocrlf", "false"},
		{"config", "commit.gpgsign", "false"},
		{"config", "gc.auto", "0"},
	}
	for _, st := range steps {
		if _, err := g.Run(ctx, st...); err != nil {
			return nil, res, err
		}
	}
	if sha == "" {
		if err := WriteConfig(s.Root(), founderConfig(project, founder)); err != nil {
			return nil, res, err
		}
		if _, err := s.Commit(ctx, "dumen: init store", founder.Email); err != nil {
			return nil, res, err
		}
		pr, perr := g.Push(ctx, branch)
		switch pr {
		case gitx.PushOK:
			res.Created = true
		case gitx.PushRejected:
			// Someone created the branch at the same moment: join theirs.
		default:
			return nil, res, fmt.Errorf("cannot create branch %q on the remote: %w", branch, perr)
		}
	}
	if _, err := s.Sync(ctx); err != nil {
		return nil, res, err
	}
	if _, err := os.Stat(filepath.Join(s.Root(), ".gitlab-ci.yml")); err == nil {
		return nil, res, fmt.Errorf("branch %q contains .gitlab-ci.yml; dumen will not use a branch that can trigger pipelines", branch)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), ConfigFile)); err != nil {
		return nil, res, fmt.Errorf("branch %q exists but has no %s; it does not look like a dumen store", branch, ConfigFile)
	}
	if strings.Contains(strings.ToLower(remote), "gitlab") {
		res.Warnings = append(res.Warnings,
			"If this GitLab project sets a custom or external CI/CD configuration path (Settings > CI/CD > General pipelines), "+
				"pushes to the dumen branch can start pipelines. Exclude the branch there, and protect it (no force-push, no delete).")
	}
	data, _ := json.MarshalIndent(s.Local, "", "  ")
	if err := os.WriteFile(filepath.Join(s.Home, "local.json"), append(data, '\n'), 0o644); err != nil {
		return nil, res, err
	}
	return s, res, nil
}

func founderConfig(project string, founder model.Actor) model.Config {
	c := model.DefaultConfig(project)
	if founder.Email != "" {
		c.Roles[strings.ToLower(founder.Email)] = "lead"
	}
	return c
}

// Sync fetches the branch and hard-resets the checkout to it. Local commits that were not
// pushed are discarded on purpose: operations are re-applied, never merged.
func (s *Store) Sync(ctx context.Context) (string, error) {
	g := s.git()
	ref := "refs/remotes/origin/" + s.Branch
	if _, err := g.Run(ctx, "fetch", "-q", "--no-tags", "origin", "+refs/heads/"+s.Branch+":"+ref); err != nil {
		return "", err
	}
	return s.ResetToFetched(ctx)
}

// ResetToFetched resets the checkout to the last fetched remote head without network access.
func (s *Store) ResetToFetched(ctx context.Context) (string, error) {
	g := s.git()
	ref := "refs/remotes/origin/" + s.Branch
	if _, err := g.Run(ctx, "reset", "-q", "--hard", ref); err != nil {
		return "", err
	}
	if _, err := g.Run(ctx, "clean", "-fdq"); err != nil {
		return "", err
	}
	return s.Head(ctx)
}

// Head returns the sha of the checkout's HEAD.
func (s *Store) Head(ctx context.Context) (string, error) {
	out, err := s.git().Run(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// RemoteHead asks the remote for the branch sha (one cheap round trip, no fetch).
func (s *Store) RemoteHead(ctx context.Context) (string, error) {
	return gitx.LsRemote(ctx, s.Remote, s.Branch)
}

// Commit stages everything and commits. It returns false when there was nothing to commit.
func (s *Store) Commit(ctx context.Context, message, email string) (bool, error) {
	g := s.git()
	if _, err := g.Run(ctx, "add", "-A"); err != nil {
		return false, err
	}
	out, err := g.Run(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) == "" {
		return false, nil
	}
	if email == "" {
		email = "unknown@dumen.invalid"
	}
	name := email
	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i]
	}
	_, err = g.Run(ctx, "-c", "user.name="+name, "-c", "user.email="+email, "commit", "-q", "--no-verify", "-m", message)
	return err == nil, err
}

// Push pushes HEAD to the remote branch, fast-forward only.
func (s *Store) Push(ctx context.Context) (gitx.PushResult, error) {
	return s.git().Push(ctx, s.Branch)
}

// AppliedOps returns which of the given operation IDs already appear in the history of the
// checkout's HEAD (as "Dumen-Op: <id>" commit trailers).
func (s *Store) AppliedOps(ctx context.Context, ids []string) (map[string]bool, error) {
	found := map[string]bool{}
	if len(ids) == 0 {
		return found, nil
	}
	args := []string{"log", "--format=%B", "-F"}
	for _, id := range ids {
		args = append(args, "--grep=Dumen-Op: "+id)
	}
	out, err := s.git().Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Dumen-Op: "); ok && want[rest] {
			found[rest] = true
		}
	}
	return found, nil
}

// HasMergeCommits reports whether the history of HEAD contains any merge commit.
func (s *Store) HasMergeCommits(ctx context.Context) (bool, error) {
	out, err := s.git().Run(ctx, "rev-list", "--merges", "-n", "1", "HEAD")
	return strings.TrimSpace(out) != "", err
}

// ---- lock ----

// Lock takes the store's exclusive lock, waiting until ctx is done.
// Only one process at a time may sync, apply and push in a local store.
func (s *Store) Lock(ctx context.Context) (unlock func(), err error) {
	path := filepath.Join(s.Home, "lock")
	const stale = 2 * time.Minute
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.WriteString(strconv.Itoa(os.Getpid()))
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > stale {
			os.Remove(path) // the holder died; a live holder finishes well within the limit
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dumen store is busy: %w", ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
}
