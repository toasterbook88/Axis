package agent

import (
	"strings"
	"testing"

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
