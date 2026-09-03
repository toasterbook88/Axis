package modelinventory

import (
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestFromSnapshotBuildsDeterministicResidentInstanceInventory(t *testing.T) {
	observed := time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC)
	snap := &models.ClusterSnapshot{
		Timestamp:   observed,
		Publication: &models.PublicationEnvelope{ID: "pub-test-1"},
		Nodes: []models.NodeFacts{
			{
				Name:        "node-b",
				Hostname:    "transient-hostname",
				Identity:    models.NewNodeIdentity("STABLE-B", "machine-id"),
				Status:      models.StatusComplete,
				CollectedAt: observed.Add(-time.Second),
				ResidentModels: []models.ResidentModel{
					{Name: "zeta", Runtime: "ollama", Processor: "gpu", Port: 11434, SizeVRAMMB: 4096},
					{Name: "alpha", Runtime: "llama.cpp", Processor: "cpu+gpu", Source: "process", Port: 8080, WeightSizeMB: 5120},
				},
			},
			{
				Name:        "node-a",
				Status:      models.StatusComplete,
				CollectedAt: observed.Add(-2 * time.Second),
				ResidentModels: []models.ResidentModel{
					{Name: "beta", Runtime: "mlx", Port: 8090, SizeRAMMB: 3072},
					{Name: "beta", Runtime: "mlx", Port: 8090, SizeRAMMB: 3072},
				},
			},
		},
	}

	got := FromSnapshot(snap, "daemon-cache")
	if got.Source != "daemon-cache" || got.PublicationID != "pub-test-1" || !got.ObservedAt.Equal(observed) {
		t.Fatalf("inventory authority = source %q publication %q observed %s", got.Source, got.PublicationID, got.ObservedAt)
	}
	if len(got.Instances) != 3 {
		t.Fatalf("instances = %#v, want 3 unique rows", got.Instances)
	}
	wantOrder := []string{"node-a/beta", "node-b/alpha", "node-b/zeta"}
	for i, instance := range got.Instances {
		if key := instance.Node + "/" + instance.Model; key != wantOrder[i] {
			t.Fatalf("instance[%d] = %q, want %q", i, key, wantOrder[i])
		}
		if instance.ID == "" || !strings.HasPrefix(instance.ID, "mi-") {
			t.Fatalf("instance[%d] id = %q", i, instance.ID)
		}
		if instance.State != models.ModelInstanceResident {
			t.Fatalf("instance[%d] state = %q", i, instance.State)
		}
		if instance.NodeStatus != models.StatusComplete {
			t.Fatalf("instance[%d] node status = %q", i, instance.NodeStatus)
		}
	}
	if got.Instances[1].Source != "process" || got.Instances[2].SizeVRAMMB != 4096 {
		t.Fatalf("resident facts not preserved: %#v", got.Instances)
	}
	if got.Instances[0].SizeRAMMB != 3072 || got.Instances[0].SizeVRAMMB != 0 {
		t.Fatalf("MLX memory facts not preserved honestly: %#v", got.Instances[0])
	}
	if got.Instances[1].WeightSizeMB != 5120 || got.Instances[1].SizeVRAMMB != 0 {
		t.Fatalf("llama.cpp memory facts not preserved honestly: %#v", got.Instances[1])
	}
	if !got.Instances[0].ObservedAt.Equal(observed.Add(-2 * time.Second)) {
		t.Fatalf("node observation time not preserved: %#v", got.Instances[0])
	}
}

func TestFromSnapshotInstanceIDUsesStableNodeIdentityAndObservedSlot(t *testing.T) {
	instanceID := func(hostname, model string, port int) string {
		inventory := FromSnapshot(&models.ClusterSnapshot{Nodes: []models.NodeFacts{{
			Name:     "logical-node",
			Hostname: hostname,
			Identity: models.NewNodeIdentity("hardware-id", "machine-id"),
			ResidentModels: []models.ResidentModel{{
				Name: model, Runtime: "llama.cpp", Port: port,
			}},
		}}}, "live")
		return inventory.Instances[0].ID
	}

	first := instanceID("old-hostname", "model-a", 8080)
	if second := instanceID("new-hostname", "model-a", 8080); first != second {
		t.Fatalf("hostname drift changed stable instance id: %q != %q", first, second)
	}
	if changed := instanceID("new-hostname", "model-a", 8081); first == changed {
		t.Fatalf("port change did not change observed slot id: %q", first)
	}
	if changed := instanceID("new-hostname", "model-b", 8080); first == changed {
		t.Fatalf("model change did not change observed slot id: %q", first)
	}
}

func TestFromSnapshotUsesEmptyArrayForNoInstances(t *testing.T) {
	got := FromSnapshot(&models.ClusterSnapshot{}, "live")
	if got.Instances == nil || len(got.Instances) != 0 {
		t.Fatalf("instances = %#v, want non-nil empty array", got.Instances)
	}
}
