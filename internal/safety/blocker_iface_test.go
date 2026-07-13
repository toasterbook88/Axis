package safety

import "testing"

func TestCheckDecoupledFromKnowledge(t *testing.T) {
	t.Skip("RED: pending fix #1 — see EXECUTION-PLAN.md")

	t.Fatal("expected Check to accept consumer-side safety inputs without knowledge.ClusterKnowledge")
}
