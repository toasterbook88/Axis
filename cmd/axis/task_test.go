package main

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
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
					RAMFreeMB: 833,
					Pressure:  "low",
				},
			},
		},
		Summary: models.ClusterSummary{
			TotalNodes:     2,
			TotalFreeRAMMB: 833,
		},
	}

	out := buildContextBlock(snap, models.TaskRequirements{MinFreeRAMMB: 4096}, "analyze repo", "daemon-cache")

	if !strings.Contains(out, "Best node: m3") {
		t.Fatalf("expected context block to choose node with resources, got:\n%s", out)
	}
	if !strings.Contains(out, "Source: daemon-cache") {
		t.Fatalf("expected source line in context block, got:\n%s", out)
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
