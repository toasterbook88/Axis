package main

import (
	"testing"

	"github.com/toasterbook88/axis/internal/api"
)

// TestMCPServeCacheAddrDefaultsToDaemonAddr pins the default to the same
// resolver the daemon uses. Before this, `mcp serve` dialed a hardcoded TCP
// address while `axis daemon` listened on a Unix socket, so every cached tool
// failed with "connection refused" on a healthy daemon.
func TestMCPServeCacheAddrDefaultsToDaemonAddr(t *testing.T) {
	cmd := mcpServeCmd()
	flag := cmd.Flags().Lookup("cache-addr")
	if flag == nil {
		t.Fatal("mcp serve has no --cache-addr flag")
	}
	if got, want := flag.DefValue, api.DefaultAddr(); got != want {
		t.Fatalf("--cache-addr default = %q, want %q (api.DefaultAddr)", got, want)
	}
}

// TestMCPServeCacheAddrDefaultIsNotLegacyTCP is the regression guard: the old
// literal must never come back as the default.
func TestMCPServeCacheAddrDefaultIsNotLegacyTCP(t *testing.T) {
	cmd := mcpServeCmd()
	flag := cmd.Flags().Lookup("cache-addr")
	if flag == nil {
		t.Fatal("mcp serve has no --cache-addr flag")
	}
	if flag.DefValue == "127.0.0.1:42425" {
		t.Fatalf("--cache-addr default still hardcodes the legacy TCP address %q", flag.DefValue)
	}
}

// TestMCPServeCacheAddrOverride proves an explicit value still wins, for both
// a Unix socket path and a TCP host:port.
func TestMCPServeCacheAddrOverride(t *testing.T) {
	for _, want := range []string{"/tmp/axis-test-probe.sock", "127.0.0.1:42499"} {
		cmd := mcpServeCmd()
		if err := cmd.Flags().Set("cache-addr", want); err != nil {
			t.Fatalf("set --cache-addr %q: %v", want, err)
		}
		got, err := cmd.Flags().GetString("cache-addr")
		if err != nil {
			t.Fatalf("read --cache-addr: %v", err)
		}
		if got != want {
			t.Fatalf("--cache-addr override = %q, want %q", got, want)
		}
	}
}
