package main

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func TestBuildContextBlockPrefersNodeWithResources(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:   "m1",
				Status: models.StatusUnreachable,
			},
			{
				Name: "m3",
				Tools: []models.ToolInfo{
					{Name: "git"},
				},
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB:        833,
					RAMReservedMB:    256,
					RAMAllocatableMB: 577,
					Pressure:         "low",
				},
			},
		},
		Summary: models.ClusterSummary{
			TotalNodes:         2,
			TotalFreeRAMMB:     833,
			TotalAllocatableMB: 577,
			TotalReservedMB:    256,
		},
	}

	out := buildContextBlock(snap, models.TaskRequirements{MinFreeRAMMB: 4096}, "analyze repo", "daemon-cache")

	if !strings.Contains(out, "Best node: m3") {
		t.Fatalf("expected context block to choose node with resources, got:\n%s", out)
	}
	if !strings.Contains(out, "Source: daemon-cache") {
		t.Fatalf("expected source line in context block, got:\n%s", out)
	}
	if !strings.Contains(out, "577MB allocatable (256MB reserved)") {
		t.Fatalf("expected allocatable RAM line in context block, got:\n%s", out)
	}
	if !strings.Contains(out, "2 nodes, 577MB allocatable across cluster (256MB reserved)") {
		t.Fatalf("expected allocatable cluster summary in context block, got:\n%s", out)
	}
	if !strings.Contains(out, "axis mcp serve") {
		t.Fatalf("expected MCP hint in context block, got:\n%s", out)
	}
}

func TestResolveTaskRunIntentRequiresExplicitForRawInput(t *testing.T) {
	_, err := resolveTaskRunIntent("totally custom raw command", false, false, &skills.Store{})
	if err == nil {
		t.Fatal("expected refusal for implicit raw execution")
	}
	if !strings.Contains(err.Error(), "refusing to execute implicitly") {
		t.Fatalf("expected explicit-execution error, got %v", err)
	}
}

func TestResolveTaskRunIntentSuggestsKnownScriptWithoutExecuting(t *testing.T) {
	intent, err := resolveTaskRunIntent("git status", false, false, &skills.Store{})
	if err != nil {
		t.Fatalf("expected script suggestion, got %v", err)
	}
	if !intent.requiresConfirmation {
		t.Fatal("expected known script to require confirmation")
	}
	if intent.matchedScript == nil {
		t.Fatal("expected matched script")
	}
	if intent.command == "" {
		t.Fatal("expected suggested command")
	}
}

func TestResolveTaskRunIntentRunsKnownScriptWithScriptFlag(t *testing.T) {
	intent, err := resolveTaskRunIntent("git status", false, true, &skills.Store{})
	if err != nil {
		t.Fatalf("expected known script to run with --script, got %v", err)
	}
	if intent.requiresConfirmation {
		t.Fatal("did not expect confirmation gate with --script")
	}
	if intent.matchedScript == nil {
		t.Fatal("expected matched script")
	}
	if intent.command != intent.matchedScript.Command {
		t.Fatalf("expected script command, got %q", intent.command)
	}
}

func TestResolveTaskRunIntentPrefersRawExec(t *testing.T) {
	intent, err := resolveTaskRunIntent("echo hello", true, false, &skills.Store{})
	if err != nil {
		t.Fatalf("expected raw exec plan, got %v", err)
	}
	if intent.command != "echo hello" {
		t.Fatalf("expected raw command, got %q", intent.command)
	}
	if intent.requiresConfirmation {
		t.Fatal("raw exec should not require confirmation")
	}
}

func TestReservationMBForRequirementsAddsHeadroom(t *testing.T) {
	reqs := models.TaskRequirements{MinFreeRAMMB: 4096}
	if got := reservationMBForRequirements(reqs); got != 5120 {
		t.Fatalf("reservationMBForRequirements() = %d, want 5120", got)
	}
}

func TestEnsureReservationCapacityRejectsOverCapNode(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:   "alpha",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMTotalMB: 8192,
				},
			},
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 7168},
		},
	}

	err := ensureReservationCapacity(snap, st, "alpha", 1025)
	if err == nil {
		t.Fatal("expected reservation capacity error")
	}
	if !strings.Contains(err.Error(), "cannot reserve") {
		t.Fatalf("expected reservation error message, got %v", err)
	}
}

func TestEnsureReservationCapacityMatchesDaemonCapLogic(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:   "alpha",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMTotalMB: 8192,
				},
			},
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 2048},
		},
	}

	if err := ensureReservationCapacity(snap, st, "alpha", 1024); err != nil {
		t.Fatalf("expected reservation to fit cap, got %v", err)
	}
	if !daemon.CanReserve(snap, st, "alpha", 1024) {
		t.Fatal("expected daemon cap logic to agree with helper")
	}
}
