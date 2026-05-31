package axismcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/models"
)

func TestTriangleTools(t *testing.T) {
	// Isolate user's actual ~/.axis/ledger.json
	t.Setenv("HOME", t.TempDir())

	// Stub currentSnapshot to return a snapshot with the target node
	restoreFetch := stubCachedSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				{
					Name: "test-node",
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  6144,
					},
				},
			},
		}, "cache", nil
	})
	defer restoreFetch()

	s := NewServer(true, "http://localhost:42425")
	if s == nil {
		t.Fatal("expected non-nil server")
	}

	// 1. Test triangle_request_lease
	reqJson := `{"node":"test-node","owner_exec_id":"session-1","owner_surface":"Grok","ram_mb":1024,"duration_seconds":60,"description":"test lease"}`
	var args map[string]any
	if err := json.Unmarshal([]byte(reqJson), &args); err != nil {
		t.Fatalf("failed to parse args: %v", err)
	}

	req := mcpproto.CallToolRequest{}
	req.Params.Name = "triangle_request_lease"
	req.Params.Arguments = args

	result, err := triangleRequestLeaseTool(context.Background(), req, true, "http://localhost:42425")
	if err != nil {
		t.Fatalf("triangleRequestLeaseTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %v", result.StructuredContent)
	}

	// Parse reserved entry
	var reserved map[string]any
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}
	if err := json.Unmarshal(data, &reserved); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	leaseID, ok := reserved["id"].(string)
	if !ok || leaseID == "" {
		t.Fatal("expected non-empty lease ID")
	}
	if reserved["node"] != "test-node" {
		t.Errorf("expected test-node, got %v", reserved["node"])
	}

	// 2. Test triangle_heartbeat_lease
	heartbeatArgs := map[string]any{"id": leaseID}
	heartbeatReq := mcpproto.CallToolRequest{}
	heartbeatReq.Params.Name = "triangle_heartbeat_lease"
	heartbeatReq.Params.Arguments = heartbeatArgs

	hbResult, err := triangleHeartbeatLeaseTool(context.Background(), heartbeatReq)
	if err != nil {
		t.Fatalf("triangleHeartbeatLeaseTool: %v", err)
	}
	if hbResult.IsError {
		t.Fatalf("expected heartbeat success, got error result")
	}

	// 3. Test triangle_release_lease
	releaseArgs := map[string]any{"id": leaseID}
	releaseReq := mcpproto.CallToolRequest{}
	releaseReq.Params.Name = "triangle_release_lease"
	releaseReq.Params.Arguments = releaseArgs

	relResult, err := triangleReleaseLeaseTool(context.Background(), releaseReq)
	if err != nil {
		t.Fatalf("triangleReleaseLeaseTool: %v", err)
	}
	if relResult.IsError {
		t.Fatalf("expected release success, got error result")
	}
}

func TestTriangleTools_DefaultDuration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	restoreFetch := stubCachedSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				{
					Name: "test-node",
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  6144,
					},
				},
			},
		}, "cache", nil
	})
	defer restoreFetch()

	args := map[string]any{
		"node":             "test-node",
		"owner_exec_id":    "session-1",
		"owner_surface":    "Grok",
		"ram_mb":           1024,
		"duration_seconds": 0, // should fallback to default 120
	}
	req := mcpproto.CallToolRequest{}
	req.Params.Name = "triangle_request_lease"
	req.Params.Arguments = args

	res, err := triangleRequestLeaseTool(context.Background(), req, true, "http://localhost:42425")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success with default duration, got error: %v", res.StructuredContent)
	}

	var reserved map[string]any
	data, _ := json.Marshal(res.StructuredContent)
	json.Unmarshal(data, &reserved)

	expiresAtStr, ok := reserved["expires_at"].(string)
	if !ok || expiresAtStr == "" {
		t.Fatal("expected expires_at to be present")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtStr)
	if err != nil {
		expiresAt, err = time.Parse(time.RFC3339, expiresAtStr)
		if err != nil {
			t.Fatalf("failed to parse expires_at: %v", err)
		}
	}

	diff := time.Until(expiresAt)
	if diff < 110*time.Second || diff > 130*time.Second {
		t.Errorf("expected expires_at to be ~120s in the future, got diff: %v", diff)
	}
}

func TestTriangleTools_StaleSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Stub currentSnapshot to return a stale snapshot
	restoreFetch := stubCachedSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Timestamp: time.Now().Add(-1 * time.Minute), // stale
			Nodes: []models.NodeFacts{
				{
					Name: "test-node",
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  6144,
					},
				},
			},
		}, "cache", nil
	})
	defer restoreFetch()

	args := map[string]any{
		"node":             "test-node",
		"owner_exec_id":    "session-1",
		"owner_surface":    "Grok",
		"ram_mb":           1024,
		"duration_seconds": 30,
	}
	req := mcpproto.CallToolRequest{}
	req.Params.Name = "triangle_request_lease"
	req.Params.Arguments = args

	res, err := triangleRequestLeaseTool(context.Background(), req, true, "http://localhost:42425")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error due to stale snapshot")
	}
}

func TestTriangleTools_Locking(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	restoreFetch := stubCachedSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				{
					Name: "test-node",
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  6144,
					},
				},
			},
		}, "cache", nil
	})
	defer restoreFetch()

	// Pre-acquire the lock
	homeDir, _ := os.UserHomeDir()
	lockPath := filepath.Join(homeDir, ".axis", "ledger.json.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("Flock: %v", err)
	}

	args := map[string]any{
		"node":             "test-node",
		"owner_exec_id":    "session-1",
		"owner_surface":    "Grok",
		"ram_mb":           1024,
		"duration_seconds": 30,
	}
	req := mcpproto.CallToolRequest{}
	req.Params.Name = "triangle_request_lease"
	req.Params.Arguments = args

	// Call tool, it should fail with lock timeout
	res, err := triangleRequestLeaseTool(context.Background(), req, true, "http://localhost:42425")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error due to lock timeout")
	}
}
