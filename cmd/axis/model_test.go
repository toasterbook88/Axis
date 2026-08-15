package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

type fakeModelRunner struct {
	started [][]string
	stopped []int
	probed  []int
}

func (f *fakeModelRunner) Start(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, argv []string) error {
	f.started = append(f.started, append([]string(nil), argv...))
	return nil
}
func (f *fakeModelRunner) Stop(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, port int) error {
	f.stopped = append(f.stopped, port)
	return nil
}
func (f *fakeModelRunner) Probe(_ context.Context, _ models.NodeFacts, _ *config.NodeConfig, port int) error {
	f.probed = append(f.probed, port)
	return nil
}

func stubModelSnapshot(t *testing.T, snap *models.ClusterSnapshot) {
	t.Helper()
	prev := loadModelSnapshot
	loadModelSnapshot = func(context.Context) (*models.ClusterSnapshot, error) { return snap, nil }
	t.Cleanup(func() { loadModelSnapshot = prev })
}

func stubModelConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	prev := loadModelConfig
	loadModelConfig = func() (*config.Config, error) { return cfg, nil }
	t.Cleanup(func() { loadModelConfig = prev })
}

func testSnap() *models.ClusterSnapshot {
	return &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{{
			Name: "storage",
			Tools: []models.ToolInfo{
				{Name: "llama-server", Path: "/usr/local/bin/llama-server"},
			},
			Resources: &models.Resources{
				Volumes: []models.Volume{
					{Mount: "/mnt/models", Kind: "local"},
				},
			},
		}},
	}
}

func TestModelCommandWiresStartStop(t *testing.T) {
	cmd := modelCmd()
	for _, name := range []string{"start", "stop"} {
		if _, _, err := cmd.Find([]string{name}); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestRunModelStartUsesPlanAndRunner(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	r := &fakeModelRunner{}
	cmd := modelStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runModelStart(context.Background(), cmd, "storage", "/mnt/models/a.gguf", 8081, r); err != nil {
		t.Fatal(err)
	}
	if len(r.started) != 1 || len(r.probed) != 1 || r.probed[0] != 8081 {
		t.Fatalf("runner=%+v", r)
	}
	argv := strings.Join(r.started[0], " ")
	if !strings.Contains(argv, "--port 8081") || !strings.Contains(argv, "/mnt/models/a.gguf") {
		t.Fatalf("argv=%v", r.started[0])
	}
	if !strings.Contains(buf.String(), "started") {
		t.Fatalf("output=%q", buf.String())
	}
}

func TestRunModelStartRefusesNetworkPath(t *testing.T) {
	snap := testSnap()
	snap.Nodes[0].Resources.Volumes = []models.Volume{{Mount: "/mnt/nas", Kind: "network"}}
	stubModelSnapshot(t, snap)
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	err := runModelStart(context.Background(), modelStartCmd(), "storage", "/mnt/nas/a.gguf", 8081, &fakeModelRunner{})
	if err == nil || !strings.Contains(err.Error(), "named local volume") {
		t.Fatalf("got %v", err)
	}
}

func TestRunModelStopRequiresPort(t *testing.T) {
	stubModelSnapshot(t, testSnap())
	stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
	err := runModelStop(context.Background(), modelStopCmd(), "storage", 0, &fakeModelRunner{})
	if err == nil {
		t.Fatal("expected port error")
	}
}
