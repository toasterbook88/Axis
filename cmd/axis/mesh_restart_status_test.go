package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
)

func TestDaemonStatusWarnsWhenMeshRestartRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"daemon-cache","ready":true,"version":"0.14.10","refresh_interval_sec":60,"mesh_restart_required":true,"mesh_restart_reason":"mesh transport, enablement or local identity changed: restart required"}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--cache-addr", server.URL, "status"})
	// Restart-required is advisory: the status stays fresh and exit 0.
	if err := cmd.Execute(); err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	var envelope struct {
		OK       bool             `json:"ok"`
		Status   string           `json:"status"`
		Data     daemon.Metadata  `json:"data"`
		Warnings []machineWarning `json:"warnings"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("daemon status is not one JSON envelope: %v\n%s", err, out.String())
	}
	if !envelope.OK || envelope.Status != "fresh" || !envelope.Data.MeshRestartRequired {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
	if len(envelope.Warnings) != 1 || envelope.Warnings[0].Kind != "mesh_restart_required" ||
		!strings.Contains(envelope.Warnings[0].Message, "axis daemon restart") ||
		!strings.Contains(envelope.Warnings[0].Message, "local identity changed") {
		t.Fatalf("warnings = %+v, want one mesh_restart_required warning", envelope.Warnings)
	}
}

func TestMeshRestartDoctorCheck(t *testing.T) {
	if got := meshRestartDoctorCheck(daemon.Metadata{}, nil); got != nil {
		t.Fatalf("no restart pending: got %+v, want nil", got)
	}
	if got := meshRestartDoctorCheck(daemon.Metadata{MeshRestartRequired: true}, errors.New("unreachable")); got != nil {
		t.Fatalf("fetch error: got %+v, want nil", got)
	}
	got := meshRestartDoctorCheck(daemon.Metadata{MeshRestartRequired: true, MeshRestartReason: "mesh transport, enablement or local identity changed: restart required"}, nil)
	if got == nil || got.Name != "Mesh config" || got.Status != "warn" || got.Fix != "axis daemon restart" || !strings.Contains(got.Message, "transport") {
		t.Fatalf("restart pending: got %+v", got)
	}
}

func TestDoctorWarnsWhenMeshRestartRequired(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "nodes.yaml")
	defer stubDoctorConfigPath(t, func() string { return tmpFile })()
	defer stubDoctorOwnershipHealthy(t)()
	defer stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return &config.Config{Nodes: []config.NodeConfig{{Name: "alpha", Hostname: "10.0.0.1", SSHUser: "axis"}}}, nil
	})()
	defer stubDoctorSSHChecker(t, func(context.Context, config.NodeConfig) error { return nil })()
	defer stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "alpha"}}}, "cached", nil
	})()
	prevMeta := doctorFetchDaemonMeta
	doctorFetchDaemonMeta = func(context.Context, string) (daemon.Metadata, error) {
		return daemon.Metadata{MeshRestartRequired: true, MeshRestartReason: "mesh transport, enablement or local identity changed: restart required"}, nil
	}
	defer func() { doctorFetchDaemonMeta = prevMeta }()

	stdout, _, _ := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	stdout = stripANSI(stdout)
	if !strings.Contains(stdout, "Mesh config") || !strings.Contains(stdout, "transport") || !strings.Contains(stdout, "axis daemon restart") {
		t.Fatalf("doctor output lacks the mesh restart warning:\n%s", stdout)
	}
}

// axis daemon restart is the fix the mesh warning prints, so a daemon that is
// otherwise serving must be restarted, not reported "already fresh".
func TestDaemonRestartRestartsWhenMeshRestartRequired(t *testing.T) {
	pending := serving()
	pending.MeshRestartRequired, pending.MeshRestartReason = true, "mesh transport, enablement or local identity changed: restart required"
	h := &restartHarness{metas: []daemon.Metadata{pending, serving()}, pid: 111}
	h.install(t)

	var out bytes.Buffer
	if err := restartDaemon(context.Background(), "127.0.0.1:1", &out); err != nil {
		t.Fatalf("restart: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "already fresh") || h.terminates != 1 || h.spawns != 1 {
		t.Fatalf("expected terminate+spawn, got out=%q terminates=%d spawns=%d", out.String(), h.terminates, h.spawns)
	}
}

// The metadata check must not depend on the snapshot fetch succeeding.
func TestDoctorWarnsWhenMeshRestartRequiredWithoutSnapshot(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "nodes.yaml")
	defer stubDoctorConfigPath(t, func() string { return tmpFile })()
	defer stubDoctorOwnershipHealthy(t)()
	defer stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return &config.Config{Nodes: []config.NodeConfig{{Name: "alpha", Hostname: "10.0.0.1", SSHUser: "axis"}}}, nil
	})()
	defer stubDoctorSSHChecker(t, func(context.Context, config.NodeConfig) error { return nil })()
	defer stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return nil, "", errors.New("snapshot not ready")
	})()
	prevMeta := doctorFetchDaemonMeta
	doctorFetchDaemonMeta = func(context.Context, string) (daemon.Metadata, error) {
		return daemon.Metadata{MeshRestartRequired: true, MeshRestartReason: "mesh transport, enablement or local identity changed: restart required"}, nil
	}
	defer func() { doctorFetchDaemonMeta = prevMeta }()

	stdout, _, _ := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	stdout = stripANSI(stdout)
	if !strings.Contains(stdout, "Mesh config") || !strings.Contains(stdout, "axis daemon restart") {
		t.Fatalf("doctor output lacks the mesh restart warning without a snapshot:\n%s", stdout)
	}
}
