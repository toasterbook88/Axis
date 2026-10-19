package placement

import (
	"math"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlacementRejectsUnreservableRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		node   models.NodeFacts
		reqs   models.TaskRequirements
		wantOK bool
		reason string
	}{
		{"default request below capacity", nodeComplete("worker", 1023, "low"), models.TaskRequirements{}, false, "reservation cap exceeded"},
		{"default request at capacity", nodeComplete("worker", 1024, "low"), models.TaskRequirements{}, true, ""},
		{"minimum plus execution overhead", nodeComplete("worker", 4096, "low"), models.TaskRequirements{MinFreeRAMMB: 4096}, false, "reservation cap exceeded"},
		{"minimum plus overhead at capacity", nodeComplete("worker", 5120, "low"), models.TaskRequirements{MinFreeRAMMB: 4096}, true, ""},
		{"explicit request replaces overhead", nodeComplete("worker", 512, "low"), models.TaskRequirements{MemoryRequestMB: 512}, true, ""},
		{"missing capacity", models.NodeFacts{Name: "worker", Status: models.StatusComplete}, models.TaskRequirements{}, false, "reservation capacity unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes := []models.NodeFacts{tc.node}
			decision := SelectBestNode(tc.reqs, nodes, nil)
			explanation := ExplainPlacement(tc.reqs, nodes, nil)
			if decision.OK != tc.wantOK || explanation.Decision.OK != tc.wantOK {
				t.Fatalf("want eligible=%v; decision=%+v explanation=%+v", tc.wantOK, decision, explanation)
			}
			if !tc.wantOK && (len(explanation.Excluded) != 1 || !strings.Contains(strings.Join(explanation.Excluded[0].Reasons, "; "), tc.reason)) {
				t.Fatalf("expected reservation exclusion, got %+v", explanation.Excluded)
			}
			if !tc.wantOK && !strings.Contains(strings.Join(decision.Reasoning, "; "), tc.reason) {
				t.Fatalf("decision must report the same exclusion: %+v", decision)
			}
		})
	}
}

func TestReservationBudgetBoundaries(t *testing.T) {
	node := nodeComplete("worker", 6000, "low")
	node.SystemReserveMB = 3000 // 8192 - 3000 = 5192MB reservable.
	node.RAMReservedMB = 4192
	for _, tc := range []struct {
		name    string
		request int64
		want    bool
	}{
		{"exact remaining capacity", 1000, true},
		{"over remaining capacity", 1001, false},
		{"integer overflow must not fit", math.MaxInt64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanReserveRAM(node, tc.request); got != tc.want {
				t.Fatalf("CanReserveRAM(%d)=%v, want %v", tc.request, got, tc.want)
			}
		})
	}
	if got := ReservationMBForRequirements(models.TaskRequirements{MinFreeRAMMB: math.MaxInt64}); got != math.MaxInt64 {
		t.Fatalf("overflowed reservation request: %d", got)
	}
}

func TestPlacementReservationCapOverridesAllocatableHint(t *testing.T) {
	busy := nodeComplete("busy", 30000, "low")
	busy.Resources.RAMTotalMB = 32768
	busy.RAMAllocatableMB = 25000
	busy.RAMReservedMB = 29500
	available := nodeComplete("available", 4096, "low")
	nodes := []models.NodeFacts{busy, available}
	decision := SelectBestNode(models.TaskRequirements{}, nodes, nil)
	explanation := ExplainPlacement(models.TaskRequirements{}, nodes, nil)
	if !decision.OK || decision.Node != "available" || explanation.Decision.Node != decision.Node {
		t.Fatalf("expected available worker, got decision=%+v explanation=%+v", decision, explanation)
	}
	if len(explanation.Excluded) != 1 || explanation.Excluded[0].Node != "busy" {
		t.Fatalf("expected busy worker excluded, got %+v", explanation.Excluded)
	}
}
