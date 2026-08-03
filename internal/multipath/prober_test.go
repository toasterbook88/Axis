package multipath

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestProbeSelectsReachableAddress(t *testing.T) {
	resetSuccessfulPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	addresses := []models.NetworkAddress{
		{},
		{Address: "127.0.0.1"},
		{Address: "192.0.2.9", Interface: "docker0"},
		{Address: "192.0.2.10"},
		{Address: "192.0.2.11", SpeedClass: "tailscale"},
	}
	durations := map[string]time.Duration{
		"192.0.2.10": 4 * time.Millisecond,
		"192.0.2.11": time.Millisecond,
	}
	decision := probe(ctx, 22, addresses, func(_ context.Context, _ int, candidate models.NetworkAddress) ProbeResult {
		duration := durations[candidate.Address]
		return successfulProbeResult(candidate, duration)
	})

	if decision.SelectedTarget != "192.0.2.10" {
		t.Fatalf("selected target = %q, want 192.0.2.10", decision.SelectedTarget)
	}
	if decision.CandidateCount != 2 {
		t.Fatalf("candidate count = %d, want 2", decision.CandidateCount)
	}
	if decision.SSHIdentificationDuration != 4*time.Millisecond {
		t.Fatalf("SSH identification duration = %v, want 4ms", decision.SSHIdentificationDuration)
	}
	if decision.AdjustedSelectionScore != 4*time.Millisecond {
		t.Fatalf("adjusted selection score = %v, want 4ms", decision.AdjustedSelectionScore)
	}
}

func TestProbeWithoutCandidates(t *testing.T) {
	resetSuccessfulPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	addresses := []models.NetworkAddress{
		{},
		{Address: "0.0.0.0"},
		{Address: "127.0.0.1"},
		{Address: "224.0.0.1"},
		{Address: "fe80::1", Scope: "link-local"},
		{Address: "192.0.2.9", Interface: "docker0"},
	}
	decision := Probe(ctx, 22, addresses)
	if decision.SelectedTarget != "" || decision.CandidateCount != 0 {
		t.Fatalf("Probe() = %#v, want an empty decision", decision)
	}
}

func TestProbeSSHValidatesIdentification(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "ssh 2", payload: "SSH-2.0-axis-test\r\n"},
		{name: "compatibility", payload: "SSH-1.99-axis-test\r\n"},
		{name: "pre-banner", payload: "maintenance notice\r\nSSH-2.0-axis-test\r\n"},
		{name: "unsupported protocol", payload: "SSH-1.5-obsolete\r\n", wantErr: true},
		{name: "not ssh", payload: "HTTP/1.1 200 OK\r\n", wantErr: true},
		{name: "overlong", payload: strings.Repeat("x", maxIdentification+1) + "\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener, port := startPayloadServer(t, tt.payload)
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			duration, err := probeSSH(ctx, "127.0.0.1", port)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("probeSSH() duration = %v, want error", duration)
				}
				return
			}
			if err != nil {
				t.Fatalf("probeSSH() error = %v", err)
			}
			if duration <= 0 {
				t.Fatalf("probeSSH() duration = %v, want positive duration", duration)
			}
		})
	}
}

func TestProbeSSHRejectsSilentListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		time.Sleep(200 * time.Millisecond)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = probeSSH(ctx, "127.0.0.1", listener.Addr().(*net.TCPAddr).Port)
	if err == nil {
		t.Fatal("probeSSH() succeeded for a silent listener")
	}
}

