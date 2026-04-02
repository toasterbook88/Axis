package models

import (
	"os"
	"testing"
)

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

	if IsLocalConfig(hostname, "198.51.100.7") {
		t.Fatal("expected logical name alone to not mark config as local")
	}
}

func TestIsLocalConfigMatchesObservedHostname(t *testing.T) {
	if !IsLocalConfig("remote-alias", "127.0.0.1") {
		t.Fatal("expected loopback config hostname to be treated as local")
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
