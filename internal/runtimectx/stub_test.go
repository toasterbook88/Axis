package runtimectx

import (
	"context"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

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
