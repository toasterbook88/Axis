package reservation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func setupTestLedger(t *testing.T, limits Limits) *Ledger {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return NewLedger(limits, nil)
}

func TestReserve_Success(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384) // 16GB

	entry, err := l.Reserve(Entry{
		ID:           "exec-1",
		Node:         "node-a",
		OwnerExecID:  "task-1",
		OwnerSurface: "guarded-exec",
		RAMMB:        4096,
	})
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}
	if entry.ID != "exec-1" {
		t.Errorf("expected exec-1, got %s", entry.ID)
	}
	if len(l.Entries()) != 1 {
		t.Errorf("expected 1 entry, got %d", len(l.Entries()))
	}
}

func TestReserve_UnknownCapacityRejected(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())

	_, err := l.Reserve(Entry{
		ID:           "exec-1",
		Node:         "node-a",
		OwnerExecID:  "task-1",
		OwnerSurface: "guarded-exec",
		RAMMB:        1024,
	})
	if err == nil {
		t.Fatal("expected reserve to fail when node capacity is unknown")
	}
	if got := len(l.Entries()); got != 0 {
		t.Fatalf("expected 0 entries after failed reserve, got %d", got)
	}
}

func TestReserve_DuplicateID(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)

	l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 1024})
	_, err := l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 1024})
	if err == nil {
		t.Error("duplicate ID should fail")
	}
}

func TestReserve_OvercommitRejection(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOvercommitRatio = 1.0
	limits.SystemReserveMB = 1024
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 8192) // 8GB total, 7GB allocatable

	// Reserve 6GB — OK
	_, err := l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 6144})
	if err != nil {
		t.Fatalf("first reserve should succeed: %v", err)
	}

	// Reserve 2GB more — should fail (6144+2048 = 8192 > 7168 allocatable)
	_, err = l.Reserve(Entry{ID: "exec-2", Node: "node-a", RAMMB: 2048})
	if err == nil {
		t.Error("should reject overcommit")
	}
}

func TestReserve_MaxEntries(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxEntriesPerNode = 2
	limits.MaxOvercommitRatio = 0 // unlimited
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 16384)

	l.Reserve(Entry{ID: "e1", Node: "node-a", RAMMB: 100})
	l.Reserve(Entry{ID: "e2", Node: "node-a", RAMMB: 100})
	_, err := l.Reserve(Entry{ID: "e3", Node: "node-a", RAMMB: 100})
	if err == nil {
		t.Error("should reject when MaxEntriesPerNode exceeded")
	}
}

func TestReclaimDoesNotEmitSuccessReceiptWhenPersistenceFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(Path(), 0o700); err != nil {
		t.Fatalf("create directory at ledger path: %v", err)
	}

	var logs bytes.Buffer
	l := NewLedger(DefaultLimits(), slog.New(slog.NewJSONHandler(&logs, nil)))
	l.mu.Lock()
	l.entries["exec-expired"] = &Entry{
		ID:            "exec-expired",
		Node:          "node-a",
		RAMMB:         512,
		CreatedAt:     time.Now().Add(-time.Hour),
		LastHeartbeat: time.Now(),
		ExpiresAt:     time.Now().Add(-time.Minute),
	}
	l.mu.Unlock()

	if reclaimed := l.Reclaim(); reclaimed != 1 {
		t.Fatalf("Reclaim() = %d, want 1 in-memory candidate", reclaimed)
	}
	if got := logs.String(); strings.Contains(got, `"msg":"maintenance receipt"`) {
		t.Fatalf("success receipt emitted despite persistence failure:\n%s", got)
	} else if !strings.Contains(got, "failed to persist ledger during reclaim") {
		t.Fatalf("persistence failure log missing:\n%s", got)
	}
}

func assertMaintenanceReceipt(t *testing.T, logs *bytes.Buffer, authority, objectType, objectID, oldValue, newValue string) {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode log record: %v", err)
		}
		if record["msg"] != "maintenance receipt" {
			continue
		}
		for key, want := range map[string]string{
			"source_authority": authority,
			"object_type":      objectType,
			"object_id":        objectID,
			"old_value":        oldValue,
			"new_value":        newValue,
		} {
			if record[key] != want {
				t.Fatalf("%s = %#v, want %q (record=%v)", key, record[key], want, record)
			}
		}
		return
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan logs: %v", err)
	}
	t.Fatalf("maintenance receipt not found in logs:\n%s", logs.String())
}

func TestReserve_PerNodeSystemReserve(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOvercommitRatio = 1.0
	limits.SystemReserveMB = 1024
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 8192) // 8GB total
	l.SetNodeReserve("node-a", 2048)  // per-node reserve: 2GB → 6GB allocatable

	// Reserve 5GB — OK (5GB < 6GB allocatable)
	_, err := l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 5120})
	if err != nil {
		t.Fatalf("first reserve should succeed: %v", err)
	}

	// Reserve 2GB more — should fail (5120+2048 = 7168 > 6144 allocatable)
	_, err = l.Reserve(Entry{ID: "exec-2", Node: "node-a", RAMMB: 2048})
	if err == nil {
		t.Error("should reject overcommit with per-node reserve")
	}
}

