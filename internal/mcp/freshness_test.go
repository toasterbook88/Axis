package axismcp

import (
	"context"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestClusterSnapshotToolReturnsFreshnessFromLiveRuntime(t *testing.T) {
	restore := stubMCPRuntime(t, &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Freshness: &models.DiscoveryFreshness{
				Source:           "udp-window",
				ExpectedWindowMS: 2250,
				ObservedWindowMS: 2250,
				CompletedWindow:  true,
			},
		},
	}, nil)
	defer restore()

	result, err := clusterSnapshotTool(context.Background(), toolRequest(nil), NewSessionCache(30*time.Second, false, ""))
	if err != nil {
		t.Fatalf("clusterSnapshotTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error result: %s", toolResultText(t, result))
	}
	snap, ok := result.StructuredContent.(*models.ClusterSnapshot)
	if !ok {
		t.Fatalf("expected cluster snapshot structured content, got %#v", result.StructuredContent)
	}
	if snap.Freshness == nil || snap.Freshness.Source != "udp-window" {
		t.Fatalf("expected freshness in MCP snapshot output, got %+v", snap.Freshness)
	}
}
