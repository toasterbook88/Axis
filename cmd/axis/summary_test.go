package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestSummaryRenderEmptyState(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	view := populateSummaryView(&models.ClusterSnapshot{}, daemon.Metadata{})
	got := normalizeGoldenOutput(view.Render())
	assertNormalizedGoldenText(t, "testdata/summary_empty_state.golden", got)
}

func TestSummaryRenderWithNodes(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	snap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{
			TotalRAMMB:         48 * 1024,
			TotalFreeRAMMB:     20 * 1024,
			TotalReservedMB:    4 * 1024,
			TotalAllocatableMB: 16 * 1024,
		},
		Nodes: []models.NodeFacts{
			{
				Name:   "alpha",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMTotalMB: 32 * 1024,
					GPUs: []models.GPUInfo{
						{Model: "NVIDIA A100"},
					},
				},
			},
			{
				Name:   "beta",
				Status: models.StatusPartial,
				Resources: &models.Resources{
					RAMTotalMB: 16 * 1024,
				},
			},
			{
				Name:   "gamma",
				Status: models.StatusUnreachable,
				Resources: &models.Resources{
					RAMTotalMB: 0,
				},
			},
		},
		Warnings: []models.Warning{
			{Kind: "cpu", Node: "beta", Message: "high CPU load"},
		},
	}

	meta := daemon.Metadata{Version: "v1.2.3", CacheAgeSec: 15}
	view := populateSummaryView(snap, meta)
	got := normalizeGoldenOutput(view.Render())
	assertNormalizedGoldenText(t, "testdata/summary_with_nodes.golden", got)
}

func TestSummaryRenderCorruptStateWarning(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	snap := &models.ClusterSnapshot{
		Warnings: []models.Warning{
			{
				Kind:    "state",
				Message: "recovered local AXIS state: quarantined corrupt file ~/.axis/state.json to ~/.axis/state.json.corrupt-20240115T120000Z: unexpected end of JSON input",
			},
		},
	}

	view := populateSummaryView(snap, daemon.Metadata{})
	got := normalizeGoldenOutput(view.Render())
	assertNormalizedGoldenText(t, "testdata/summary_corrupt_state.golden", got)
}

func TestSummaryCommandDaemonCache(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	meta := daemon.Metadata{Version: "v1.0.0", CacheAgeSec: 12, Ready: true}
	snap := models.ClusterSnapshot{
		Summary: models.ClusterSummary{
			TotalRAMMB:         16 * 1024,
			TotalFreeRAMMB:     8 * 1024,
			TotalReservedMB:    0,
			TotalAllocatableMB: 8 * 1024,
		},
		Nodes: []models.NodeFacts{
			{
				Name:   "local",
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMTotalMB: 16 * 1024,
					GPUs: []models.GPUInfo{
						{Model: "RTX 4090"},
					},
				},
			},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/meta", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(meta)
	})
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	t.Setenv(auth.TokenEnvVar, "test-token")

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := summaryCmd()
		cmd.SetArgs([]string{"--cached", "--cache-addr", server.URL})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("summary daemon cache: %v", err)
	}

	got := normalizeGoldenOutput(renderGoldenSections(stderr, stdout))
	assertNormalizedGoldenText(t, "testdata/summary_daemon_cache.golden", got)
}

func TestSummaryCommandLiveSnapshot(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	snap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{
			TotalRAMMB:         8 * 1024,
			TotalFreeRAMMB:     4 * 1024,
			TotalReservedMB:    0,
			TotalAllocatableMB: 4 * 1024,
		},
		Nodes: []models.NodeFacts{
			{
				Name:      "local",
				Status:    models.StatusComplete,
				Resources: &models.Resources{RAMTotalMB: 8 * 1024},
			},
		},
	}

	restore := stubStatusRuntimeLoader(t, func(ctx context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{Snapshot: snap}, nil
	})
	defer restore()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := summaryCmd()
		cmd.SetArgs([]string{"--cached=false"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("summary live snapshot: %v", err)
	}

	got := normalizeGoldenOutput(renderGoldenSections(stderr, stdout))
	assertNormalizedGoldenText(t, "testdata/summary_live_snapshot.golden", got)
}

func TestSummaryRenderBarEdgeCases(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	tests := []struct {
		name      string
		pct       float64
		width     int
		wantEmpty bool
	}{
		{"zero pct", 0, 30, false},
		{"full pct", 100, 30, false},
		{"tiny pct", 0.1, 30, false},
		{"overflow pct", 120, 30, false},
		{"zero width", 50, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bar := renderBar(tt.pct, tt.width)
			if bar == "" && !tt.wantEmpty {
				t.Fatalf("expected non-empty bar, got empty")
			}
			if tt.width > 0 && len(bar) == 0 {
				t.Fatalf("expected bar to have length > 0")
			}
		})
	}
}

func TestSummaryStaleCacheIndicator(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	snap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 1, ReachableNodes: 1},
		Nodes: []models.NodeFacts{
			{Name: "n1", Status: models.StatusComplete, Resources: &models.Resources{RAMTotalMB: 16 * 1024}},
		},
	}

	meta := daemon.Metadata{Version: "v2.0.0", CacheAgeSec: 120}
	view := populateSummaryView(snap, meta)
	out := view.Render()

	if !strings.Contains(out, "Cache Age: 2m0s") {
		t.Fatalf("expected stale cache age indicator, got:\n%s", out)
	}
}

func TestSummaryRenderTopology(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{Name: "M3 Pro"},
			{Name: "M1 Scout"},
			{Name: "NixOS"},
			{Name: "Foundry"},
			{Name: "Latitude"},
		},
	}

	view := populateSummaryView(snap, daemon.Metadata{})
	out := view.Render()

	expectedLines := []string{
		"⚡ CLUSTER TOPOLOGY",
		"==================",
		"M3 Pro     <======== (Thunderbolt: 10 Gbps) ========> M1 Scout",
		"NixOS      <........ (Gigabit LAN: 1 Gbps)  ........> Foundry",
		"Latitude   <-------- (Tailscale VPN)        --------> NixOS",
	}

	for _, expected := range expectedLines {
		if !strings.Contains(out, expected) {
			t.Errorf("expected output to contain %q, but got:\n%s", expected, out)
		}
	}
}
