package reservation

import (
	"errors"
	"os"
	"testing"
	"time"
)

// Two ledgers on one ledger file stand in for two AXIS processes (the daemon
// and a CLI run, say), each of which loaded the file earlier. Every mutation
// must work from the file as it is under the lock, or one process admits over
// the ceiling and its later write erases the other's lease.

const sharedNode = "node-a"

func sharedLedgers(t *testing.T) (a, b *Ledger) {
	t.Helper()
	a = setupTestLedger(t, DefaultLimits())
	b = NewLedger(DefaultLimits(), nil)
	for _, l := range []*Ledger{a, b} {
		l.SetNodeCapacity(sharedNode, 16384)
		l.SetNodeReserve(sharedNode, 5968) // a 10416 MB ceiling
		if err := l.Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
	}
	return a, b
}

func lease(id string, mb int64) Entry {
	return Entry{ID: id, Node: sharedNode, RAMMB: mb, OwnerExecID: id, OwnerSurface: "test"}
}

func onDisk(t *testing.T) map[string]bool {
	t.Helper()
	fresh := NewLedger(DefaultLimits(), nil)
	if err := fresh.LoadReadOnly(); err != nil {
		t.Fatalf("LoadReadOnly: %v", err)
	}
	ids := map[string]bool{}
	for _, e := range fresh.Entries() {
		ids[e.ID] = true
	}
	return ids
}

func TestReserveSeesLeasesGrantedByAnotherLedger(t *testing.T) {
	a, b := sharedLedgers(t)
	if _, err := a.Reserve(lease("from-a", 6000)); err != nil {
		t.Fatalf("first lease: %v", err)
	}
	if _, err := b.Reserve(lease("from-b", 6000)); err == nil {
		t.Fatal("second 6000 MB lease granted over the 10416 MB ceiling: b admitted against its stale copy")
	}
	if got := onDisk(t); !got["from-a"] || got["from-b"] {
		t.Fatalf("ledger file holds %v, want only from-a", got)
	}
}

func TestReserveKeepsLeasesGrantedByAnotherLedger(t *testing.T) {
	a, b := sharedLedgers(t)
	if _, err := a.Reserve(lease("from-a", 3000)); err != nil {
		t.Fatalf("lease from a: %v", err)
	}
	if _, err := b.Reserve(lease("from-b", 3000)); err != nil {
		t.Fatalf("lease from b: %v", err)
	}
	if got := onDisk(t); !got["from-a"] || !got["from-b"] {
		t.Fatalf("ledger file holds %v, want both leases", got)
	}
}

