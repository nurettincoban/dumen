// Package gitx is the only place dumen runs the system git binary.
// Shelling out means the user's SSH keys and credential helpers keep working.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// Git runs git commands in a directory.
type Git struct {
	Dir string
	Env []string // extra environment, appended after the process environment
}

// Error is a failed git invocation.
type Error struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int // -1 when the process did not start or was killed
	Err      error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.Stdout)
	}
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

var networkCmd = map[string]bool{"fetch": true, "push": true, "ls-remote": true, "clone": true}

var (
	sshOnce sync.Once
	sshEnv  []string
)

// sshBatch makes ssh fail instead of asking for a passphrase or a host key, which a
// background sync could never answer. A person's own ssh command is left alone: it is set
// only when neither the environment nor git's configuration names one.
func sshBatch(dir string) []string {
	sshOnce.Do(func() {
		if os.Getenv("GIT_SSH_COMMAND") != "" || os.Getenv("GIT_SSH") != "" {
			return
		}
		cmd := exec.Command("git", "config", "--get", "core.sshCommand")
		cmd.Dir = dir
		if out, _ := cmd.Output(); strings.TrimSpace(string(out)) != "" {
			return
		}
		sshEnv = []string{"GIT_SSH_COMMAND=ssh -oBatchMode=yes"}
	})
	return sshEnv
}

// Run executes git with args and returns its stdout.
func (g Git) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // never block on a credential prompt
		"GCM_INTERACTIVE=never", // nor open a Git Credential Manager window from a background sync
		"LC_ALL=C",              // stable messages for classification
	)
	if len(args) > 0 && networkCmd[args[0]] {
		cmd.Env = append(cmd.Env, sshBatch(g.Dir)...)
	}
	cmd.Env = append(cmd.Env, g.Env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return out.String(), &Error{Args: args, Stdout: out.String(), Stderr: errb.String(), ExitCode: code, Err: err}
	}
	return out.String(), nil
}

// PushResult classifies the outcome of a push.
type PushResult int

const (
	PushOK       PushResult = iota
	PushRejected            // the remote moved or refused the ref update: fetch and re-apply
	PushFailed              // network, auth or another error: the outcome is unknown
	PushRefused             // the host turned the push down for good (protected branch, missing permission, a server-side rule): retrying will not help
)

// RefusedError carries what the git host said when it refused a push.
type RefusedError struct{ Message string }

func (e *RefusedError) Error() string { return "the git host refused the push: " + e.Message }

// raceMarkers are the ways a push that merely lost a race shows up.
var raceMarkers = []string{"non-fast-forward", "fetch first", "cannot lock ref", "failed to update ref",
	"failed to lock", "is at ", "reference already exists", "stale info"}

// hostMessage extracts the reason from a "[remote rejected]" line and the remote's own output.
func hostMessage(text string) string {
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "remote:"):
			if m := strings.TrimSpace(strings.TrimPrefix(line, "remote:")); m != "" {
				parts = append(parts, m)
			}
		case strings.Contains(line, "[remote rejected]"):
			if i := strings.Index(line, "[remote rejected]"); i >= 0 {
				parts = append(parts, strings.Trim(strings.TrimSpace(line[i+len("[remote rejected]"):]), "()"))
			}
		}
	}
	msg := strings.Join(parts, "; ")
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	if msg == "" {
		msg = "no reason given"
	}
	return msg
}

// Push pushes HEAD to refs/heads/<branch> on origin without force, so it only
// ever succeeds as a fast-forward (or as the creation of the branch).
func (g Git) Push(ctx context.Context, branch string) (PushResult, error) {
	out, err := g.Run(ctx, "push", "--porcelain", "origin", "HEAD:refs/heads/"+branch)
	if err == nil {
		return PushOK, nil
	}
	var ge *Error
	text := out
	if errors.As(err, &ge) {
		text += "\n" + ge.Stderr
	}
	// Porcelain marks a refused ref with "!". A push that lost a race (someone else pushed
	// first, or the remote's ref lock was taken) wrote nothing and is worth retrying.
	for _, marker := range raceMarkers {
		if strings.Contains(text, marker) {
			return PushRejected, err
		}
	}
	// Anything else the remote itself rejected is a decision of the host: a protected
	// branch, a missing permission, a rule such as "commits must be signed".
	if strings.Contains(text, "[remote rejected]") {
		return PushRefused, &RefusedError{Message: hostMessage(text)}
	}
	if strings.Contains(text, "[rejected]") {
		return PushRejected, err
	}
	return PushFailed, err
}

var remoteHelper = regexp.MustCompile(`^\s*[A-Za-z0-9+.-]+::`)

// CheckRemote refuses a remote that git would read as an option or as a remote-helper
// command. A remote can come from a `.dumen` file in a repository somebody else wrote.
func CheckRemote(remote string) error {
	switch {
	case strings.TrimSpace(remote) == "":
		return errors.New("the store remote is empty")
	case strings.HasPrefix(strings.TrimSpace(remote), "-"):
		return fmt.Errorf("refusing store remote %q: a remote cannot start with \"-\"", remote)
	case remoteHelper.MatchString(remote):
		return fmt.Errorf("refusing store remote %q: remote helpers (\"<name>::\") are not allowed", remote)
	case strings.ContainsAny(remote, "\n\r\x00"):
		return errors.New("refusing a store remote with control characters")
	}
	return nil
}

var branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// CheckBranch refuses a branch name that is not a plain ref name.
func CheckBranch(branch string) error {
	if !branchName.MatchString(branch) || strings.Contains(branch, "..") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".lock") {
		return fmt.Errorf("refusing store branch %q: use letters, digits, \".\", \"_\", \"-\" and \"/\"", branch)
	}
	return nil
}

// CheckStore checks both parts of a store location.
func CheckStore(remote, branch string) error {
	if err := CheckRemote(remote); err != nil {
		return err
	}
	return CheckBranch(branch)
}

// LsRemote returns the sha of refs/heads/<branch> on the remote, or "" if the branch does not exist.
func LsRemote(ctx context.Context, remote, branch string) (string, error) {
	if err := CheckStore(remote, branch); err != nil {
		return "", err
	}
	out, err := Git{}.Run(ctx, "ls-remote", "--heads", "--end-of-options", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}
