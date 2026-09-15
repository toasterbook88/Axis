package agent

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// The per-turn cluster context injected into the agent's system prompt must
// surface resident models — the live-warm signal that changes placement and
// model-routing answers. A node serving a model must be distinguishable from
// an idle one without any extra tool round-trip.
func TestSummarizeSnapshotIncludesResidentModels(t *testing.T) {
	node := models.NodeFacts{
		Name:     "cachyos",
		Hostname: "cachyos",
		Status:   models.StatusComplete,
		ResidentModels: []models.ResidentModel{
			{Name: "bitnet-2B", Runtime: "llama.cpp", Port: 8080, SizeRAMMB: 1132},
			{Name: "qwen3-14b", Runtime: "ollama", Port: 11434, SizeVRAMMB: 8192},
		},
	}
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{node}}

	out := summarizeSnapshot(snap)
	if !strings.Contains(out, "resident: bitnet-2B (1132MB RAM)") {
		t.Fatalf("summary missing resident model with RAM size:\n%s", out)
	}
	if !strings.Contains(out, "qwen3-14b (8192MB VRAM)") {
		t.Fatalf("summary missing resident model with VRAM size:\n%s", out)
	}
}

// Nodes without resident models must not gain a fabricated resident segment.
func TestSummarizeSnapshotOmitsResidentWhenEmpty(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{
		{Name: "idle", Hostname: "idle", Status: models.StatusComplete},
	}}
	out := summarizeSnapshot(snap)
	if strings.Contains(out, "resident:") {
		t.Fatalf("summary fabricated resident segment for empty node:\n%s", out)
	}
}
