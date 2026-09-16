package runtimectx

import (
	"context"
	"errors"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func TestLoadReturnsEmptySnapshotWhenBuilderReturnsNil(t *testing.T) {
	restore := stubRuntimeDeps(t,
		func(string) (*config.Config, error) {
			return &config.Config{Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.internal", SSHUser: "me"}}}, nil
		},
		func(context.Context, *config.Config) discovery.Result { return discovery.Result{} },
		func([]models.NodeFacts) *models.ClusterSnapshot { return nil },
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil
		},
		func(*models.ClusterSnapshot, *state.ClusterState, *reservation.Ledger) {},
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	defer restore()

	rt, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rt.Snapshot == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if len(rt.Snapshot.Nodes) != 0 {
		t.Fatalf("expected empty snapshot nodes, got %#v", rt.Snapshot.Nodes)
	}
}

func TestLoadFailsOnHardStateError(t *testing.T) {
	restore := stubRuntimeDeps(t,
		func(string) (*config.Config, error) {
			return &config.Config{Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.internal", SSHUser: "me"}}}, nil
		},
		func(context.Context, *config.Config) discovery.Result { return discovery.Result{} },
		func([]models.NodeFacts) *models.ClusterSnapshot { return &models.ClusterSnapshot{} },
		func() (*state.ClusterState, error) { return nil, errors.New("state hard fail") },
		func(*models.ClusterSnapshot, *state.ClusterState, *reservation.Ledger) {},
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	defer restore()

	if _, err := Load(context.Background()); err == nil || err.Error() != "state hard fail" {
		t.Fatalf("expected hard state error, got %v", err)
	}
}

func TestLoadFailsOnHardSkillsError(t *testing.T) {
	restore := stubRuntimeDeps(t,
		func(string) (*config.Config, error) {
			return &config.Config{Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.internal", SSHUser: "me"}}}, nil
		},
		func(context.Context, *config.Config) discovery.Result { return discovery.Result{} },
		func([]models.NodeFacts) *models.ClusterSnapshot { return &models.ClusterSnapshot{} },
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil
		},
		func(*models.ClusterSnapshot, *state.ClusterState, *reservation.Ledger) {},
		func() (*skills.Store, error) { return nil, errors.New("skills hard fail") },
	)
	defer restore()

	if _, err := Load(context.Background()); err == nil || err.Error() != "skills hard fail" {
		t.Fatalf("expected hard skills error, got %v", err)
	}
}

func TestLoadSurfacesDiscoveryWarnings(t *testing.T) {
	restore := stubRuntimeDeps(t,
		func(string) (*config.Config, error) {
			return &config.Config{Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.internal", SSHUser: "me"}}}, nil
		},
		func(context.Context, *config.Config) discovery.Result {
			nodes := []models.NodeFacts{
				{Name: "node-a", Status: models.StatusComplete},
			}
			warnings := []models.Warning{
				{Kind: "discovery", Message: "discovery beacon window ended early"},
			}
			return discovery.Result{Nodes: nodes, Warnings: warnings}
		},
		func(nodes []models.NodeFacts) *models.ClusterSnapshot {
			return &models.ClusterSnapshot{Nodes: nodes}
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil
		},
		func(*models.ClusterSnapshot, *state.ClusterState, *reservation.Ledger) {},
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	defer restore()

	rt, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rt.Snapshot.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %#v", rt.Snapshot.Warnings)
	}
	if rt.Snapshot.Warnings[0].Kind != "discovery" {
		t.Fatalf("expected discovery warning, got %#v", rt.Snapshot.Warnings)
	}
}
