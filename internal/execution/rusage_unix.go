//go:build linux || darwin

package execution

import (
	"os"
	"runtime"
	"syscall"
)

// peakRAMFromProcessState extracts the peak RSS of a completed process from its
// ProcessState. Returns MB, or 0 if unavailable.
func peakRAMFromProcessState(ps *os.ProcessState) int64 {
	if ps == nil {
		return 0
	}
	sysUsage := ps.SysUsage()
	if sysUsage == nil {
		return 0
	}
	rusage, ok := sysUsage.(*syscall.Rusage)
	if !ok || rusage == nil {
		return 0
	}
	switch runtime.GOOS {
	case "darwin":
		// Maxrss is in bytes on Darwin.
		return rusage.Maxrss / (1024 * 1024)
	case "linux":
		// Maxrss is in kilobytes on Linux.
		return rusage.Maxrss / 1024
	default:
		return 0
	}
}
