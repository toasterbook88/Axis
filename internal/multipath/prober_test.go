package multipath

import (
	"context"
	"net"
	"net/netip"
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

func TestIsDockerBridgeAddr(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "172.17.0.1", want: true},
		{address: "172.18.10.2", want: true},
		{address: "172.31.255.254", want: true},
		{address: "::ffff:172.17.1.2", want: true},
		{address: "172.16.0.1", want: false},
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
		{address: "100.64.0.1", want: false},
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
