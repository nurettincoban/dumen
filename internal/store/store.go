package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nurettincoban/dumen/internal/proc"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nurettincoban/dumen/internal/gitx"
	"github.com/nurettincoban/dumen/internal/model"

	"gopkg.in/yaml.v3"
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

// RemoteProject reads the project name from dumen.yml on the store branch without
// setting anything up locally. It returns "" when the name cannot be read.
func RemoteProject(ctx context.Context, remote, branch string) string {
	if gitx.CheckStore(remote, branch) != nil {
		return ""
	}
	tmp, err := os.MkdirTemp("", "dumen-peek-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(tmp)
	g := gitx.Git{Dir: tmp}
	for _, st := range [][]string{{"init", "-q"}, {"fetch", "-q", "--depth", "1", "--end-of-options", remote, "refs/heads/" + branch}} {
		if _, err := g.Run(ctx, st...); err != nil {
			return ""
		}
	}
	out, err := g.Run(ctx, "show", "FETCH_HEAD:"+ConfigFile)
	if err != nil {
		return ""
	}
	var cfg struct {
		Project string `yaml:"project"`
	}
	if yaml.Unmarshal([]byte(out), &cfg) != nil {
		return ""
	}
	return model.Slug(cfg.Project)
}

// Local is the per-machine description of a store (~/.dumen/<project>/local.json).
type Local struct {
	Project string `json:"project"`
	Remote  string `json:"remote"`
	Branch  string `json:"branch"`
	Email   string `json:"email,omitempty"` // this user's git email, captured at init/join so hooks need not run git
	// Roots are the directories that joined this store on this machine: the repositories
	// and workspaces where `dumen init` or `dumen join` was run. A `.dumen` file somewhere
	// else that merely names the same project does not get the team's records.
	Roots []string `json:"roots,omitempty"`
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
	if err := model.CheckName("project", project); err != nil {
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
	if err := gitx.CheckStore(s.Remote, s.Branch); err != nil {
		return nil, err
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
	if err := gitx.CheckStore(remote, branch); err != nil {
		return nil, res, err
	}
	if project == "" {
		project = ProjectName(remote)
	}
	if err := model.CheckName("project", project); err != nil {
		return nil, res, err
	}
	s := &Store{Home: filepath.Join(base, project), Local: Local{Project: project, Remote: remote, Branch: branch, Email: strings.ToLower(founder.Email)}}
	if prev, err := Open(project); err == nil {
		s.Roots = prev.Roots
		if prev.Remote != remote || prev.Branch != branch {
			return nil, res, fmt.Errorf("project %q already points at %s#%s; pass a different project name", project, prev.Remote, prev.Branch)
		}
		// Another repository of the same project on this machine already set the store up:
		// share it (one clone, one cache, one outbox per project).
		if _, err := prev.Sync(ctx); err == nil {
			return prev, res, nil
		}
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
		{"remote", "add", "--", "origin", remote},
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
	if ci := ciConfig(s.Root()); ci != "" {
		return nil, res, fmt.Errorf("branch %q contains %s; dumen will not use a branch that can trigger pipelines", branch, ci)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), ConfigFile)); err != nil {
		return nil, res, fmt.Errorf("branch %q exists but has no %s; it does not look like a dumen store", branch, ConfigFile)
	}
	if res.Created {
		warn := "Protect the " + branch + " branch on your git host: let team members push, forbid force pushes and deletion."
		if strings.Contains(strings.ToLower(remote), "gitlab") {
			warn += " If this GitLab project sets a custom or external CI/CD configuration path (Settings > CI/CD > General pipelines), " +
				"pushes to the branch can start pipelines; exclude the branch there."
		}
		res.Warnings = append(res.Warnings, warn)
	}
	if err := s.saveLocal(); err != nil {
		return nil, res, err
	}
	return s, res, nil
}

func (s *Store) saveLocal() error {
	data, _ := json.MarshalIndent(s.Local, "", "  ")
	path := filepath.Join(s.Home, "local.json")
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// realDir resolves symbolic links so that one directory has one name.
func realDir(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	return filepath.Clean(dir)
}

// AddRoot records that the directory joined this store.
func (s *Store) AddRoot(dir string) error {
	dir = realDir(dir)
	if cur, err := Open(s.Project); err == nil {
		s.Roots = cur.Roots // another repository may have joined since this one was opened
	}
	for _, r := range s.Roots {
		if sameDir(r, dir) {
			return nil
		}
	}
	s.Roots = append(s.Roots, dir)
	return s.saveLocal()
}

// Knows reports whether the directory joined this store.
func (s *Store) Knows(dir string) bool {
	dir = realDir(dir)
	for _, r := range s.Roots {
		if sameDir(r, dir) {
			return true
		}
	}
	return false
}

// sameDir compares two resolved directory names. Where the file system ignores case
// (Windows, macOS by default) one directory can arrive spelled in more than one way.
func sameDir(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// ciFiles are the pipeline definitions of the common git hosts and CI services. A store
// branch must hold none of them, or every dumen write could start a pipeline.
var ciFiles = []string{
	".gitlab-ci.yml",          // GitLab
	".github/workflows",       // GitHub Actions
	".forgejo/workflows",      // Forgejo, Codeberg
	".gitea/workflows",        // Gitea
	"bitbucket-pipelines.yml", // Bitbucket
	"azure-pipelines.yml",     // Azure DevOps
	".circleci",               // CircleCI
	".woodpecker.yml",         // Woodpecker
	".woodpecker",             //
	".drone.yml",              // Drone
	".buildkite",              // Buildkite
	"Jenkinsfile",             // Jenkins
}

// ciConfig returns the first pipeline definition found in root, or "".
func ciConfig(root string) string {
	for _, f := range ciFiles {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			return f
		}
	}
	return ""
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
//
// The lock file holds the holder's pid and a random token, and the holder touches it
// every lockRefresh while it works. A lock whose holder is gone, or that nobody has
// touched for lockStale, is taken over; a long flush keeps its lock for as long as it runs.
func (s *Store) Lock(ctx context.Context) (unlock func(), err error) {
	path := filepath.Join(s.Home, "lock")
	mine := fmt.Sprintf("%d %s", os.Getpid(), model.NewID("L"))
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.WriteString(mine)
			f.Close()
			s.clearGitLocks()
			stop := make(chan struct{})
			go func() {
				t := time.NewTicker(lockRefresh)
				defer t.Stop()
				for {
					select {
					case <-stop:
						return
					case now := <-t.C:
						os.Chtimes(path, now, now)
					}
				}
			}()
			var once sync.Once
			return func() {
				once.Do(func() {
					close(stop)
					if cur, _ := os.ReadFile(path); string(cur) == mine {
						os.Remove(path)
					}
				})
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if held, st := readLock(path); st != nil && lockAbandoned(held, st.ModTime()) {
			// Remove it only if it is still the lock that was judged abandoned.
			if cur, _ := os.ReadFile(path); string(cur) == held {
				os.Remove(path)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dumen store is busy: %w", ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
}

const (
	lockRefresh = 20 * time.Second
	lockStale   = 2 * time.Minute
)

func readLock(path string) (string, os.FileInfo) {
	st, err := os.Stat(path)
	if err != nil {
		return "", nil
	}
	data, _ := os.ReadFile(path)
	return string(data), st
}

// lockAbandoned reports whether a lock can be taken over: its holder's process is gone,
// or nobody has touched it for lockStale (a live holder touches it every lockRefresh, so
// an old lock under a live pid means the pid now belongs to some other program).
func lockAbandoned(content string, touched time.Time) bool {
	if time.Since(touched) > lockStale {
		return true
	}
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return time.Since(touched) > 2*time.Second // being written right now, or left empty by a crash
	}
	pid, err := strconv.Atoi(fields[0])
	return err == nil && pid > 0 && !proc.Alive(pid)
}

// clearGitLocks removes lock files a killed git left in the store's clone. Every git
// command in the clone runs under the store lock, so whoever holds that lock knows that
// no git process is working there.
func (s *Store) clearGitLocks() {
	gitDir := filepath.Join(s.Root(), ".git")
	for _, name := range []string{"index.lock", "HEAD.lock", "config.lock", "shallow.lock", "packed-refs.lock"} {
		os.Remove(filepath.Join(gitDir, name))
	}
	branch := filepath.Join(gitDir, "refs", "heads", filepath.FromSlash(s.Branch)+".lock")
	os.Remove(branch)
}

// Maintain packs the clone's loose objects when that has not been done for a day. Every
// record is a commit and automatic garbage collection is off in the clone (it must never
// start in the middle of a write), so without this the clone grows without bound. The
// caller holds no lock; Maintain takes it.
func (s *Store) Maintain(ctx context.Context) error {
	marker := filepath.Join(s.Home, "maintained")
	if st, err := os.Stat(marker); err == nil && time.Since(st.ModTime()) < 24*time.Hour {
		return nil
	}
	unlock, err := s.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.WriteFile(marker, nil, 0o644); err != nil { // first: a failing gc is not retried in a loop
		return err
	}
	_, err = s.git().Run(ctx, "gc", "--quiet")
	return err
}
