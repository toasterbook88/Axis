package snapshotview

import (
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func Clone(snap *models.ClusterSnapshot) *models.ClusterSnapshot {
	if snap == nil {
		return nil
	}

	clone := *snap
	clone.Nodes = make([]models.NodeFacts, len(snap.Nodes))
	for i, node := range snap.Nodes {
		nodeCopy := node
		if node.Resources != nil {
			res := *node.Resources
			res.GPUs = append([]models.GPUInfo(nil), node.Resources.GPUs...)
			nodeCopy.Resources = &res
		}
		nodeCopy.Addresses = append([]models.NetworkAddress(nil), node.Addresses...)
		nodeCopy.Tools = append([]models.ToolInfo(nil), node.Tools...)
		if node.Ollama != nil {
			ollama := *node.Ollama
			ollama.Models = append([]string(nil), node.Ollama.Models...)
			nodeCopy.Ollama = &ollama
		}
		if node.TurboQuant != nil {
			turbo := *node.TurboQuant
			turbo.Backends = append([]string(nil), node.TurboQuant.Backends...)
			turbo.Capabilities = append([]string(nil), node.TurboQuant.Capabilities...)
			nodeCopy.TurboQuant = &turbo
		}
		clone.Nodes[i] = nodeCopy
	}
	clone.Warnings = append([]models.Warning(nil), snap.Warnings...)
	return &clone
}

// ApplyReservationView overlays locally persisted reservations onto a snapshot
// so read paths can reason about allocatable RAM without requiring daemon-only
// semantics.
func ApplyReservationView(snap *models.ClusterSnapshot, st *state.ClusterState) {
	if snap == nil {
		return
	}

	var totalReserved, totalAllocatable int64
	for i := range snap.Nodes {
		node := &snap.Nodes[i]
		if node.Resources == nil {
			continue
		}

		reserved := int64(0)
		if st != nil && st.Nodes != nil {
			if ns, ok := st.Nodes[node.Name]; ok {
				reserved = ns.ReservedMB
			}
		}
		node.Resources.RAMReservedMB = reserved

		allocatable := node.Resources.RAMFreeMB - reserved
		if allocatable < 0 {
			allocatable = 0
		}
		node.Resources.RAMAllocatableMB = allocatable

		totalReserved += reserved
		totalAllocatable += allocatable
	}

	snap.Summary.TotalReservedMB = totalReserved
	snap.Summary.TotalAllocatableMB = totalAllocatable
}
