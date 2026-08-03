package multipath

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
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

const (
	maxConcurrentProbes = 3
	successCacheTTL     = 2 * time.Minute
)

type cachedPath struct {
	address   string
	expiresAt time.Time
}

var successfulPaths = struct {
	sync.Mutex
	entries map[string]cachedPath
}{entries: make(map[string]cachedPath)}

// Probe evaluates filtered addresses with bounded concurrency to find the
// fastest reachable path that speaks SSH. preferred contains already-known
// logical or connected targets and is used to choose/order equivalent routes.
// A recent successful result is revalidated alone before another fan-out.
func Probe(ctx context.Context, port int, addresses []models.NetworkAddress, preferred ...string) string {
	candidates := probeCandidates(addresses, preferred)
	if len(candidates) == 0 {
		return ""
	}

	cacheKey := pathCacheKey(port, candidates)
	if cached := loadSuccessfulPath(cacheKey, candidates); cached != "" {
		result := measureProbe(ctx, port, models.NetworkAddress{Address: cached})
		if result.Err == nil {
			storeSuccessfulPath(cacheKey, cached)
			return cached
		}
		deleteSuccessfulPath(cacheKey)
	}

	results := make(chan ProbeResult, len(candidates))
	jobs := make(chan models.NetworkAddress)
	var wg sync.WaitGroup
	workers := min(maxConcurrentProbes, len(candidates))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for candidate := range jobs {
				results <- measureProbe(ctx, port, candidate)
			}
		}()
	}

	go func() {
		for _, candidate := range candidates {
			jobs <- candidate
		}
		close(jobs)
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
	if best != "" {
		storeSuccessfulPath(cacheKey, best)
	}

	return best
}

func measureProbe(ctx context.Context, port int, candidate models.NetworkAddress) ProbeResult {
	latency, err := probeSSH(ctx, candidate.Address, port)
	if err == nil {
		latency += overlayPenalty(candidate)
	}
	return ProbeResult{Address: candidate.Address, Latency: latency, Err: err}
}

// probeCandidates removes unusable/exact duplicates and collapses multiple IPv6
// addresses advertising the same interface route. Linux privacy addresses share
// that route with their stable peer, so probing each one only creates redundant
// unauthenticated SSH connections. A configured/connected target wins its route.
func probeCandidates(addresses []models.NetworkAddress, preferred []string) []models.NetworkAddress {
	preferredSet := make(map[string]bool, len(preferred))
	for _, raw := range preferred {
		if ip, err := netip.ParseAddr(strings.TrimSpace(raw)); err == nil {
			preferredSet[ip.Unmap().String()] = true
		}
	}

	var candidates []models.NetworkAddress
	exact := make(map[string]int)
	ipv6Routes := make(map[string]int)
	for _, candidate := range addresses {
		ip, err := netip.ParseAddr(strings.TrimSpace(candidate.Address))
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		candidate.Address = ip.String()
		if isDockerBridgeAddr(candidate.Address) {
			continue
		}

		if idx, ok := exact[candidate.Address]; ok {
			if candidatePreferred(candidate, preferredSet) || overlayPenalty(candidate) < overlayPenalty(candidates[idx]) {
				candidates[idx] = candidate
			}
			continue
		}

		if route := ipv6RouteKey(candidate, ip); route != "" {
			if idx, ok := ipv6Routes[route]; ok {
				if candidatePreferred(candidate, preferredSet) && !candidatePreferred(candidates[idx], preferredSet) {
					delete(exact, candidates[idx].Address)
					candidates[idx] = candidate
					exact[candidate.Address] = idx
				}
				continue
			}
			ipv6Routes[route] = len(candidates)
		}

		exact[candidate.Address] = len(candidates)
		candidates = append(candidates, candidate)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidatePreferred(candidates[i], preferredSet) && !candidatePreferred(candidates[j], preferredSet)
	})
	return candidates
}

func ipv6RouteKey(candidate models.NetworkAddress, ip netip.Addr) string {
	if !ip.Is6() || candidate.Interface == "" || candidate.Subnet == "" {
		return ""
	}
	prefix, err := netip.ParsePrefix(candidate.Subnet)
	if err != nil || !prefix.Addr().Is6() || prefix.Bits() == 128 {
		return ""
	}
	return candidate.Interface + "|" + prefix.Masked().String()
}

func candidatePreferred(candidate models.NetworkAddress, preferred map[string]bool) bool {
	return preferred[candidate.Address]
}

func overlayPenalty(candidate models.NetworkAddress) time.Duration {
	switch candidate.SpeedClass {
	case "tailscale", "zerotier", "vpn", "wireguard", "netbird":
		return 50 * time.Millisecond
	}
	if ip, err := netip.ParseAddr(candidate.Address); err == nil && isTailscaleAddr(ip) {
		return 50 * time.Millisecond
	}
	return 0
}

func pathCacheKey(port int, candidates []models.NetworkAddress) string {
	addresses := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		addresses = append(addresses, candidate.Address)
	}
	sort.Strings(addresses)
	return strconv.Itoa(port) + "|" + strings.Join(addresses, ",")
}

func loadSuccessfulPath(key string, candidates []models.NetworkAddress) string {
	successfulPaths.Lock()
	defer successfulPaths.Unlock()
	entry, ok := successfulPaths.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(successfulPaths.entries, key)
		return ""
	}
	for _, candidate := range candidates {
		if candidate.Address == entry.address {
			return entry.address
		}
	}
	delete(successfulPaths.entries, key)
	return ""
}

func storeSuccessfulPath(key, address string) {
	successfulPaths.Lock()
	defer successfulPaths.Unlock()
	successfulPaths.entries[key] = cachedPath{address: address, expiresAt: time.Now().Add(successCacheTTL)}
}

func deleteSuccessfulPath(key string) {
	successfulPaths.Lock()
	defer successfulPaths.Unlock()
	delete(successfulPaths.entries, key)
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
	_, _ = conn.Read(make([]byte, 255))

	return time.Since(start), nil
}

func isDockerBridgeAddr(ip string) bool {
	parsed, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	parsed = parsed.Unmap()
	if parsed.Is4() {
		b := parsed.As4()
		// Docker default and user-defined bridges: 172.16.0.0/12
		if b[0] == 172 && b[1] >= 16 && b[1] <= 31 {
			return true
		}
	}
	return false
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
