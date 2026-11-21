//go:build !windows

package store

import (
	"os"
	"syscall"
)

// tryFlock takes an exclusive lock on the open file without waiting. The kernel holds
// the lock on the open file and drops it when the file is closed or the process dies.
func tryFlock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN || err == syscall.EINTR {
		return false, nil
	}
	return false, err
}

func unFlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
