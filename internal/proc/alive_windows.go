//go:build windows

package proc

import "os"

// Alive reports whether a process with this pid exists. On Windows FindProcess opens the
// process and fails when there is none.
func Alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}
