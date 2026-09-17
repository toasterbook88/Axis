package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
)

func writeMeshTestConfig(t *testing.T, home, body string) {
	t.Helper()
	cfgPath := filepath.Join(home, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestMeshStatusCmd(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	writeMeshTestConfig(t, tempHome, `nodes:
  - name: local
    hostname: localhost
    ssh_user: axis
`)

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := meshCmd()
		cmd.SetArgs([]string{"status"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("mesh status Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "Discovery Beacons:") || !strings.Contains(stdout, "DISABLED") {
		t.Errorf("expected disabled discovery status, got %q", stdout)
	}
	if !strings.Contains(stdout, "Gossip Mesh:") {
		t.Errorf("expected gossip plane in status, got %q", stdout)
	}
	if strings.Contains(stdout, "No active Gossip neighbors discovered.") {
		t.Errorf("status must not use the old empty-peer lie: %q", stdout)
	}
}

func TestMeshCommandsPropagateWriterFailures(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	writeMeshTestConfig(t, tempHome, `nodes:
  - name: local
    hostname: localhost
    ssh_user: axis
`)

	wantErr := errors.New("writer unavailable")
	for _, command := range []struct {
		name string
		new  func() *cobra.Command
	}{
		{name: "status", new: meshStatusCmd},
		{name: "peers", new: meshPeersCmd},
	} {
		t.Run(command.name, func(t *testing.T) {
			cmd := command.new()
			cmd.SetOut(rejectingOutputWriter{err: wantErr})
			cmd.SetErr(&strings.Builder{})
			if err := cmd.Execute(); !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want writer failure", err)
			}
		})
	}
}

func TestMeshCommandsHonorCanceledContext(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	writeMeshTestConfig(t, tempHome, `nodes:
  - name: local
    hostname: localhost
    ssh_user: axis
`)

	for _, command := range []struct {
		name string
		new  func() *cobra.Command
	}{
		{name: "status", new: meshStatusCmd},
		{name: "peers", new: meshPeersCmd},
	} {
		t.Run(command.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var out strings.Builder
			cmd := command.new()
			cmd.SilenceUsage = true
			cmd.SetContext(ctx)
			cmd.SetOut(&out)
			cmd.SetErr(&strings.Builder{})
			if err := cmd.Execute(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation", err)
			}
			if out.Len() != 0 {
				t.Fatalf("canceled command wrote output: %q", out.String())
			}
		})
	}
}

func TestMeshPeersReadsDaemonInsteadOfScanning(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	writeMeshTestConfig(t, tempHome, `nodes:
  - name: local
    hostname: localhost
    ssh_user: axis
discovery:
  enabled: true
  udp_port: 42424
`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/mesh" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("view") != "all" {
			t.Fatalf("view = %q, want all", r.URL.Query().Get("view"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[{"name":"alpha","hostname":"10.0.0.1","state":"suspect","source":"gossip","last_seen":"2026-05-22T22:00:00Z"}],"count":1,"view":"all"}`))
	}))
	t.Cleanup(server.Close)

	cmd := meshCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--cache-addr", server.URL, "peers"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mesh peers: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "suspect") {
		t.Fatalf("expected daemon peer table, got %q", got)
	}
	if strings.Contains(got, "Listening for") {
		t.Fatalf("default peers must not open a live scan: %q", got)
	}
}

func TestMeshPeersLiveReportsListenerFailure(t *testing.T) {
	holder, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	port := holder.LocalAddr().(*net.UDPAddr).Port

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	writeMeshTestConfig(t, tempHome, `nodes:
  - name: local
    hostname: localhost
    ssh_user: axis
discovery:
  enabled: true
  udp_port: `+strconv.Itoa(port)+`
`)

	cmd := meshCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"peers", "--live"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mesh peers --live: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "listener failed") {
		t.Fatalf("expected listener failure, got %q", got)
	}
	if strings.Contains(got, "No active Gossip neighbors discovered.") || strings.Contains(got, "running, 0 peers") {
		t.Fatalf("listener failure must not look like an empty peer list: %q", got)
	}
}

func TestMeshPeersJSONEnvelope(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	writeMeshTestConfig(t, tempHome, `nodes:
  - name: local
    hostname: localhost
    ssh_user: axis
`)

	cmd := meshCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"--format", "json", "peers"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mesh peers json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out.String()), &payload); err != nil {
		t.Fatalf("json: %v (%q)", err, out.String())
	}
	if payload["source"] != "daemon" {
		t.Fatalf("source = %v", payload["source"])
	}
	if payload["status"] != "daemon unavailable" {
		t.Fatalf("status = %v", payload["status"])
	}
}

func TestWatchBeaconChangesReturnsListenError(t *testing.T) {
	holder, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	port := holder.LocalAddr().(*net.UDPAddr).Port

	cfg := &config.Config{Discovery: &config.DiscoveryConfig{Enabled: true, UDPPort: port}}
	err = discovery.WatchBeaconChanges(context.Background(), cfg, discovery.NewBeaconRegistry(), nil)
	if err == nil {
		t.Fatal("expected listen error when port is already bound")
	}
}
