package safety

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestCheckDecoupledFromKnowledge(t *testing.T) {
	tiny := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalAllocatableMB: 1024},
		Nodes: []models.NodeFacts{{
			Name:      "cpu-only",
			Resources: &models.Resources{CPUCores: 4},
		}},
	}

	if result := Check(tiny, "run a large model", nil); !result.Blocked {
		t.Fatalf("expected large model request to be blocked, got %+v", result)
	}
	if result := Check(tiny, "run gpu workload", nil); !result.Blocked {
		t.Fatalf("expected GPU request to be blocked, got %+v", result)
	}
	if result := Check(tiny, "echo hello", nil); result.Blocked {
		t.Fatalf("expected benign request to be allowed, got %+v", result)
	}
}
