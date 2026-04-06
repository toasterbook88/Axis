package models

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"testing"
)

func stubCurrentLocalIdentity(t *testing.T, hostname string, ips []string, stableID string) {
	prevHostname := localHostnameFn
	prevIPs := localInterfaceIPsFn
	prevReadFile := localStableIDReadFile
	prevCommand := localStableIDCommand

	localHostnameFn = func() (string, error) { return hostname, nil }
	localInterfaceIPsFn = func() []string { return ips }
	localStableIDReadFile = func(path string) ([]byte, error) {
		if stableID == "" {
			return nil, os.ErrNotExist
		}
		return []byte(stableID), nil
	}
	localStableIDCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if stableID == "" {
			return "", errors.New("stable identity unavailable")
		}
		if runtime.GOOS == "darwin" {
			return `"IOPlatformUUID" = "` + stableID + `"`, nil
		}
		return "", errors.New("unexpected command invocation")
	}
	localStableIDCached = ""
	localStableIDOnce = sync.Once{}

	t.Cleanup(func() {
		localHostnameFn = prevHostname
		localInterfaceIPsFn = prevIPs
		localStableIDReadFile = prevReadFile
		localStableIDCommand = prevCommand
		localStableIDCached = ""
		localStableIDOnce = sync.Once{}
	})
}

func TestIsLocalTargetMatchesHostnameAndLoopback(t *testing.T) {
	if !IsLocalTarget("localhost", "m3.local") {
		t.Fatal("expected localhost to match current machine")
	}
	if !IsLocalTarget("127.0.0.1", "m3.local") {
		t.Fatal("expected loopback IPv4 to match current machine")
	}
	if !IsLocalTarget("::1", "m3.local") {
		t.Fatal("expected loopback IPv6 to match current machine")
	}
	if !IsLocalTarget("m3", "m3.local") {
		t.Fatal("expected short hostname to match fqdn")
	}
}

func TestIsLocalConfigIgnoresLogicalNameMatches(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}

	if IsLocalConfig(hostname, "198.51.100.7", "") {
		t.Fatal("expected logical name alone to not mark config as local")
	}
}

func TestIsLocalConfigMatchesObservedHostname(t *testing.T) {
	if !IsLocalConfig("remote-alias", "127.0.0.1", "") {
		t.Fatal("expected loopback config hostname to be treated as local")
	}
}

func TestIsLocalConfigMatchesStableIdentity(t *testing.T) {
	stubCurrentLocalIdentity(t, "local.example", []string{"192.0.2.10"}, "ABC-123")

	if !IsLocalConfig("remote-alias", "198.51.100.7", "abc-123") {
		t.Fatal("expected stable identity to mark config as local")
	}
}

func TestIsLocalNodeIgnoresLogicalNameMatches(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}

	node := NodeFacts{
		Name:     hostname,
		Hostname: "definitely-remote.invalid",
		Addresses: []NetworkAddress{
			{Kind: "ipv4", Address: "198.51.100.8"},
		},
	}

	if IsLocalNode(node) {
		t.Fatal("expected logical node name alone to not mark node as local")
	}
}

func TestIsLocalNodeMatchesObservedHostnameAndAddress(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}

	if !IsLocalNode(NodeFacts{Hostname: hostname}) {
		t.Fatal("expected observed hostname to match local machine")
	}
	if !IsLocalNode(NodeFacts{
		Hostname: "remote.invalid",
		Addresses: []NetworkAddress{
			{Kind: "ipv4", Address: "127.0.0.1"},
		},
	}) {
		t.Fatal("expected loopback address to match local machine")
	}
}

func TestIsLocalNodeMatchesStableIdentity(t *testing.T) {
	stubCurrentLocalIdentity(t, "local.example", []string{"192.0.2.10"}, "ABC-123")

	node := NodeFacts{
		Name:     "remote-alias",
		Hostname: "definitely-remote.invalid",
		Identity: NewNodeIdentity("abc-123", "linux-machine-id"),
	}

	if !IsLocalNode(node) {
		t.Fatal("expected stable identity to mark node as local")
	}
}

func TestFindLocalNodePrefersLocalIdentityMatch(t *testing.T) {
	stubCurrentLocalIdentity(t, "local.example", []string{"192.0.2.10"}, "abc-123")

	nodes := []NodeFacts{
		{Name: "remote", Hostname: "remote.invalid"},
		{Name: "local", Hostname: "other.invalid", Identity: NewNodeIdentity("abc-123", "linux-machine-id")},
	}

	got, ok := FindLocalNode(nodes)
	if !ok {
		t.Fatal("expected local node match")
	}
	if got.Name != "local" {
		t.Fatalf("FindLocalNode() = %q, want local", got.Name)
	}
}

func TestParseDarwinPlatformUUID(t *testing.T) {
	ioreg := `"IOPlatformUUID" = "F47AC10B-58CC-4372-A567-0E02B2C3D479"`
	if got := ParseDarwinPlatformUUID(ioreg); got != "f47ac10b-58cc-4372-a567-0e02b2c3d479" {
		t.Fatalf("ParseDarwinPlatformUUID(ioreg) = %q", got)
	}

	systemProfiler := "Hardware UUID: F47AC10B-58CC-4372-A567-0E02B2C3D479"
	if got := ParseDarwinPlatformUUID(systemProfiler); got != "f47ac10b-58cc-4372-a567-0e02b2c3d479" {
		t.Fatalf("ParseDarwinPlatformUUID(system_profiler) = %q", got)
	}
}
