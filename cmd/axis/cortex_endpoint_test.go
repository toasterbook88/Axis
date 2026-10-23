package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/cortex"
)

// writeCortexNodesYAML points AXIS_HOME at a temp dir holding the given nodes.yaml body.
// An empty body leaves no config file at all.
func writeCortexNodesYAML(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AXIS_HOME", dir)
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "nodes.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildCortexClient_ExplicitServiceURLNeedsNoNodesYAML(t *testing.T) {
	writeCortexNodesYAML(t, "")
	t.Setenv("AXIS_CORTEX_URL", "https://cortex.example.test")
	t.Setenv("AXIS_CORTEX_QDRANT_URL", "")
	t.Setenv("AXIS_CORTEX_NODE", "")

	client, err := buildCortexClient(time.Second)
	if err != nil {
		t.Fatalf("buildCortexClient: %v", err)
	}
	if got := client.MCPEndpoint(); got != "https://cortex.example.test/mcp" {
		t.Fatalf("MCPEndpoint = %q, want the explicit service URL with /mcp", got)
	}
}

func TestBuildCortexClient_ExplicitServiceURLBeatsNodeSelection(t *testing.T) {
	writeCortexNodesYAML(t, "nodes:\n  - name: foundry\n    hostname: foundry-ssh-alias\n    ssh_user: axis\n")
	t.Setenv("AXIS_CORTEX_URL", "https://cortex.example.test/mcp")
	t.Setenv("AXIS_CORTEX_NODE", "foundry")

	client, err := buildCortexClient(time.Second)
	if err != nil {
		t.Fatalf("buildCortexClient: %v", err)
	}
	if strings.Contains(client.MCPEndpoint(), "foundry-ssh-alias") {
		t.Fatalf("explicit service URL must not be replaced by the SSH node hostname: %q", client.MCPEndpoint())
	}
}

func TestBuildCortexClient_InvalidServiceURLFailsInsteadOfFallingBack(t *testing.T) {
	writeCortexNodesYAML(t, "nodes:\n  - name: foundry\n    hostname: foundry-ssh-alias\n    ssh_user: axis\n")
	t.Setenv("AXIS_CORTEX_URL", "ftp://cortex.example.test/mcp")

	_, err := buildCortexClient(time.Second)
	if err == nil || !strings.Contains(err.Error(), "AXIS_CORTEX_URL") {
		t.Fatalf("err = %v, want an AXIS_CORTEX_URL error rather than a silent node fallback", err)
	}
}

func TestBuildCortexClient_InvalidQdrantURLFails(t *testing.T) {
	writeCortexNodesYAML(t, "")
	t.Setenv("AXIS_CORTEX_URL", "https://cortex.example.test/mcp")
	t.Setenv("AXIS_CORTEX_QDRANT_URL", "not a url")

	_, err := buildCortexClient(time.Second)
	if err == nil || !strings.Contains(err.Error(), "AXIS_CORTEX_QDRANT_URL") {
		t.Fatalf("err = %v, want an AXIS_CORTEX_QDRANT_URL error", err)
	}
}

func TestBuildCortexClient_UnsetServiceURLKeepsNodeFallback(t *testing.T) {
	writeCortexNodesYAML(t, "nodes:\n  - name: foundry\n    hostname: foundry-ssh-alias\n    ssh_user: axis\n")
	t.Setenv("AXIS_CORTEX_URL", "")
	t.Setenv("AXIS_CORTEX_NODE", "")

	client, err := buildCortexClient(time.Second)
	if err != nil {
		t.Fatalf("buildCortexClient: %v", err)
	}
	if got := client.MCPEndpoint(); got != "http://foundry-ssh-alias:8200/mcp" {
		t.Fatalf("MCPEndpoint = %q, want unchanged legacy node-derived URL", got)
	}
}

func TestPrintCortexStatus_UnknownMemoriesIsNotZero(t *testing.T) {
	var buf bytes.Buffer
	if err := printCortexStatus(&buf, &cortex.HealthResponse{Status: "healthy", MCPTools: 42, MemoriesUnknown: true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "Endpoint:") {
		t.Fatalf("empty endpoint must not print an Endpoint line: %q", out)
	}
	if !strings.Contains(out, "unknown") || strings.Contains(out, "0 (Qdrant points)") {
		t.Fatalf("status output = %q, want unknown memories rather than 0", out)
	}
}

func TestPrintCortexStatus_ShowsProbedEndpoint(t *testing.T) {
	var buf bytes.Buffer
	if err := printCortexStatus(&buf, &cortex.HealthResponse{Status: "healthy", Endpoint: "https://cortex.example.test/mcp"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "https://cortex.example.test/mcp") {
		t.Fatalf("status output = %q, want the probed endpoint", buf.String())
	}
}

func TestBuildCortexClient_WhitespaceServiceURLFailsInsteadOfFallingBack(t *testing.T) {
	writeCortexNodesYAML(t, "nodes:\n  - name: foundry\n    hostname: foundry-ssh-alias\n    ssh_user: axis\n")
	t.Setenv("AXIS_CORTEX_URL", "   ")

	_, err := buildCortexClient(time.Second)
	if err == nil || !strings.Contains(err.Error(), "AXIS_CORTEX_URL") {
		t.Fatalf("err = %v, want a whitespace-only AXIS_CORTEX_URL rejected, not a node fallback", err)
	}
}
