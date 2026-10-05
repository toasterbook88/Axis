package modellife

import (
	"path"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// planStartDefault is the no-flag llama-server profile. Production start
// builds that profile from flags and calls PlanStartProfile.
func planStartDefault(node models.NodeFacts, weights string, port int) (StartPlan, error) {
	weights = path.Clean(strings.TrimSpace(weights))
	return PlanStartProfile(node, models.ModelRunProfile{
		Schema:       models.ModelRunSchema,
		Node:         node.Name,
		Engine:       models.EngineLlamaCpp,
		ToolName:     models.ToolLlamaServer,
		ArtifactKind: models.ArtifactWeightsPath,
		WeightsPath:  weights,
		BindHost:     "127.0.0.1",
		Port:         port,
		PortSource:   models.PortSourceExplicit,
	})
}

func storageNode() models.NodeFacts {
	return models.NodeFacts{
		Name: "storage",
		Tools: []models.ToolInfo{
			{Name: "llama-server", Path: "/usr/local/bin/llama-server"},
		},
		Resources: &models.Resources{
			Volumes: []models.Volume{
				{Device: "/dev/nvme0n1p1", Mount: "/", Kind: "local", Role: "root", Bus: "nvme", Class: "nvme"},
				{Device: "/dev/sda2", Mount: "/mnt/models", Kind: "local", Role: "other", Bus: "usb", Class: "ssd", Removable: true, LinkMbit: 5000},
				{Device: "//nas@STORAGE._smb._tcp.local/share", Mount: "/mnt/nas", Kind: "network", Bus: "cifs"},
			},
		},
	}
}

func TestPlanStartRequiresValidPortAndWeights(t *testing.T) {
	n := storageNode()
	for _, port := range []int{-1, 0, 65536} {
		if _, err := planStartDefault(n, "/mnt/models/a.gguf", port); err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
			t.Fatalf("port %d error = %v, want valid range error", port, err)
		}
	}
	if _, err := planStartDefault(n, "", 8081); err == nil {
		t.Fatal("expected error for missing weights")
	}
}

func TestPlanStartRequiresNamedLocalVolume(t *testing.T) {
	n := storageNode()
	if _, err := planStartDefault(n, "/mnt/nas/a.gguf", 8081); err == nil || !strings.Contains(err.Error(), "named local volume") {
		t.Fatalf("network path must be refused: %v", err)
	}
	if _, err := planStartDefault(n, "/not-a-volume/a.gguf", 8081); err == nil || !strings.Contains(err.Error(), "named local volume") {
		t.Fatalf("unknown path must be refused: %v", err)
	}
}

func TestPlanStartBuildsLlamaServerArgv(t *testing.T) {
	n := storageNode()
	p, err := planStartDefault(n, "/mnt/models/a.gguf", 8081)
	if err != nil {
		t.Fatal(err)
	}
	if p.Node != "storage" || p.Port != 8081 || p.Volume != "/mnt/models" {
		t.Fatalf("plan=%+v", p)
	}
	got := strings.Join(p.Argv, " ")
	if !strings.Contains(got, "llama-server") || !strings.Contains(got, "-m /mnt/models/a.gguf") {
		t.Fatalf("argv=%v", p.Argv)
	}
	if !strings.Contains(got, "--port 8081") || !strings.Contains(got, "--host 127.0.0.1") {
		t.Fatalf("argv=%v", p.Argv)
	}
}

func TestPlanStartRequiresLlamaServerTool(t *testing.T) {
	n := storageNode()
	n.Tools = nil
	if _, err := planStartDefault(n, "/mnt/models/a.gguf", 8081); err == nil || !strings.Contains(err.Error(), "llama-server") {
		t.Fatalf("expected missing tool error, got %v", err)
	}
}
