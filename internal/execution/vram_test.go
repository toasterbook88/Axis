package execution

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func selfPID() int { return os.Getpid() }

// The sampler degrades to 0 on machines without nvidia-smi (Apple Silicon,
// Intel-only nodes).
func TestSampleTaskVRAMReturnsZeroWhenNvidiaSmiMissing(t *testing.T) {
	t.Setenv("PATH", "/nonexistent/empty-path")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sampleTaskVRAM(ctx, 20*time.Millisecond, selfPID)
	time.Sleep(80 * time.Millisecond)
	if peak := stop(); peak != 0 {
		t.Fatalf("expected 0 peak when nvidia-smi missing, got %d", peak)
	}
}

func TestSampleTaskVRAMStopsCleanly(t *testing.T) {
	t.Setenv("PATH", "/nonexistent/empty-path")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sampleTaskVRAM(ctx, 50*time.Millisecond, selfPID)
	if peak := stop(); peak < 0 {
		t.Fatalf("expected non-negative peak, got %d", peak)
	}
}

func TestQueryComputeAppsVRAMHandlesMissingBinary(t *testing.T) {
	t.Setenv("PATH", "/nonexistent/empty-path")
	apps, ok := queryComputeAppsVRAM(context.Background())
	if ok || len(apps) != 0 {
		t.Fatalf("expected ok=false and no apps when nvidia-smi missing, got ok=%v apps=%v", ok, apps)
	}
}

func TestParseComputeApps(t *testing.T) {
	out := "4242, 1024\n4242, 512\n77, [N/A]\nbogus\n0, 9\n  88 , 300 \n"
	got := parseComputeApps(out)
	want := map[int]int64{4242: 1536, 88: 300}
	if len(got) != len(want) {
		t.Fatalf("parseComputeApps = %v, want %v", got, want)
	}
	for pid, mib := range want {
		if got[pid] != mib {
			t.Errorf("pid %d = %d MiB, want %d (multi-GPU rows sum, N/A rows skipped)", pid, got[pid], mib)
		}
	}
}

func stubVRAMSources(apps map[int]int64, tree map[int]bool) func() {
	prevApps, prevTree := queryComputeAppsVRAM, taskProcessTree
	queryComputeAppsVRAM = func(context.Context) (map[int]int64, bool) { return apps, true }
	taskProcessTree = func(int) map[int]bool { return tree }
	return func() { queryComputeAppsVRAM, taskProcessTree = prevApps, prevTree }
}

// GPU memory held by processes outside the task's tree, such as a resident
// model server, is never task VRAM.
func TestTaskVRAMUsedAttributesOnlyTheTaskTree(t *testing.T) {
	const root, child, unrelated = 500, 501, 900
	defer stubVRAMSources(
		map[int]int64{child: 3000, unrelated: 8000},
		map[int]bool{root: true, child: true},
	)()
	if got := taskVRAMUsed(context.Background(), root); got != 3000 {
		t.Fatalf("taskVRAMUsed = %d, want 3000 (child only)", got)
	}
}

func TestTaskVRAMUsedIsZeroForCPUOnlyTaskBesideBusyGPU(t *testing.T) {
	const root, unrelated = 500, 900
	defer stubVRAMSources(map[int]int64{unrelated: 8000}, map[int]bool{root: true})()
	if got := taskVRAMUsed(context.Background(), root); got != 0 {
		t.Fatalf("CPU-only task got %d MiB from an unrelated GPU process, want 0", got)
	}
}

func TestTaskVRAMUsedIsZeroBeforeShellStarts(t *testing.T) {
	queried := false
	prev := queryComputeAppsVRAM
	queryComputeAppsVRAM = func(context.Context) (map[int]int64, bool) {
		queried = true
		return map[int]int64{1: 100}, true
	}
	defer func() { queryComputeAppsVRAM = prev }()
	if got := taskVRAMUsed(context.Background(), 0); got != 0 || queried {
		t.Fatalf("unknown PID: got %d, queried=%v; want 0 without querying", got, queried)
	}
}

func TestSampleTaskVRAMTracksPeak(t *testing.T) {
	const root = 500
	defer stubVRAMSources(map[int]int64{root: 1200}, map[int]bool{root: true})()
	stop := sampleTaskVRAM(context.Background(), 5*time.Millisecond, func() int { return root })
	time.Sleep(40 * time.Millisecond)
	if peak := stop(); peak != 1200 {
		t.Fatalf("peak = %d, want 1200", peak)
	}
}

// The local shell runner reports the PID of the shell it started.
func TestStreamLocalShellReportsPID(t *testing.T) {
	ctx, sink := withLocalPIDSink(context.Background())
	var out strings.Builder
	if _, err := StreamLocalShell(ctx, "echo $$", os.Environ(), &out, nil); err != nil {
		t.Fatalf("StreamLocalShell: %v", err)
	}
	if got := strconv.Itoa(sink.PID()); strings.TrimSpace(out.String()) != got {
		t.Fatalf("reported PID %s, shell printed %q", got, out.String())
	}
}

func TestRunLocalShellReportsPID(t *testing.T) {
	ctx, sink := withLocalPIDSink(context.Background())
	out, _, err := RunLocalShell(ctx, "echo $$", os.Environ())
	if err != nil {
		t.Fatalf("RunLocalShell: %v", err)
	}
	if got := strconv.Itoa(sink.PID()); strings.TrimSpace(string(out)) != got {
		t.Fatalf("reported PID %s, shell printed %q", got, out)
	}
}
