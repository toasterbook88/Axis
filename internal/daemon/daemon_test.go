package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func TestRefreshStoresSnapshotAndMeta(t *testing.T) {
	d := New(30*time.Second, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Timestamp: time.Unix(1700000000, 0).UTC(),
			Status:    models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name:   "alpha",
					Status: models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  4096,
					},
				},
			},
			Summary: models.ClusterSummary{
				TotalNodes:     1,
				ReachableNodes: 1,
				TotalRAMMB:     8192,
				TotalFreeRAMMB: 4096,
			},
		}, nil
	})

	path := filepath.Join(t.TempDir(), "snapshot.json")
	d.SetSnapshotPath(path)

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	snap, ok := d.Snapshot()
	if !ok {
		t.Fatal("expected cached snapshot")
	}
	if snap.Summary.TotalFreeRAMMB != 4096 {
		t.Fatalf("expected free ram 4096, got %d", snap.Summary.TotalFreeRAMMB)
	}
	if snap.Summary.TotalAllocatableMB != 4096 {
		t.Fatalf("expected allocatable ram 4096, got %d", snap.Summary.TotalAllocatableMB)
	}

	meta := d.Meta()
	if !meta.Ready {
		t.Fatal("expected daemon to be ready")
	}
	if meta.Source != "daemon-cache" {
		t.Fatalf("expected source daemon-cache, got %q", meta.Source)
	}
	if meta.Version != Version {
		t.Fatalf("expected version %q, got %q", Version, meta.Version)
	}
	if meta.RefreshIntervalSec != 30 {
		t.Fatalf("expected refresh interval sec 30, got %d", meta.RefreshIntervalSec)
	}
	if meta.LastRefreshTrigger != "manual" {
		t.Fatalf("expected last refresh trigger manual, got %q", meta.LastRefreshTrigger)
	}
	if meta.CollectedAt.IsZero() {
		t.Fatal("expected collected_at to be populated")
	}
	if meta.NextRefreshAt.IsZero() {
		t.Fatal("expected next_refresh_at to be populated")
	}
	if meta.LastError != "" {
		t.Fatalf("expected empty last_error, got %q", meta.LastError)
	}
	if meta.CacheAgeSec < 0 {
		t.Fatalf("expected non-negative cache age, got %d", meta.CacheAgeSec)
	}
	if meta.Stale {
		t.Fatal("expected fresh metadata immediately after refresh")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot file: %v", err)
	}
	var persisted models.ClusterSnapshot
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal snapshot file: %v", err)
	}
	if persisted.Summary.TotalNodes != 1 {
		t.Fatalf("expected persisted total nodes 1, got %d", persisted.Summary.TotalNodes)
	}
}

func TestRefreshFailurePreservesPreviousSnapshot(t *testing.T) {
	calls := 0
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		calls++
		if calls == 1 {
			return &models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
				Summary: models.ClusterSummary{
					TotalFreeRAMMB: 2048,
				},
			}, nil
		}
		return nil, context.DeadlineExceeded
	})
	d.SetSnapshotPath(filepath.Join(t.TempDir(), "snapshot.json"))

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := d.Refresh(context.Background()); err == nil {
		t.Fatal("expected second refresh to fail")
	}

	snap, ok := d.Snapshot()
	if !ok {
		t.Fatal("expected previous snapshot to remain available")
	}
	if snap.Summary.TotalFreeRAMMB != 2048 {
		t.Fatalf("expected preserved free ram 2048, got %d", snap.Summary.TotalFreeRAMMB)
	}

	meta := d.Meta()
	if !meta.Ready {
		t.Fatal("expected cache to remain ready after failed refresh")
	}
	if !strings.Contains(meta.LastError, "deadline exceeded") {
		t.Fatalf("expected deadline exceeded in last_error, got %q", meta.LastError)
	}
}

