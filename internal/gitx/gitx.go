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
	"strings"
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

// Run executes git with args and returns its stdout.
func (g Git) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // never block on a credential prompt
		"LC_ALL=C",              // stable messages for classification
	)
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
)

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
	// Porcelain marks a refused ref with "!"; concurrent pushes to the same ref can
	// also surface as a lock failure on the remote. Both mean: nothing was written.
	for _, marker := range []string{"[rejected]", "[remote rejected]", "non-fast-forward", "fetch first",
		"cannot lock ref", "failed to update ref", "failed to lock", "is at ", "reference already exists"} {
		if strings.Contains(text, marker) {
			return PushRejected, err
		}
	}
	return PushFailed, err
}

// LsRemote returns the sha of refs/heads/<branch> on the remote, or "" if the branch does not exist.
func LsRemote(ctx context.Context, remote, branch string) (string, error) {
	out, err := Git{}.Run(ctx, "ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}
