package runtimectx

import (
	"context"
	"errors"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func TestLoadBuildsRuntimeAndSurfacesRecoverableWarnings(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.internal", SSHUser: "me"}},
	}
	nodes := []models.NodeFacts{
		{
			Name:   "node-a",
			Status: models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  4096,
				Pressure:   "low",
				CPUCores:   8,
			},
		},
	}
	stateValue := &state.ClusterState{Nodes: map[string]state.NodeState{"node-a": {ReservedMB: 512}}}
	skillStore := &skills.Store{Skills: []skills.LearnedSkill{{ID: "skill-1"}}}

	restore := stubRuntimeDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, *config.Config) ([]models.NodeFacts, []models.Warning) { return nodes, nil },
		func([]models.NodeFacts) *models.ClusterSnapshot {
			return &models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
				Nodes:  append([]models.NodeFacts(nil), nodes...),
			}
		},
		func() (*state.ClusterState, error) { return stateValue, errors.New("recovered local AXIS state") },
		func(*models.ClusterSnapshot, *state.ClusterState) {
			nodes[0].Resources.RAMReservedMB = 512
			nodes[0].Resources.RAMAllocatableMB = 3584
		},
		func() (*skills.Store, error) { return skillStore, errors.New("recovered learned skills store") },
	)
	defer restore()

	rt, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rt.Config != cfg {
		t.Fatal("expected config to propagate")
	}
	if rt.State != stateValue {
		t.Fatal("expected recovered state to propagate")
	}
	if rt.Skills != skillStore {
		t.Fatal("expected recovered skills to propagate")
	}
	if len(rt.Snapshot.Warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(rt.Snapshot.Warnings))
	}
	if rt.Snapshot.Warnings[0].Kind != "state" || rt.Snapshot.Warnings[1].Kind != "skills" {
		t.Fatalf("unexpected warnings: %#v", rt.Snapshot.Warnings)
	}
}

func TestLoadReturnsEmptySnapshotWhenBuilderReturnsNil(t *testing.T) {
	restore := stubRuntimeDeps(t,
		func(string) (*config.Config, error) {
			return &config.Config{Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.internal", SSHUser: "me"}}}, nil
		},
		func(context.Context, *config.Config) ([]models.NodeFacts, []models.Warning) { return nil, nil },
		func([]models.NodeFacts) *models.ClusterSnapshot { return nil },
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil
		},
		func(*models.ClusterSnapshot, *state.ClusterState) {},
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
		func(context.Context, *config.Config) ([]models.NodeFacts, []models.Warning) { return nil, nil },
		func([]models.NodeFacts) *models.ClusterSnapshot { return &models.ClusterSnapshot{} },
		func() (*state.ClusterState, error) { return nil, errors.New("state hard fail") },
		func(*models.ClusterSnapshot, *state.ClusterState) {},
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
		func(context.Context, *config.Config) ([]models.NodeFacts, []models.Warning) { return nil, nil },
		func([]models.NodeFacts) *models.ClusterSnapshot { return &models.ClusterSnapshot{} },
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil
		},
		func(*models.ClusterSnapshot, *state.ClusterState) {},
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
		func(context.Context, *config.Config) ([]models.NodeFacts, []models.Warning) {
			nodes := []models.NodeFacts{
				{Name: "node-a", Status: models.StatusComplete},
			}
			warnings := []models.Warning{
				{Kind: "discovery", Message: "discovery beacon window ended early"},
			}
			return nodes, warnings
		},
		func(nodes []models.NodeFacts) *models.ClusterSnapshot {
			return &models.ClusterSnapshot{Nodes: nodes}
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil
		},
		func(*models.ClusterSnapshot, *state.ClusterState) {},
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

func TestPrependWarningReasoningIncludesOperatorWarnings(t *testing.T) {
	got := PrependWarningReasoning([]string{"chosen node"}, []models.Warning{
		{Kind: "partial", Message: "ignore me"},
		{Kind: "state", Message: "state warning"},
		{Kind: "cache", Message: "cache warning"},
		{Kind: "discovery", Message: "discovery warning"},
		{Kind: "skills", Message: "skills warning"},
	})

	want := []string{
		"warning: state warning",
		"warning: cache warning",
		"warning: discovery warning",
		"warning: skills warning",
		"chosen node",
	}
	if len(got) != len(want) {
		t.Fatalf("reasoning len = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reasoning[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func stubRuntimeDeps(
	t *testing.T,
	cfgFn func(string) (*config.Config, error),
	discoverFn func(context.Context, *config.Config) ([]models.NodeFacts, []models.Warning),
	buildFn func([]models.NodeFacts) *models.ClusterSnapshot,
	stateFn func() (*state.ClusterState, error),
	applyFn func(*models.ClusterSnapshot, *state.ClusterState),
	skillsFn func() (*skills.Store, error),
) func() {
	t.Helper()

	prevLoadConfig := loadConfig
	prevDiscoverNodes := discoverNodes
	prevBuildSnapshot := buildSnapshot
	prevLoadState := loadState
	prevApplyReservationView := applyReservationView
	prevLoadSkills := loadSkills

	loadConfig = cfgFn
	discoverNodes = discoverFn
	buildSnapshot = buildFn
	loadState = stateFn
	applyReservationView = applyFn
	loadSkills = skillsFn

	return func() {
		loadConfig = prevLoadConfig
		discoverNodes = prevDiscoverNodes
		buildSnapshot = prevBuildSnapshot
		loadState = prevLoadState
		applyReservationView = prevApplyReservationView
		loadSkills = prevLoadSkills
	}
}