func TestInvalidateClearsSnapshotAndRemovesPersistedFile(t *testing.T) {
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 1,
			},
		}, nil
	})

	path := filepath.Join(t.TempDir(), "snapshot.json")
	d.SetSnapshotPath(path)

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected persisted snapshot file: %v", err)
	}

	d.Invalidate()

	if _, ok := d.Snapshot(); ok {
		t.Fatal("expected snapshot to be cleared")
	}

	meta := d.Meta()
	if meta.Ready {
		t.Fatal("expected cache to be marked not ready")
	}
	if !meta.CollectedAt.IsZero() {
		t.Fatal("expected collected_at to be cleared")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected snapshot file to be removed, got %v", err)
	}
}

func TestRefreshNowStoresSnapshotImmediately(t *testing.T) {
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 2,
			},
		}, nil
	})
	d.SetSnapshotPath(filepath.Join(t.TempDir(), "snapshot.json"))

	if err := d.RefreshNow(context.Background()); err != nil {
		t.Fatalf("RefreshNow: %v", err)
	}

	snap, ok := d.Snapshot()
	if !ok {
		t.Fatal("expected snapshot after RefreshNow")
	}
	if snap.Summary.TotalNodes != 2 {
		t.Fatalf("expected total nodes 2, got %d", snap.Summary.TotalNodes)
	}

	if !d.Meta().Ready {
		t.Fatal("expected daemon to be ready after RefreshNow")
	}
	if got := d.Meta().LastRefreshTrigger; got != "manual" {
		t.Fatalf("expected RefreshNow trigger manual, got %q", got)
	}
}

func TestRefreshWithTriggerStoresExplicitTrigger(t *testing.T) {
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, nil
	})
	d.SetSnapshotPath("")

	if err := d.RefreshWithTrigger(context.Background(), execution.StateChangeExecutionFinished); err != nil {
		t.Fatalf("RefreshWithTrigger: %v", err)
	}
	if got := d.Meta().LastRefreshTrigger; got != execution.StateChangeExecutionFinished {
		t.Fatalf("expected explicit execution trigger, got %q", got)
	}
}

func TestMetaIncludesReservedMB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, nil
	})
	d.ledger.SetNodeCapacity("alpha", 8192)
	d.ledger.SetNodeCapacity("beta", 8192)
	d.ledger.Reserve(reservation.Entry{
		ID:          "exec-a",
		Node:        "alpha",
		OwnerExecID: "exec-a",
		RAMMB:       512,
	})
	d.ledger.Reserve(reservation.Entry{
		ID:          "exec-b",
		Node:        "beta",
		OwnerExecID: "exec-b",
		RAMMB:       256,
	})
	meta := d.Meta()
	if meta.ReservedMB != 768 {
		t.Fatalf("expected reserved_mb 768, got %d", meta.ReservedMB)
	}
}

func TestMetaMarksStaleSnapshots(t *testing.T) {
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, nil
	})
	d.collectedAt = time.Now().UTC().Add(-6 * time.Minute)

	meta := d.Meta()
	if !meta.Stale {
		t.Fatal("expected metadata to be stale")
	}
	if meta.CacheAgeSec < 360 {
		t.Fatalf("expected cache age >= 360s, got %d", meta.CacheAgeSec)
	}
}

func TestWatchConfigRefreshesOnContentChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, "nodes.yaml")
	if err := os.WriteFile(configPath, []byte("nodes:\n  - name: alpha\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	prevPoll := watchConfigPollInterval
	watchConfigPollInterval = 10 * time.Millisecond
	defer func() { watchConfigPollInterval = prevPoll }()

	var calls atomic.Int32
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		calls.Add(1)
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 1,
			},
		}, nil
	})
	d.SetSnapshotPath("")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.WatchConfig(ctx, configPath)
	time.Sleep(3 * watchConfigPollInterval)

	if err := os.WriteFile(configPath, []byte("nodes:\n  - name: beta\n"), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		meta := d.Meta()
		if calls.Load() >= 1 && meta.LastRefreshTrigger == "config-change" && !meta.LastConfigEventAt.IsZero() && meta.Ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected config change refresh, got meta=%+v calls=%d", d.Meta(), calls.Load())
}

