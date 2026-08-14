package multipath

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// ProbeResult holds the ephemeral result of probing a single address. It is not
// persisted because failures and durations are relative to the process that
// performed the probe.
type ProbeResult struct {
	Address                   string
	SSHIdentificationDuration time.Duration
	SelectionScore            time.Duration
	Err                       error
}

// ProbeDecision describes the selected concrete SSH endpoint. Durations are
// SSH identification probe durations, not network latency or a full SSH
// handshake. The decision remains internal until snapshots can attribute route
// observations to an explicit vantage point.
type ProbeDecision struct {
	SelectedTarget              string
	SSHIdentificationDuration   time.Duration
	AdjustedSelectionScore      time.Duration
	CandidateCount              int
	FailedProbeCount            int
	ObservedAt                  time.Time
	RevalidatedCachedTargetOnly bool
}

// Stats is process-lifetime observability for route selection. It deliberately
// reports counts rather than persisting probe latencies without a durable
// observer identity.
type Stats struct {
	Decisions          int64 `json:"decisions"`
	CandidateAttempts  int64 `json:"candidate_attempts"`
	CacheRevalidations int64 `json:"cache_revalidations"`
	FanoutDecisions    int64 `json:"fanout_decisions"`
	FailedAttempts     int64 `json:"failed_attempts"`
}

var probeStats struct {
	decisions          atomic.Int64
	candidateAttempts  atomic.Int64
	cacheRevalidations atomic.Int64
	fanoutDecisions    atomic.Int64
	failedAttempts     atomic.Int64
}

// SnapshotStats returns a consistent-enough lock-free snapshot of monotonic
// route-probe counters. Individual fields may advance during the read.
func SnapshotStats() Stats {
	return Stats{
		Decisions:          probeStats.decisions.Load(),
		CandidateAttempts:  probeStats.candidateAttempts.Load(),
		CacheRevalidations: probeStats.cacheRevalidations.Load(),
		FanoutDecisions:    probeStats.fanoutDecisions.Load(),
		FailedAttempts:     probeStats.failedAttempts.Load(),
	}
}

