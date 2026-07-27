package execution

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// samplePeakVRAM polls nvidia-smi at a fixed interval while run is executing
// and returns the peak total GPU memory used across all GPUs, in MiB. If
// nvidia-smi is unavailable or every sample fails, it returns 0 and a nil
// error — VRAM is best-effort telemetry, not a load-bearing signal.
//
// The caller starts the sampler before launching the workload, then calls
// stop() once the workload has finished; the returned peak is the max total
// observed across all successful samples taken between start and stop.
//
// On machines without NVIDIA GPUs (e.g. Apple Silicon, Intel-only nodes),
// nvidia-smi will not be on PATH and this returns 0 cleanly.
func samplePeakVRAM(ctx context.Context, interval time.Duration) (stop func() int64) {
	var mu sync.Mutex
	peak := int64(0)
	done := make(chan struct{})

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				used, ok := queryTotalVRAMUsed(ctx)
				if !ok {
					continue
				}
				mu.Lock()
				if used > peak {
					peak = used
				}
				mu.Unlock()
			}
		}
	}()

	return func() int64 {
		close(done)
		mu.Lock()
		defer mu.Unlock()
		return peak
	}
}

// queryTotalVRAMUsed runs nvidia-smi once and sums memory.used across all
// GPUs. Returns (totalMiB, true) on success, (0, false) if nvidia-smi is
// missing, fails, or emits unparseable output.
func queryTotalVRAMUsed(ctx context.Context) (int64, bool) {
	cmd := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=memory.used",
		"--format=csv,noheader,nounits",
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	var total int64
	scan := bufio.NewScanner(strings.NewReader(string(out)))
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		mib, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return 0, false
		}
		total += mib
	}
	if err := scan.Err(); err != nil {
		return 0, false
	}
	return total, true
}