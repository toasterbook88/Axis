package execution

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// startTaskVRAMSampler is the seam run_local uses to sample task VRAM.
var startTaskVRAMSampler = sampleTaskVRAM

// localPIDSink carries the PID of a local shell from the runner that starts
// it to the VRAM sampler running alongside it.
type localPIDSink struct{ pid atomic.Int64 }

type localPIDSinkKey struct{}

// withLocalPIDSink returns a context whose local shell runner will report
// its child PID into the returned sink.
func withLocalPIDSink(ctx context.Context) (context.Context, *localPIDSink) {
	sink := &localPIDSink{}
	return context.WithValue(ctx, localPIDSinkKey{}, sink), sink
}

// reportLocalPID records pid in the context's sink, if there is one.
func reportLocalPID(ctx context.Context, pid int) {
	if sink, ok := ctx.Value(localPIDSinkKey{}).(*localPIDSink); ok && sink != nil {
		sink.pid.Store(int64(pid))
	}
}

// PID returns the reported shell PID, or 0 before the shell has started.
func (s *localPIDSink) PID() int {
	if s == nil {
		return 0
	}
	return int(s.pid.Load())
}

// sampleTaskVRAM polls nvidia-smi per-process accounting while a local task
// runs and returns the peak GPU memory held by the task's own process tree,
// in MiB. rootPID reports the task shell's PID (0 until it has started).
//
// Device-wide memory.used is not task VRAM: resident models and other jobs
// would be attributed to a CPU-only task, and placement ranks on this value.
// A process outside the tree, such as an inference server the task calls
// over HTTP, is not attributed either. The result is 0 ("not observed")
// when nvidia-smi is missing, per-process accounting is unavailable, or no
// GPU process belongs to the task.
//
// The caller starts the sampler before launching the workload and calls
// stop() once it has finished.
func sampleTaskVRAM(ctx context.Context, interval time.Duration, rootPID func() int) (stop func() int64) {
	// The sampler owns its context so stop() can kill an in-flight
	// nvidia-smi instead of waiting on a wedged one.
	sampleCtx, cancel := context.WithCancel(ctx)
	var peak int64
	exited := make(chan struct{})

	go func() {
		defer close(exited)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				return
			case <-ticker.C:
				if used := taskVRAMUsed(sampleCtx, rootPID()); used > peak {
					peak = used
				}
			}
		}
	}()

	// stop returns only after the sampler has exited, so no sample runs
	// after the task is recorded.
	return func() int64 {
		cancel()
		<-exited
		return peak
	}
}

// taskVRAMUsed sums per-process GPU memory for processes in root's tree.
func taskVRAMUsed(ctx context.Context, root int) int64 {
	if root <= 0 {
		return 0
	}
	apps, ok := queryComputeAppsVRAM(ctx)
	if !ok || len(apps) == 0 {
		return 0
	}
	tree := taskProcessTree(root)
	var total int64
	for pid, mib := range apps {
		if tree[pid] {
			total += mib
		}
	}
	return total
}

// queryComputeAppsVRAM runs nvidia-smi once and returns GPU memory used per
// PID in MiB, summed across GPUs. ok is false if nvidia-smi is missing or
// fails.
var queryComputeAppsVRAM = func(ctx context.Context) (map[int]int64, bool) {
	cmd := exec.CommandContext(ctx, "nvidia-smi",
		"--query-compute-apps=pid,used_memory",
		"--format=csv,noheader,nounits",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return parseComputeApps(string(out)), true
}

// parseComputeApps parses "pid, used_memory" rows. Rows whose memory is not
// reported (e.g. "[N/A]" under MIG or WSL) are skipped.
func parseComputeApps(out string) map[int]int64 {
	apps := make(map[int]int64)
	scan := bufio.NewScanner(strings.NewReader(out))
	for scan.Scan() {
		pidField, memField, ok := strings.Cut(scan.Text(), ",")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidField))
		if err != nil || pid <= 0 {
			continue
		}
		mib, err := strconv.ParseInt(strings.TrimSpace(memField), 10, 64)
		if err != nil || mib < 0 {
			continue
		}
		apps[pid] += mib
	}
	return apps
}
