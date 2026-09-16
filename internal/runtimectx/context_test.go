package runtimectx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
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
	stateUpdatedAt := time.Unix(1_700_000_000, 0).UTC()
	stateValue := &state.ClusterState{Version: 1, Nodes: map[string]state.NodeState{"node-a": {ReservedMB: 512}}, UpdatedAt: stateUpdatedAt}
	skillStore := &skills.Store{Skills: []skills.LearnedSkill{{ID: "skill-1"}}}

	restore := stubRuntimeDeps(t,
		func(string) (*config.Config, error) { return cfg, nil },
		func(context.Context, *config.Config) discovery.Result { return discovery.Result{Nodes: nodes} },
		func([]models.NodeFacts) *models.ClusterSnapshot {
			return &models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
				Nodes:  append([]models.NodeFacts(nil), nodes...),
			}
		},
		func() (*state.ClusterState, error) { return stateValue, errors.New("recovered local AXIS state") },
		func(*models.ClusterSnapshot, *state.ClusterState, *reservation.Ledger) {
			nodes[0].RAMReservedMB = 512
			nodes[0].RAMAllocatableMB = 3584
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
	publication := rt.Snapshot.Publication
	if publication == nil || publication.Source != "live-runtime" {
		t.Fatalf("expected live publication envelope, got %+v", publication)
	}
	if !publication.Facts.Available || !publication.Ledger.Available || !publication.State.Available {
		t.Fatalf("expected all publication authorities available, got %+v", publication)
	}
	if publication.State.SchemaVersion != 1 || !publication.State.UpdatedAt.Equal(stateUpdatedAt) || publication.State.Warning != "recovered local AXIS state" {
		t.Fatalf("unexpected state publication evidence: %+v", publication.State)
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

func TestLoadFailsWhenLedgerInstanceCouldNotLoad(t *testing.T) {
	restore := stubRuntimeDeps(t,
		func(string) (*config.Config, error) {
			return &config.Config{Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "node-a.internal", SSHUser: "me"}}}, nil
		},
		func(context.Context, *config.Config) discovery.Result {
			return discovery.Result{Nodes: []models.NodeFacts{{
				Name:      "node-a",
				Resources: &models.Resources{RAMTotalMB: 8192, RAMFreeMB: 4096},
			}}}
		},
		func(nodes []models.NodeFacts) *models.ClusterSnapshot {
			return &models.ClusterSnapshot{Nodes: nodes}
		},
		func() (*state.ClusterState, error) {
			return &state.ClusterState{Nodes: map[string]state.NodeState{
				"node-a": {ReservedMB: 1024},
			}}, nil
		},
		func(*models.ClusterSnapshot, *state.ClusterState, *reservation.Ledger) {
			t.Fatal("reservation view must not be published when ledger authority is unavailable")
		},
		func() (*skills.Store, error) { return &skills.Store{}, nil },
	)
	defer restore()

	prevLoadLedger := loadLedger
	loadLedger = func() (*reservation.Ledger, error) {
		return reservation.NewLedger(reservation.DefaultLimits(), nil), errors.New("ledger unavailable")
	}
	defer func() { loadLedger = prevLoadLedger }()

	if _, err := Load(context.Background()); err == nil || !strings.Contains(err.Error(), "load reservation ledger: ledger unavailable") {
		t.Fatalf("expected ledger load error, got %v", err)
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
	discoverFn func(context.Context, *config.Config) discovery.Result,
	buildFn func([]models.NodeFacts) *models.ClusterSnapshot,
	stateFn func() (*state.ClusterState, error),
	applyFn func(*models.ClusterSnapshot, *state.ClusterState, *reservation.Ledger),
	skillsFn func() (*skills.Store, error),
) func() {
	t.Helper()

	prevLoadConfig := loadConfig
	prevDiscoverNodes := discoverNodes
	prevBuildSnapshot := buildSnapshot
	prevLoadState := loadState
	prevApplyReservationEntries := applyReservationEntries
	prevLoadSkills := loadSkills

	loadConfig = cfgFn
	discoverNodes = discoverFn
	buildSnapshot = buildFn
	loadState = stateFn
	applyReservationEntries = func(snap *models.ClusterSnapshot, st *state.ClusterState, _ []reservation.Entry, _ bool) {
		applyFn(snap, st, nil)
	}
	loadSkills = skillsFn

	return func() {
		loadConfig = prevLoadConfig
		discoverNodes = prevDiscoverNodes
		buildSnapshot = prevBuildSnapshot
		loadState = prevLoadState
		applyReservationEntries = prevApplyReservationEntries
		loadSkills = prevLoadSkills
	}
}