func TestProbeBoundsConcurrentConnections(t *testing.T) {
	resetSuccessfulPaths(t)
	addresses := make([]models.NetworkAddress, 0, 8)
	for i := 1; i <= 8; i++ {
		addresses = append(addresses, models.NetworkAddress{Address: "192.0.2." + strconv.Itoa(i)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var active atomic.Int64
	var peak atomic.Int64
	measure := func(_ context.Context, _ int, candidate models.NetworkAddress) ProbeResult {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := peak.Load()
			if current <= observed || peak.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
		return successfulProbeResult(candidate, time.Millisecond)
	}

	decision := probe(ctx, 22, addresses, measure)
	if decision.SelectedTarget == "" {
		t.Fatal("probe() returned no reachable address")
	}
	if got := peak.Load(); got > maxConcurrentProbes {
		t.Fatalf("peak concurrent probes = %d, want <= %d", got, maxConcurrentProbes)
	}
}

func TestProbeCandidatesFilterAndCollapseRoutes(t *testing.T) {
	addresses := []models.NetworkAddress{
		{Address: "127.0.0.1", Interface: "lo"},
		{Address: "0.0.0.0"},
		{Address: "224.0.0.1"},
		{Address: "192.0.2.9", Interface: "docker0"},
		{Address: "172.18.0.1", Interface: "eth0"},
		{Address: "169.254.1.2", Interface: "en5", Scope: "link-local"},
		{Address: "fe80::1", Scope: "link-local"},
		{Address: "fe80::2", Interface: "eth0", Scope: "link-local"},
		{Address: "2001:db8::1", Interface: "eth0", Subnet: "2001:db8::/64"},
		{Address: "2001:db8::2", Interface: "eth0", Subnet: "2001:db8::/64"},
		{Address: "2001:db8:1::1", Interface: "eth1", Subnet: "2001:db8:1::/64"},
	}

	got := probeCandidates(addresses, []string{"fe80::2", "2001:db8::2"})
	want := []string{"2001:db8::2", "172.18.0.1", "169.254.1.2", "2001:db8:1::1"}
	if len(got) != len(want) {
		t.Fatalf("candidate count = %d, want %d: %#v", len(got), len(want), got)
	}
	for i, address := range want {
		if got[i].Address != address {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i].Address, address)
		}
	}
}

func TestProbeRevalidatesCachedSuccessfulPathAlone(t *testing.T) {
	resetSuccessfulPaths(t)
	addresses := []models.NetworkAddress{
		{Address: "192.0.2.10"},
		{Address: "192.0.2.11"},
		{Address: "192.0.2.12"},
		{Address: "192.0.2.13"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var calls atomic.Int64
	measure := func(_ context.Context, _ int, candidate models.NetworkAddress) ProbeResult {
		calls.Add(1)
		return successfulProbeResult(candidate, time.Millisecond)
	}
	first := probe(ctx, 22, addresses, measure)
	if first.SelectedTarget == "" {
		t.Fatal("first probe() returned no reachable address")
	}
	firstCalls := calls.Load()
	if firstCalls != int64(len(addresses)) {
		t.Fatalf("first probe calls = %d, want %d", firstCalls, len(addresses))
	}
	second := probe(ctx, 22, addresses, measure)
	if second.SelectedTarget != first.SelectedTarget {
		t.Fatalf("cached target = %q, want %q", second.SelectedTarget, first.SelectedTarget)
	}
	if !second.RevalidatedCachedTargetOnly {
		t.Fatal("cached decision did not report single-target revalidation")
	}
	if delta := calls.Load() - firstCalls; delta != 1 {
		t.Fatalf("cached probe made %d calls, want 1", delta)
	}
}

func TestProbeReportsFailures(t *testing.T) {
	resetSuccessfulPaths(t)
	addresses := []models.NetworkAddress{{Address: "192.0.2.10"}, {Address: "192.0.2.11"}}
	measure := func(_ context.Context, _ int, candidate models.NetworkAddress) ProbeResult {
		if candidate.Address == "192.0.2.10" {
			return ProbeResult{Address: candidate.Address, Err: errors.New("unreachable")}
		}
		return successfulProbeResult(candidate, time.Millisecond)
	}
	decision := probe(context.Background(), 22, addresses, measure)
	if decision.SelectedTarget != "192.0.2.11" || decision.FailedProbeCount != 1 {
		t.Fatalf("probe() = %#v, want one failed probe and target 192.0.2.11", decision)
	}
}

func TestSuccessfulPathCacheIsBounded(t *testing.T) {
	resetSuccessfulPaths(t)
	for i := 0; i < maxSuccessCacheSize+20; i++ {
		storeSuccessfulPath("key-"+strconv.Itoa(i), "192.0.2.1")
	}
	successfulPaths.Lock()
	got := len(successfulPaths.entries)
	successfulPaths.Unlock()
	if got != maxSuccessCacheSize {
		t.Fatalf("cache size = %d, want %d", got, maxSuccessCacheSize)
	}
}

func TestPathCacheKeyIncludesSelectionMetadata(t *testing.T) {
	base := []models.NetworkAddress{{Address: "192.0.2.10", Interface: "eth0", SpeedClass: "gigabit"}}
	changed := []models.NetworkAddress{{Address: "192.0.2.10", Interface: "eth0", SpeedClass: "tailscale"}}
	if pathCacheKey(22, base) == pathCacheKey(22, changed) {
		t.Fatal("cache key did not change with selection metadata")
	}
}

func TestIsTailscaleAddr(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "fd7a:115c:a1e0::1", want: true},
		{address: "fd7a:115c:a1e1::1", want: false},
		{address: "100.64.0.1", want: true},
		{address: "100.127.255.254", want: true},
		{address: "100.63.0.1", want: false},
		{address: "100.128.0.1", want: false},
		{address: "::ffff:100.64.0.1", want: true},
		{address: "192.168.1.1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			address := netip.MustParseAddr(tt.address)
			if got := isTailscaleAddr(address); got != tt.want {
				t.Fatalf("isTailscaleAddr(%q) = %t, want %t", tt.address, got, tt.want)
			}
		})
	}
}

func successfulProbeResult(candidate models.NetworkAddress, duration time.Duration) ProbeResult {
	return ProbeResult{
		Address:                   candidate.Address,
		SSHIdentificationDuration: duration,
		SelectionScore:            duration + overlayPenalty(candidate),
	}
}

func resetSuccessfulPaths(t *testing.T) {
	t.Helper()
	successfulPaths.Lock()
	previous := successfulPaths.entries
	successfulPaths.entries = make(map[string]cachedPath)
	successfulPaths.Unlock()
	t.Cleanup(func() {
		successfulPaths.Lock()
		successfulPaths.entries = previous
		successfulPaths.Unlock()
	})
}

func startPayloadServer(t *testing.T, payload string) (net.Listener, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_, _ = conn.Write([]byte(payload))
			_ = conn.Close()
		}
	}()
	return listener, port
}
