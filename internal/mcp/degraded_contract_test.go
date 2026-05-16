package axismcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

var (
	mcpCorruptStampPattern = regexp.MustCompile(`\.corrupt-\d{8}T\d{6}Z`)
	mcpTimePattern         = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)
)

func TestPlacementDecisionCorruptPersistenceGolden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCorruptMCPAxisFile(t, home, "state.json", "{")
	writeCorruptMCPAxisFile(t, home, "skills.json", "{")

	restore := stubMCPRuntime(t, recoveredMCPRuntimeContextFromDisk(t), nil)
	defer restore()

	result, err := placementDecisionTool(context.Background(), toolRequest(map[string]any{
		"description": "analyze a git repo",
	}), false, "")
	if err != nil {
		t.Fatalf("placementDecisionTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success result, got error: %s", toolResultText(t, result))
	}

	assertMCPQuarantinedFile(t, home, "state.json")
	assertMCPQuarantinedFile(t, home, "skills.json")

	data, err := json.MarshalIndent(result.StructuredContent, "", "  ")
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	assertMCPGoldenText(t,
		filepath.Join("testdata", "placement_decision_corrupt_persistence.golden"),
		normalizeMCPDegradedOutput(string(data), home),
	)
}

func recoveredMCPRuntimeContextFromDisk(t *testing.T) *runtimectx.Context {
	t.Helper()

	st, stateErr := state.Load()
	if stateErr != nil && st == nil {
		t.Fatalf("state.Load failed: %v", stateErr)
	}
	skillStore, skillErr := skills.Load()
	if skillErr != nil && skillStore == nil {
		t.Fatalf("skills.Load failed: %v", skillErr)
	}

	nodes := []models.NodeFacts{mcpNode("node-a", "localhost", 8192, 4096, "low", "git")}
	snap := &models.ClusterSnapshot{
		Status:  models.SnapshotHealthy,
		Nodes:   nodes,
		Summary: models.ClusterSummary{TotalNodes: 1, ReachableNodes: 1, TotalRAMMB: 8192, TotalFreeRAMMB: 4096},
	}
	daemon.ApplyReservationView(snap, st, nil)
	if stateErr != nil {
		snap.Warnings = append(snap.Warnings, models.Warning{
			Kind:    "state",
			Message: stateErr.Error(),
		})
	}
	if skillErr != nil {
		snap.Warnings = append(snap.Warnings, models.Warning{
			Kind:    "skills",
			Message: skillErr.Error(),
		})
	}

	return &runtimectx.Context{
		Snapshot: snap,
		State:    st,
		Skills:   skillStore,
	}
}

func writeCorruptMCPAxisFile(t *testing.T, home string, name string, content string) {
	t.Helper()
	path := filepath.Join(home, ".axis", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir axis dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
}

func assertMCPQuarantinedFile(t *testing.T, home string, name string) {
	t.Helper()
	originalPath := filepath.Join(home, ".axis", name)
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Fatalf("expected original %s to be quarantined, stat err=%v", name, err)
	}
	matches, err := filepath.Glob(originalPath + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob quarantine files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one quarantine file for %s, got %v", name, matches)
	}
}

func normalizeMCPDegradedOutput(s string, home string) string {
	s = strings.ReplaceAll(s, home, "$HOME")
	s = mcpCorruptStampPattern.ReplaceAllString(s, ".corrupt-<STAMP>")
	s = mcpTimePattern.ReplaceAllString(s, "<TIME>")
	return strings.TrimSpace(s) + "\n"
}

func assertMCPGoldenText(t *testing.T, path string, actual string) {
	t.Helper()
	expectedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if actual != string(expectedBytes) {
		t.Fatalf("golden mismatch for %s\nEXPECTED:\n%s\nACTUAL:\n%s", path, string(expectedBytes), actual)
	}
}
