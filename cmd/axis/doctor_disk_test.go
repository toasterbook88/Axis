package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func diskNode(name string, free, total int64) models.NodeFacts {
	return models.NodeFacts{Name: name, Resources: &models.Resources{DiskFreeGB: free, DiskTotalGB: total}}
}

func TestDiskSpaceDoctorCheck(t *testing.T) {
	for _, tc := range []struct {
		name       string
		nodes      []models.NodeFacts
		status     string
		contains   []string
		notContain []string
	}{
		{"all healthy", []models.NodeFacts{diskNode("a", 50, 100), diskNode("b", 400, 900)},
			"pass", []string{"none of 2 node(s) with disk facts is below 10% free"}, []string{"no disk facts"}},
		{"below 10% after rounding", []models.NodeFacts{diskNode("a", 8, 100)},
			"warn", []string{"a 8 of 100 GB free (8%)"}, []string{"critically low"}},
		{"exactly 10% passes", []models.NodeFacts{diskNode("a", 10, 100)}, "pass", nil, nil},
		{"certainly below 10% warns", []models.NodeFacts{diskNode("a", 9, 100)},
			"warn", []string{"a 9 of 100 GB free (9%)"}, []string{"critically low"}},
		{"certainly below 3% is critical", []models.NodeFacts{diskNode("a", 2, 100)},
			"warn", []string{"a 2 of 100 GB free (2%), critically low"}, nil},
		{"critical rounding edge is low, not critical", []models.NodeFacts{diskNode("a", 1, 40)},
			"warn", []string{"a 1 of 40 GB free (2%)"}, []string{"critically low"}},
		{"unreachable and errored nodes have no current facts", []models.NodeFacts{
			diskNode("a", 50, 100),
			{Name: "stale", Status: models.StatusUnreachable, Resources: &models.Resources{DiskFreeGB: 1, DiskTotalGB: 100}},
			{Name: "broken", Status: models.StatusError, Resources: &models.Resources{DiskFreeGB: 1, DiskTotalGB: 100}}},
			"pass", []string{"none of 1 node(s)", "no disk facts for broken, stale"}, []string{"critically low"}},
		{"rounding edge passes", []models.NodeFacts{diskNode("a", 1, 15)}, "pass", nil, nil},
		{"critically low", []models.NodeFacts{diskNode("a", 1, 100)},
			"warn", []string{"a 1 of 100 GB free (1%), critically low"}, nil},
		{"at 3% is low, not critical", []models.NodeFacts{diskNode("a", 3, 100)},
			"warn", []string{"a 3 of 100 GB free (3%)"}, []string{"critically low"}},
		{"lowest share first, ties by name", []models.NodeFacts{
			diskNode("m", 8, 100), diskNode("z", 1, 100), diskNode("b", 1, 100), diskNode("ok", 60, 100)},
			"warn", []string{"b 1 of 100 GB free (1%), critically low; z 1 of 100 GB free (1%), critically low; m 8 of 100 GB free (8%)"},
			[]string{"ok "}},
		{"nodes without facts are named", []models.NodeFacts{
			diskNode("a", 50, 100), {Name: "gone"}, diskNode("tiny", 0, 0)},
			"pass", []string{"none of 1 node(s)", "; no disk facts for gone, tiny"}, nil},
		{"no facts at all", []models.NodeFacts{{Name: "gone"}, diskNode("tiny", 0, 0)},
			"warn", []string{"no disk facts in the snapshot"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := diskSpaceDoctorCheck(tc.nodes)
			if got.Name != "Disk space" || got.Status != tc.status {
				t.Fatalf("check = %+v, want status %s", got, tc.status)
			}
			for _, s := range tc.contains {
				if !strings.Contains(got.Message, s) {
					t.Fatalf("message %q lacks %q", got.Message, s)
				}
			}
			for _, s := range tc.notContain {
				if strings.Contains(got.Message, s) {
					t.Fatalf("message %q should not contain %q", got.Message, s)
				}
			}
		})
	}
}

// runDiskDoctor runs `axis doctor` with one configured node and the given
// daemon snapshot result, and returns the rendered report and the error.
func runDiskDoctor(t *testing.T, snap *models.ClusterSnapshot, snapErr error, args ...string) (string, error) {
	t.Helper()
	tmpFile := filepath.Join(t.TempDir(), "nodes.yaml")
	defer stubDoctorConfigPath(t, func() string { return tmpFile })()
	defer stubDoctorOwnershipHealthy(t)()
	defer stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return &config.Config{Nodes: []config.NodeConfig{{Name: "alpha", Hostname: "10.0.0.1", SSHUser: "axis"}}}, nil
	})()
	defer stubDoctorSSHChecker(t, func(context.Context, config.NodeConfig) error { return nil })()
	defer stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return snap, "cached", snapErr
	})()
	stdout, _, err := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs(args)
		return cmd.Execute()
	})
	return stripANSI(stdout), err
}

func TestDoctorIncludesDiskSpaceOnlyWithSnapshotNodes(t *testing.T) {
	out, err := runDiskDoctor(t, &models.ClusterSnapshot{Nodes: []models.NodeFacts{diskNode("alpha", 50, 100)}}, nil)
	if err != nil || !strings.Contains(out, "Disk space") || !strings.Contains(out, "none of 1 node(s) with disk facts") {
		t.Fatalf("err=%v; report lacks the passing disk check:\n%s", err, out)
	}
	out, _ = runDiskDoctor(t, nil, errors.New("daemon unavailable"))
	if strings.Contains(out, "Disk space") {
		t.Fatalf("an unreachable daemon must not produce a disk check:\n%s", out)
	}
	out, _ = runDiskDoctor(t, &models.ClusterSnapshot{}, nil)
	if !strings.Contains(out, "Reachable, 0 nodes cached") || strings.Contains(out, "Disk space") {
		t.Fatalf("a snapshot with no nodes must not produce a disk check:\n%s", out)
	}
}

// A nearly full disk is advice: it warns, and --strict still succeeds.
func TestDoctorDiskSpaceWarnsWithoutFailingStrict(t *testing.T) {
	out, err := runDiskDoctor(t, &models.ClusterSnapshot{Nodes: []models.NodeFacts{diskNode("alpha", 1, 100)}}, nil, "--strict")
	if err != nil {
		t.Fatalf("doctor --strict failed on a disk warning: %v\n%s", err, out)
	}
	if !strings.Contains(out, "alpha 1 of 100 GB free (1%), critically low") || !strings.Contains(out, "Core checks passed with advisory warnings") {
		t.Fatalf("report lacks the disk warning or the advisory summary:\n%s", out)
	}
}
