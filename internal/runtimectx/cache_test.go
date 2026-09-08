package runtimectx

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func stubCacheDeps(
	t *testing.T,
	cfgFn func(string) (*config.Config, error),
	daemonFn func(context.Context, string) (*models.ClusterSnapshot, string, error),
	diskFn func() (*models.ClusterSnapshot, error),
	stateFn func() (*state.ClusterState, error),
	skillsFn func() (*skills.Store, error),
) func() {
	t.Helper()
	prevCfg := loadConfig
	prevDaemon := fetchDaemonSnapshot
	prevDisk := readDiskSnapshot
	prevStat := loadState
	prevSkills := loadSkills

	if cfgFn != nil {
		loadConfig = cfgFn
	}
	if daemonFn != nil {
		fetchDaemonSnapshot = daemonFn
	}
	if diskFn != nil {
		readDiskSnapshot = diskFn
	}
	if stateFn != nil {
		loadState = stateFn
	}
	if skillsFn != nil {
		loadSkills = skillsFn
	}

	return func() {
		loadConfig = prevCfg
		fetchDaemonSnapshot = prevDaemon
		readDiskSnapshot = prevDisk
		loadState = prevStat
		loadSkills = prevSkills
	}
}

func TestLoadCachedFromDaemonHTTP(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.example.com"}},
	}
	observedAt := time.Now().UTC().Add(-30 * time.Second)
	daemonSnap := &models.ClusterSnapshot{
		Timestamp: observedAt,
		Status:    models.SnapshotHealthy,
		Nodes: []models.NodeFacts{
			{
				Name:   "node-a",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMTotalMB: 16384,
					RAMFreeMB:  8192,
				},
			},
		},
		Publication: &models.PublicationEnvelope{
			ID:     "pub-daemon-123",
			Source: "daemon-publication",
		},
		Vantage: &models.VantageInfo{
			NodeName:   "foundry",
			ObservedAt: observedAt,
		},
	}

	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return daemonSnap, "daemon-cache", nil
		},
		func() (*models.ClusterSnapshot, error) {
			t.Fatal("readDiskSnapshot must not be called when daemon succeeds")
			return nil, nil
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Version: 1, Nodes: map[string]state.NodeState{}}, nil
		},
		func() (*skills.Store, error) {
			return &skills.Store{}, nil
		},
	)
	defer restore()

	rt, err := LoadCached(context.Background())
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}
	if rt == nil || rt.Snapshot == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if rt.Snapshot.Publication == nil || rt.Snapshot.Publication.ID != "pub-daemon-123" {
		t.Fatalf("expected publication ID pub-daemon-123, got %+v", rt.Snapshot.Publication)
	}
	if rt.Snapshot.Vantage == nil || rt.Snapshot.Vantage.NodeName != "foundry" {
		t.Fatalf("expected vantage foundry, got %+v", rt.Snapshot.Vantage)
	}
	if len(rt.Snapshot.Nodes) != 1 || rt.Snapshot.Nodes[0].Name != "node-a" {
		t.Fatalf("unexpected nodes: %+v", rt.Snapshot.Nodes)
	}
}

func TestLoadCachedFallbackToDiskSnapshot(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "node-b", Hostname: "node-b.example.com"}},
	}
	observedAt := time.Now().UTC().Add(-1 * time.Minute)
	diskSnap := &models.ClusterSnapshot{
		Timestamp: observedAt,
		Status:    models.SnapshotHealthy,
		Nodes: []models.NodeFacts{
			{
				Name:   "node-b",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMTotalMB: 32768,
					RAMFreeMB:  16384,
				},
			},
		},
		Publication: &models.PublicationEnvelope{
			ID:     "pub-disk-456",
			Source: "daemon-publication",
		},
		Vantage: &models.VantageInfo{
			NodeName:   "cranium",
			ObservedAt: observedAt,
		},
	}

	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("connection refused")
		},
		func() (*models.ClusterSnapshot, error) {
			return diskSnap, nil
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Version: 1, Nodes: map[string]state.NodeState{}}, nil
		},
		func() (*skills.Store, error) {
			return &skills.Store{}, nil
		},
	)
	defer restore()

	rt, err := LoadCached(context.Background())
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}
	if rt.Snapshot == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if rt.Snapshot.Vantage == nil || rt.Snapshot.Vantage.NodeName != "cranium" {
		t.Fatalf("expected vantage cranium, got %+v", rt.Snapshot.Vantage)
	}

	foundWarning := false
	for _, w := range rt.Snapshot.Warnings {
		if w.Kind == "cache" && w.Message == "daemon unreachable; snapshot loaded from disk cache" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected daemon unreachable warning, got: %+v", rt.Snapshot.Warnings)
	}
}

func TestLoadCachedFallbackToBootstrapSkeleton(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "node-1", Hostname: "node-1.example.com", Role: "compute"},
			{Name: "node-2", Hostname: "node-2.example.com", Role: "storage"},
		},
	}

	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("connection refused")
		},
		func() (*models.ClusterSnapshot, error) {
			return nil, os.ErrNotExist
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Version: 1, Nodes: map[string]state.NodeState{}}, nil
		},
		func() (*skills.Store, error) {
			return &skills.Store{}, nil
		},
	)
	defer restore()

	rt, err := LoadCached(context.Background())
	if err != nil {
		t.Fatalf("LoadCached should not fail on cold bootstrap, got: %v", err)
	}
	if rt.Snapshot == nil {
		t.Fatal("expected bootstrap skeleton snapshot")
	}
	if len(rt.Snapshot.Nodes) != 2 {
		t.Fatalf("expected 2 bootstrap nodes from config, got %d", len(rt.Snapshot.Nodes))
	}
	if rt.Snapshot.Nodes[0].Name != "node-1" || rt.Snapshot.Nodes[1].Name != "node-2" {
		t.Fatalf("unexpected node names: %+v", rt.Snapshot.Nodes)
	}
	// Per C1 contract: Vantage must be nil (unknown), never inferred
	if rt.Snapshot.Vantage != nil {
		t.Fatalf("bootstrap snapshot must not synthesize a fake vantage, got: %+v", rt.Snapshot.Vantage)
	}
	if rt.Snapshot.Publication == nil {
		t.Fatal("expected fallback publication envelope")
	}
}

func TestLoadCachedStaleWarning(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.example.com"}},
	}
	staleTime := time.Now().UTC().Add(-15 * time.Minute)
	diskSnap := &models.ClusterSnapshot{
		Timestamp: staleTime,
		Status:    models.SnapshotHealthy,
		Nodes: []models.NodeFacts{
			{Name: "node-a", Status: models.StatusComplete},
		},
	}

	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("connection refused")
		},
		func() (*models.ClusterSnapshot, error) {
			return diskSnap, nil
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{}, nil
		},
		func() (*skills.Store, error) {
			return &skills.Store{}, nil
		},
	)
	defer restore()

	rt, err := LoadCached(context.Background())
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}

	foundStale := false
	for _, w := range rt.Snapshot.Warnings {
		if w.Kind == "cache" && len(w.Message) > 0 {
			foundStale = true
		}
	}
	if !foundStale {
		t.Fatal("expected stale cache warning")
	}
}