func TestWatchConfigInvalidatesCacheWhenConfigDisappears(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, "nodes.yaml")
	if err := os.WriteFile(configPath, []byte("nodes:\n  - name: alpha\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	prevPoll := watchConfigPollInterval
	watchConfigPollInterval = 10 * time.Millisecond
	defer func() { watchConfigPollInterval = prevPoll }()

	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		if _, err := os.Stat(configPath); err != nil {
			return nil, err
		}
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 1,
			},
		}, nil
	})
	d.SetSnapshotPath("")

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if !d.Meta().Ready {
		t.Fatal("expected initial cache readiness")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.WatchConfig(ctx, configPath)
	time.Sleep(3 * watchConfigPollInterval)

	if err := os.Remove(configPath); err != nil {
		t.Fatalf("remove config: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		meta := d.Meta()
		if !meta.Ready && meta.LastRefreshTrigger == "config-change" && strings.Contains(meta.LastError, "no such file or directory") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected config removal to invalidate cache, got meta=%+v", d.Meta())
}

func TestWatchStateRefreshesOnContentChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 256, LastPlacedAt: time.Now().UTC(), ActiveTasks: 1, ActiveExecs: []string{"exec-a"}},
		},
	}
	if err := st.Save(); err != nil {
		t.Fatalf("state save: %v", err)
	}

	prevPoll := watchConfigPollInterval
	watchConfigPollInterval = 10 * time.Millisecond
	defer func() { watchConfigPollInterval = prevPoll }()

	var calls atomic.Int32
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		calls.Add(1)
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  4096,
				},
				Status: models.StatusComplete,
			}},
		}, nil
	})
	d.SetSnapshotPath("")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.WatchState(ctx, state.Path())
	time.Sleep(3 * watchConfigPollInterval)

	st.Nodes["alpha"] = state.NodeState{ReservedMB: 512, LastPlacedAt: time.Now().UTC(), ActiveTasks: 1, ActiveExecs: []string{"exec-b"}}
	if err := st.Save(); err != nil {
		t.Fatalf("state resave: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		meta := d.Meta()
		if calls.Load() >= 1 && meta.LastRefreshTrigger == "state-change" && meta.Ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected state change refresh, got meta=%+v calls=%d", d.Meta(), calls.Load())
}

func TestWatchStateIgnoresHeartbeatOnlyChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {
				ReservedMB:   256,
				LastPlacedAt: time.Now().UTC(),
				ActiveTasks:  1,
				ActiveExecs:  []string{"exec-a"},
				ExecReservationsMB: map[string]int64{
					"exec-a": 256,
				},
				ExecHeartbeatAt: map[string]time.Time{
					"exec-a": time.Now().UTC().Add(-time.Minute),
				},
			},
		},
	}
	if err := st.Save(); err != nil {
		t.Fatalf("state save: %v", err)
	}

	prevPoll := watchConfigPollInterval
	watchConfigPollInterval = 10 * time.Millisecond
	defer func() { watchConfigPollInterval = prevPoll }()

	var calls atomic.Int32
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		calls.Add(1)
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  4096,
				},
				Status: models.StatusComplete,
			}},
		}, nil
	})
	d.SetSnapshotPath("")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.WatchState(ctx, state.Path())
	time.Sleep(3 * watchConfigPollInterval)

	ns := st.Nodes["alpha"]
	ns.ExecHeartbeatAt["exec-a"] = time.Now().UTC()
	st.Nodes["alpha"] = ns
	if err := st.Save(); err != nil {
		t.Fatalf("state heartbeat save: %v", err)
	}

	time.Sleep(6 * watchConfigPollInterval)
	if got := calls.Load(); got != 0 {
		t.Fatalf("expected heartbeat-only state write to avoid refresh, got %d refresh calls", got)
	}
	if meta := d.Meta(); meta.LastRefreshTrigger == RefreshTriggerStateChange {
		t.Fatalf("expected no state-change trigger for heartbeat-only write, got meta=%+v", meta)
	}

	ns.ReservedMB = 512
	ns.ExecReservationsMB["exec-a"] = 512
	st.Nodes["alpha"] = ns
	if err := st.Save(); err != nil {
		t.Fatalf("state reservation save: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		meta := d.Meta()
		if calls.Load() >= 1 && meta.LastRefreshTrigger == RefreshTriggerStateChange && meta.Ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected reservation change refresh after heartbeat-only write, got meta=%+v calls=%d", d.Meta(), calls.Load())
}

func TestWatchFileWithFingerprintReportsFingerprintErrors(t *testing.T) {
	prevPoll := watchConfigPollInterval
	watchConfigPollInterval = 10 * time.Millisecond
	defer func() { watchConfigPollInterval = prevPoll }()

	errCh := make(chan struct {
		path    string
		trigger string
		err     error
	}, 2)
	prevReport := reportWatchFingerprintError
	reportWatchFingerprintError = func(path, trigger string, err error) {
		errCh <- struct {
			path    string
			trigger string
			err     error
		}{path: path, trigger: trigger, err: err}
	}

	d := New(time.Minute, func(context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		d.WaitStopped(waitCtx)
		reportWatchFingerprintError = prevReport
	}()

	path := filepath.Join(t.TempDir(), "state.json")
	wantErr := errors.New("fingerprint boom")
	d.watchFileWithFingerprint(ctx, path, RefreshTriggerStateChange, func(string) (configFingerprint, error) {
		return configFingerprint{}, wantErr
	})

	select {
	case got := <-errCh:
		if got.path != path {
			t.Fatalf("path = %q, want %q", got.path, path)
		}
		if got.trigger != RefreshTriggerStateChange {
			t.Fatalf("trigger = %q, want %q", got.trigger, RefreshTriggerStateChange)
		}
		if !errors.Is(got.err, wantErr) {
			t.Fatalf("err = %v, want %v", got.err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("expected fingerprint error to be reported")
	}
}

func TestWatchSkillsRefreshesWhenFileAppears(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	prevPoll := watchConfigPollInterval
	watchConfigPollInterval = 10 * time.Millisecond
	defer func() { watchConfigPollInterval = prevPoll }()

	var calls atomic.Int32
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		calls.Add(1)
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 1,
			},
		}, nil
	})
	d.SetSnapshotPath("")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.WatchSkills(ctx, skills.Path())
	time.Sleep(3 * watchConfigPollInterval)

	store := &skills.Store{
		Skills: []skills.LearnedSkill{{
			ID:           "skill-1",
			Description:  "test skill",
			Command:      "echo ok",
			SuccessCount: 1,
			LastUsed:     time.Now().UTC(),
		}},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("skills save: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		meta := d.Meta()
		if calls.Load() >= 1 && meta.LastRefreshTrigger == "skills-change" && meta.Ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected skills change refresh, got meta=%+v calls=%d", d.Meta(), calls.Load())
}

func TestWatchDiscoveryRefreshesOnBeaconChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	port := freeUDPPort(t)
	configPath := filepath.Join(home, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configBody := []byte("nodes:\n  - name: local\n    hostname: localhost\n    ssh_user: axis\n    role: primary\ndiscovery:\n  enabled: true\n  udp_port: " + strconv.Itoa(port) + "\n  beacon_interval_sec: 60\n  secret: shared\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	prevPoll := watchConfigPollInterval
	watchConfigPollInterval = 10 * time.Millisecond
	defer func() { watchConfigPollInterval = prevPoll }()

	var calls atomic.Int32
	registry := discovery.NewBeaconRegistry()
	var d *Daemon
	d = New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		calls.Add(1)
		nodes := registry.Snapshot()
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: len(nodes),
			},
		}, nil
	})
	d.beaconRegistry = registry
	d.SetSnapshotPath("")

	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
		defer drainCancel()
		d.WaitStopped(drainCtx)
		watchConfigPollInterval = prevPoll
	}()
	d.WatchDiscovery(ctx, configPath)
	time.Sleep(3 * watchConfigPollInterval)

	beacon := discovery.Beacon{
		Type:      "axis",
		Name:      "beacon-node",
		StableID:  "abc-123",
		IP:        "10.0.0.9",
		SSHPort:   2200,
		Role:      "worker",
		Timestamp: time.Now().UTC(),
	}
	beacon.Sig = discoveryTestSignBeacon(beacon, "shared")

	deadline := time.Now().Add(2 * time.Second)
	sendTicker := time.NewTicker(25 * time.Millisecond)
	defer sendTicker.Stop()
	for time.Now().Before(deadline) {
		sendDaemonBeacon(t, port, beacon)
		meta := d.Meta()
		snap, ok := d.Snapshot()
		if ok && calls.Load() >= 1 && meta.LastRefreshTrigger == RefreshTriggerBeaconChange && snap.Summary.TotalNodes == 1 {
			return
		}
		<-sendTicker.C
	}

	snap, _ := d.Snapshot()
	t.Fatalf("expected beacon change refresh, got meta=%+v calls=%d snap=%+v", d.Meta(), calls.Load(), snap)
}

