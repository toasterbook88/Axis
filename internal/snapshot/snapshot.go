// Package snapshot is STABLE — cluster snapshot assembly from collected NodeFacts.
// It is part of the stable operator path.
package snapshot

import (
	"fmt"
	"net/netip"
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

	var totalRAM, freeRAM int64
	reachable := 0

	for i := range deduped {
		deduped[i].NetworkClass = classifyNetwork(&deduped[i])
		n := &deduped[i]

		// Count reachable and aggregate resources
		if n.Status == models.StatusComplete || n.Status == models.StatusPartial {
			reachable++
			if n.Resources != nil {
				totalRAM += n.Resources.RAMTotalMB
				freeRAM += n.Resources.RAMFreeMB
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
		TotalNodes:     len(deduped),
		ReachableNodes: reachable,
		TotalRAMMB:     totalRAM,
		TotalFreeRAMMB: freeRAM,
	}

	return snap
}

func classifyNetwork(n *models.NodeFacts) models.NetworkClass {
	if n.Status == models.StatusUnreachable {
		return models.NetworkClassUnknown
	}

	// 1. If it is the local node, it has direct-lan quality (effectively 0ms RTT)
	if models.IsLocalNode(*n) {
		return models.NetworkClassDirectLAN
	}

	isTailscale := false
	isVPN := false

	checkIP := func(ipStr string) {
		if ip, err := netip.ParseAddr(ipStr); err == nil {
			if ip.Is4() {
				// Tailscale IPv4 range: 100.64.0.0/10
				bytes := ip.As4()
				if bytes[0] == 100 && bytes[1] >= 64 && bytes[1] <= 127 {
					isTailscale = true
				}
			} else if ip.Is6() {
				// Tailscale IPv6 range starts with fd7a:115c:a1e0::/48
				bytes := ip.As16()
				if bytes[0] == 0xfd && bytes[1] == 0x7a && bytes[2] == 0x11 && bytes[3] == 0x5c && bytes[4] == 0xa1 && bytes[5] == 0xe0 {
					isTailscale = true
				}
			}
		}
	}

	// Check the hostname (which might be an IP address)
	checkIP(n.Hostname)

	// Check all node addresses
	for _, addr := range n.Addresses {
		checkIP(addr.Address)
		if addr.SpeedClass == "tailscale" {
			isTailscale = true
		} else if addr.SpeedClass == "wireguard" || addr.SpeedClass == "vpn" || addr.SpeedClass == "netbird" || addr.SpeedClass == "zerotier" {
			isVPN = true
		}
	}

	if isTailscale {
		// Sub-classify based on SSHHandshakeLatencyMs:
		// If SSHHandshakeLatencyMs < 60ms -> tailscale-direct
		// If SSHHandshakeLatencyMs >= 150ms -> relayed (DERP)
		if n.SSHHandshakeLatencyMs >= 150 {
			return models.NetworkClassRelayed
		}
		return models.NetworkClassTailscale
	}

	if isVPN {
		if n.SSHHandshakeLatencyMs >= 150 {
			return models.NetworkClassRelayed
		}
		return models.NetworkClassVPN
	}

	// Fallback to LAN if latency is very low
	if n.SSHHandshakeLatencyMs > 0 && n.SSHHandshakeLatencyMs < 20 {
		return models.NetworkClassDirectLAN
	}

	// If latency is very high, classify as relayed
	if n.SSHHandshakeLatencyMs >= 150 {
		return models.NetworkClassRelayed
	}

	return models.NetworkClassUnknown
}
