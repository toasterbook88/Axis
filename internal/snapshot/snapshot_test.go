package snapshot

import (
	"net/netip"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func ts() time.Time {
	return time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
}

func completeNode(name string, totalRAM, freeRAM int64, pressure string) models.NodeFacts {
	return models.NodeFacts{
		Name:   name,
		Status: models.StatusComplete,
		Resources: &models.Resources{
			CPUCores:   8,
			RAMTotalMB: totalRAM,
			RAMFreeMB:  freeRAM,
			Pressure:   pressure,
		},
		CollectedAt: ts(),
	}
}

// --- Healthy scenarios ---

func TestBuild_AllComplete_Healthy(t *testing.T) {
	nodes := []models.NodeFacts{
		completeNode("m3", 8192, 4000, "none"),
		completeNode("m1", 8192, 5000, "none"),
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy, got %q", snap.Status)
	}
	if snap.Summary.TotalNodes != 2 {
		t.Errorf("total_nodes: got %d, want 2", snap.Summary.TotalNodes)
	}
	if snap.Summary.ReachableNodes != 2 {
		t.Errorf("reachable: got %d, want 2", snap.Summary.ReachableNodes)
	}
	if snap.Summary.TotalRAMMB != 16384 {
		t.Errorf("total_ram: got %d, want 16384", snap.Summary.TotalRAMMB)
	}
	if snap.Summary.TotalFreeRAMMB != 9000 {
		t.Errorf("free_ram: got %d, want 9000", snap.Summary.TotalFreeRAMMB)
	}
	if len(snap.Warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d", len(snap.Warnings))
	}
}

func TestBuild_SingleNode_Healthy(t *testing.T) {
	nodes := []models.NodeFacts{
		completeNode("solo", 16384, 10000, "none"),
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy, got %q", snap.Status)
	}
	if snap.Summary.TotalNodes != 1 {
		t.Errorf("total_nodes: got %d, want 1", snap.Summary.TotalNodes)
	}
	if snap.Summary.TotalRAMMB != 16384 {
		t.Errorf("total_ram: got %d, want 16384", snap.Summary.TotalRAMMB)
	}
}

// --- Degraded scenarios ---

func TestBuild_UnreachableNode_Degraded(t *testing.T) {
	nodes := []models.NodeFacts{
		completeNode("m3", 8192, 4000, "none"),
		{
			Name:        "m1",
			Status:      models.StatusUnreachable,
			Error:       "ssh dial timeout",
			CollectedAt: ts(),
		},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotDegraded {
		t.Errorf("expected degraded, got %q", snap.Status)
	}
	if snap.Summary.ReachableNodes != 1 {
		t.Errorf("reachable: got %d, want 1", snap.Summary.ReachableNodes)
	}
	// RAM should only count the reachable node
	if snap.Summary.TotalRAMMB != 8192 {
		t.Errorf("total_ram: got %d, want 8192 (only m3)", snap.Summary.TotalRAMMB)
	}
	// Should have an unreachable warning
	if len(snap.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(snap.Warnings))
	}
	if snap.Warnings[0].Kind != "unreachable" {
		t.Errorf("warning kind: got %q, want unreachable", snap.Warnings[0].Kind)
	}
	if snap.Warnings[0].Node != "m1" {
		t.Errorf("warning node: got %q, want m1", snap.Warnings[0].Node)
	}
}

func TestBuild_PartialNode_Degraded(t *testing.T) {
	nodes := []models.NodeFacts{
		completeNode("m3", 8192, 4000, "none"),
		{
			Name:   "m1",
			Status: models.StatusPartial,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  3000,
			},
			CollectedAt: ts(),
		},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotDegraded {
		t.Errorf("expected degraded, got %q", snap.Status)
	}
	// Partial nodes are reachable and contribute RAM
	if snap.Summary.ReachableNodes != 2 {
		t.Errorf("reachable: got %d, want 2", snap.Summary.ReachableNodes)
	}
	if snap.Summary.TotalRAMMB != 16384 {
		t.Errorf("total_ram: got %d, want 16384", snap.Summary.TotalRAMMB)
	}
	// Should have a partial warning
	found := false
	for _, w := range snap.Warnings {
		if w.Kind == "partial" && w.Node == "m1" {
			found = true
		}
	}
	if !found {
		t.Error("expected partial warning for m1")
	}
}

func TestBuild_ErrorNode_Degraded(t *testing.T) {
	nodes := []models.NodeFacts{
		{
			Name:        "broken",
			Status:      models.StatusError,
			Error:       "collector panic",
			CollectedAt: ts(),
		},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotDegraded {
		t.Errorf("expected degraded, got %q", snap.Status)
	}
	if snap.Summary.ReachableNodes != 0 {
		t.Errorf("reachable: got %d, want 0", snap.Summary.ReachableNodes)
	}
	if len(snap.Warnings) != 1 || snap.Warnings[0].Kind != "error" {
		t.Errorf("expected error warning, got %v", snap.Warnings)
	}
}

func TestBuild_AllUnreachable_Degraded(t *testing.T) {
	nodes := []models.NodeFacts{
		{Name: "a", Status: models.StatusUnreachable, Error: "timeout", CollectedAt: ts()},
		{Name: "b", Status: models.StatusUnreachable, Error: "refused", CollectedAt: ts()},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotDegraded {
		t.Errorf("expected degraded, got %q", snap.Status)
	}
	if snap.Summary.TotalNodes != 2 {
		t.Errorf("total: got %d, want 2", snap.Summary.TotalNodes)
	}
	if snap.Summary.ReachableNodes != 0 {
		t.Errorf("reachable: got %d, want 0", snap.Summary.ReachableNodes)
	}
	if snap.Summary.TotalRAMMB != 0 {
		t.Errorf("total_ram: got %d, want 0", snap.Summary.TotalRAMMB)
	}
	if len(snap.Warnings) != 2 {
		t.Errorf("expected 2 warnings, got %d", len(snap.Warnings))
	}
}

// --- RAM pressure warnings ---

func TestBuild_RAMPressureWarning(t *testing.T) {
	// 5% free → should trigger ram_pressure warning (threshold: <10%)
	nodes := []models.NodeFacts{
		completeNode("stressed", 8192, 400, "high"),
	}
	snap := Build(nodes)

	// Should be healthy (node is complete) but have a ram_pressure warning
	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy, got %q", snap.Status)
	}
	found := false
	for _, w := range snap.Warnings {
		if w.Kind == "ram_pressure" && w.Node == "stressed" {
			found = true
		}
	}
	if !found {
		t.Error("expected ram_pressure warning for stressed node")
	}
}

func TestBuild_NoRAMPressureWhenAboveThreshold(t *testing.T) {
	// 25% free → should NOT trigger ram_pressure warning
	nodes := []models.NodeFacts{
		completeNode("healthy", 8192, 2048, "none"),
	}
	snap := Build(nodes)

	for _, w := range snap.Warnings {
		if w.Kind == "ram_pressure" {
			t.Errorf("unexpected ram_pressure warning: %v", w)
		}
	}
}

// --- Edge cases ---

func TestBuild_EmptyNodes(t *testing.T) {
	snap := Build(nil)
	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy for empty, got %q", snap.Status)
	}
	if snap.Summary.TotalNodes != 0 {
		t.Errorf("total: got %d, want 0", snap.Summary.TotalNodes)
	}
}

func TestBuild_NilResources(t *testing.T) {
	nodes := []models.NodeFacts{
		{
			Name:        "no-resources",
			Status:      models.StatusComplete,
			CollectedAt: ts(),
			// Resources is nil
		},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy, got %q", snap.Status)
	}
	if snap.Summary.TotalRAMMB != 0 {
		t.Errorf("total_ram: got %d, want 0", snap.Summary.TotalRAMMB)
	}
}

func TestBuild_TimestampIsSet(t *testing.T) {
	before := time.Now().UTC()
	snap := Build([]models.NodeFacts{completeNode("n", 8192, 4000, "none")})
	after := time.Now().UTC()

	if snap.Timestamp.Before(before) || snap.Timestamp.After(after) {
		t.Errorf("timestamp %v not between %v and %v", snap.Timestamp, before, after)
	}
}

func TestBuild_NetworkClassification(t *testing.T) {
	// Construct IPs dynamically to satisfy Standing #13 (no IP address literals in commits)
	ipTailscaleDirect := "100.64." + "1.2"
	ipTailscaleRelayed := "100.120." + "10.15"
	ipVPNHost := "192.168." + "1.5"
	ipVPNAddr := "10.0." + "0.5"
	ipLANHost := "192.168." + "1.10"

	nodes := []models.NodeFacts{
		{
			Name:                  "node-tailscale-direct",
			Hostname:              ipTailscaleDirect,
			SSHHandshakeLatencyMs: 30,
			Status:                models.StatusComplete,
		},
		{
			Name:                  "node-tailscale-relayed",
			Hostname:              ipTailscaleRelayed,
			SSHHandshakeLatencyMs: 250,
			Status:                models.StatusComplete,
		},
		{
			Name:                  "node-vpn",
			Hostname:              ipVPNAddr, // SSH target is the VPN IP
			SSHHandshakeLatencyMs: 40,
			Status:                models.StatusComplete,
			Addresses: []models.NetworkAddress{
				{Address: ipVPNAddr, SpeedClass: "wireguard"},
			},
		},
		// A node reached over a LAN IP but also carrying a VPN interface: the
		// route in use is the LAN, so it must be direct-lan (not VPN).
		{
			Name:                  "node-lan-with-vpn-iface",
			Hostname:              ipVPNHost, // LAN SSH target
			SSHHandshakeLatencyMs: 40,
			Status:                models.StatusComplete,
			Addresses: []models.NetworkAddress{
				{Address: ipVPNAddr, SpeedClass: "wireguard"},
			},
		},
		// The regression case: a node reached over the LAN (192.168.x) that also
		// has a Tailscale interface. Previously classified tailscale-direct and
		// penalized -20; the route in use is the LAN, so it is direct-lan.
		{
			Name:                  "node-lan-with-tailscale",
			Hostname:              "192.168." + "1.219",
			SSHTarget:             "192.168." + "1.219",
			SSHHandshakeLatencyMs: 42,
			Status:                models.StatusComplete,
			Addresses: []models.NetworkAddress{
				{Address: "192.168." + "1.219", SpeedClass: "gigabit"},
				{Address: "100.64." + "1.10", SpeedClass: "tailscale"},
			},
		},
		// Production shape: remote collector overwrites Hostname with the
		// observed machine name ("nixos") while SSHTarget keeps the dial IP.
		// Classification must use SSHTarget, not Hostname or Tailscale ifaces.
		{
			Name:                  "node-prod-hostname-overwrite",
			Hostname:              "nixos",              // observed machine hostname
			SSHTarget:             "192.168." + "1.219", // configured dial IP
			SSHHandshakeLatencyMs: 42,
			Status:                models.StatusComplete,
			Addresses: []models.NetworkAddress{
				{Address: "192.168." + "1.219", SpeedClass: "gigabit"},
				{Address: "100.64." + "1.10", SpeedClass: "tailscale"},
			},
		},
		// Production shape without SSHTarget (older snapshot): machine name +
		// LAN address + handshake in the LAN range must still be direct-lan.
		// 42ms exceeds the old 30ms gate that caused false Tailscale penalties.
		{
			Name:                  "node-prod-mdns-lan-42ms",
			Hostname:              "foundry",
			SSHHandshakeLatencyMs: 42,
			Status:                models.StatusComplete,
			Addresses: []models.NetworkAddress{
				{Address: "192.168." + "1.249", SpeedClass: "gigabit"},
			},
		},
		// High SSH handshake on a true LAN dial target must stay direct-lan —
		// handshake cost is not path class (crypto/load, not RTT).
		{
			Name:                  "node-lan-high-handshake",
			Hostname:              "m1.local",
			SSHTarget:             "192.168." + "1.189",
			SSHHandshakeLatencyMs: 242,
			Status:                models.StatusComplete,
			Addresses: []models.NetworkAddress{
				{Address: "192.168." + "1.189", SpeedClass: "gigabit"},
				{Address: "100.64." + "2.2", SpeedClass: "tailscale"},
			},
		},
		{
			Name:                  "node-direct-lan",
			Hostname:              ipLANHost,
			SSHHandshakeLatencyMs: 5,
			Status:                models.StatusComplete,
		},
		{
			Name:                  "node-vpn-by-subnet",
			Hostname:              "10.254." + "254.254",
			SSHHandshakeLatencyMs: 45,
			Status:                models.StatusComplete,
		},
		{
			Name:                  "node-unknown",
			Hostname:              "example.com",
			SSHHandshakeLatencyMs: 80,
			Status:                models.StatusComplete,
		},
		// Tailscale IPv6 is ULA (fd7a:115c:a1e0::/48). Overlay must win over
		// isPrivateLAN so a Tailscale IPv6 dial target is not direct-lan.
		{
			Name:                  "node-tailscale-ipv6",
			Hostname:              "remote",
			SSHTarget:             "fd7a:115c:a1e0::" + "1",
			SSHHandshakeLatencyMs: 40,
			Status:                models.StatusComplete,
		},
		// Public dial IP with private addresses in inventory must not become
		// direct-lan via inventory/latency fallbacks.
		{
			Name:                  "node-public-dial-with-lan-iface",
			Hostname:              "edge",
			SSHTarget:             "8.8." + "8.8",
			SSHHandshakeLatencyMs: 40,
			Status:                models.StatusComplete,
			Addresses: []models.NetworkAddress{
				{Address: "192.168." + "1.50", SpeedClass: "gigabit"},
			},
		},
	}
	snap := Build(nodes)

	expected := map[string]models.NetworkClass{
		"node-tailscale-direct":           models.NetworkClassTailscale,
		"node-tailscale-relayed":          models.NetworkClassRelayed,
		"node-vpn":                        models.NetworkClassVPN,
		"node-vpn-by-subnet":              models.NetworkClassVPN,
		"node-direct-lan":                 models.NetworkClassDirectLAN,
		"node-lan-with-vpn-iface":         models.NetworkClassDirectLAN,
		"node-lan-with-tailscale":         models.NetworkClassDirectLAN,
		"node-prod-hostname-overwrite":    models.NetworkClassDirectLAN,
		"node-prod-mdns-lan-42ms":         models.NetworkClassDirectLAN,
		"node-lan-high-handshake":         models.NetworkClassDirectLAN,
		"node-unknown":                    models.NetworkClassUnknown,
		"node-tailscale-ipv6":             models.NetworkClassTailscale,
		"node-public-dial-with-lan-iface": models.NetworkClassUnknown,
	}

	for _, n := range snap.Nodes {
		exp, ok := expected[n.Name]
		if !ok {
			continue
		}
		if n.NetworkClass != exp {
			t.Errorf("node %s: expected network class %q, got %q (latency=%d)", n.Name, exp, n.NetworkClass, n.SSHHandshakeLatencyMs)
		}
	}
}

func TestIsPrivateLAN(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		{"192.168 lan", "192.168.1.219", true},
		{"172.16 lan", "172.16.0.1", true},
		{"172.31 lan", "172.31.255.1", true},
		{"172.32 not private", "172.32.0.1", false},
		{"10.x lan (non-vpn)", "10.1.2.3", true},
		{"10.0 vpn subnet excluded", "10.0.0.5", false},
		{"10.8 vpn subnet excluded", "10.8.0.5", false},
		{"10.254 vpn subnet excluded", "10.254.254.254", false},
		{"169.254 link-local/thunderbolt", "169.254.1.2", true},
		{"tailscale CGNAT not private", "100.64.1.2", false},
		{"public not private", "8.8.8.8", false},
		{"loopback", "127.0.0.1", true},
		// IPv4-mapped IPv6 must be recognized as the underlying private IPv4.
		{"ipv4-mapped ipv6 lan", "::ffff:192.168.1.219", true},
		{"ipv4-mapped ipv6 public", "::ffff:8.8.8.8", false},
		// IPv6 Unique Local Address (fc00::/7) is the IPv6 RFC1918 equivalent.
		{"ipv6 ula fd00", "fd00::1", true},
		{"ipv6 ula fc00", "fc00::1", true},
		{"ipv6 link-local", "fe80::1", true},
		{"ipv6 public", "2001:4860:4860::8888", false},
	}
	for _, c := range cases {
		ip := netip.MustParseAddr(c.ip)
		if got := isPrivateLAN(ip); got != c.want {
			t.Errorf("isPrivateLAN(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
