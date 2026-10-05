package placement

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

// Integration coverage for the required-label seam: evaluateCandidate must
// exclude nodes that fail the RequiredLabels conjunction, with the reason
// strings surfaced in ExplainPlacement.Excluded. This is the seam the CLI
// flag (--require-label) feeds — regression here silently breaks the feature.

func labeledNode(name string, labels map[string]string) models.NodeFacts {
	n := nodeComplete(name, 2048, "low")
	n.Labels = labels
	return n
}

func TestEvaluateCandidate_RequiredLabelsExclude(t *testing.T) {
	st := &state.ClusterState{}
	reqs := models.TaskRequirements{
		Description:    "probe",
		RequiredLabels: map[string]string{"os": "linux"},
	}

	t.Run("unlabeled node excluded", func(t *testing.T) {
		n := labeledNode("plain", nil)
		ev := evaluateCandidate(reqs, n, st, "")
		if ev.Eligible() {
			t.Fatal("unlabeled node must be excluded when a label is required")
		}
		found := false
		for _, r := range ev.ExclusionReasons {
			if strings.Contains(r, "missing required label: os=linux") {
				found = true
			}
		}
		if !found {
			t.Fatalf("exclusion reason missing; got %v", ev.ExclusionReasons)
		}
	})

	t.Run("matching node eligible", func(t *testing.T) {
		n := labeledNode("linux-box", map[string]string{"os": "linux"})
		ev := evaluateCandidate(reqs, n, st, "")
		if !ev.Eligible() {
			t.Fatalf("matching node must be eligible; got %v", ev.ExclusionReasons)
		}
	})

	t.Run("mismatched value excluded with reason", func(t *testing.T) {
		n := labeledNode("mac", map[string]string{"os": "darwin"})
		ev := evaluateCandidate(reqs, n, st, "")
		if ev.Eligible() {
			t.Fatal("mismatched value must exclude")
		}
		found := false
		for _, r := range ev.ExclusionReasons {
			if strings.Contains(r, "required label mismatch: os=linux (node has os=darwin)") {
				found = true
			}
		}
		if !found {
			t.Fatalf("mismatch reason missing; got %v", ev.ExclusionReasons)
		}
	})
}

func TestExplainPlacement_LabelExclusionsSurfaced(t *testing.T) {
	st := &state.ClusterState{}
	nodes := []models.NodeFacts{
		labeledNode("linux-box", map[string]string{"os": "linux"}),
		labeledNode("mac", map[string]string{"os": "darwin"}),
		labeledNode("bare", nil),
	}
	reqs := models.TaskRequirements{
		Description:    "probe",
		RequiredLabels: map[string]string{"os": "linux"},
	}

	explanation := ExplainPlacement(reqs, nodes, st)

	if !explanation.Decision.OK {
		t.Fatalf("expected linux-box to qualify; reasoning: %v", explanation.Decision.Reasoning)
	}
	if explanation.Decision.Node != "linux-box" {
		t.Fatalf("expected linux-box, got %q", explanation.Decision.Node)
	}

	excludedByName := map[string]models.PlacementExclusion{}
	for _, ex := range explanation.Excluded {
		excludedByName[ex.Node] = ex
	}
	for _, name := range []string{"mac", "bare"} {
		ex, ok := excludedByName[name]
		if !ok {
			t.Fatalf("node %q missing from Excluded", name)
		}
		if len(ex.Reasons) == 0 {
			t.Fatalf("node %q excluded without reasons", name)
		}
	}
}
