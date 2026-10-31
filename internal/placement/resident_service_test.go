package placement

import (
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// A resident memory service (a RAM cache or scratch store) can grow to its
// cap at any time without telling AXIS. docs/runbooks/resident-memory-services.md
// tells operators to set system_reserve_mb to everything outside AXIS at its
// peak: other use now, plus the service's cap, plus a margin. These tests pin
// the behaviour that advice relies on.

const (
	residentTotalMB  = 16384
	residentIdleFree = 14000 // the cache is empty; other processes use the rest
	residentOtherMB  = residentTotalMB - residentIdleFree
	residentCapMB    = 3072
	residentMarginMB = 512
	residentReserve  = residentOtherMB + residentCapMB + residentMarginMB
	residentCeiling  = residentTotalMB - residentReserve
)

// residentNode builds a node the way a published snapshot has it: nodes.yaml's
// system_reserve_mb copied into the facts by discovery, then the ranking
// figure set to reservable minus reserved, as snapshotview's overlay does.
func residentNode(freeMB, systemReserveMB, reservedMB int64) models.NodeFacts {
	n := models.NodeFacts{
		Name:   "worker",
		Status: models.StatusComplete,
		Resources: &models.Resources{
			CPUCores:   8,
			RAMTotalMB: residentTotalMB,
			RAMFreeMB:  freeMB,
			Pressure:   "low",
		},
		SystemReserveMB: systemReserveMB,
		RAMReservedMB:   reservedMB,
		CollectedAt:     time.Now().UTC(),
	}
	n.PopulateMemoryMetrics()
	n.RAMAllocatableMB = max(n.ReservableRAM()-reservedMB, 0)
	return n
}

// With the recipe's reserve, the largest request AXIS accepts still leaves
// room for the idle cache to grow to its cap.
func TestResidentServiceLargestAcceptedTaskLeavesRoomForTheCap(t *testing.T) {
	node := residentNode(residentIdleFree, residentReserve, 0)
	budget := node.ReservableRAM()
	if budget != residentCeiling {
		t.Fatalf("reservable = %d, want total - system_reserve_mb = %d", budget, residentCeiling)
	}
	if !CanReserveRAM(node, budget) || CanReserveRAM(node, budget+1) {
		t.Fatalf("the budget boundary moved: CanReserveRAM(%d)=%v, (%d)=%v",
			budget, CanReserveRAM(node, budget), budget+1, CanReserveRAM(node, budget+1))
	}
	if peak := residentOtherMB + budget + residentCapMB; peak > residentTotalMB {
		t.Fatalf("other use + largest task + cap = %d MB, over the %d MB node", peak, residentTotalMB)
	}

	reqs := models.TaskRequirements{MemoryRequestMB: budget + 1}
	explanation := ExplainPlacement(reqs, []models.NodeFacts{node}, nil)
	if explanation.Decision.OK {
		t.Fatalf("a request over the ceiling was placed: %+v", explanation.Decision)
	}
	if len(explanation.Excluded) != 1 || !strings.Contains(strings.Join(explanation.Excluded[0].Reasons, "; "), "reservation cap exceeded") {
		t.Fatalf("want a reservation-cap exclusion, got %+v", explanation.Excluded)
	}
}

// Reserving only OS headroom plus the cap ignores what other processes already
// use, so a task AXIS accepts can leave the cache too little room. This is why
// the runbook's recipe counts other use.
func TestResidentServiceReserveWithoutOtherUseOverflows(t *testing.T) {
	naive := residentNode(residentIdleFree, models.MinimumSystemReserveMB+residentCapMB, 0)
	budget := naive.ReservableRAM()
	if !CanReserveRAM(naive, budget) {
		t.Fatalf("CanReserveRAM(%d) = false on the naive reserve", budget)
	}
	if peak := residentOtherMB + budget + residentCapMB; peak <= residentTotalMB {
		t.Fatalf("expected the naive reserve to overflow; peak = %d MB of %d", peak, residentTotalMB)
	}
}

// The reserve is a ceiling on AXIS reservations, not a deduction from free
// RAM: once free RAM falls below the ceiling (the cache filled, or other use
// grew), the budget is free RAM, and the reserve is not subtracted again.
func TestResidentServiceReserveIsACeilingNotADeduction(t *testing.T) {
	full := residentNode(residentIdleFree-residentCapMB, residentReserve, 0)
	if got := full.ReservableRAM(); got != min(int64(residentIdleFree-residentCapMB), residentCeiling) {
		t.Fatalf("reservable with the cache full = %d, want min(free, ceiling)", got)
	}

	const busyFree = 9000
	busy := residentNode(busyFree, residentReserve, 0)
	if got := busy.ReservableRAM(); got != busyFree {
		t.Fatalf("reservable = %d, want free RAM %d (the reserve must not be subtracted from it)", got, busyFree)
	}
	if !SelectBestNode(models.TaskRequirements{MemoryRequestMB: busyFree}, []models.NodeFacts{busy}, nil).OK {
		t.Fatalf("a request equal to free RAM below the ceiling was rejected")
	}
}

// Tasks AXIS has already reserved come out of the same ceiling.
func TestResidentServiceReserveBoundsReservationBudget(t *testing.T) {
	const reserved = 6000
	node := residentNode(residentIdleFree, residentReserve, reserved)
	remaining := int64(residentCeiling - reserved)
	if !CanReserveRAM(node, remaining) || CanReserveRAM(node, remaining+1) {
		t.Fatalf("remaining budget is not %d: CanReserveRAM(%d)=%v, (%d)=%v",
			remaining, remaining, CanReserveRAM(node, remaining), remaining+1, CanReserveRAM(node, remaining+1))
	}
	if got := allocatableRAM(node); got != remaining {
		t.Fatalf("ranking figure = %d, want reservable - reserved = %d", got, remaining)
	}
}
