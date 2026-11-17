//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package filelock

import "errors"

func Acquire(string, bool) (func(), error) {
	return nil, errors.New("persistent quota file locking unavailable on this platform")
}
