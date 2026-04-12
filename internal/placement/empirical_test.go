package placement

import (
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestRankCandidatesPrefersFreshEmpiricalObservationWhenAllocatableTied(t *testing.T) {
	alpha := nodeComplete("alpha", 6000, "none", "git")
	beta := nodeComplete("beta", 6000, "none", "git")
	reqs := models.TaskRequirements{
		RequiredTools: []string{"git"},
		Workload:      models.WorkloadProfileMatch{Class: models.ClassRepoAnalysis},
	}
	st := &state.ClusterState{
		Observations: map[string]models.ExecutionObservation{},
	}
	st.RecordObservation(models.ExecutionObservation{
		Scope:       ObservationScopeForRequirements("alpha", reqs, "git"),
		ObservedAt:  time.Now().UTC(),
		SampleCount: 1,
		LastSuccess: true,
		WallTimeMS:  6000,
	})
	st.RecordObservation(models.ExecutionObservation{
		Scope:       ObservationScopeForRequirements("beta", reqs, "git"),
		ObservedAt:  time.Now().UTC(),
		SampleCount: 1,
		LastSuccess: true,
		WallTimeMS:  1500,
	})

	ranked := RankCandidates([]models.NodeFacts{alpha, beta}, reqs, st)
	if ranked[0].Name != "beta" {
		t.Fatalf("expected beta first on faster fresh empirical history, got %s", ranked[0].Name)
	}
}

func TestRankCandidatesIgnoresStaleObservations(t *testing.T) {
	alpha := nodeComplete("alpha", 6000, "none", "git")
	beta := nodeComplete("beta", 6000, "none", "git")
	reqs := models.TaskRequirements{
		RequiredTools: []string{"git"},
		Workload:      models.WorkloadProfileMatch{Class: models.ClassRepoAnalysis},
	}
	st := &state.ClusterState{
		Observations: map[string]models.ExecutionObservation{},
	}
	st.RecordObservation(models.ExecutionObservation{
		Scope:       ObservationScopeForRequirements("beta", reqs, "git"),
		ObservedAt:  time.Now().UTC().Add(-(state.ObservationStaleAfter + time.Minute)),
		SampleCount: 1,
		LastSuccess: true,
		WallTimeMS:  1,
	})

	ranked := RankCandidates([]models.NodeFacts{beta, alpha}, reqs, st)
	if ranked[0].Name != "alpha" {
		t.Fatalf("expected stale observation to be ignored and alphabetical tie-break to win, got %s", ranked[0].Name)
	}
}

func TestRankCandidatesPrefersResidentModelLocality(t *testing.T) {
	cold := nodeComplete("cold", 6000, "none", "ollama")
	warm := nodeComplete("warm", 6000, "none", "ollama")
	warm.Ollama = &models.OllamaInfo{Installed: true, Running: true}
	warm.ResidentModels = []models.ResidentModel{
		{Name: "llama3:8b", Runtime: "ollama", Source: "ollama-ps"},
	}

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLocalLLMInference},
	}

	ranked := RankCandidates([]models.NodeFacts{cold, warm}, reqs, nil)
	if ranked[0].Name != "warm" {
		t.Fatalf("expected resident ollama model locality to win, got %s", ranked[0].Name)
	}
}

func TestSelectBestNodeReasoningMentionsEmpiricalAndResidentModelSignals(t *testing.T) {
	alpha := nodeComplete("alpha", 6000, "none", "ollama")
	alpha.Ollama = &models.OllamaInfo{Installed: true, Running: true}
	alpha.ResidentModels = []models.ResidentModel{
		{Name: "llama3:8b", Runtime: "ollama", Source: "ollama-ps"},
	}
	beta := nodeComplete("beta", 6000, "none", "ollama")

	reqs := models.TaskRequirements{
		RequiredTools: []string{"ollama"},
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLocalLLMInference},
	}
	st := &state.ClusterState{
		Observations: map[string]models.ExecutionObservation{},
	}
	st.RecordObservation(models.ExecutionObservation{
		Scope:       ObservationScopeForRequirements("alpha", reqs, "ollama"),
		ObservedAt:  time.Now().UTC(),
		SampleCount: 2,
		LastSuccess: true,
		WallTimeMS:  1200,
	})

	decision := SelectBestNode(reqs, []models.NodeFacts{beta, alpha}, st)
	reasoning := strings.Join(decision.Reasoning, "\n")
	if !strings.Contains(reasoning, "empirical history:") {
		t.Fatalf("expected empirical reasoning, got %v", decision.Reasoning)
	}
	if !strings.Contains(reasoning, "resident model locality:") {
		t.Fatalf("expected resident model reasoning, got %v", decision.Reasoning)
	}
}
