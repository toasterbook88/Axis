package multipath

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestProbeSelectsReachableAddress(t *testing.T) {
	listener, port := startBannerServer(t)
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	addresses := []models.NetworkAddress{
		{},
		{Address: "172.17.0.2"},
		{Address: "127.0.0.2"},
		{Address: "127.0.0.1", SpeedClass: "tailscale"},
		{Address: "127.0.0.1"},
	}
	if got := Probe(ctx, port, addresses); got != "127.0.0.1" {
		t.Fatalf("Probe() = %q, want 127.0.0.1", got)
	}
}

func TestProbeWithoutCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	tests := []struct {
		name      string
		addresses []models.NetworkAddress
	}{
		{name: "empty"},
		{
			name: "skipped",
			addresses: []models.NetworkAddress{
				{},
				{Address: "172.18.0.2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Probe(ctx, 22, tt.addresses); got != "" {
				t.Fatalf("Probe() = %q, want empty result", got)
			}
		})
	}
}

func TestProbeSSHReadsBanner(t *testing.T) {
	listener, port := startBannerServer(t)
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	latency, err := probeSSH(ctx, "127.0.0.1", port)
	if err != nil {
		t.Fatalf("probeSSH() error = %v", err)
	}
	if latency <= 0 {
		t.Fatalf("probeSSH() latency = %v, want positive duration", latency)
	}
}

func TestProbeBoundsConcurrentConnections(t *testing.T) {
	listener, port, peak, _ := startTrackingBannerServer(t, 50*time.Millisecond)
	defer listener.Close()

	addresses := make([]models.NetworkAddress, 0, 8)
	for i := 1; i <= 8; i++ {
		addresses = append(addresses, models.NetworkAddress{Address: "127.0.0." + strconv.Itoa(i)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if got := Probe(ctx, port, addresses); got == "" {
		t.Fatal("Probe() returned no reachable address")
	}
	if got := peak.Load(); got > maxConcurrentProbes {
		t.Fatalf("peak concurrent probes = %d, want <= %d", got, maxConcurrentProbes)
	}
}

func TestProbeCandidatesCollapseIPv6Routes(t *testing.T) {
	addresses := []models.NetworkAddress{
		{Address: "192.0.2.10", Interface: "eth0", Subnet: "192.0.2.0/24"},
		{Address: "2001:db8::1", Interface: "eth0", Subnet: "2001:db8::/64"},
		{Address: "2001:db8::2", Interface: "eth0", Subnet: "2001:db8::/64"},
		{Address: "2001:db8:1::1", Interface: "eth1", Subnet: "2001:db8:1::/64"},
		{Address: "172.18.0.1", Interface: "docker0", Subnet: "172.18.0.0/16"},
	}

	got := probeCandidates(addresses, []string{"2001:db8::2"})
	if len(got) != 3 {
		t.Fatalf("candidate count = %d, want 3: %#v", len(got), got)
	}
	if got[0].Address != "2001:db8::2" {
		t.Fatalf("preferred route candidate = %q, want 2001:db8::2", got[0].Address)
	}
}

func TestProbeRevalidatesCachedSuccessfulPathAlone(t *testing.T) {
	listener, port, _, accepts := startTrackingBannerServer(t, 0)
	defer listener.Close()
	addresses := []models.NetworkAddress{
		{Address: "127.0.0.10"},
		{Address: "127.0.0.11"},
		{Address: "127.0.0.12"},
		{Address: "127.0.0.13"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	first := Probe(ctx, port, addresses)
	if first == "" {
		t.Fatal("first Probe() returned no reachable address")
	}
	firstAccepts := accepts.Load()
	if firstAccepts != int64(len(addresses)) {
		t.Fatalf("first probe accepts = %d, want %d", firstAccepts, len(addresses))
	}
	if second := Probe(ctx, port, addresses); second != first {
		t.Fatalf("cached Probe() = %q, want %q", second, first)
	}
	if delta := accepts.Load() - firstAccepts; delta != 1 {
		t.Fatalf("cached probe opened %d connections, want 1", delta)
	}
}

func TestIsDockerBridgeAddr(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "172.16.0.1", want: true}, // low end of 172.16.0.0/12
		{address: "172.17.0.1", want: true},
		{address: "172.18.10.2", want: true},
		{address: "172.31.255.254", want: true},
		{address: "::ffff:172.17.1.2", want: true},
		{address: "172.15.0.1", want: false},
		{address: "172.32.0.1", want: false},
		{address: "192.168.1.10", want: false},
		{address: "fd00::1", want: false},
		{address: "not-an-address", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			if got := isDockerBridgeAddr(tt.address); got != tt.want {
				t.Fatalf("isDockerBridgeAddr(%q) = %t, want %t", tt.address, got, tt.want)
			}
		})
	}
}

func TestIsTailscaleAddr(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "fd7a:115c:a1e0::1", want: true},
		{address: "fd7a:115c:a1e1::1", want: false},
		{address: "100.64.0.1", want: true},        // IPv4 CGNAT low
		{address: "100.127.255.254", want: true},   // IPv4 CGNAT high
		{address: "100.63.0.1", want: false},       // just below CGNAT
		{address: "100.128.0.1", want: false},      // just above CGNAT
		{address: "::ffff:100.64.0.1", want: true}, // v4-mapped CGNAT must still match
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

func startBannerServer(t *testing.T) (net.Listener, int) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("SSH-2.0-axis-test\r\n"))
			_ = conn.Close()
		}
	}()

	return listener, port
}

func startTrackingBannerServer(t *testing.T, delay time.Duration) (net.Listener, int, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	var active atomic.Int64
	var peak atomic.Int64
	var accepts atomic.Int64

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			current := active.Add(1)
			for {
				observed := peak.Load()
				if current <= observed || peak.CompareAndSwap(observed, current) {
					break
				}
			}
			go func() {
				defer active.Add(-1)
				defer conn.Close()
				time.Sleep(delay)
				_, _ = conn.Write([]byte("SSH-2.0-axis-test\r\n"))
			}()
		}
	}()

	return listener, port, &peak, &accepts
}
