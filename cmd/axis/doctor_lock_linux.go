//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// probeAdvisoryLockHeld reports whether path's inode appears in /proc/locks.
// It is deliberately observational: unlike TryLockEx, it never takes the lock
// even briefly and therefore cannot race a daemon that is starting.
func probeAdvisoryLockHeld(path string) (bool, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return false, fmt.Errorf("stat lock inode: %w", err)
	}

	f, err := os.Open("/proc/locks")
	if err != nil {
		return false, fmt.Errorf("read kernel lock table: %w", err)
	}
	defer f.Close()

	major := uint64(unix.Major(uint64(st.Dev)))
	minor := uint64(unix.Minor(uint64(st.Dev)))
	inode := uint64(st.Ino)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		for _, field := range strings.Fields(scanner.Text()) {
			parts := strings.Split(field, ":")
			if len(parts) != 3 {
				continue
			}
			gotMajor, majorErr := strconv.ParseUint(parts[0], 16, 64)
			gotMinor, minorErr := strconv.ParseUint(parts[1], 16, 64)
			gotInode, inodeErr := strconv.ParseUint(parts[2], 10, 64)
			if majorErr == nil && minorErr == nil && inodeErr == nil &&
				gotMajor == major && gotMinor == minor && gotInode == inode {
				return true, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("scan kernel lock table: %w", err)
	}
	return false, nil
}
