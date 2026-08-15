package modellife

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

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

func TestPlanStartRequiresPortAndWeights(t *testing.T) {
	n := storageNode()
	if _, err := PlanStart(n, "/mnt/models/a.gguf", 0); err == nil {
		t.Fatal("expected error for missing port")
	}
	if _, err := PlanStart(n, "", 8081); err == nil {
		t.Fatal("expected error for missing weights")
	}
}

func TestPlanStartRequiresNamedLocalVolume(t *testing.T) {
	n := storageNode()
	if _, err := PlanStart(n, "/mnt/nas/a.gguf", 8081); err == nil || !strings.Contains(err.Error(), "named local volume") {
		t.Fatalf("network path must be refused: %v", err)
	}
	if _, err := PlanStart(n, "/not-a-volume/a.gguf", 8081); err == nil || !strings.Contains(err.Error(), "named local volume") {
		t.Fatalf("unknown path must be refused: %v", err)
	}
}

func TestPlanStartBuildsLlamaServerArgv(t *testing.T) {
	n := storageNode()
	p, err := PlanStart(n, "/mnt/models/a.gguf", 8081)
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
	if _, err := PlanStart(n, "/mnt/models/a.gguf", 8081); err == nil || !strings.Contains(err.Error(), "llama-server") {
		t.Fatalf("expected missing tool error, got %v", err)
	}
}