func TestRefreshInjectsReservationViewIntoSnapshot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name:   "alpha",
					Status: models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  4096,
					},
				},
			},
		}, nil
	})
	d.ledger.SetNodeCapacity("alpha", 8192)
	d.ledger.Reserve(reservation.Entry{
		ID:           "exec-a",
		Node:         "alpha",
		OwnerExecID:  "exec-a",
		OwnerSurface: "test",
		RAMMB:        768,
	})
	path := filepath.Join(t.TempDir(), "snapshot.json")
	d.SetSnapshotPath(path)

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	snap, ok := d.Snapshot()
	if !ok {
		t.Fatal("expected cached snapshot")
	}
	if got := snap.Nodes[0].RAMReservedMB; got != 768 {
		t.Fatalf("expected reserved RAM 768, got %d", got)
	}
	if got := snap.Nodes[0].RAMAllocatableMB; got != 3328 {
		t.Fatalf("expected allocatable RAM 3328, got %d", got)
	}
	if got := snap.Summary.TotalReservedMB; got != 768 {
		t.Fatalf("expected summary reserved RAM 768, got %d", got)
	}
	if got := snap.Summary.TotalAllocatableMB; got != 3328 {
		t.Fatalf("expected summary allocatable RAM 3328, got %d", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot file: %v", err)
	}
	var persisted models.ClusterSnapshot
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal snapshot file: %v", err)
	}
	if got := persisted.Nodes[0].RAMAllocatableMB; got != 3328 {
		t.Fatalf("expected persisted allocatable RAM 3328, got %d", got)
	}
	if got := persisted.Summary.TotalAllocatableMB; got != 3328 {
		t.Fatalf("expected persisted summary allocatable RAM 3328, got %d", got)
	}
}