const (
	maxConcurrentProbes = 3
	successCacheTTL     = 2 * time.Minute
	maxSuccessCacheSize = 256
	maxIdentification   = 255
	maxPreBannerLines   = 20
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
// fastest reachable path that identifies itself as SSH. preferred contains
// already-known logical or connected targets and is used to choose/order
// equivalent routes. A recent successful result is revalidated alone before
// another fan-out.
func Probe(ctx context.Context, port int, addresses []models.NetworkAddress, preferred ...string) ProbeDecision {
	return probe(ctx, port, addresses, measureProbe, preferred...)
}

type measureFunc func(context.Context, int, models.NetworkAddress) ProbeResult

func probe(ctx context.Context, port int, addresses []models.NetworkAddress, measure measureFunc, preferred ...string) ProbeDecision {
	candidates := probeCandidates(addresses, preferred)
	decision := ProbeDecision{
		CandidateCount: len(candidates),
	}
	if len(candidates) == 0 {
		return decision
	}
	probeStats.decisions.Add(1)
	decision.ObservedAt = time.Now().UTC()

	cacheKey := pathCacheKey(port, candidates)
	if cached := loadSuccessfulPath(cacheKey, candidates); cached != "" {
		probeStats.candidateAttempts.Add(1)
		result := measure(ctx, port, candidateByAddress(candidates, cached))
		if result.Err == nil {
			storeSuccessfulPath(cacheKey, cached)
			probeStats.cacheRevalidations.Add(1)
			decision.SelectedTarget = cached
			decision.SSHIdentificationDuration = result.SSHIdentificationDuration
			decision.AdjustedSelectionScore = result.SelectionScore
			decision.RevalidatedCachedTargetOnly = true
			return decision
		}
		decision.FailedProbeCount++
		probeStats.failedAttempts.Add(1)
		deleteSuccessfulPath(cacheKey)
	}

	probeStats.fanoutDecisions.Add(1)
	results := make(chan ProbeResult, len(candidates))
	jobs := make(chan models.NetworkAddress)
	var wg sync.WaitGroup
	workers := min(maxConcurrentProbes, len(candidates))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for candidate := range jobs {
				probeStats.candidateAttempts.Add(1)
				results <- measure(ctx, port, candidate)
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

	var best ProbeResult
	found := false

	for res := range results {
		if res.Err != nil {
			decision.FailedProbeCount++
			probeStats.failedAttempts.Add(1)
			continue
		}
		if !found || res.SelectionScore < best.SelectionScore ||
			(res.SelectionScore == best.SelectionScore && res.Address < best.Address) {
			best = res
			found = true
		}
	}
	if found {
		storeSuccessfulPath(cacheKey, best.Address)
		decision.SelectedTarget = best.Address
		decision.SSHIdentificationDuration = best.SSHIdentificationDuration
		decision.AdjustedSelectionScore = best.SelectionScore
	}

	return decision
}

func measureProbe(ctx context.Context, port int, candidate models.NetworkAddress) ProbeResult {
	duration, err := probeSSH(ctx, candidate.Address, port)
	score := duration
	if err == nil {
		score += overlayPenalty(candidate)
	}
	return ProbeResult{
		Address:                   candidate.Address,
		SSHIdentificationDuration: duration,
		SelectionScore:            score,
		Err:                       err,
	}
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
		if !eligibleAddress(ip) {
			continue
		}
		candidate.Address = ip.String()
		if isContainerBridgeCandidate(candidate) {
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

func eligibleAddress(ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
		return false
	}
	// NetworkAddress.Interface names the remote interface. It cannot provide
	// the observer-side zone required to dial an IPv6 link-local address.
	if ip.Is6() && ip.IsLinkLocalUnicast() {
		return false
	}
	return true
}

func isContainerBridgeCandidate(candidate models.NetworkAddress) bool {
	iface := strings.ToLower(strings.TrimSpace(candidate.Interface))
	return strings.HasPrefix(iface, "docker") ||
		strings.HasPrefix(iface, "br-") ||
		strings.HasPrefix(iface, "veth") ||
		strings.HasPrefix(iface, "cni") ||
		strings.HasPrefix(iface, "podman")
}

func candidateByAddress(candidates []models.NetworkAddress, address string) models.NetworkAddress {
	for _, candidate := range candidates {
		if candidate.Address == address {
			return candidate
		}
	}
	return models.NetworkAddress{Address: address}
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
	identities := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		identities = append(identities, strings.Join([]string{
			candidate.Address,
			candidate.Interface,
			candidate.Subnet,
			candidate.SpeedClass,
			candidate.Scope,
		}, "\x1f"))
	}
	sort.Strings(identities)
	return strconv.Itoa(port) + "|" + strings.Join(identities, "\x1e")
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
	now := time.Now()
	for existingKey, entry := range successfulPaths.entries {
		if now.After(entry.expiresAt) {
			delete(successfulPaths.entries, existingKey)
		}
	}
	if _, exists := successfulPaths.entries[key]; !exists && len(successfulPaths.entries) >= maxSuccessCacheSize {
		oldestKey := ""
		var oldestExpiry time.Time
		for existingKey, entry := range successfulPaths.entries {
			if oldestKey == "" || entry.expiresAt.Before(oldestExpiry) {
				oldestKey = existingKey
				oldestExpiry = entry.expiresAt
			}
		}
		delete(successfulPaths.entries, oldestKey)
	}
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

	readDeadline := time.Now().Add(time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(readDeadline) {
		readDeadline = deadline
	}
	if err := conn.SetReadDeadline(readDeadline); err != nil {
		return 0, fmt.Errorf("set SSH identification deadline: %w", err)
	}
	if err := readSSHIdentification(conn); err != nil {
		return 0, fmt.Errorf("read SSH identification from %s: %w", ip, err)
	}

	return time.Since(start), nil
}

func readSSHIdentification(conn net.Conn) error {
	reader := bufio.NewReaderSize(conn, maxIdentification+1)
	for range maxPreBannerLines + 1 {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxIdentification {
				return fmt.Errorf("identification line exceeds %d bytes", maxIdentification)
			}
			return err
		}
		if len(line) > maxIdentification {
			return fmt.Errorf("identification line exceeds %d bytes", maxIdentification)
		}
		identification := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
		if !strings.HasPrefix(identification, "SSH-") {
			continue
		}
		if strings.HasPrefix(identification, "SSH-2.0-") || strings.HasPrefix(identification, "SSH-1.99-") {
			return nil
		}
		return fmt.Errorf("unsupported SSH identification %q", identification)
	}
	return fmt.Errorf("SSH identification not found within %d pre-banner lines", maxPreBannerLines)
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
