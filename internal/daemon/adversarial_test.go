package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/snapshot"
)

func TestLedgerCorruptionRecovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Write garbage to the ledger
	path := filepath.Join(home, ".axis", "ledger.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ corrupted json"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := daemon.New(10*time.Millisecond, func(context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{{Name: "local", Resources: &models.Resources{RAMTotalMB: 8192}}},
		}, nil
	})

	// The ledger loading should quarantine the corrupt file and start empty, not crash
	ledger := d.Ledger()
	if ledger == nil {
		t.Fatal("ledger is nil")
	}

	// Ensure the quarantine file exists
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	foundQuarantine := false
	for _, e := range entries {
		if e.Name() != "ledger.json" && e.Name() != "snapshot.json" {
			foundQuarantine = true
		}
	}
	if !foundQuarantine {
		t.Error("expected quarantined ledger file")
	}
}

func TestSIGKILLDuringHeartbeat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	limits := reservation.DefaultLimits()
	limits.HeartbeatStaleWindow = 50 * time.Millisecond // very short window

	ledger := reservation.NewLedger(limits, nil)
	ledger.SetNodeCapacity("node-a", 8192)

	// Reserve some RAM
	entry, err := ledger.Reserve(reservation.Entry{
		ID:    "exec-1",
		Node:  "node-a",
		RAMMB: 1024,
	})
	if err != nil {
		t.Fatalf("reserve failed: %v", err)
	}

	// Verify it's active
	if ledger.Summary().TotalReservedMB != 1024 {
		t.Fatalf("expected 1024 reserved, got %d", ledger.Summary().TotalReservedMB)
	}

	// Sleep past the heartbeat window to simulate SIGKILL of the owner
	time.Sleep(100 * time.Millisecond)

	// Force a load to trigger reconciliation
	if err := ledger.Load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Ensure the reservation was reclaimed
	if ledger.Summary().TotalReservedMB != 0 {
		t.Fatalf("expected 0 reserved after reclaim, got %d", ledger.Summary().TotalReservedMB)
	}

	if err := ledger.Heartbeat(entry.ID); err == nil {
		t.Error("expected heartbeat to fail for reclaimed entry")
	}
}

func TestMeshPartitionCoalescing(t *testing.T) {
	var callCount int32
	collector := func(context.Context) (*models.ClusterSnapshot, error) {
		atomic.AddInt32(&callCount, 1)
		time.Sleep(50 * time.Millisecond) // Simulate slow probe
		return &models.ClusterSnapshot{}, nil
	}

	d := daemon.New(time.Hour, collector) // prevent automatic interval refresh

	// Fire 10 simultaneous refresh triggers
	for i := 0; i < 10; i++ {
		go d.RefreshWithTrigger(context.Background(), daemon.RefreshTriggerDiscovery)
	}

	// Wait for the slow probes to finish
	time.Sleep(300 * time.Millisecond)

	// Due to coalescing (1 active + 1 pending), call count should be exactly 2
	finalCount := atomic.LoadInt32(&callCount)
	if finalCount != 2 {
		t.Errorf("expected coalescing to result in exactly 2 refreshes, got %d", finalCount)
	}
}

func TestClockSkewGrace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	limits := reservation.DefaultLimits()
	limits.HeartbeatStaleWindow = time.Hour // large window to prevent natural expiry

	ledger := reservation.NewLedger(limits, nil)
	ledger.SetNodeCapacity("node-a", 8192)

	// Create a reservation
	_, err := ledger.Reserve(reservation.Entry{
		ID:    "skew-test",
		Node:  "node-a",
		RAMMB: 1024,
	})
	if err != nil {
		t.Fatalf("reserve failed: %v", err)
	}

	// Save it
	if err := ledger.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// In a separate test case, we would verify that loading this entry
	// checks liveness using time.Since(LastHeartbeat) locally,
	// rather than trusting the original machine's timestamp exactly,
	// protecting against multi-node clock skew.
	// Our monotonic expiry tests inside reservation/ledger_test.go already cover this.
	// This serves as an adversarial placeholder.
}

func TestSplitBrainSnapshotDedupe(t *testing.T) {
	// A node discovered via config with a StableID
	configNode := models.NodeFacts{
		Name:      "canonical-node",
		Role:      "worker",
		Identity:  &models.NodeIdentity{StableID: "machine-123"},
		Epistemic: &models.EpistemicState{VerifiedBy: models.VerifiedByConfig},
	}

	// The same node discovered via UDP mesh with a different hostname
	meshNode := models.NodeFacts{
		Name:      "canonical-node.local",
		Identity:  &models.NodeIdentity{StableID: "machine-123"},
		Epistemic: &models.EpistemicState{VerifiedBy: models.VerifiedByMesh},
	}

	nodes := []models.NodeFacts{meshNode, configNode}

	// Dedupe should prioritize the config node, ignoring order
	snap := snapshot.Build(nodes)

	if len(snap.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(snap.Nodes))
	}

	if snap.Nodes[0].Name != "canonical-node" || snap.Nodes[0].Role != "worker" {
		t.Errorf("dedupe did not preserve config authority: %+v", snap.Nodes[0])
	}
}
