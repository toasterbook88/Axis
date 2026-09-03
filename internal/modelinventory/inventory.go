// Package modelinventory derives canonical resident-model instances from a
// cluster snapshot without adding lifecycle or ownership claims.
package modelinventory

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// FromSnapshot builds a deterministic inventory from observed resident model
// facts. Repeated rows for the same node, engine, model, and port collapse to
// one instance.
func FromSnapshot(snap *models.ClusterSnapshot, source string) models.ModelInventory {
	inventory := models.ModelInventory{
		Source:    strings.TrimSpace(source),
		Instances: []models.ModelInstance{},
	}
	if snap == nil {
		return inventory
	}

	inventory.ObservedAt = snap.Timestamp
	inventory.Warnings = append([]models.Warning(nil), snap.Warnings...)
	if snap.Publication != nil {
		inventory.PublicationID = snap.Publication.ID
	}
	byID := make(map[string]models.ModelInstance)
	for _, node := range snap.Nodes {
		nodeIdentity := strings.TrimSpace(node.Name)
		if node.Identity != nil && models.NormalizeStableID(node.Identity.StableID) != "" {
			nodeIdentity = models.NormalizeStableID(node.Identity.StableID)
		}
		for _, resident := range node.ResidentModels {
			instance := models.ModelInstance{
				ID:           instanceID(nodeIdentity, resident.Runtime, resident.Name, resident.Port),
				Model:        resident.Name,
				Engine:       resident.Runtime,
				Node:         node.Name,
				NodeStatus:   node.Status,
				State:        models.ModelInstanceResident,
				Port:         resident.Port,
				Processor:    resident.Processor,
				Source:       resident.Source,
				WeightSizeMB: resident.WeightSizeMB,
				SizeRAMMB:    resident.SizeRAMMB,
				SizeVRAMMB:   resident.SizeVRAMMB,
				ExpiresAt:    resident.ExpiresAt,
				WarmthScore:  resident.WarmthScore,
				ObservedAt:   node.CollectedAt,
			}
			byID[instance.ID] = instance
		}
	}

	for _, instance := range byID {
		inventory.Instances = append(inventory.Instances, instance)
	}
	sort.Slice(inventory.Instances, func(i, j int) bool {
		left, right := inventory.Instances[i], inventory.Instances[j]
		if left.Node != right.Node {
			return left.Node < right.Node
		}
		if left.Model != right.Model {
			return left.Model < right.Model
		}
		if left.Engine != right.Engine {
			return left.Engine < right.Engine
		}
		if left.Port != right.Port {
			return left.Port < right.Port
		}
		return left.ID < right.ID
	})
	return inventory
}

func instanceID(nodeIdentity, engine, model string, port int) string {
	parts := []string{
		"axis-model-instance-v1",
		strings.TrimSpace(nodeIdentity),
		strings.ToLower(strings.TrimSpace(engine)),
		strings.TrimSpace(model),
		strconv.Itoa(port),
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "mi-" + hex.EncodeToString(digest[:12])
}
