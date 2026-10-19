package placement

import (
	"fmt"
	"math"

	"github.com/toasterbook88/axis/internal/models"
)

// ReservationMBForRequirements is the request used by placement and guarded
// execution: an explicit memory request, or the minimum plus 1024MB overhead.
func ReservationMBForRequirements(reqs models.TaskRequirements) int64 {
	if request := reqs.GetMemoryRequestMB(); request > 0 {
		return request
	}
	if reqs.MinFreeRAMMB > math.MaxInt64-1024 {
		return math.MaxInt64
	}
	return reqs.MinFreeRAMMB + 1024
}

// CanReserveRAM checks the snapshot's reservation budget without acquiring a
// lease. Execution must still acquire its reservation from the ledger.
func CanReserveRAM(node models.NodeFacts, requestMB int64) bool {
	if requestMB <= 0 {
		return true
	}
	reserved := max(node.RAMReservedMB, 0)
	capacity := node.ReservableRAM()
	return reserved <= capacity && requestMB <= capacity-reserved
}

func reservationExclusionReasons(reqs models.TaskRequirements, node models.NodeFacts) []string {
	if node.Resources == nil {
		return []string{"reservation capacity unavailable: resource facts missing"}
	}
	request := ReservationMBForRequirements(reqs)
	if CanReserveRAM(node, request) {
		return nil
	}
	return []string{fmt.Sprintf("reservation cap exceeded: need %dMB, have %dMB reserved of %dMB reservable", request, node.RAMReservedMB, node.ReservableRAM())}
}
