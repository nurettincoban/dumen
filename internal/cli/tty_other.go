//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package cli

// ttyFD is false where dumen cannot tell a terminal apart: human actions then go through
// the permission prompt in Claude Code.
func ttyFD(uintptr) bool { return false }
