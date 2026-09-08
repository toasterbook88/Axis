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

func TestSummaryHelpNamesDashboardAndCacheDefault(t *testing.T) {
	stdout, _, err := captureProcessOutput(t, func() error {
		cmd := summaryCmd()
		cmd.SetArgs([]string{"--help"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("summary --help: %v", err)
	}
	for _, want := range []string{"dashboard", "--cached=false", "live"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("summary help missing %q\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "summary <command> --help") {
		t.Fatalf("leaf summary help invented subcommands:\n%s", stdout)
	}
}

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

	meta := daemon.Metadata{Version: "v1.0.0", CacheAgeSec: 12, Ready: true, PublicationID: "pub-1"}
	snap := models.ClusterSnapshot{
		Publication: &models.PublicationEnvelope{ID: "pub-1"},
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

func TestSummaryRenderReachability(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	snap := mixedReachabilitySnapshot()
	view := populateSummaryView(snap, daemon.Metadata{CacheAgeSec: 90})
	out := view.Render()

	t.Run("T1_same_cidr_no_connectors", func(t *testing.T) {
		assertNoPairwiseConnectors(t, out)
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "node-a") && strings.Contains(line, "node-b") {
				t.Fatalf("same-CIDR nodes rendered on one connector line: %q", line)
			}
		}
	})
	t.Run("T2_darwin_null_subnet_present", func(t *testing.T) {
		if !strings.Contains(out, "node-c") {
			t.Fatalf("darwin node omitted:\n%s", out)
		}
		if !strings.Contains(out, "direct-lan") {
			t.Fatalf("darwin node missing route class:\n%s", out)
		}
		if !strings.Contains(out, "handshake 38ms") {
			t.Fatalf("darwin node missing handshake duration:\n%s", out)
		}
		if !strings.Contains(out, "thunderbolt iface present") {
			t.Fatalf("darwin node missing thunderbolt node attribute:\n%s", out)
		}
	})
	t.Run("T3_cached_vantage_is_snapshot_host", func(t *testing.T) {
		if !strings.Contains(out, "observed from node-a") {
			t.Fatalf("vantage is not the snapshot host:\n%s", out)
		}
		if strings.Contains(out, "observed from node-b") || strings.Contains(out, "observed from cli-host") {
			t.Fatalf("vantage was inferred at render time:\n%s", out)
		}
		if got := reachabilityClass(out, "node-a"); got != "local" {
			t.Fatalf("vantage node class = %q, want local:\n%s", got, out)
		}
		if got := reachabilityClass(out, "cli-host"); got != "relayed" {
			t.Fatalf("non-vantage CLI-named node class = %q, want relayed:\n%s", got, out)
		}
	})
	t.Run("T4_unknown_route_present", func(t *testing.T) {
		if got := reachabilityClass(out, "node-d"); got != "unknown" {
			t.Fatalf("node with no route observation class = %q, want unknown:\n%s", got, out)
		}
		if !strings.Contains(out, "no route observed") {
			t.Fatalf("unknown state not explicit:\n%s", out)
		}
	})
	t.Run("T5_no_pairwise_glyphs", func(t *testing.T) {
		assertNoPairwiseConnectors(t, out)
	})
	t.Run("T6_vantage_label_present", func(t *testing.T) {
		if !strings.Contains(out, "REACHABILITY (observed from node-a, 1m30s ago)") {
			t.Fatalf("vantage label missing:\n%s", out)
		}
	})
	t.Run("T7_observation_age", func(t *testing.T) {
		if !strings.Contains(out, "1m30s ago") {
			t.Fatalf("observation age missing:\n%s", out)
		}
	})
	t.Run("R8_handshake_not_latency", func(t *testing.T) {
		lower := strings.ToLower(out)
		for _, banned := range []string{"latency", "rtt", "ping"} {
			if strings.Contains(lower, banned) {
				t.Errorf("route rendering labeled handshake as %q:\n%s", banned, out)
			}
		}
	})
}

func TestSummaryRenderReachabilityGolden(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	view := populateSummaryView(mixedReachabilitySnapshot(), daemon.Metadata{CacheAgeSec: 90})
	got := normalizeGoldenOutput(view.Render())
	assertNormalizedGoldenText(t, "testdata/summary_reachability.golden", got)
	assertNoPairwiseConnectors(t, got)
}

func TestSummaryRenderReachabilityUnknownVantage(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "node-a",
				Addresses: []models.NetworkAddress{
					{Kind: "ipv4", Address: "192.0.2.10", Subnet: "192.0.2.0/24", SpeedClass: "gigabit"},
				},
			},
			{
				Name:         "node-b",
				NetworkClass: models.NetworkClassRelayed,
				Addresses: []models.NetworkAddress{
					{Kind: "ipv4", Address: "192.0.2.20", Subnet: "192.0.2.0/24", SpeedClass: "gigabit"},
				},
			},
		},
	}
	out := populateSummaryView(snap, daemon.Metadata{}).Render()
	if !strings.Contains(out, "REACHABILITY (vantage unknown)") {
		t.Fatalf("cached snapshot without vantage must name the absence:\n%s", out)
	}
	assertNoPairwiseConnectors(t, out)
	if got := reachabilityClass(out, "node-a"); got != "unknown" {
		t.Fatalf("node-a class = %q, want unknown:\n%s", got, out)
	}
	if got := reachabilityClass(out, "node-b"); got != "relayed" {
		t.Fatalf("node-b class = %q, want relayed:\n%s", got, out)
	}
}

func mixedReachabilitySnapshot() *models.ClusterSnapshot {
	return &models.ClusterSnapshot{
		Vantage: &models.VantageInfo{NodeName: "node-a"},
		Nodes: []models.NodeFacts{
			{
				Name:         "node-a",
				Status:       models.StatusComplete,
				NetworkClass: models.NetworkClassDirectLAN,
				Addresses: []models.NetworkAddress{
					{Kind: "ipv4", Address: "192.0.2.10", Subnet: "192.0.2.0/24", SpeedClass: "gigabit"},
				},
			},
			{
				Name:                  "node-b",
				Status:                models.StatusComplete,
				NetworkClass:          models.NetworkClassRelayed,
				SSHHandshakeLatencyMs: 212,
				Addresses: []models.NetworkAddress{
					{Kind: "ipv4", Address: "192.0.2.20", Subnet: "192.0.2.0/24", SpeedClass: "gigabit"},
				},
			},
			{
				Name:                  "node-c",
				Status:                models.StatusComplete,
				NetworkClass:          models.NetworkClassDirectLAN,
				SSHHandshakeLatencyMs: 38,
				Addresses: []models.NetworkAddress{
					{Kind: "ipv4", Address: "192.0.2.30", SpeedClass: "thunderbolt"},
				},
			},
			{
				Name:   "node-d",
				Status: models.StatusUnreachable,
			},
			{
				Name:                  "cli-host",
				Status:                models.StatusComplete,
				NetworkClass:          models.NetworkClassRelayed,
				SSHHandshakeLatencyMs: 200,
			},
		},
	}
}

func reachabilityClass(out, node string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == node {
			return fields[1]
		}
	}
	return ""
}

func assertNoPairwiseConnectors(t *testing.T, out string) {
	t.Helper()
	for _, glyph := range []string{
		"CLUSTER TOPOLOGY",
		"<========",
		"<........",
		"<~~~~~~~~",
		"<--------",
		"========>",
		"........>",
		"~~~~~~~~>",
		"-------->",
	} {
		if strings.Contains(out, glyph) {
			t.Errorf("pairwise connector %q still rendered:\n%s", glyph, out)
		}
	}
}
