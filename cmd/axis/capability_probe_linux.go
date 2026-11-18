//go:build linux

package main

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// probeHardware reads this machine's hardware through system calls rather than
// through the AXIS fact collector, which reads /proc/meminfo, nproc and df.
func probeHardware() (hardwareReading, string, error) {
	const ref = "uname(2), sched_getaffinity(2), sysinfo(2), statfs(2) on /"
	var r hardwareReading
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return r, ref, fmt.Errorf("uname: %w", err)
	}
	r.OS = strings.ToLower(unix.ByteSliceToString(u.Sysname[:]))
	r.Arch = normalizeArch(unix.ByteSliceToString(u.Machine[:]))
	var cpus unix.CPUSet
	if err := unix.SchedGetaffinity(0, &cpus); err != nil {
		return r, ref, fmt.Errorf("sched_getaffinity: %w", err)
	}
	r.Cores = cpus.Count()
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return r, ref, fmt.Errorf("sysinfo: %w", err)
	}
	r.RAMTotalMB = int64(uint64(si.Totalram) * uint64(si.Unit) / (1 << 20))
	var fs unix.Statfs_t
	if err := unix.Statfs("/", &fs); err != nil {
		return r, ref, fmt.Errorf("statfs: %w", err)
	}
	block := uint64(fs.Frsize)
	if block == 0 {
		block = uint64(fs.Bsize)
	}
	r.DiskTotalGB = int64(fs.Blocks * block / (1 << 30))
	return r, ref, nil
}
