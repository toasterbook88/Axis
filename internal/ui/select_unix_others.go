//go:build !windows && !darwin
// +build !windows,!darwin

package ui

import (
	"time"

	"golang.org/x/sys/unix"
)

func isInputAvailable(fd int, timeout time.Duration) (bool, error) {
	var rfds unix.FdSet
	rfds.Bits[fd/64] |= 1 << (uint(fd) % 64)

	var tv *unix.Timeval
	if timeout >= 0 {
		sec := int64(timeout / time.Second)
		usec := int64((timeout % time.Second) / time.Microsecond)
		// On many other unix platforms Usec is int64; use int64 here.
		tv = &unix.Timeval{Sec: sec, Usec: usec}
	}

	n, err := unix.Select(fd+1, &rfds, nil, nil, tv)
	if err != nil {
		if err == unix.EINTR {
			return false, nil
		}
		return false, err
	}
	return n > 0, nil
}
