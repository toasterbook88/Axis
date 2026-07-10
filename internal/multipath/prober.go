package multipath

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// ProbeResult holds the result of probing a single address.
type ProbeResult struct {
	Address string
	Latency time.Duration
	Err     error
}

// Probe evaluates all provided addresses concurrently to find the fastest
// reachable path that speaks SSH. It applies latency penalties to known
// overlay networks (CGNAT) to prefer true LAN links.
func Probe(ctx context.Context, port int, addresses []models.NetworkAddress) string {
	if len(addresses) == 0 {
		return ""
	}

	results := make(chan ProbeResult, len(addresses))
	var wg sync.WaitGroup

	for _, addr := range addresses {
		if addr.Address == "" {
			continue
		}
		wg.Add(1)
		go func(a models.NetworkAddress) {
			defer wg.Done()
			latency, err := probeSSH(ctx, a.Address, port)

			// Apply penalties to overlay networks to ensure direct LAN wins
			if err == nil {
				penalty := time.Duration(0)
				if a.SpeedClass == "tailscale" || a.SpeedClass == "zerotier" || a.SpeedClass == "vpn" || a.SpeedClass == "wireguard" || a.SpeedClass == "netbird" {
					penalty = 50 * time.Millisecond
				} else if ip, parseErr := netip.ParseAddr(a.Address); parseErr == nil {
					// Apply penalty to Tailscale CGNAT range if not explicitly tagged
					if isTailscaleAddr(ip) {
						penalty = 50 * time.Millisecond
					}
				}
				latency += penalty
			}

			results <- ProbeResult{Address: a.Address, Latency: latency, Err: err}
		}(addr)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var best string
	var minLatency time.Duration = -1

	for res := range results {
		if res.Err != nil {
			continue
		}
		if minLatency == -1 || res.Latency < minLatency {
			minLatency = res.Latency
			best = res.Address
		}
	}

	return best
}

func probeSSH(ctx context.Context, ip string, port int) (time.Duration, error) {
	start := time.Now()

	// Fast timeout for the TCP connection to avoid stalling discovery
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	// Read the SSH banner to avoid fail2ban/IDS triggering on empty TCP connects.
	// Many security tools ban IPs that connect and immediately close.
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 255)
	_, _ = conn.Read(buf) // Ignore read errors, we just want to appear protocol-compliant

	return time.Since(start), nil
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
