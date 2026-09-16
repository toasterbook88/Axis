package facts

import (
	"net"
	"os"
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

func TestClassifyInterfaceSpeed_Thunderbolt(t *testing.T) {
	restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, nil })
	defer restore()
	ip := net.ParseIP("169.254.1.1")
	// Link-local addresses are filtered before this is called, but the
	// classifier itself just looks at the interface name.
	ip2 := net.ParseIP("192.168.100.1")
	if got := classifyInterfaceSpeed("bridge0", ip2); got != "thunderbolt" {
		t.Errorf("bridge0 = %q, want thunderbolt", got)
	}
	_ = ip // suppress unused
}

func TestClassifyInterfaceSpeed_Ethernet(t *testing.T) {
	// Force the heuristic path: a CI runner's eth0 may genuinely report a
	// 10GbE link via sysfs, which would otherwise override the heuristic.
	restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, nil })
	defer restore()
	ip := net.ParseIP("192.168.1.50")
	if got := classifyInterfaceSpeed("eth0", ip); got != "gigabit" {
		t.Errorf("eth0 = %q, want gigabit", got)
	}
	if got := classifyInterfaceSpeed("enp3s0", ip); got != "gigabit" {
		t.Errorf("enp3s0 = %q, want gigabit", got)
	}
}

func TestClassifyInterfaceSpeed_WiFi(t *testing.T) {
	restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, nil })
	defer restore()
	ip := net.ParseIP("192.168.1.100")
	if got := classifyInterfaceSpeed("wlan0", ip); got != "wifi" {
		t.Errorf("wlan0 = %q, want wifi", got)
	}
	if got := classifyInterfaceSpeed("wlp2s0", ip); got != "wifi" {
		t.Errorf("wlp2s0 = %q, want wifi", got)
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

func TestClassifyInterfaceSpeed_Unknown(t *testing.T) {
	restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, nil })
	defer restore()
	ip := net.ParseIP("192.168.1.1")
	if got := classifyInterfaceSpeed("vmnet8", ip); got != "unknown" {
		t.Errorf("vmnet8 = %q, want unknown", got)
	}
}

// TestClassifyInterfaceSpeed_SysfsMeasured covers the sysfs fast path directly:
// a stubbed link speed drives classification regardless of interface name, and
// sub-gigabit or error results fall through to the name/IP heuristic.
func TestClassifyInterfaceSpeed_SysfsMeasured(t *testing.T) {
	ip := net.ParseIP("192.168.1.50")
	t.Run("10gbe", func(t *testing.T) {
		restore := stubSysfsLinkSpeed(func(string) (int, error) { return 10000, nil })
		defer restore()
		if got := classifyInterfaceSpeed("eth9", ip); got != "10gbe" {
			t.Errorf("eth9 @10000 = %q, want 10gbe", got)
		}
	})
	t.Run("gigabit", func(t *testing.T) {
		restore := stubSysfsLinkSpeed(func(string) (int, error) { return 1000, nil })
		defer restore()
		if got := classifyInterfaceSpeed("eth9", ip); got != "gigabit" {
			t.Errorf("eth9 @1000 = %q, want gigabit", got)
		}
	})
	t.Run("slow_falls_through", func(t *testing.T) {
		restore := stubSysfsLinkSpeed(func(string) (int, error) { return 100, nil })
		defer restore()
		if got := classifyInterfaceSpeed("eth9", ip); got != "gigabit" {
			t.Errorf("eth9 @100 = %q, want gigabit (heuristic fallback)", got)
		}
	})
	t.Run("error_falls_through", func(t *testing.T) {
		restore := stubSysfsLinkSpeed(func(string) (int, error) { return 0, os.ErrNotExist })
		defer restore()
		if got := classifyInterfaceSpeed("eth9", ip); got != "gigabit" {
			t.Errorf("eth9 err = %q, want gigabit (heuristic fallback)", got)
		}
	})
}

func TestIsTailscaleIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"100.64.0.1", true},
		{"100.100.5.10", true},
		{"100.127.255.255", true},
		{"100.63.255.255", false}, // below CGNAT range
		{"100.128.0.0", false},    // above CGNAT range
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"::1", false}, // IPv6
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if got := isTailscaleIP(ip); got != tt.want {
				t.Errorf("isTailscaleIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestParseRemoteAddrLine_IPOFormat(t *testing.T) {
	// ip -o addr show format
	line := "2: eth0    inet 192.168.1.5/24 brd 192.168.1.255 scope global dynamic eth0"
	addr := parseRemoteAddrLine(line)
	if addr.Address != "192.168.1.5" {
		t.Errorf("Address = %q, want 192.168.1.5", addr.Address)
	}
	if addr.Interface != "eth0" {
		t.Errorf("Interface = %q, want eth0", addr.Interface)
	}
	if addr.Kind != "ipv4" {
		t.Errorf("Kind = %q, want ipv4", addr.Kind)
	}
	if addr.Subnet != "192.168.1.0/24" {
		t.Errorf("Subnet = %q, want 192.168.1.0/24", addr.Subnet)
	}
}

func TestParseRemoteAddrLine_BareIP(t *testing.T) {
	addr := parseRemoteAddrLine("10.0.0.5")
	if addr.Address != "10.0.0.5" {
		t.Errorf("Address = %q, want 10.0.0.5", addr.Address)
	}
}

func TestParseRemoteAddrLine_IfNameIP(t *testing.T) {
	addr := parseRemoteAddrLine("wg0 10.0.0.1")
	if addr.Address != "10.0.0.1" {
		t.Errorf("Address = %q, want 10.0.0.1", addr.Address)
	}
	if addr.Interface != "wg0" {
		t.Errorf("Interface = %q, want wg0", addr.Interface)
	}
	if addr.SpeedClass != "wireguard" {
		t.Errorf("SpeedClass = %q, want wireguard", addr.SpeedClass)
	}
}

func TestParseRemoteAddrLine_IPv6(t *testing.T) {
	line := "3: eth0    inet6 2001:db8::1/64 scope global"
	addr := parseRemoteAddrLine(line)
	if addr.Kind != "ipv6" {
		t.Errorf("Kind = %q, want ipv6", addr.Kind)
	}
	if addr.Address != "2001:db8::1" {
		t.Errorf("Address = %q, want 2001:db8::1", addr.Address)
	}
	if addr.Subnet != "2001:db8::/64" {
		t.Errorf("Subnet = %q, want 2001:db8::/64", addr.Subnet)
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
