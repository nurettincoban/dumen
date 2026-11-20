//go:build windows

package session

import "os"

// alive reports whether a process with this pid exists. On Windows FindProcess opens the
// process and fails when there is none.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}
