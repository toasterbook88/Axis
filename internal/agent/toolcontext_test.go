package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestNewToolContextNilInitial(t *testing.T) {
	if NewToolContext(nil, nil) != nil {
		t.Fatal("NewToolContext(nil, ...) should return nil")
	}
}

func TestToolContextCurrentReturnsInitialView(t *testing.T) {
	view := &RuntimeView{
		Config:   &config.Config{},
		Snapshot: &models.ClusterSnapshot{},
		State:    &state.ClusterState{},
	}
	tc := NewToolContext(view, nil)
	if tc == nil {
		t.Fatal("NewToolContext returned nil")
	}
	if tc.Current() != view {
		t.Fatal("Current did not return initial view")
	}
}

func TestToolContextReloadCurrentPreservesPointerOnFailure(t *testing.T) {
	initial := &RuntimeView{
		Config:   &config.Config{},
		Snapshot: &models.ClusterSnapshot{},
		State:    &state.ClusterState{},
	}
	tc := NewToolContext(initial, func(context.Context) (*RuntimeView, error) {
		return nil, errors.New("reload failed")
	})

	before := tc.Current()
	if err := tc.ReloadCurrent(context.Background()); err == nil {
		t.Fatal("expected reload error")
	}
	if tc.Current() != before {
		t.Fatal("pointer identity changed after failed reload")
	}
}

func TestToolContextReloadCurrentRejectsIncompleteView(t *testing.T) {
	initial := &RuntimeView{
		Config:   &config.Config{},
		Snapshot: &models.ClusterSnapshot{},
		State:    &state.ClusterState{},
	}
	tc := NewToolContext(initial, func(context.Context) (*RuntimeView, error) {
		return &RuntimeView{
			Config:   &config.Config{},
			Snapshot: nil,
			State:    &state.ClusterState{},
		}, nil
	})

	if err := tc.ReloadCurrent(context.Background()); err == nil {
		t.Fatal("expected error for incomplete view")
	}
	if tc.Current() != initial {
		t.Fatal("initial view should be preserved after rejecting incomplete reload")
	}
}

func TestToolContextReloadCurrentAtomicallyUpdatesView(t *testing.T) {
	initial := &RuntimeView{
		Config:   &config.Config{},
		Snapshot: &models.ClusterSnapshot{},
		State:    &state.ClusterState{},
	}
	updated := &RuntimeView{
		Config:   &config.Config{},
		Snapshot: &models.ClusterSnapshot{Status: "updated"},
		State:    &state.ClusterState{},
	}
	tc := NewToolContext(initial, func(context.Context) (*RuntimeView, error) {
		return updated, nil
	})

	if err := tc.ReloadCurrent(context.Background()); err != nil {
		t.Fatalf("unexpected reload error: %v", err)
	}
	if tc.Current() != updated {
		t.Fatal("Current did not return updated view")
	}
}
