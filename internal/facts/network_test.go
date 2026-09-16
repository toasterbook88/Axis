package facts

import (
	"net"
	"testing"
)

// stubSysfsLinkSpeed replaces the sysfs link-speed reader for deterministic
// testing. Pass a fake returning (0, nil) to force the name/IP heuristic path
// (a sysfs speed below 1000 falls through), so these tests do not depend on
// the host's real network interfaces — e.g. a CI runner whose eth0 genuinely
// reports a 10GbE link via /sys/class/net/eth0/speed.
func stubSysfsLinkSpeed(fake func(string) (int, error)) func() {
	orig := readSysfsLinkSpeed
	readSysfsLinkSpeed = fake
	return func() { readSysfsLinkSpeed = orig }
}

func TestClassifyInterfaceSpeed_WireGuard(t *testing.T) {
	restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, nil })
	defer restore()
	ip := net.ParseIP("10.0.0.1")
	if got := classifyInterfaceSpeed("wg0", ip); got != "wireguard" {
		t.Errorf("wg0 = %q, want wireguard", got)
	}
}

func TestClassifyInterfaceSpeed_Tailscale_Utun(t *testing.T) {
	restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, nil })
	defer restore()
	ip := net.ParseIP("100.100.1.5")
	if got := classifyInterfaceSpeed("utun4", ip); got != "tailscale" {
		t.Errorf("utun4 + tailscale IP = %q, want tailscale", got)
	}
}

func TestClassifyInterfaceSpeed_Tailscale_ByIP(t *testing.T) {
	restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, nil })
	defer restore()
	ip := net.ParseIP("100.64.0.1")
	if got := classifyInterfaceSpeed("en5", ip); got != "tailscale" {
		t.Errorf("en5 + tailscale IP = %q, want tailscale", got)
	}
}

func TestClassifyInterfaceSpeed_ZeroTier(t *testing.T) {
	restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, nil })
	defer restore()
	ip := net.ParseIP("10.147.17.5")
	if got := classifyInterfaceSpeed("zt0", ip); got != "zerotier" {
		t.Errorf("zt0 = %q, want zerotier", got)
	}
}

func TestClassifyInterfaceSpeed_NetBird(t *testing.T) {
	restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, nil })
	defer restore()
	ip := net.ParseIP("10.0.0.5")
	if got := classifyInterfaceSpeed("nb0", ip); got != "netbird" {
		t.Errorf("nb0 = %q, want netbird", got)
	}
}

func TestParseRemoteAddrLine_BareIP(t *testing.T) {
	addr := parseRemoteAddrLine("10.0.0.5")
	if addr.Address != "10.0.0.5" {
		t.Errorf("Address = %q, want 10.0.0.5", addr.Address)
	}
}

func TestParseRemoteAddrLine_BareCIDR(t *testing.T) {
	addr := parseRemoteAddrLine("10.0.0.5/24")
	if addr.Address != "10.0.0.5" {
		t.Errorf("Address = %q, want 10.0.0.5", addr.Address)
	}
	if addr.Subnet != "10.0.0.0/24" {
		t.Errorf("Subnet = %q, want 10.0.0.0/24", addr.Subnet)
	}
}

func TestParseRemoteAddrLine_Invalid(t *testing.T) {
	addr := parseRemoteAddrLine("not-an-ip")
	if addr.Address != "" {
		t.Errorf("expected empty Address for invalid input, got %q", addr.Address)
	}
}
