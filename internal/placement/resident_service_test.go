package placement

import (
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// A resident memory service (a RAM cache or scratch store) can grow to its
// cap at any time without telling AXIS. docs/runbooks/resident-memory-services.md
// tells operators to add that cap to the node's system_reserve_mb; these tests
// pin the behaviour that advice relies on.

const (
	residentTotalMB = 16384
	residentCapMB   = 3072
	osReserveMB     = models.MinimumSystemReserveMB
)

// residentNode builds a node the way discovery does: nodes.yaml's
// system_reserve_mb copied into the facts, then the memory metrics filled in.
func residentNode(freeMB, systemReserveMB int64) models.NodeFacts {
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
		CollectedAt:     time.Now().UTC(),
	}
	n.PopulateMemoryMetrics()
	return n
}

// While the service is idle its cap is still free RAM, so only the reserve
// keeps placement from promising it to a task.
func TestResidentServiceCapIsHeldOutWhileIdle(t *testing.T) {
	const freeMB = 14000
	reqs := models.TaskRequirements{MemoryRequestMB: 13000}

	without := residentNode(freeMB, 0)
	if !SelectBestNode(reqs, []models.NodeFacts{without}, nil).OK {
		t.Fatalf("without a reserve the request fits free RAM: reservable=%d", without.ReservableRAM())
	}

	with := residentNode(freeMB, osReserveMB+residentCapMB)
	if got, want := with.ReservableRAM(), int64(residentTotalMB-osReserveMB-residentCapMB); got != want {
		t.Fatalf("reservable with the service cap reserved = %d, want total - OS reserve - cap = %d", got, want)
	}
	explanation := ExplainPlacement(reqs, []models.NodeFacts{with}, nil)
	if explanation.Decision.OK {
		t.Fatalf("the service's cap was promised to a task: %+v", explanation.Decision)
	}
	if len(explanation.Excluded) != 1 || !strings.Contains(strings.Join(explanation.Excluded[0].Reasons, "; "), "reservation cap exceeded") {
		t.Fatalf("want a reservation-cap exclusion, got %+v", explanation.Excluded)
	}
}

// Once the service has filled its cap, that memory is no longer free, and the
// reserve must not subtract it a second time.
func TestResidentServiceAtCapIsNotCountedTwice(t *testing.T) {
	const freeMB = 14000 - residentCapMB
	node := residentNode(freeMB, osReserveMB+residentCapMB)
	if got := node.ReservableRAM(); got != freeMB {
		t.Fatalf("reservable = %d, want the observed free RAM %d (the service's use already left free RAM)", got, freeMB)
	}
	reqs := models.TaskRequirements{MemoryRequestMB: freeMB}
	if !SelectBestNode(reqs, []models.NodeFacts{node}, nil).OK {
		t.Fatalf("a request equal to free RAM was rejected: the service was counted twice")
	}
	if CanReserveRAM(node, freeMB+1) {
		t.Fatalf("a request above free RAM was accepted")
	}
}

// The reserve also bounds the reservation budget, so tasks already reserved
// on the node and the service's cap together cannot exceed total RAM.
func TestResidentServiceReserveBoundsReservationBudget(t *testing.T) {
	node := residentNode(14000, osReserveMB+residentCapMB)
	node.RAMReservedMB = 10000
	budget := int64(residentTotalMB-osReserveMB-residentCapMB) - node.RAMReservedMB
	if !CanReserveRAM(node, budget) {
		t.Fatalf("CanReserveRAM(%d) = false, want true at the remaining budget", budget)
	}
	if CanReserveRAM(node, budget+1) {
		t.Fatalf("CanReserveRAM(%d) = true, want false one MB over the remaining budget", budget+1)
	}
}
