package facts

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

func localAddresses() []models.NetworkAddress {
	var addrs []models.NetworkAddress

	ifaces, err := net.Interfaces()
	if err != nil {
		return addrs
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		lowerName := strings.ToLower(iface.Name)
		if strings.HasPrefix(lowerName, "docker") || strings.HasPrefix(lowerName, "br-") || strings.HasPrefix(lowerName, "veth") || strings.HasPrefix(lowerName, "virbr") {
			continue
		}
		ifAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range ifAddrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			scope := ""
			if ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
				scope = "link-local"
			}

			kind := "ipv4"
			if ip.To4() == nil {
				kind = "ipv6"
			}
			addrs = append(addrs, models.NetworkAddress{
				Kind:       kind,
				Address:    ip.String(),
				Interface:  iface.Name,
				Subnet:     subnetFromIPNet(ipNet),
				SpeedClass: classifyInterfaceSpeed(iface.Name, ip),
				Scope:      scope,
			})
		}
	}
	return addrs
}

func subnetFromIPNet(ipNet *net.IPNet) string {
	if ipNet == nil {
		return ""
	}
	ones, _ := ipNet.Mask.Size()
	return ipNet.IP.Mask(ipNet.Mask).String() + "/" + strconv.Itoa(ones)
}

func parseAddressWithOptionalCIDR(raw string) (net.IP, string) {
	if strings.Contains(raw, "/") {
		ip, ipNet, err := net.ParseCIDR(raw)
		if err == nil {
			return ip, subnetFromIPNet(ipNet)
		}
	}
	return net.ParseIP(raw), ""
}

// readSysfsLinkSpeed reads the negotiated link speed (Mbps) for a Linux network
// interface from /sys/class/net/<iface>/speed. It returns an error when sysfs
// is unavailable (a missing interface, or non-numeric content) so
// classifyInterfaceSpeed can fall back to the name/IP heuristic. It is a
// package-level var so tests can stub it for deterministic classification
// independent of the host's real interfaces — e.g. a CI runner whose eth0
// genuinely reports a 10GbE link via sysfs.
var readSysfsLinkSpeed = func(ifName string) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/speed", ifName))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// classifyInterfaceSpeed determines the speed class of a network interface. On
// Linux it first consults the exact negotiated link speed from sysfs (≥10000 →
// "10gbe", ≥1000 → "gigabit"); otherwise it falls back to a name/IP heuristic.
// This enables topology-aware decisions (e.g., preferring Thunderbolt links
// for heavy data transfers).

func classifyInterfaceSpeed(ifName string, ip net.IP) string {
	if runtime.GOOS == "linux" {
		if speed, err := readSysfsLinkSpeed(ifName); err == nil {
			if speed >= 10000 {
				return "10gbe"
			}
			if speed >= 1000 {
				return "gigabit"
			}
		}
	}

	lower := strings.ToLower(ifName)

	// Overlay / VPN tunnels — detect by interface name
	if strings.HasPrefix(lower, "wg") {
		return "wireguard"
	}
	if strings.HasPrefix(lower, "tailscale") || strings.HasPrefix(lower, "ts") {
		return "tailscale"
	}
	if strings.HasPrefix(lower, "utun") || strings.HasPrefix(lower, "tun") {
		// Tailscale on macOS typically uses utun; check by IP range
		if isTailscaleIP(ip) {
			return "tailscale"
		}
		return "vpn"
	}
	if strings.HasPrefix(lower, "zt") {
		return "zerotier"
	}
	if strings.HasPrefix(lower, "nb") || strings.HasPrefix(lower, "netbird") {
		return "netbird"
	}

	// Detect by IP range for non-tunnel interfaces
	if isTailscaleIP(ip) {
		return "tailscale"
	}

	// Thunderbolt bridge / point-to-point
	if strings.Contains(lower, "bridge") || strings.Contains(lower, "thunder") {
		return "thunderbolt"
	}

	// Wi-Fi — common interface names across platforms
	if lower == "wlan0" || lower == "wlp" || strings.HasPrefix(lower, "wlp") {
		return "wifi"
	}
	if runtime.GOOS == "darwin" && (lower == "en0") {
		// On many Macs, en0 is Wi-Fi; can't be 100% certain without IOKit
		return "wifi"
	}

	// Ethernet
	if strings.HasPrefix(lower, "en") || strings.HasPrefix(lower, "eth") || strings.HasPrefix(lower, "enp") {
		return "gigabit"
	}

	return "unknown"
}

// isTailscaleIP returns true if the IP is in the Tailscale CGNAT range (100.64.0.0/10).

func isTailscaleIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	// 100.64.0.0/10 → first byte 100, second byte 64-127
	return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
}
