package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestSummarizePlacementDecisionUsesStructuredRanking(t *testing.T) {
	dec := models.PlacementDecision{
		OK:   true,
		Node: "node-a",
		Ranking: &models.PlacementRanking{
			Objective: models.PlacementObjectiveCapacity,
			Source:    models.ObjectiveSourceDefault,
			Metric: models.RankingMetric{
				Name:       "allocatable_ram",
				Value:      0,
				Unit:       "MB",
				Provenance: models.MetricProvenanceDerived,
			},
			DecisiveBy: models.RankingCriterionOnlyCandidate,
		},
	}

	got := summarizePlacementDecision(dec)
	for _, want := range []string{"allocatable_ram = 0MB", "provenance: derived", "objective: capacity", "only_candidate"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q: %s", want, got)
		}
	}
}

func TestSummarizeSnapshotIncludesAge(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().Add(-35 * time.Second),
		Summary: models.ClusterSummary{
			TotalNodes:     2,
			ReachableNodes: 2,
		},
		Status: models.SnapshotHealthy,
	}

	got := summarizeSnapshot(snap)
	if !strings.Contains(got, "age: 35s") {
		t.Fatalf("expected snapshot summary to contain 'age: 35s', got: %s", got)
	}
}

func TestSummarizePlacementExplanationIncludesRunnerUpsAndExcluded(t *testing.T) {
	exp := models.PlacementExplanation{
		Decision: models.PlacementDecision{
			OK:       true,
			Node:     "cranium",
			FitScore: 90,
			IsLocal:  true,
			Ranking: &models.PlacementRanking{
				Objective:  models.PlacementObjectiveCapacity,
				Source:     models.ObjectiveSourceDefault,
				DecisiveBy: "allocatable_ram",
				Metric: models.RankingMetric{
					Name:       "allocatable_ram",
					Value:      29398,
					Unit:       "MB",
					Provenance: models.MetricProvenanceDerived,
				},
			},
			Reasoning: []string{"Local node fits without network transfer"},
		},
		Eligible: []models.PlacementCandidateExplanation{
			{
				Node:       "cranium",
				FitScore:   90,
				IsLocal:    true,
				HeadroomMB: 29398,
				Metric: models.RankingMetric{
					Name:  "allocatable_ram",
					Value: 29398,
					Unit:  "MB",
				},
			},
			{
				Node:       "cachyos",
				FitScore:   95,
				IsLocal:    false,
				HeadroomMB: 146038,
				Metric: models.RankingMetric{
					Name:  "allocatable_ram",
					Value: 146038,
					Unit:  "MB",
				},
			},
		},
		Excluded: []models.PlacementExclusion{
			{
				Node:    "axis5",
				Reasons: []string{"insufficient RAM: 3122 MB free < 16384 MB required"},
			},
		},
	}

	got := summarizePlacementExplanation(exp)
	for _, want := range []string{
		"Placement: cranium (allocatable_ram = 29398MB",
		"Runner-up candidates:",
		"- cachyos: allocatable_ram = 146038MB",
		"Excluded nodes:",
		"- axis5: insufficient RAM",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
}
