package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	apiCorruptStampPattern = regexp.MustCompile(`\.corrupt-\d{8}T\d{6}Z`)
	apiTimePattern         = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)
)

func TestKnowledgeEndpointCorruptPersistenceGolden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCorruptAPIAxisFile(t, home, "state.json", "{")
	writeCorruptAPIAxisFile(t, home, "skills.json", "{")

	restore := stubLiveRuntime(t, recoveredRuntimeContextFromDisk(t), nil)
	defer restore()

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/knowledge", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	assertAPIQuarantinedFile(t, home, "state.json")
	assertAPIQuarantinedFile(t, home, "skills.json")

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, rec.Body.Bytes(), "", "  "); err != nil {
		t.Fatalf("indent response: %v", err)
	}
	assertAPIGoldenText(t,
		filepath.Join("testdata", "knowledge_corrupt_persistence.golden"),
		normalizeAPIDegradedOutput(pretty.String(), home),
	)
}

func recoveredRuntimeContextFromDisk(t *testing.T) *runtimectx.Context {
	t.Helper()

	st, stateErr := state.Load()
	if stateErr != nil && st == nil {
		t.Fatalf("state.Load failed: %v", stateErr)
	}
	if st != nil {
		state.Maintain(st)
	}
	skillStore, skillErr := skills.Load()
	if skillErr != nil && skillStore == nil {
		t.Fatalf("skills.Load failed: %v", skillErr)
	}

	nodes := []models.NodeFacts{testNode("node-a", "localhost", 8192, 4096, "low", "git")}
	snap := &models.ClusterSnapshot{
		Status:  models.SnapshotHealthy,
		Nodes:   nodes,
		Summary: summarizeNodes(nodes),
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

func writeCorruptAPIAxisFile(t *testing.T, home string, name string, content string) {
	t.Helper()
	path := filepath.Join(home, ".axis", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir axis dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
}

func assertAPIQuarantinedFile(t *testing.T, home string, name string) {
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

func normalizeAPIDegradedOutput(s string, home string) string {
	s = strings.ReplaceAll(s, home, "$HOME")
	s = apiCorruptStampPattern.ReplaceAllString(s, ".corrupt-<STAMP>")
	s = apiTimePattern.ReplaceAllString(s, "<TIME>")
	return strings.TrimSpace(s) + "\n"
}

func assertAPIGoldenText(t *testing.T, path string, actual string) {
	t.Helper()
	expectedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if actual != string(expectedBytes) {
		t.Fatalf("golden mismatch for %s\nEXPECTED:\n%s\nACTUAL:\n%s", path, string(expectedBytes), actual)
	}
}
