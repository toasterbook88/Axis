package modelinventory

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

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

func TestFromSnapshotDerivesGenerationIDOnlyFromCompleteProcessEvidence(t *testing.T) {
	build := func(hostname string, pid int, startToken string) models.ModelInstance {
		inventory := FromSnapshot(&models.ClusterSnapshot{Nodes: []models.NodeFacts{{
			Name:     "logical-node",
			Hostname: hostname,
			Identity: models.NewNodeIdentity("hardware-id", "machine-id"),
			ResidentModels: []models.ResidentModel{{
				Name: "model-a", Runtime: "llama.cpp", Port: 8080,
				PID: pid, Executable: "/opt/llama-server", ProcessOwner: "axis-user", ProcessStartToken: startToken,
			}},
		}}}, "daemon-cache")
		return inventory.Instances[0]
	}

	first := build("old-hostname", 4242, "Thu Sep 3 09:00:00 2026")
	if !strings.HasPrefix(first.GenerationID, "mg-") {
		t.Fatalf("generation id = %q, want mg-*", first.GenerationID)
	}
	if first.PID != 4242 || first.Executable != "/opt/llama-server" || first.ProcessOwner != "axis-user" {
		t.Fatalf("process evidence not preserved: %+v", first)
	}
	if same := build("new-hostname", 4242, "Thu Sep 3 09:00:00 2026"); same.GenerationID != first.GenerationID {
		t.Fatalf("hostname drift changed generation id: %q != %q", same.GenerationID, first.GenerationID)
	}
	if changed := build("new-hostname", 4243, "Thu Sep 3 09:00:00 2026"); changed.GenerationID == first.GenerationID {
		t.Fatalf("PID change did not change generation id: %q", first.GenerationID)
	}
	if changed := build("new-hostname", 4242, "Thu Sep 3 09:01:00 2026"); changed.GenerationID == first.GenerationID {
		t.Fatalf("start-token change did not change generation id: %q", first.GenerationID)
	}
	if incomplete := build("new-hostname", 4242, ""); incomplete.GenerationID != "" {
		t.Fatalf("incomplete evidence invented generation id %q", incomplete.GenerationID)
	}
}
