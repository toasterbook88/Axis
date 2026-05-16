package runtimectx

import (
	"context"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/snapshot"
	"github.com/toasterbook88/axis/internal/snapshotview"
	"github.com/toasterbook88/axis/internal/state"
)

type Context struct {
	Config   *config.Config
	Snapshot *models.ClusterSnapshot
	State    *state.ClusterState
	Ledger   *reservation.Ledger
	Skills   *skills.Store
}

var loadConfig = config.Load
var discoverNodes = discovery.DiscoverResult
var buildSnapshot = snapshot.Build
var loadState = state.Load
var loadLedger = func() (*reservation.Ledger, error) {
	l := reservation.NewLedger(reservation.DefaultLimits(), nil)
	err := l.Load()
	return l, err
}
var applyReservationView = snapshotview.ApplyReservationView
var loadSkills = skills.Load

func Load(ctx context.Context) (*Context, error) {
	cfg, err := loadConfig(config.DefaultConfigPath())
	if err != nil {
		return nil, err
	}

	discoveryResult := discoverNodes(ctx, cfg)
	snap := buildSnapshot(discoveryResult.Nodes)
	if snap == nil {
		snap = &models.ClusterSnapshot{}
	}
	snap.Freshness = discoveryResult.Freshness
	for _, warning := range discoveryResult.Warnings {
		models.AppendWarningIfMissing(snap, warning)
	}

	st, err := loadState()
	if err != nil && st == nil {
		return nil, err
	}

	ledger, ledgerErr := loadLedger()
	if ledgerErr != nil && ledger == nil {
		return nil, ledgerErr
	}
	if ledger != nil {
		for _, n := range snap.Nodes {
			if n.Resources != nil {
				ledger.SetNodeCapacity(n.Name, n.Resources.RAMTotalMB)
			}
		}
	}

	applyReservationView(snap, st, ledger)
	if err != nil {
		snap.Warnings = append(snap.Warnings, models.Warning{
			Kind:    "state",
			Message: err.Error(),
		})
	}

	skillStore, skillErr := loadSkills()
	if skillErr != nil && skillStore == nil {
		return nil, skillErr
	}
	if skillErr != nil {
		snap.Warnings = append(snap.Warnings, models.Warning{
			Kind:    "skills",
			Message: skillErr.Error(),
		})
	}

	return &Context{
		Config:   cfg,
		Snapshot: snap,
		State:    st,
		Ledger:   ledger,
		Skills:   skillStore,
	}, nil
}

func PrependWarningReasoning(reasoning []string, warnings []models.Warning) []string {
	if len(warnings) == 0 {
		return reasoning
	}

	prefixed := make([]string, 0, len(warnings)+len(reasoning))
	for _, warning := range warnings {
		if warning.Kind != "state" && warning.Kind != "skills" && warning.Kind != "cache" && warning.Kind != "discovery" {
			continue
		}
		prefixed = append(prefixed, "warning: "+warning.Message)
	}
	return append(prefixed, reasoning...)
}
