//go:build darwin

package main

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// probeHardware reads this machine's hardware through system calls rather than
// through the AXIS fact collector, which shells out to sysctl and df.
func probeHardware() (hardwareReading, string, error) {
	const ref = "uname(2), sysctl(3) hw.ncpu and hw.memsize, statfs(2) on /"
	var r hardwareReading
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return r, ref, fmt.Errorf("uname: %w", err)
	}
	r.OS = strings.ToLower(unix.ByteSliceToString(u.Sysname[:]))
	r.Arch = normalizeArch(unix.ByteSliceToString(u.Machine[:]))
	ncpu, err := unix.SysctlUint32("hw.ncpu")
	if err != nil {
		return r, ref, fmt.Errorf("sysctl hw.ncpu: %w", err)
	}
	r.Cores = int(ncpu)
	mem, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return r, ref, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	r.RAMTotalMB = int64(mem / (1 << 20))
	var fs unix.Statfs_t
	if err := unix.Statfs("/", &fs); err != nil {
		return r, ref, fmt.Errorf("statfs: %w", err)
	}
	r.DiskTotalGB = int64(fs.Blocks * uint64(fs.Bsize) / (1 << 30))
	return r, ref, nil
}
