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

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

var apiTurboTimePattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)

func TestKnowledgeEndpointTurboQuantGolden(t *testing.T) {
	restore := stubLiveRuntime(t, testRuntimeContext(
		[]models.NodeFacts{testTurboNode("node-a", "localhost", true)},
		[]config.NodeConfig{{Name: "node-a", Hostname: "localhost", SSHUser: "me"}},
		&state.ClusterState{Nodes: map[string]state.NodeState{}},
		&skills.Store{},
		nil, nil,
	), nil)
	defer restore()

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/knowledge", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, rec.Body.Bytes(), "", "  "); err != nil {
		t.Fatalf("indent response: %v", err)
	}

	assertAPITurboGoldenText(t,
		filepath.Join("testdata", "knowledge_turboquant.golden"),
		normalizeAPITurboOutput(pretty.String()),
	)
}

func normalizeAPITurboOutput(s string) string {
	s = apiTurboTimePattern.ReplaceAllString(s, "<TIME>")
	return strings.TrimSpace(s) + "\n"
}

func assertAPITurboGoldenText(t *testing.T, path string, actual string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "true" {
		_ = os.WriteFile(path, []byte(actual), 0644)
	}
	expectedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if actual != string(expectedBytes) {
		t.Fatalf("golden mismatch for %s\nEXPECTED:\n%s\nACTUAL:\n%s", path, string(expectedBytes), actual)
	}
}
