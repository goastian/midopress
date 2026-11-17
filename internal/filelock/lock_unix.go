//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package filelock

import (
	"os"

	"golang.org/x/sys/unix"
)

func Acquire(path string, nonblocking bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	op := unix.LOCK_EX
	if nonblocking {
		op |= unix.LOCK_NB
	}
	if err := unix.Flock(int(f.Fd()), op); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
