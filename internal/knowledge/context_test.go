package knowledge

import (
	"encoding/json"
	"testing"

	"github.com/toasterbook88/axis/internal/git"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestBuildIsNilSafe(t *testing.T) {
	k := Build(nil, nil, "")
	if k == nil {
		t.Fatal("expected knowledge")
	}
	if len(k.Snapshot.Nodes) != 0 {
		t.Fatalf("expected empty snapshot nodes, got %d", len(k.Snapshot.Nodes))
	}
	if len(k.Load) != 0 {
		t.Fatalf("expected empty load map, got %v", k.Load)
	}
}

func TestExecutionContextJSONPreservesTopLevelKeys(t *testing.T) {
	prevGit := GetGitRepoState
	GetGitRepoState = func(string) (git.RepoState, error) {
		return git.RepoState{IsRepo: true, Branch: "feature-test"}, nil
	}
	t.Cleanup(func() { GetGitRepoState = prevGit })

	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  4096,
					Load1M:     2.25,
				},
			},
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 512},
		},
	}
	decision := models.PlacementDecision{Node: "alpha", OK: true}

	data, err := ExecutionContextJSON(
		snap,
		st,
		decision,
		"run tests",
		map[string]any{"name": "test-script"},
		map[string]any{"name": "test-skill"},
	)
	if err != nil {
		t.Fatalf("ExecutionContextJSON() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	for _, key := range []string{"timestamp", "best_node", "snapshot", "state", "ollama", "load", "decision", "task_desc", "git", "script", "skill"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected key %q in execution payload", key)
		}
	}

	loadMap, ok := payload["load"].(map[string]any)
	if !ok {
		t.Fatalf("expected load map, got %#v", payload["load"])
	}
	if loadMap["alpha"] != 2.25 {
		t.Fatalf("expected load alpha 2.25, got %#v", loadMap["alpha"])
	}
}

func TestClusterKnowledgeJSON(t *testing.T) {
	k := Build(&models.ClusterSnapshot{}, nil, "")
	if got := k.JSON(); got == "" {
		t.Fatal("expected JSON output")
	}
}

func TestClusterKnowledgeIncludesGit(t *testing.T) {
	prevGit := GetGitRepoState
	GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{
			IsRepo: true,
			Branch: "feature-test",
		}, nil
	}
	t.Cleanup(func() {
		GetGitRepoState = prevGit
	})

	k := Build(&models.ClusterSnapshot{}, nil, "")
	if k.Git == nil {
		t.Fatal("expected Git field to be populated")
	}
	if !k.Git.IsRepo {
		t.Fatal("expected k.Git.IsRepo to be true")
	}
	if k.Git.Branch != "feature-test" {
		t.Fatalf("expected branch 'feature-test', got %q", k.Git.Branch)
	}
}
