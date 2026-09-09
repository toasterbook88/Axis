package runtimectx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func BenchmarkLoadCachedWarm(b *testing.B) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "node-a", Hostname: "node-a.example.com"},
			{Name: "node-b", Hostname: "node-b.example.com"},
		},
	}
	daemonSnap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Status:    models.SnapshotHealthy,
		Nodes: []models.NodeFacts{
			{Name: "cranium", Status: models.StatusComplete},
			{Name: "foundry", Status: models.StatusComplete},
		},
		Publication: &models.PublicationEnvelope{
			ID:     "pub-bench",
			Source: "daemon-publication",
		},
	}

	restore := stubCacheDeps(&testing.T{},
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return daemonSnap, "daemon-cache", nil
		},
		nil,
		func() (*state.ClusterState, error) { return &state.ClusterState{}, nil },
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	defer restore()

	ctx := context.Background()
	// Warm up
	_, _ = LoadCached(ctx)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rt, err := LoadCached(ctx)
		if err != nil || rt == nil {
			b.Fatalf("LoadCached: %v", err)
		}
	}
}

func TestLoadCachedWarmLatencyUnder10ms(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "node-a", Hostname: "node-a.example.com"},
			{Name: "node-b", Hostname: "node-b.example.com"},
		},
	}
	daemonSnap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Status:    models.SnapshotHealthy,
		Nodes: []models.NodeFacts{
			{Name: "cranium", Status: models.StatusComplete},
			{Name: "foundry", Status: models.StatusComplete},
		},
		Publication: &models.PublicationEnvelope{
			ID:     "pub-bench",
			Source: "daemon-publication",
		},
	}

	restore := stubCacheDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return daemonSnap, "daemon-cache", nil
		},
		nil,
		func() (*state.ClusterState, error) { return &state.ClusterState{}, nil },
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	defer restore()

	ctx := context.Background()
	// Warm up
	_, _ = LoadCached(ctx)

	const iters = 20
	start := time.Now()
	for i := 0; i < iters; i++ {
		rt, err := LoadCached(ctx)
		if err != nil || rt == nil {
			t.Fatalf("LoadCached: %v", err)
		}
	}
	avg := time.Since(start) / iters
	if avg > 10*time.Millisecond {
		t.Fatalf("average LoadCached latency %v exceeded 10ms gate", avg)
	}
}

func BenchmarkLoadCachedDaemonDown(b *testing.B) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.example.com"}},
	}
	diskSnap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Status:    models.SnapshotHealthy,
		Nodes:     []models.NodeFacts{{Name: "node-a", Status: models.StatusComplete}},
	}

	restore := stubCacheDeps(&testing.T{},
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, string) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("daemon down")
		},
		func() (*models.ClusterSnapshot, error) { return diskSnap, nil },
		func() (*state.ClusterState, error) { return &state.ClusterState{}, nil },
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	defer restore()

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rt, err := LoadCached(ctx)
		if err != nil || rt == nil {
			b.Fatalf("LoadCached: %v", err)
		}
	}
}
