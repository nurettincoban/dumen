//go:build windows

package cli

import "syscall"

// ttyFD reports whether fd is a console: GetConsoleMode fails on NUL, pipes and files.
func ttyFD(fd uintptr) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(fd), &mode) == nil
}
