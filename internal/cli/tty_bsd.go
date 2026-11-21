//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package cli

import (
	"syscall"
	"unsafe"
)

// ttyFD reports whether fd is a terminal: the terminal settings ioctl succeeds only on a
// tty, not on /dev/null or another character device.
func ttyFD(fd uintptr) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