func TestReleaseKeepsLeasesGrantedByAnotherLedger(t *testing.T) {
	a, b := sharedLedgers(t)
	if _, err := b.Reserve(lease("old", 1000)); err != nil {
		t.Fatalf("lease old: %v", err)
	}
	if err := a.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := b.Reserve(lease("new", 1000)); err != nil {
		t.Fatalf("lease new: %v", err)
	}
	if err := a.Release("old"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := onDisk(t); got["old"] || !got["new"] {
		t.Fatalf("ledger file holds %v, want only new", got)
	}
}

func TestHeartbeatKeepsLeasesGrantedByAnotherLedger(t *testing.T) {
	a, b := sharedLedgers(t)
	if _, err := b.Reserve(lease("old", 1000)); err != nil {
		t.Fatalf("lease old: %v", err)
	}
	if err := a.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := b.Reserve(lease("new", 1000)); err != nil {
		t.Fatalf("lease new: %v", err)
	}
	if err := a.Heartbeat("old"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if got := onDisk(t); !got["old"] || !got["new"] {
		t.Fatalf("ledger file holds %v, want both leases", got)
	}
}

// A lease released by another process must not come back when this process
// writes.
func TestReserveDoesNotRestoreLeasesReleasedByAnotherLedger(t *testing.T) {
	a, b := sharedLedgers(t)
	if _, err := b.Reserve(lease("gone", 1000)); err != nil {
		t.Fatalf("lease gone: %v", err)
	}
	if err := a.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := b.Release("gone"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := a.Reserve(lease("next", 1000)); err != nil {
		t.Fatalf("lease next: %v", err)
	}
	if got := onDisk(t); got["gone"] || !got["next"] {
		t.Fatalf("ledger file holds %v, want only next", got)
	}
}

func TestReclaimKeepsLeasesGrantedByAnotherLedger(t *testing.T) {
	a, b := sharedLedgers(t)
	expired := lease("expired", 1000)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := b.Reserve(expired); err != nil {
		t.Fatalf("lease expired: %v", err)
	}
	if err := a.LoadReadOnly(); err != nil {
		t.Fatalf("LoadReadOnly: %v", err)
	}
	if _, err := b.Reserve(lease("new", 1000)); err != nil {
		t.Fatalf("lease new: %v", err)
	}
	if got := a.Reclaim(); got != 1 {
		t.Fatalf("Reclaim() = %d, want the one expired lease", got)
	}
	if got := onDisk(t); got["expired"] || !got["new"] {
		t.Fatalf("ledger file holds %v, want only new", got)
	}
}

// A lease this ledger held but another process already removed (the daemon
// reclaimed it, say) is released without error, so a finished task does not
// report a failure. An ID this ledger never held is still an error.
func TestReleaseOfLeaseAlreadyRemovedElsewhereSucceeds(t *testing.T) {
	a, b := sharedLedgers(t)
	if _, err := a.Reserve(lease("mine", 1000)); err != nil {
		t.Fatalf("lease mine: %v", err)
	}
	if err := b.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := b.Release("mine"); err != nil {
		t.Fatalf("Release by b: %v", err)
	}
	if err := a.Release("mine"); err != nil {
		t.Fatalf("Release of a lease already removed elsewhere = %v, want nil", err)
	}
	if err := a.Release("never-held"); err == nil {
		t.Fatal("Release of an unknown ID succeeded")
	}
}

// A lease whose write failed must not be reported as granted: the next reload
// would forget it while its task runs, and admit another task in its place.
func TestReserveFailsAndRollsBackWhenTheWriteFails(t *testing.T) {
	a, _ := sharedLedgers(t)
	if _, err := a.Reserve(lease("kept", 1000)); err != nil {
		t.Fatalf("lease kept: %v", err)
	}
	prevWrite := writeLedgerFile
	writeLedgerFile = func(string, []byte) error { return errors.New("disk full") }
	t.Cleanup(func() { writeLedgerFile = prevWrite })

	if _, err := a.Reserve(lease("unsaved", 1000)); err == nil {
		t.Fatal("Reserve succeeded although its lease was not written")
	}
	for _, e := range a.Entries() {
		if e.ID == "unsaved" {
			t.Fatal("the unsaved lease is still in memory")
		}
	}
	writeLedgerFile = prevWrite
	if got := onDisk(t); !got["kept"] || got["unsaved"] {
		t.Fatalf("ledger file holds %v, want only kept", got)
	}
}

// A heartbeat in between reloads the ledger and drops the removed lease from
// memory; the ledger must still remember it granted that lease.
func TestReleaseAfterHeartbeatOfLeaseRemovedElsewhereSucceeds(t *testing.T) {
	a, b := sharedLedgers(t)
	if _, err := a.Reserve(lease("mine", 1000)); err != nil {
		t.Fatalf("lease mine: %v", err)
	}
	if err := b.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := b.Release("mine"); err != nil {
		t.Fatalf("Release by b: %v", err)
	}
	if err := a.Heartbeat("mine"); err == nil {
		t.Fatal("Heartbeat of a lease removed elsewhere succeeded; the task should learn its lease is gone")
	}
	if err := a.Release("mine"); err != nil {
		t.Fatalf("Release after the heartbeat = %v, want nil", err)
	}
	if err := a.Release("never-held"); err == nil {
		t.Fatal("Release of an unknown ID succeeded")
	}
}

// A ledger file with a null entry decodes, so every reader must reject it
// before touching memory: a nil dereference under l.mu would leave the mutex
// locked or crash the daemon.
func TestNullEntryInLedgerFileFailsClosed(t *testing.T) {
	a, _ := sharedLedgers(t)
	if _, err := a.Reserve(lease("kept", 1000)); err != nil {
		t.Fatalf("lease kept: %v", err)
	}
	if err := os.WriteFile(Path(), []byte(`{"entries":[null]}`), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	if _, err := a.Reserve(lease("next", 1000)); err == nil {
		t.Error("Reserve accepted a ledger with a null entry")
	}
	if err := a.Heartbeat("kept"); err == nil {
		t.Error("Heartbeat accepted a ledger with a null entry")
	}
	if got := a.Reclaim(); got != 0 {
		t.Errorf("Reclaim() = %d on a ledger with a null entry, want 0", got)
	}
	if err := a.Load(); err == nil {
		t.Error("Load accepted a ledger with a null entry")
	}
	if err := a.LoadReadOnly(); err == nil {
		t.Error("LoadReadOnly accepted a ledger with a null entry")
	}

	done := make(chan []Entry, 1)
	go func() { done <- a.Entries() }()
	select {
	case entries := <-done:
		if len(entries) != 1 || entries[0].ID != "kept" {
			t.Fatalf("in-memory entries = %v, want the earlier lease unchanged", entries)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the ledger mutex was left locked")
	}
}

// The granted set must not grow with lifetime traffic: an ID missing from the
// file for longer than grantedRetention is forgotten.
func TestGrantedIDsRetireAfterAnHourMissing(t *testing.T) {
	a, b := sharedLedgers(t)
	now := time.Now()
	a.now = func() time.Time { return now }
	if _, err := a.Reserve(lease("old", 1000)); err != nil {
		t.Fatalf("lease old: %v", err)
	}
	if err := b.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := b.Release("old"); err != nil {
		t.Fatalf("Release by b: %v", err)
	}
	if _, err := a.Reserve(lease("seen-missing", 1000)); err != nil {
		t.Fatalf("lease seen-missing: %v", err)
	}
	if _, ok := a.granted["old"]; !ok {
		t.Fatalf("granted = %v, want old kept within the retention window", a.granted)
	}
	now = now.Add(grantedRetention + time.Minute)
	if _, err := a.Reserve(lease("later", 1000)); err != nil {
		t.Fatalf("lease later: %v", err)
	}
	if _, ok := a.granted["old"]; ok {
		t.Fatalf("granted still holds old after %s missing: %v", grantedRetention, a.granted)
	}
	if _, ok := a.granted["seen-missing"]; !ok {
		t.Fatalf("granted dropped a lease still in the file: %v", a.granted)
	}
}
