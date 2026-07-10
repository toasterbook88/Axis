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

	// Local node: effectively 0ms RTT on the loopback/local path.
	if models.IsLocalNode(*n) {
		return models.NetworkClassDirectLAN
	}

	// The dial target is the route actually in use. Prefer SSHTarget (configured
	// dial address preserved through fact collection) over Hostname, which the
	// remote collector overwrites with the observed machine hostname (e.g.
	// "nixos") after connect. Falling back to Hostname keeps older snapshots
	// and tests that only set Hostname working.
	target := dialTarget(n)

	// 1. Dial target is a parseable IP: classify by the path we actually used.
	// Interface inventory is irrelevant here — a node may advertise Tailscale
	// while being reached over the LAN.
	if hostIP, err := netip.ParseAddr(target); err == nil {
		if isPrivateLAN(hostIP) {
			// Route is LAN regardless of SSH handshake cost (crypto/sleep can
			// push handshake well above RTT). Do not reclassify LAN as relayed.
			return models.NetworkClassDirectLAN
		}
		if isTailscaleAddr(hostIP) {
			if n.SSHHandshakeLatencyMs >= 150 {
				return models.NetworkClassRelayed
			}
			return models.NetworkClassTailscale
		}
		if isVPNAddr(hostIP) {
			if n.SSHHandshakeLatencyMs >= 150 {
				return models.NetworkClassRelayed
			}
			return models.NetworkClassVPN
		}
	}

	// 2. Non-IP dial target (mDNS / machine name) or missing SSHTarget on an
	// older snapshot: if the node has a private LAN address and handshake
	// latency is in the LAN range, the route is LAN. SSH handshake on a
	// gigabit LAN is typically 20–80ms (crypto, not RTT), so the gate is 100ms
	// — not 30ms.
	if hasPrivateLANAddress(n) && n.SSHHandshakeLatencyMs > 0 && n.SSHHandshakeLatencyMs < 100 {
		return models.NetworkClassDirectLAN
	}

	// 3. Overlay only when the dial target itself (or its speed-class metadata
	// when target is an address string we already handled) is overlay. Scan
	// address inventory solely as a last-resort signal when there is no LAN
	// path evidence at all.
	isTailscale, isVPN := classifyOverlayFromTargetAndInventory(n, target)
	if isTailscale {
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

	// 4. Latency-only fallbacks.
	if n.SSHHandshakeLatencyMs > 0 && n.SSHHandshakeLatencyMs < 50 {
		return models.NetworkClassDirectLAN
	}
	if n.SSHHandshakeLatencyMs >= 150 {
		return models.NetworkClassRelayed
	}
	return models.NetworkClassUnknown
}

// dialTarget returns the address used to reach the node. SSHTarget is the
// configured dial string preserved by the remote collector; Hostname is the
// observed machine name and is only a fallback for older data.
func dialTarget(n *models.NodeFacts) string {
	if n.SSHTarget != "" {
		return n.SSHTarget
	}
	return n.Hostname
}

func isTailscaleAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is4() {
		b := ip.As4()
		// Tailscale IPv4 CGNAT: 100.64.0.0/10
		return b[0] == 100 && b[1] >= 64 && b[1] <= 127
	}
	// Tailscale IPv6: fd7a:115c:a1e0::/48
	b := ip.As16()
	return b[0] == 0xfd && b[1] == 0x7a && b[2] == 0x11 && b[3] == 0x5c && b[4] == 0xa1 && b[5] == 0xe0
}

func isVPNAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.Is4() {
		return false
	}
	b := ip.As4()
	// WireGuard / AXIS VPN default ranges:
	// - 10.8.0.0/16 (standard WireGuard)
	// - 10.0.0.0/16 (AXIS VPN / local tunnels)
	// - 10.254.0.0/16 (test VPN subnet)
	return b[0] == 10 && (b[1] == 0 || b[1] == 8 || b[1] == 254)
}

// classifyOverlayFromTargetAndInventory returns overlay flags only when there
// is no private-LAN dial evidence. Prefer dial-target string; fall back to
// address inventory speed classes when the dial target is a bare name.
func classifyOverlayFromTargetAndInventory(n *models.NodeFacts, target string) (isTailscale, isVPN bool) {
	if ip, err := netip.ParseAddr(target); err == nil {
		if isTailscaleAddr(ip) {
			return true, false
		}
		if isVPNAddr(ip) {
			return false, true
		}
		return false, false
	}
	// Bare name with no LAN evidence: inventory is the only signal left.
	for _, addr := range n.Addresses {
		if addr.SpeedClass == "tailscale" {
			isTailscale = true
		} else if addr.SpeedClass == "wireguard" || addr.SpeedClass == "vpn" || addr.SpeedClass == "netbird" || addr.SpeedClass == "zerotier" {
			isVPN = true
		}
		if ip, err := netip.ParseAddr(addr.Address); err == nil {
			if isTailscaleAddr(ip) {
				isTailscale = true
			}
			if isVPNAddr(ip) {
				isVPN = true
			}
		}
	}
	return isTailscale, isVPN
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
