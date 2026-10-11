package facts

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDatalessFlags(t *testing.T) {
	if !datalessFlags(sfDataless | 0x20) {
		t.Fatal("SF_DATALESS bit must mark a placeholder")
	}
	if datalessFlags(0x20) || isDataless(nil) {
		t.Fatal("other flags and nil info are not placeholders")
	}
}

func TestScanDiskWeightsBoundedStopsWaitingOnBlockedScan(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	restoreFn, restoreLimit := scanDiskWeightsFn, diskWeightHardLimit
	t.Cleanup(func() { scanDiskWeightsFn, diskWeightHardLimit = restoreFn, restoreLimit })
	scanDiskWeightsFn = func(context.Context, DiskWeightScanConfig) DiskWeightScanResult {
		<-release // a readdir stuck in the kernel ignores ctx
		return DiskWeightScanResult{}
	}
	diskWeightHardLimit = 20 * time.Millisecond

	start := time.Now()
	res := scanDiskWeightsBounded(context.Background(), DiskWeightScanConfig{})
	if !res.Truncated || len(res.Weights) != 0 {
		t.Fatalf("res = %+v, want truncated and empty", res)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s on a blocked scan", elapsed)
	}
}

func TestScanDiskWeightsBoundedReturnsFinishedScan(t *testing.T) {
	restore := scanDiskWeightsFn
	t.Cleanup(func() { scanDiskWeightsFn = restore })
	scanDiskWeightsFn = func(context.Context, DiskWeightScanConfig) DiskWeightScanResult {
		return DiskWeightScanResult{Truncated: false}
	}
	if res := scanDiskWeightsBounded(context.Background(), DiskWeightScanConfig{}); res.Truncated {
		t.Fatalf("res = %+v, want the scan's own result", res)
	}
}

// The remote walk must end even when a readdir never returns (an iCloud
// placeholder directory on macOS): the script stops waiting at HARD_LIMIT.
func TestDiskWeightsScriptEndsWhenWalkBlocks(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	s := DiskWeightsDiscoveryScript
	for _, needle := range []string{"SF_DATALESS = 0x40000000", "not dataless(os.path.join(dirpath, d))", "HARD_LIMIT = 12", "def walk_all():\n", "os._exit(0)"} {
		if strings.Count(s, needle) != 1 {
			t.Fatalf("script must contain %q once", needle)
		}
	}
	_, rest, _ := strings.Cut(s, "<<'PY'\n")
	body, _, _ := strings.Cut(rest, "\nPY")
	body = strings.Replace(body, "HARD_LIMIT = 12", "HARD_LIMIT = 1", 1)
	body = strings.Replace(body, "def walk_all():\n", "def walk_all():\n    threading.Event().wait()\n", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-")
	cmd.Stdin = strings.NewReader(body)
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload struct {
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(out, &payload); err != nil || !payload.Truncated {
		t.Fatalf("output %q (err %v), want truncated JSON", out, err)
	}
}