func TestRefreshRegistersNodeCapacitiesInLedger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name:   "alpha",
					Status: models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 16384,
						RAMFreeMB:  8192,
					},
				},
				{
					Name:   "beta",
					Status: models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  4096,
					},
				},
			},
		}, nil
	})
	// Do NOT call d.ledger.SetNodeCapacity manually — the daemon should do it.
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Reserve should now succeed because capacities were auto-registered.
	_, err := d.ledger.Reserve(reservation.Entry{
		ID:    "exec-a",
		Node:  "alpha",
		RAMMB: 4096,
	})
	if err != nil {
		t.Fatalf("expected reserve to succeed after auto capacity registration: %v", err)
	}
	_, err = d.ledger.Reserve(reservation.Entry{
		ID:    "exec-b",
		Node:  "beta",
		RAMMB: 2048,
	})
	if err != nil {
		t.Fatalf("expected reserve on beta to succeed: %v", err)
	}

	// Node without capacity should still fail.
	_, err = d.ledger.Reserve(reservation.Entry{
		ID:    "exec-c",
		Node:  "gamma",
		RAMMB: 1024,
	})
	if err == nil {
		t.Fatal("expected reserve on unknown node gamma to fail")
	}
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func sendDaemonBeacon(t *testing.T, port int, beacon discovery.Beacon) {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()

	data, err := json.Marshal(beacon)
	if err != nil {
		t.Fatalf("Marshal beacon: %v", err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("Write beacon: %v", err)
	}
}

func discoveryTestSignBeacon(b discovery.Beacon, secret string) string {
	timestamp := b.Timestamp
	data, err := json.Marshal(struct {
		Type      string    `json:"t"`
		Name      string    `json:"n"`
		Hostname  string    `json:"h"`
		StableID  string    `json:"id,omitempty"`
		IP        string    `json:"ip"`
		SSHPort   int       `json:"p"`
		Role      string    `json:"r"`
		Version   string    `json:"v"`
		Timestamp time.Time `json:"ts"`
	}{
		Type:      b.Type,
		Name:      b.Name,
		Hostname:  b.Hostname,
		StableID:  b.StableID,
		IP:        b.IP,
		SSHPort:   b.SSHPort,
		Role:      b.Role,
		Version:   b.Version,
		Timestamp: timestamp,
	})
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestCanReserveUsesReservableRAMCap(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  3072,
				},
			},
		},
	}
	snap.Nodes[0].RAMReservedMB = 2048

	if !CanReserve(snap, "alpha", 1024) {
		t.Fatal("expected reservation to fit under cap")
	}
	if CanReserve(snap, "alpha", 1025) {
		t.Fatal("expected reservation to exceed live reservable cap")
	}
}

