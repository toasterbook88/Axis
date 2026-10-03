package reservation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func intPtr(v int) *int { return &v }

func futureHold(id string, gpu int) DeviceHold {
	return DeviceHold{
		ID:        id,
		Node:      "node-a",
		GPUIndex:  intPtr(gpu),
		MiB:       512,
		Owner:     "operator",
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

func writeLedgerFixture(t *testing.T, df diskFormat) []byte {
	t.Helper()
	data, err := json.MarshalIndent(df, "", "  ")
	if err != nil {
		t.Fatalf("marshal ledger fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatalf("create ledger directory: %v", err)
	}
	if err := os.WriteFile(Path(), data, 0o600); err != nil {
		t.Fatalf("write ledger fixture: %v", err)
	}
	return data
}

func TestHoldDeviceRefusesIncomplete(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	expiry := time.Now().Add(time.Hour)
	base := DeviceHold{ID: "gpu-0", Node: "node-a", GPUIndex: intPtr(1), MiB: 256, Owner: "operator", ExpiresAt: expiry}
	cases := []struct {
		name string
		edit func(*DeviceHold)
		want string
	}{
		{name: "nil index", edit: func(h *DeviceHold) { h.GPUIndex = nil }, want: "GPU index"},
		{name: "zero mib", edit: func(h *DeviceHold) { h.MiB = 0 }, want: "MiB"},
		{name: "negative mib", edit: func(h *DeviceHold) { h.MiB = -5 }, want: "MiB"},
		{name: "empty id", edit: func(h *DeviceHold) { h.ID = "" }, want: "ID"},
		{name: "blank id", edit: func(h *DeviceHold) { h.ID = "  " }, want: "ID"},
		{name: "empty node", edit: func(h *DeviceHold) { h.Node = "" }, want: "node"},
		{name: "blank node", edit: func(h *DeviceHold) { h.Node = " " }, want: "node"},
		{name: "empty owner", edit: func(h *DeviceHold) { h.Owner = "" }, want: "owner"},
		{name: "blank owner", edit: func(h *DeviceHold) { h.Owner = "\t" }, want: "owner"},
		{name: "zero expiry", edit: func(h *DeviceHold) { h.ExpiresAt = time.Time{} }, want: "expiry"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			hold := base
			tt.edit(&hold)
			if _, err := l.HoldDevice(hold); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("HoldDevice error = %v, want substring %q", err, tt.want)
			}
		})
	}
	if got := len(l.DeviceHolds()); got != 0 {
		t.Fatalf("refusals stored %d holds", got)
	}
}

func TestHoldDeviceIndexZeroRoundTrips(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	gpu := 0
	hold := futureHold("gpu-0", 1)
	hold.GPUIndex = &gpu
	got, err := l.HoldDevice(hold)
	if err != nil {
		t.Fatal(err)
	}
	gpu = 4
	*got.GPUIndex = 9
	holds := l.DeviceHolds()
	if len(holds) != 1 || holds[0].GPUIndex == nil || *holds[0].GPUIndex != 0 {
		t.Fatalf("in-memory hold = %+v, want GPU index 0", holds)
	}

	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"gpu_index": 0`) {
		t.Fatalf("ledger JSON omitted GPU index 0:\n%s", raw)
	}

	reloaded := NewLedger(DefaultLimits(), nil)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	again := reloaded.DeviceHolds()
	if len(again) != 1 || again[0].GPUIndex == nil || *again[0].GPUIndex != 0 {
		t.Fatalf("reloaded hold = %+v, want GPU index 0", again)
	}
}

func TestHoldDeviceRejectsDuplicateEntryOrHold(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)
	if _, err := l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.HoldDevice(futureHold("exec-1", 0)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("hold matching an entry: %v", err)
	}
	if _, err := l.HoldDevice(futureHold("gpu-0", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.HoldDevice(futureHold("gpu-0", 1)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate hold: %v", err)
	}
	if _, err := l.Reserve(Entry{ID: "gpu-0", Node: "node-a", RAMMB: 128}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("entry matching a hold: %v", err)
	}
}

func TestLoadOldEntriesFileHasNoHolds(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	now := time.Now().UTC()
	writeLedgerFixture(t, diskFormat{Entries: []*Entry{{
		ID:            "exec-1",
		Node:          "node-a",
		RAMMB:         128,
		CreatedAt:     now,
		LastHeartbeat: now,
	}}})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if len(l.DeviceHolds()) != 0 {
		t.Fatalf("old entries file invented holds: %+v", l.DeviceHolds())
	}
	entries := l.Entries()
	if len(entries) != 1 || entries[0].ID != "exec-1" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestLoadDropsExpiredHoldWithoutReclaimingEntries(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	now := time.Now().UTC()
	writeLedgerFixture(t, diskFormat{
		Entries: []*Entry{{
			ID:            "exec-fresh",
			Node:          "node-a",
			RAMMB:         128,
			CreatedAt:     now,
			LastHeartbeat: now,
		}},
		DeviceHolds: []*DeviceHold{
			{ID: "hold-old", Node: "node-a", GPUIndex: intPtr(1), MiB: 100, Owner: "op", ExpiresAt: now.Add(-time.Minute)},
			{ID: "hold-new", Node: "node-a", GPUIndex: intPtr(2), MiB: 200, Owner: "op", ExpiresAt: now.Add(time.Hour)},
		},
	})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if entries := l.Entries(); len(entries) != 1 || entries[0].ID != "exec-fresh" {
		t.Fatalf("entries after hold expiry = %+v", entries)
	}
	holds := l.DeviceHolds()
	if len(holds) != 1 || holds[0].ID != "hold-new" {
		t.Fatalf("holds after expiry = %+v", holds)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hold-old") {
		t.Fatalf("expired hold was left on disk:\n%s", raw)
	}
	if !strings.Contains(string(raw), "hold-new") || !strings.Contains(string(raw), "exec-fresh") {
		t.Fatalf("rewrite dropped a live record:\n%s", raw)
	}

	again := NewLedger(DefaultLimits(), nil)
	if err := again.Load(); err != nil {
		t.Fatal(err)
	}
	if len(again.DeviceHolds()) != 1 || again.DeviceHolds()[0].ID != "hold-new" {
		t.Fatalf("second load resurrected or dropped holds: %+v", again.DeviceHolds())
	}
}

func TestLoadKeepsHoldExpiringAtNow(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	l := setupTestLedger(t, DefaultLimits())
	l.now = func() time.Time { return now }
	writeLedgerFixture(t, diskFormat{
		Entries: []*Entry{{
			ID: "exec-fresh", Node: "node-a", RAMMB: 64,
			CreatedAt: now, LastHeartbeat: now,
		}},
		DeviceHolds: []*DeviceHold{{
			ID: "hold-eq", Node: "node-a", GPUIndex: intPtr(0), MiB: 32, Owner: "op", ExpiresAt: now,
		}},
	})
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	holds := l.DeviceHolds()
	if len(holds) != 1 || holds[0].GPUIndex == nil || *holds[0].GPUIndex != 0 {
		t.Fatalf("equal expiry dropped the hold: %+v", holds)
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("equal-expiry load rewrote the ledger\n got: %s\nwant: %s", after, before)
	}
}

func TestLoadEntryReclaimKeepsFutureHold(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	now := time.Now().UTC()
	stale := now.Add(-10 * time.Minute)
	writeLedgerFixture(t, diskFormat{
		Entries: []*Entry{{
			ID: "exec-stale", Node: "node-a", RAMMB: 256,
			CreatedAt: stale, LastHeartbeat: stale,
		}},
		DeviceHolds: []*DeviceHold{{
			ID: "hold-live", Node: "node-a", GPUIndex: intPtr(0), MiB: 768, Owner: "op",
			ExpiresAt: now.Add(time.Hour),
		}},
	})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if len(l.Entries()) != 0 {
		t.Fatalf("stale entry survived: %+v", l.Entries())
	}
	holds := l.DeviceHolds()
	if len(holds) != 1 || holds[0].ID != "hold-live" || holds[0].GPUIndex == nil || *holds[0].GPUIndex != 0 {
		t.Fatalf("entry reclaim deleted the hold: %+v", holds)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "exec-stale") || !strings.Contains(text, "hold-live") || !strings.Contains(text, `"gpu_index": 0`) {
		t.Fatalf("reclaim snapshot = %s", text)
	}
}

func TestLoadKeepsZeroExpiryHold(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	now := time.Now().UTC()
	before := writeLedgerFixture(t, diskFormat{
		Entries: []*Entry{{
			ID: "exec-fresh", Node: "node-a", RAMMB: 64,
			CreatedAt: now, LastHeartbeat: now,
		}},
		DeviceHolds: []*DeviceHold{{
			ID: "hold-open", Node: "node-a", GPUIndex: intPtr(3), MiB: 16, Owner: "op",
		}},
	})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if len(l.DeviceHolds()) != 1 || l.DeviceHolds()[0].ID != "hold-open" {
		t.Fatalf("zero expiry was dropped: %+v", l.DeviceHolds())
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("zero-expiry load rewrote the ledger\n got: %s\nwant: %s", after, before)
	}
}

func TestLoadReadOnlyKeepsExpiredHoldAndBytes(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	now := time.Now().UTC()
	before := writeLedgerFixture(t, diskFormat{DeviceHolds: []*DeviceHold{{
		ID: "hold-old", Node: "node-a", GPUIndex: intPtr(1), MiB: 100, Owner: "op",
		ExpiresAt: now.Add(-time.Hour),
	}}})
	if err := l.LoadReadOnly(); err != nil {
		t.Fatal(err)
	}
	holds := l.DeviceHolds()
	if len(holds) != 1 || holds[0].ID != "hold-old" {
		t.Fatalf("read-only load dropped the expired hold: %+v", holds)
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("read-only load rewrote ledger.json\n got: %s\nwant: %s", after, before)
	}
}

func TestLoadReadOnlyMissingFileClearsHolds(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	if _, err := l.HoldDevice(futureHold("gpu-0", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path()); err != nil {
		t.Fatalf("HoldDevice did not persist: %v", err)
	}
	if err := os.Remove(Path()); err != nil {
		t.Fatal(err)
	}
	if err := l.LoadReadOnly(); err != nil {
		t.Fatal(err)
	}
	if len(l.DeviceHolds()) != 0 {
		t.Fatalf("missing file left holds in memory: %+v", l.DeviceHolds())
	}
}

func TestReleaseAndHeartbeatKeepDeviceHold(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)
	if _, err := l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 1024, VRAMMB: 2048}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.HoldDevice(futureHold("gpu-0", 0)); err != nil {
		t.Fatal(err)
	}
	if err := l.Heartbeat("exec-1"); err != nil {
		t.Fatal(err)
	}
	if holds := l.DeviceHolds(); len(holds) != 1 || holds[0].GPUIndex == nil || *holds[0].GPUIndex != 0 {
		t.Fatalf("heartbeat dropped the hold: %+v", holds)
	}
	if err := l.Release("exec-1"); err != nil {
		t.Fatal(err)
	}
	if err := l.Release("gpu-0"); err == nil {
		t.Fatal("release of a hold id deleted or accepted the hold")
	}
	if holds := l.DeviceHolds(); len(holds) != 1 {
		t.Fatalf("release removed the hold from memory: %+v", holds)
	}

	reloaded := NewLedger(DefaultLimits(), nil)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Entries()) != 0 {
		t.Fatalf("released entry returned: %+v", reloaded.Entries())
	}
	holds := reloaded.DeviceHolds()
	if len(holds) != 1 || holds[0].ID != "gpu-0" || holds[0].GPUIndex == nil || *holds[0].GPUIndex != 0 {
		t.Fatalf("reload lost the hold: %+v", holds)
	}
}

func TestSummaryIgnoresHoldMiB(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	l.SetNodeCapacity("node-a", 16384)
	if _, err := l.Reserve(Entry{ID: "exec-1", Node: "node-a", RAMMB: 1024, VRAMMB: 2048}); err != nil {
		t.Fatal(err)
	}
	hold := futureHold("gpu-0", 0)
	hold.MiB = 9000
	if _, err := l.HoldDevice(hold); err != nil {
		t.Fatal(err)
	}
	summary := l.Summary()
	if summary.TotalVRAMMB != 2048 {
		t.Fatalf("TotalVRAMMB = %d, want entry VRAM 2048", summary.TotalVRAMMB)
	}
	var reserved int64
	for _, node := range summary.Nodes {
		if node.Node == "node-a" {
			reserved = node.ReservedVRAMMB
		}
	}
	if reserved != 2048 {
		t.Fatalf("ReservedVRAMMB = %d, want 2048", reserved)
	}
}

func TestPastExpiryIsStoredUntilLoad(t *testing.T) {
	l := setupTestLedger(t, DefaultLimits())
	hold := futureHold("hold-old", 1)
	hold.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := l.HoldDevice(hold); err != nil {
		t.Fatal(err)
	}
	if len(l.DeviceHolds()) != 1 {
		t.Fatal("past expiry was refused")
	}
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if len(l.DeviceHolds()) != 0 {
		t.Fatalf("Load kept the expired hold: %+v", l.DeviceHolds())
	}
}
