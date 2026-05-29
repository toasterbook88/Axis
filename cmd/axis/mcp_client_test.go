package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

func TestMCPClientListEmptyConfig(t *testing.T) {
	restore := func() {
		loadMCPClientConfig = config.Load
	}
	defer restore()

	loadMCPClientConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{
				{Name: "dummy", Hostname: "localhost", SSHUser: "root"},
			},
		}, nil
	}

	var buf bytes.Buffer
	err := runMCPClientList(context.Background(), &buf, "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "No MCP servers configured") {
		t.Fatalf("expected empty config message, got: %s", out)
	}
}

func TestMCPClientListJSON(t *testing.T) {
	restore := func() {
		loadMCPClientConfig = config.Load
	}
	defer restore()

	loadMCPClientConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{
				{Name: "dummy", Hostname: "localhost", SSHUser: "root"},
			},
			MCPServers: map[string]config.MCPServerConfig{
				"test": {Transport: "stdio", Command: []string{"echo", "hello"}},
			},
		}, nil
	}

	var buf bytes.Buffer
	err := runMCPClientList(context.Background(), &buf, "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[") {
		t.Fatalf("expected JSON array, got: %s", out)
	}
}

func TestMCPClientToolsMissingServer(t *testing.T) {
	restore := func() {
		loadMCPClientConfig = config.Load
	}
	defer restore()

	loadMCPClientConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{
				{Name: "dummy", Hostname: "localhost", SSHUser: "root"},
			},
			MCPServers: map[string]config.MCPServerConfig{},
		}, nil
	}

	var buf bytes.Buffer
	err := runMCPClientTools(context.Background(), &buf, "missing", "text")
	if err == nil {
		t.Fatal("expected error for missing server")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMCPClientParseArgs(t *testing.T) {
	restore := func() {
		loadMCPClientConfig = config.Load
	}
	defer restore()

	loadMCPClientConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{
				{Name: "dummy", Hostname: "localhost", SSHUser: "root"},
			},
			MCPServers: map[string]config.MCPServerConfig{},
		}, nil
	}

	var buf bytes.Buffer
	// Call with a non-existent server to test arg parsing path
	err := runMCPClientCall(context.Background(), &buf, "missing", "tool", `{"key":"value"}`)
	if err == nil {
		t.Fatal("expected error for missing server")
	}
}
