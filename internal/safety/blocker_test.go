package safety

import (
	"testing"

	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/models"
)

func TestCheckBlocksKnownBadCommandFirst(t *testing.T) {
	got := Check(nil, "echo hello", func(s string) bool { return s == "echo hello" })
	if !got.Blocked {
		t.Fatal("expected known-bad command to be blocked")
	}
	if got.Score != 92 {
		t.Fatalf("expected score 92, got %d", got.Score)
	}
}

func TestCheckAllowsExplicitSafePattern(t *testing.T) {
	got := Check(nil, "git status", nil)
	if got.Blocked {
		t.Fatalf("expected safe pattern to be allowed, got %+v", got)
	}
	if got.Score != 0 {
		t.Fatalf("expected zero score, got %d", got.Score)
	}
}

func TestCheckReturnsNonBlockingLowScorePattern(t *testing.T) {
	got := Check(nil, "printf hi > /dev/null", nil)
	if got.Blocked {
		t.Fatalf("expected low-score pattern to avoid instant block, got %+v", got)
	}
	if got.Score != 70 {
		t.Fatalf("expected score 70, got %d", got.Score)
	}
}

func TestCheckBlocksHeavyModelOnSmallCluster(t *testing.T) {
	k := &knowledge.ClusterKnowledge{
		Snapshot: models.ClusterSnapshot{
			Summary: models.ClusterSummary{
				TotalFreeRAMMB: 2048,
			},
		},
	}

	got := Check(k, "run large model inference", nil)
	if !got.Blocked {
		t.Fatal("expected heavy model to be blocked on small cluster")
	}
	if got.Score != 87 {
		t.Fatalf("expected score 87, got %d", got.Score)
	}
}

func TestCheckAllowsExplicitAppleFoundationModelsHelperOnSmallCluster(t *testing.T) {
	k := &knowledge.ClusterKnowledge{
		Snapshot: models.ClusterSnapshot{
			Summary: models.ClusterSummary{
				TotalFreeRAMMB: 2048,
			},
		},
	}

	got := Check(k, "xcrun swift hack/apple-foundation-models.swift --self-test", nil)
	if got.Blocked {
		t.Fatalf("expected explicit apple helper to bypass heavy-model heuristic, got %+v", got)
	}
}

func TestCheckBlocksGPURequestWhenNoGPUAvailable(t *testing.T) {
	k := &knowledge.ClusterKnowledge{
		Snapshot: models.ClusterSnapshot{
			Summary: models.ClusterSummary{
				TotalFreeRAMMB: 8192,
			},
			Nodes: []models.NodeFacts{
				{
					Name:      "cpu-only",
					Resources: &models.Resources{CPUCores: 8},
				},
			},
		},
	}

	got := Check(k, "run gpu workload", nil)
	if !got.Blocked {
		t.Fatal("expected gpu request to be blocked without gpu")
	}
	if got.Score != 75 {
		t.Fatalf("expected score 75, got %d", got.Score)
	}
}
