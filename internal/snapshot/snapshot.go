// Package snapshot assembles a ClusterSnapshot from collected NodeFacts.
package snapshot

import (
	"fmt"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// Build assembles a ClusterSnapshot from node facts.
// Computes cluster-level aggregates, generates warnings, assigns snapshot status.
// Rule: any node with status != complete → snapshot is degraded.
func Build(nodes []models.NodeFacts) *models.ClusterSnapshot {
	var deduped []models.NodeFacts
	seenStableID := make(map[string]bool)
	seenName := make(map[string]bool)

	// Pass 1: Config nodes win.
	for _, n := range nodes {
		isConfig := (n.Epistemic != nil && n.Epistemic.VerifiedBy == models.VerifiedByConfig) || (n.Role != "")
		if !isConfig {
			continue
		}
		deduped = append(deduped, n)
		if n.Identity != nil && n.Identity.StableID != "" {
			seenStableID[n.Identity.StableID] = true
		}
		seenName[n.Name] = true
	}

	// Pass 2: Mesh/Beacon nodes.
	for _, n := range nodes {
		isConfig := (n.Epistemic != nil && n.Epistemic.VerifiedBy == models.VerifiedByConfig) || (n.Role != "")
		if isConfig {
			continue
		}
		if n.Identity != nil && n.Identity.StableID != "" {
			if seenStableID[n.Identity.StableID] {
				continue // deduplicated
			}
			seenStableID[n.Identity.StableID] = true
		}
		if seenName[n.Name] {
			continue // deduplicated by name to be safe
		}
		seenName[n.Name] = true
		deduped = append(deduped, n)
	}

	snap := &models.ClusterSnapshot{
		Timestamp: time.Now().UTC(),
		Status:    models.SnapshotHealthy,
		Nodes:     deduped,
	}

	var totalRAM, freeRAM, reservableRAM, allocatableRAM, reservedRAM int64
	reachable := 0

	for _, n := range deduped {
		// Count reachable and aggregate resources
		if n.Status == models.StatusComplete || n.Status == models.StatusPartial {
			reachable++
			if n.Resources != nil {
				totalRAM += n.Resources.RAMTotalMB
				freeRAM += n.Resources.RAMFreeMB
				reserved := n.RAMReservedMB
				if reserved < 0 {
					reserved = 0
				}
				reservedRAM += reserved
				reservable := n.Resources.ReservableRAM()
				if reservable < 0 {
					reservable = 0
				}
				reservableRAM += reservable
				alloc := n.RAMAllocatableMB
				if alloc <= 0 && (n.Resources.RAMFreeMB > 0 || n.Resources.RAMTotalMB > 0) {
					alloc = reservable - reserved
				}
				if alloc < 0 {
					alloc = 0
				}
				allocatableRAM += alloc
			}
		}

		// Any non-complete node → snapshot is degraded
		if n.Status != models.StatusComplete {
			snap.Status = models.SnapshotDegraded
		}

		// Generate per-node warnings
		switch n.Status {
		case models.StatusUnreachable:
			snap.Warnings = append(snap.Warnings, models.Warning{
				Node:    n.Name,
				Kind:    "unreachable",
				Message: "node unreachable: " + n.Error,
			})
		case models.StatusPartial:
			snap.Warnings = append(snap.Warnings, models.Warning{
				Node:    n.Name,
				Kind:    "partial",
				Message: "some facts failed to collect",
			})
		case models.StatusError:
			snap.Warnings = append(snap.Warnings, models.Warning{
				Node:    n.Name,
				Kind:    "error",
				Message: "collector error: " + n.Error,
			})
		}

		// RAM pressure warning (separate from status warning)
		if n.Resources != nil && n.Resources.RAMTotalMB > 0 {
			pct := float64(n.Resources.RAMFreeMB) / float64(n.Resources.RAMTotalMB)
			if pct < 0.10 {
				snap.Warnings = append(snap.Warnings, models.Warning{
					Node:    n.Name,
					Kind:    "ram_pressure",
					Message: fmt.Sprintf("RAM pressure: %dMB/%dMB free (%.0f%%)", n.Resources.RAMFreeMB, n.Resources.RAMTotalMB, pct*100),
				})
			}
		}
	}

	snap.Summary = models.ClusterSummary{
		TotalNodes:         len(deduped),
		ReachableNodes:     reachable,
		TotalRAMMB:         totalRAM,
		TotalFreeRAMMB:     freeRAM,
		TotalReservableMB:  reservableRAM,
		TotalAllocatableMB: allocatableRAM,
		TotalReservedMB:    reservedRAM,
	}

	return snap
}