func TestDaemonRefreshCoalescingAndLatency(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	collector := func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{}, nil
	}

	d := New(10*time.Second, collector)
	d.snapshotPath = filepath.Join(home, "snap.json")

	// Trigger first refresh
	ctx := context.Background()

	// We lock the refreshing state by setting refreshing to true manually
	d.refreshing.Store(true)

	// While it is refreshing, queue skills-change and state-change
	if err := d.RefreshWithTrigger(ctx, "skills-change"); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	if err := d.RefreshWithTrigger(ctx, "state-change"); err != nil {
		t.Fatalf("second enqueue failed: %v", err)
	}

	// Release manual lock so deferred / scheduled refresh can run
	d.refreshing.Store(false)

	// Schedule next coalesced run manually using the defer-trigger queue logic
	d.pendingMu.Lock()
	var keys []string
	for k := range d.pendingTriggers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	nextTrigger := strings.Join(keys, ",")
	d.pendingMu.Unlock()

	if nextTrigger != "skills-change,state-change" {
		t.Fatalf("expected keys to be sorted coalesced 'skills-change,state-change', got %q", nextTrigger)
	}

	// Run coalesced refresh manually
	err := d.RefreshWithTrigger(ctx, nextTrigger)
	if err != nil {
		t.Fatalf("manual coalesced refresh failed: %v", err)
	}

	meta := d.Meta()
	if meta.LastRefreshTrigger != "skills-change,state-change" {
		t.Fatalf("expected last trigger to be 'skills-change,state-change', got %q", meta.LastRefreshTrigger)
	}

	if meta.MaxRefreshLatencyMs < 0 {
		t.Fatalf("expected non-negative MaxRefreshLatencyMs, got %d", meta.MaxRefreshLatencyMs)
	}

	// Grace period for any leaked goroutines from prior watch tests to finish
	// file operations before t.TempDir() cleanup runs.
	time.Sleep(100 * time.Millisecond)
}

func TestNewDefaultCreatesMeshWhenNoDiscoveryConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d := NewDefault(time.Minute)
	if d.Mesh() == nil {
		t.Fatal("expected mesh to be created when no discovery config exists")
	}
}

func TestNewDefaultCreatesMeshWhenDiscoveryEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configBody := []byte("nodes:\n  - name: local\n    hostname: localhost\n    ssh_user: axis\ndiscovery:\n  enabled: true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	d := NewDefault(time.Minute)
	if d.Mesh() == nil {
		t.Fatal("expected mesh to be created when discovery.enabled is true")
	}
}

func TestNewDefaultOmitsMeshWhenDiscoveryDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configBody := []byte("nodes:\n  - name: local\n    hostname: localhost\n    ssh_user: axis\ndiscovery:\n  enabled: false\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	d := NewDefault(time.Minute)
	if d.Mesh() != nil {
		t.Fatal("expected mesh to be omitted when discovery.enabled is false")
	}
}
