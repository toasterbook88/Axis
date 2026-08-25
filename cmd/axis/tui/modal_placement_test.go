package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
)

func TestPlacementModalReceivesScoreAndConfirmsAdvisorySelection(t *testing.T) {
	modal := NewPlacementModal()
	updated, cmd := modal.Update(placementScoredMsg{
		Decision: &models.PlacementDecision{
			Node:     "node-a",
			FitScore: 82,
			OK:       true,
		},
	})
	if cmd != nil {
		t.Fatalf("score update returned command %v, want nil", cmd)
	}

	modal = updated.(PlacementModal)
	if modal.loading {
		t.Fatal("modal remains loading after score message")
	}
	if modal.decision == nil || modal.decision.Node != "node-a" {
		t.Fatalf("decision = %+v, want node-a", modal.decision)
	}

	updated, cmd = modal.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("confirmation returned command %v, want nil", cmd)
	}
	if !updated.(PlacementModal).confirmed {
		t.Fatal("Enter did not confirm the advisory selection")
	}
}

func TestPlacementModalEscapeCancels(t *testing.T) {
	modal := NewPlacementModal()
	updated, cmd := modal.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if cmd != nil {
		t.Fatalf("escape returned command %v, want nil", cmd)
	}
	if !updated.(PlacementModal).cancelled {
		t.Fatal("Escape did not cancel the modal")
	}
}

func TestPlaceTaskCmdSurfacesMissingSnapshot(t *testing.T) {
	t.Setenv(persist.AxisHomeEnv, t.TempDir())

	msg := placeTaskCmd("run a build")()
	result, ok := msg.(placementScoredMsg)
	if !ok {
		t.Fatalf("placeTaskCmd returned %T, want placementScoredMsg", msg)
	}
	if result.Error == nil {
		t.Fatal("missing snapshot should produce an error")
	}
	if result.Decision != nil {
		t.Fatalf("missing snapshot produced decision %+v", result.Decision)
	}
}
