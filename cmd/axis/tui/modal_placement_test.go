package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/toasterbook88/axis/internal/models"
)

func TestPlacementModalReceivesScoreAndConfirmsAdvisorySelection(t *testing.T) {
	modal := NewPlacementModal(&models.ClusterSnapshot{}, "daemon-cache", "9:41AM")
	updated, cmd := modal.Update(placementScoredMsg{
		Requirements: models.TaskRequirements{
			Workload: models.WorkloadProfileMatch{Class: models.WorkloadClass("build")},
		},
		Explanation: &models.PlacementExplanation{
			Decision: models.PlacementDecision{
				Node:     "node-a",
				FitScore: 82,
				OK:       true,
			},
		},
	})
	if cmd != nil {
		t.Fatalf("score update returned command %v, want nil", cmd)
	}

	modal = updated.(PlacementModal)
	if modal.loading {
		t.Fatal("modal remains loading after score message")
	}
	if modal.explanation == nil || modal.explanation.Decision.Node != "node-a" {
		t.Fatalf("explanation = %+v, want node-a", modal.explanation)
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
	modal := NewPlacementModal(nil, "", "")
	updated, cmd := modal.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if cmd != nil {
		t.Fatalf("escape returned command %v, want nil", cmd)
	}
	if !updated.(PlacementModal).cancelled {
		t.Fatal("Escape did not cancel the modal")
	}
}

func TestPlaceTaskCmdRejectsMissingDisplayedSnapshot(t *testing.T) {
	msg := placeTaskCmd("run a build", nil)()
	result, ok := msg.(placementScoredMsg)
	if !ok {
		t.Fatalf("placeTaskCmd returned %T, want placementScoredMsg", msg)
	}
	if result.Error == nil {
		t.Fatal("missing displayed snapshot should produce an error")
	}
	if result.Explanation != nil {
		t.Fatalf("missing snapshot produced explanation %+v", result.Explanation)
	}
}

func TestPlacementModalRendersAuthorityRequirementsAndRankOrder(t *testing.T) {
	snapshot := &models.ClusterSnapshot{Nodes: []models.NodeFacts{
		{Name: "node-a", ResidentModels: []models.ResidentModel{{Name: "model"}}, Resources: &models.Resources{GPUs: []models.GPUInfo{{Model: "GPU"}}}},
		{Name: "node-b"},
	}}
	modal := NewPlacementModal(snapshot, "daemon-cache", "9:41AM")
	modal.input.SetValue("run inference")
	modal.requirements = models.TaskRequirements{
		Workload:          models.WorkloadProfileMatch{Class: models.WorkloadClass("local-llm-inference")},
		MinFreeRAMMB:      8192,
		RequiredTools:     []string{"llama.cpp"},
		PreferredBackends: []string{"llama.cpp"},
	}
	modal.explanation = &models.PlacementExplanation{
		Decision: models.PlacementDecision{
			Node:      "node-a",
			FitScore:  60,
			OK:        true,
			Reasoning: []string{"more allocatable RAM"},
		},
		Eligible: []models.PlacementCandidateExplanation{
			{Node: "node-a", HeadroomMB: 16384},
			{Node: "node-b", HeadroomMB: 8192},
		},
	}

	out := stripANSI(modal.View())
	for _, want := range []string{
		"Snapshot: daemon-cache · observed 9:41AM",
		"class local-llm-inference",
		"RAM ≥ 8.0GiB",
		"Recommendation: node-a",
		"Diagnostic fit: 60/100 · rank order is authoritative",
		"1. node-a · headroom 16.0GiB · 1 GPU · 1 warm model",
		"2. node-b · headroom 8.0GiB",
		"Advisory only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("placement view missing %q in:\n%s", want, out)
		}
	}
}