func TestReserve_PerNodeSystemReserveFallsBackToGlobal(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOvercommitRatio = 1.0
	limits.SystemReserveMB = 1024
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 8192) // 8GB total, no per-node reserve set → 7168 allocatable

	// Reserve exactly 7168 — OK (ratio == 1.0)
	_, err := l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 7168})
	if err != nil {
		t.Fatalf("reserve at exact limit should succeed: %v", err)
	}

	// Reserve 1MB more — should fail
	_, err = l.Reserve(Entry{ID: "exec-2", Node: "node-a", RAMMB: 1})
	if err == nil {
		t.Error("should reject overcommit past exact limit")
	}
}

func TestAllocatableRAM_PerNodeReserve(t *testing.T) {
	limits := DefaultLimits()
	limits.SystemReserveMB = 1024
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 16384) // 16GB
	l.SetNodeReserve("node-a", 4096)   // per-node reserve: 4GB → 12GB allocatable

	alloc := l.AllocatableRAM("node-a")
	if alloc != 12288 { // 16384 - 4096
		t.Errorf("expected 12288 allocatable with per-node reserve, got %d", alloc)
	}
}

func TestSetNodeReserve_ClearsOnZeroOrNegative(t *testing.T) {
	limits := DefaultLimits()
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 8192)
	l.SetNodeReserve("node-a", 2048)

	if l.systemReserveFor("node-a") != 2048 {
		t.Fatalf("expected per-node reserve 2048, got %d", l.systemReserveFor("node-a"))
	}

	l.SetNodeReserve("node-a", 0)
	if l.systemReserveFor("node-a") != limits.SystemReserveMB {
		t.Fatalf("expected fallback to global reserve %d, got %d", limits.SystemReserveMB, l.systemReserveFor("node-a"))
	}
}

func TestReserve_ParentReservationInheritance(t *testing.T) {
	limits := DefaultLimits()
	limits.SystemReserveMB = 0
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 8192)

	// Create parent reservation of 6000MB
	parentReq := Entry{
		ID:           "parent-exec-id",
		Node:         "node-a",
		RAMMB:        6000,
		OwnerExecID:  "parent-exec-id",
		OwnerSurface: "test",
	}
	parentEntry, err := l.Reserve(parentReq)
	if err != nil {
		t.Fatalf("failed to reserve parent: %v", err)
	}
	if parentEntry == nil {
		t.Fatal("expected parent entry to be created")
	}

	// Try to create another reservation of 3000MB on node-a, should fail
	childReq := Entry{
		ID:           "child-exec-id",
		Node:         "node-a",
		RAMMB:        3000,
		OwnerSurface: "test",
	}
	_, err = l.Reserve(childReq)
	if err == nil {
		t.Fatal("expected reservation of 3000MB to fail (6000 + 3000 > 8192)")
	}

	// Set AXIS_EXECUTION_PARENT_ID to parent-exec-id
	t.Setenv("AXIS_EXECUTION_PARENT_ID", "parent-exec-id")

	// Try again, should succeed because parent reservation (6000MB) is deducted
	childEntry, err := l.Reserve(childReq)
	if err != nil {
		t.Fatalf("expected child reservation to succeed, got error: %v", err)
	}
	if childEntry == nil {
		t.Fatal("expected child entry to be created")
	}

	// Clean env, delete reservations, try again with OwnerExecID match
	t.Setenv("AXIS_EXECUTION_PARENT_ID", "")
	err = l.Release("child-exec-id")
	if err != nil {
		t.Fatalf("release child failed: %v", err)
	}
	err = l.Release("parent-exec-id")
	if err != nil {
		t.Fatalf("release parent failed: %v", err)
	}

	// Re-add parent with ID = "some-id", OwnerExecID = "parent-exec-id"
	parentReq.ID = "some-id"
	parentReq.OwnerExecID = "parent-exec-id"
	_, err = l.Reserve(parentReq)
	if err != nil {
		t.Fatalf("failed to reserve parent: %v", err)
	}

	// Try child of 3000MB without parent env, should fail
	childReq.ID = "child-exec-id"
	_, err = l.Reserve(childReq)
	if err == nil {
		t.Fatal("expected reservation of 3000MB to fail")
	}

	// Set AXIS_EXECUTION_PARENT_ID to parent-exec-id (matches OwnerExecID)
	t.Setenv("AXIS_EXECUTION_PARENT_ID", "parent-exec-id")
	childEntry, err = l.Reserve(childReq)
	if err != nil {
		t.Fatalf("expected child reservation to succeed by matching OwnerExecID, got error: %v", err)
	}
	if childEntry == nil {
		t.Fatal("expected child entry to be created")
	}
}
