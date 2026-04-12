//go:build !linux && !darwin

package execution

import "os"

// peakRAMFromProcessState returns 0 on non-Unix platforms.
func peakRAMFromProcessState(_ *os.ProcessState) int64 {
	return 0
}
