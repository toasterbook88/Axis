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
		n.PopulateMemoryMetrics()

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
				// WireGuard / AXIS VPN default ranges:
				// - 10.8.0.0/16 (standard WireGuard)
				// - 10.0.0.0/16 (AXIS VPN / local tunnels)
				// - 10.254.0.0/16 (test VPN subnet)
				if bytes[0] == 10 && (bytes[1] == 0 || bytes[1] == 8 || bytes[1] == 254) {
					isVPN = true
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
	// The configured SSH target (hostname) is the route actually in use. A node
	// may have a Tailscale/VPN interface but be reached over the LAN — the
	// route in use is what matters for placement, not the interface list. So if
	// the hostname is a private LAN address, prefer direct-lan unless latency
	// shows the LAN route itself is degraded.
	if hostIP, err := netip.ParseAddr(n.Hostname); err == nil && isPrivateLAN(hostIP) {
		if n.SSHHandshakeLatencyMs >= 150 {
			return models.NetworkClassRelayed
		}
		return models.NetworkClassDirectLAN
	}
	// mDNS or unparseable hostname: if a LAN address exists and latency is
	// clearly local, the route is the LAN (overlay interfaces are present but
	// not in use).
	if _, err := netip.ParseAddr(n.Hostname); err != nil {
		if hasPrivateLANAddress(n) && n.SSHHandshakeLatencyMs > 0 && n.SSHHandshakeLatencyMs < 30 {
			return models.NetworkClassDirectLAN
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

// isPrivateLAN reports whether an IP is a private LAN address: RFC1918
// (192.168.x, 10.x, 172.16-31.x) or link-local (169.254.x, e.g. Thunderbolt).
// It deliberately returns false for the Tailscale CGNAT range (100.64.0.0/10),
// which is an overlay, not a LAN. 10.x is included because genuine LANs use it
// too; the VPN-specific 10.0/10.8/10.254 subnets are handled by the existing
// isVPN path when they are the SSH target rather than a LAN address.
func isPrivateLAN(ip netip.Addr) bool {
	// Normalize IPv4-mapped IPv6 (e.g. ::ffff:192.168.1.1) to plain IPv4 so the
	// subnet checks below apply. Without this, Is4() returns false for mapped
	// addresses and a private LAN would be missed.
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	if !ip.Is4() {
		// IPv6: treat Unique Local Addresses (fc00::/7) — the IPv6 equivalent of
		// RFC1918 — as private LAN. IsPrivate covers ULA.
		return ip.IsPrivate()
	}
	b := ip.As4()
	// Link-local (169.254.0.0/16) — Thunderbolt point-to-point, APIPA.
	if b[0] == 169 && b[1] == 254 {
		return true
	}
	// 192.168.0.0/16
	if b[0] == 192 && b[1] == 168 {
		return true
	}
	// 172.16.0.0 – 172.31.255.255
	if b[0] == 172 && b[1] >= 16 && b[1] <= 31 {
		return true
	}
	// 10.0.0.0/8 (private), EXCEPT the VPN-specific subnets the existing
	// classifier treats as VPN (10.0/10.8/10.254) — those are disambiguated by
	// the isVPN path when they are the SSH target.
	if b[0] == 10 {
		if b[1] == 0 || b[1] == 8 || b[1] == 254 {
			return false
		}
		return true
	}
	return false
}

// hasPrivateLANAddress reports whether any of the node's addresses is a
// private LAN IP, used for the mDNS-hostname fallback.
func hasPrivateLANAddress(n *models.NodeFacts) bool {
	for _, addr := range n.Addresses {
		if ip, err := netip.ParseAddr(addr.Address); err == nil && isPrivateLAN(ip) {
			return true
		}
	}
	return false
}
