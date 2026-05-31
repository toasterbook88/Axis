package reservation

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func setupTestLedger(t *testing.T, limits Limits) *Ledger {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return NewLedger(limits, nil)
}

func TestNewLedger(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	if l == nil {
		t.Fatal("NewLedger returned nil")
	}
	if len(l.Entries()) != 0 {
		t.Error("new ledger should have no entries")
	}
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

func TestRelease(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)
	l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 4096})

	err := l.Release("exec-1")
	if err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	if len(l.Entries()) != 0 {
		t.Error("should have 0 entries after release")
	}
}

func TestRelease_Unknown(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	err := l.Release("nonexistent")
	if err == nil {
		t.Error("releasing unknown entry should fail")
	}
}

func TestHeartbeat(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	current := base
	l.now = func() time.Time { return current }
	l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 1024})

	current = current.Add(10 * time.Millisecond)
	err := l.Heartbeat("exec-1")
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}

	entries := l.Entries()
	if entries[0].LastHeartbeat.Before(entries[0].CreatedAt) {
		t.Error("heartbeat should be after created")
	}
}

func TestReclaim_StaleEntries(t *testing.T) {
	limits := DefaultLimits()
	limits.HeartbeatStaleWindow = 10 * time.Millisecond
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 16384)
	l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 1024})

	time.Sleep(20 * time.Millisecond)
	reclaimed := l.Reclaim()
	if reclaimed != 1 {
		t.Errorf("expected 1 reclaimed, got %d", reclaimed)
	}
	if len(l.Entries()) != 0 {
		t.Error("stale entry should be removed")
	}
}

func TestReclaim_ExpiredEntries(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)

	l.mu.Lock()
	l.entries["exec-1"] = &Entry{
		ID:            "exec-1",
		Node:          "node-a",
		RAMMB:         1024,
		CreatedAt:     time.Now(),
		LastHeartbeat: time.Now(),
		ExpiresAt:     time.Now().Add(-1 * time.Second), // already expired
	}
	l.mu.Unlock()

	reclaimed := l.Reclaim()
	if reclaimed != 1 {
		t.Errorf("expected 1 expired entry reclaimed, got %d", reclaimed)
	}
}

func TestAllocatableRAM(t *testing.T) {
	limits := DefaultLimits()
	limits.SystemReserveMB = 1024
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 16384) // 16GB

	// Before any reservations
	alloc := l.AllocatableRAM("node-a")
	if alloc != 15360 { // 16384 - 1024
		t.Errorf("expected 15360 allocatable, got %d", alloc)
	}

	// Reserve 4GB
	l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 4096})
	alloc = l.AllocatableRAM("node-a")
	if alloc != 11264 { // 15360 - 4096
		t.Errorf("expected 11264 allocatable, got %d", alloc)
	}
}

func TestAllocatableRAM_UnknownNode(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	alloc := l.AllocatableRAM("nonexistent")
	if alloc != 0 {
		t.Errorf("unknown node should return 0, got %d", alloc)
	}
}

func TestNodeSummary(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)
	l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 4096})
	l.Reserve(Entry{ID: "exec-2", Node: "node-a", RAMMB: 2048, VRAMMB: 1024})

	s := l.NodeSummaryFor("node-a")
	if s.ReservedRAMMB != 6144 {
		t.Errorf("expected 6144 reserved, got %d", s.ReservedRAMMB)
	}
	if s.ReservedVRAMMB != 1024 {
		t.Errorf("expected 1024 VRAM, got %d", s.ReservedVRAMMB)
	}
	if s.ActiveEntries != 2 {
		t.Errorf("expected 2 active, got %d", s.ActiveEntries)
	}
}

func TestClusterSummary(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)
	l.SetNodeCapacity("node-b", 32768)
	l.Reserve(Entry{ID: "e1", Node: "node-a", RAMMB: 4096})
	l.Reserve(Entry{ID: "e2", Node: "node-b", RAMMB: 8192})

	cs := l.Summary()
	if cs.TotalReservedMB != 12288 {
		t.Errorf("expected 12288 total reserved, got %d", cs.TotalReservedMB)
	}
	if cs.ActiveEntries != 2 {
		t.Errorf("expected 2 active, got %d", cs.ActiveEntries)
	}
	if len(cs.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(cs.Nodes))
	}
}

func TestMetrics(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)
	l.Reserve(Entry{ID: "e1", Node: "node-a", RAMMB: 4096})
	l.Release("e1")

	m := l.Metrics()
	if m.TotalReservedMB != 4096 {
		t.Errorf("expected 4096 total reserved, got %d", m.TotalReservedMB)
	}
	if m.TotalReleasedMB != 4096 {
		t.Errorf("expected 4096 total released, got %d", m.TotalReleasedMB)
	}
	if m.ActiveEntries != 0 {
		t.Errorf("expected 0 active, got %d", m.ActiveEntries)
	}
}

func TestEntry_IsStale(t *testing.T) {
	e := Entry{LastHeartbeat: time.Now().Add(-5 * time.Minute)}
	if !e.IsStale(time.Now(), 2*time.Minute) {
		t.Error("5min old heartbeat should be stale with 2min window")
	}
	if e.IsStale(time.Now(), 10*time.Minute) {
		t.Error("5min old heartbeat should not be stale with 10min window")
	}
}

func TestEntry_IsExpired(t *testing.T) {
	e := Entry{ExpiresAt: time.Now().Add(-1 * time.Second)}
	if !e.IsExpired(time.Now()) {
		t.Error("past expiry should be expired")
	}
	e.ExpiresAt = time.Time{} // zero value
	if e.IsExpired(time.Now()) {
		t.Error("zero expiry should not be expired")
	}
}

func TestLedgerLockTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	l := NewLedger(DefaultLimits(), nil)

	// Open the lock file on a separate file descriptor and acquire exclusive lock
	lockPath := Path() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("Flock failed: %v", err)
	}

	// Try to Load() - it should fail due to the lock timeout (500ms)
	start := time.Now()
	err = l.Load()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected Load to fail due to lock timeout")
	}

	if elapsed < 500*time.Millisecond {
		t.Errorf("expected timeout to take at least 500ms, took %v", elapsed)
	}

	// Release the lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("Flock unlock failed: %v", err)
	}

	// Now Load() should succeed
	if err := l.Load(); err != nil {
		t.Fatalf("Load failed after lock release: %v", err)
	}
}
