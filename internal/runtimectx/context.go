package runtimectx

import (
	"context"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/snapshot"
	"github.com/toasterbook88/axis/internal/state"
)

type Context struct {
	Config   *config.Config
	Snapshot *models.ClusterSnapshot
	State    *state.ClusterState
	Skills   *skills.Store
}

var loadConfig = config.Load
var discoverNodes = discovery.Discover
var buildSnapshot = snapshot.Build
var loadState = state.Load
var applyReservationView = daemon.ApplyReservationView
var loadSkills = skills.Load

func Load(ctx context.Context) (*Context, error) {
	cfg, err := loadConfig(config.DefaultConfigPath())
	if err != nil {
		return nil, err
	}

	nodes := discoverNodes(ctx, cfg)
	snap := buildSnapshot(nodes)
	if snap == nil {
		snap = &models.ClusterSnapshot{}
	}

	st, err := loadState()
	if err != nil && st == nil {
		return nil, err
	}
	applyReservationView(snap, st)
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
		Skills:   skillStore,
	}, nil
}

func PrependWarningReasoning(reasoning []string, warnings []models.Warning) []string {
	if len(warnings) == 0 {
		return reasoning
	}

	prefixed := make([]string, 0, len(warnings)+len(reasoning))
	for _, warning := range warnings {
		if warning.Kind != "state" && warning.Kind != "skills" {
			continue
		}
		prefixed = append(prefixed, "warning: "+warning.Message)
	}
	return append(prefixed, reasoning...)
}
