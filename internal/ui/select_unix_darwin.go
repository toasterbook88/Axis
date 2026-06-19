//go:build darwin
// +build darwin

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
		// On darwin Timeval.Usec is typically int32, so cast to int32.
		tv = &unix.Timeval{Sec: sec, Usec: int32(usec)}
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
