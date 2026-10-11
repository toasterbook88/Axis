package facts

import (
	"context"
	"io/fs"
	"time"
)

// sfDataless is the macOS st_flags bit for an iCloud placeholder. Reading
// one downloads it, and a readdir on one can block for minutes; the flag
// itself comes from a local lstat.
const sfDataless = 0x40000000

func datalessFlags(flags uint32) bool { return flags&sfDataless != 0 }

func isDataless(info fs.FileInfo) bool { return info != nil && datalessFlags(statFlags(info)) }

// diskWeightHardLimit bounds the local scan. Its budget is only checked
// between entries, and a blocked readdir ignores ctx.
var diskWeightHardLimit = diskWeightScanBudget + 4*time.Second

// scanDiskWeightsBounded runs the scan off the caller's goroutine and stops
// waiting at diskWeightHardLimit, reporting a truncated empty result.
func scanDiskWeightsBounded(ctx context.Context, cfg DiskWeightScanConfig) DiskWeightScanResult {
	scan := scanDiskWeightsFn
	done := make(chan DiskWeightScanResult, 1)
	go func() { done <- scan(ctx, cfg) }()
	timer := time.NewTimer(diskWeightHardLimit)
	defer timer.Stop()
	select {
	case res := <-done:
		return res
	case <-timer.C:
		return DiskWeightScanResult{Truncated: true}
	}
}
