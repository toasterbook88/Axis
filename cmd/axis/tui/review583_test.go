package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
)

// Review #583 (1): a snapshot file without a collection timestamp must not
// report its mtime as the observed time; the age badge says "age unknown".
func TestFileSnapshotWithoutTimestampHasUnknownAge(t *testing.T) {
	home := t.TempDir()
	t.Setenv(persist.AxisHomeEnv, home)
	if err := os.WriteFile(filepath.Join(home, "snapshot.json"), []byte(`{"nodes":[{"name":"node-a","status":"complete"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	msg, err := loadFileSnapshotMsg()
	if err != nil {
		t.Fatal(err)
	}
	if !msg.Observed.IsZero() {
		t.Fatalf("observed = %v, want zero (collection time unknown)", msg.Observed)
	}
}

// Review #583 (2): the placement wizard follows the dashboard's ASCII mode.
func TestPlacementModalASCII(t *testing.T) {
	m := loadedModel(t, 100, 30)
	m.glyphs = asciiGlyphs
	m = press(t, m, "p")
	m.modal.explanation = sampleExplanation(5, 8)
	for _, r := range m.View() {
		if r > 0x7e && r != 0x1b {
			t.Fatalf("ASCII placement view emitted %q", r)
		}
	}
}

// Review #583 (3): a scored result with many candidates and reasons keeps
// the advisory notice and action keys on screen at 80x24.
func TestPlacementResultFitsAndKeepsAdvisoryFooter(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {80, 16}, {100, 30}} {
		m := press(t, loadedModel(t, sz[0], sz[1]), "p")
		m.modal.explanation = sampleExplanation(6, 10)
		m.modal.requirements = models.TaskRequirements{Workload: models.WorkloadProfileMatch{Class: models.ClassRepoAnalysis}, MinFreeRAMMB: 4096}
		out := stripANSI(m.View())
		lines := strings.Split(out, "\n")
		if len(lines) != sz[1] {
			t.Fatalf("%dx%d: frame has %d lines", sz[0], sz[1], len(lines))
		}
		for _, want := range []string{"no task is executed", "Escape cancel", "Recommendation"} {
			if !strings.Contains(out, want) {
				t.Errorf("%dx%d: %q cut off:\n%s", sz[0], sz[1], want, out)
			}
		}
		for i, l := range lines {
			if lipgloss.Width(l) > sz[0] {
				t.Errorf("%dx%d: line %d overflows", sz[0], sz[1], i)
			}
		}
	}
}

// Review #583 (4): on a narrow terminal a refresh failure outranks the key
// hints.
func TestNarrowFooterShowsRefreshFailure(t *testing.T) {
	m := loadedModel(t, 60, 20)
	next, _ := UpdateWithRefresh(m, loadErrMsg{Err: fmt.Errorf("daemon down")})
	out := stripANSI(renderFooter(next.(Model), 60))
	if !strings.Contains(out, "Refresh failed") {
		t.Fatalf("narrow footer hides the refresh failure: %q", out)
	}
}

// Review #583 (5): cluster-scoped warnings (empty Node) are readable, not
// just counted.
func TestClusterWarningsAppearInOverview(t *testing.T) {
	m := loadedModel(t, 120, 40)
	m.snapshot.Warnings = append(m.snapshot.Warnings, models.Warning{Kind: "cache_stale", Message: "daemon cache is 20m old; run axis daemon status"})
	out := stripANSI(m.View())
	if !strings.Contains(out, "daemon cache is 20m old") {
		t.Fatalf("cluster warning not shown:\n%s", out)
	}
}

// Review #583 (6): resident rows show their observed state; only serving
// rows count as warm.
func TestModelsTabShowsObservedState(t *testing.T) {
	node := models.NodeFacts{ResidentModels: []models.ResidentModel{
		{Name: "m-loaded", Runtime: "ollama", State: models.ModelCatalogLoaded},
		{Name: "m-loading", Runtime: "llama.cpp", State: models.ModelCatalogLoading},
		{Name: "m-down", Runtime: "llama.cpp", State: models.ModelCatalogDown},
		{Name: "m-listed", Runtime: "ollama", State: models.ModelCatalogListed},
		{Name: "m-legacy", Runtime: "ollama"},
	}}
	out := joinPlain(modelsTab(node, unicodeGlyphs))
	if strings.Contains(out, "Loaded in memory") {
		t.Fatalf("heading claims every row is loaded:\n%s", out)
	}
	for _, want := range []string{"m-loaded", "loaded", "m-loading", "loading", "m-down", "down", "m-listed", "listed", "m-legacy", "serving"} {
		if !strings.Contains(out, want) {
			t.Errorf("models tab missing %q:\n%s", want, out)
		}
	}
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "n", ResidentModels: node.ResidentModels}}}
	if got := candidateSummary(models.PlacementCandidateExplanation{Node: "n"}, snap); !strings.Contains(got, "2 warm model") {
		t.Fatalf("warm count = %q, want only serving rows (2)", got)
	}
}

// Review #583 (7): allocatable comes from the reservation overlay's
// node-level value, and an authoritative zero is shown as zero.
func TestAllocatableUsesReservationOverlay(t *testing.T) {
	m := loadedModel(t, 120, 40)
	m.snapshot.Nodes[0].RAMAllocatableMB = 0 // reservations exhausted the node
	m.snapshot.Nodes[0].Resources.RAMAllocatableMB = 26000
	over := joinPlain(overviewTab(m, m.snapshot.Nodes[0], 120))
	res := joinPlain(reservationsTab(m, m.snapshot.Nodes[0]))
	for name, out := range map[string]string{"overview": over, "reservations": res} {
		if strings.Contains(out, "25 GB") {
			t.Errorf("%s shows pre-reservation allocatable:\n%s", name, out)
		}
		if !strings.Contains(out, "0.0 GB") {
			t.Errorf("%s hides the authoritative zero:\n%s", name, out)
		}
	}
}

func sampleExplanation(candidates, reasons int) *models.PlacementExplanation {
	e := &models.PlacementExplanation{Decision: models.PlacementDecision{Node: "node-a", OK: true, FitScore: 80}}
	for i := 0; i < reasons; i++ {
		e.Decision.Reasoning = append(e.Decision.Reasoning, fmt.Sprintf("reason %d: a fairly long explanation of why the ranking came out this way", i))
	}
	for i := 0; i < candidates; i++ {
		e.Eligible = append(e.Eligible, models.PlacementCandidateExplanation{Node: fmt.Sprintf("node-%c", 'a'+i), HeadroomMB: int64(4096 * (i + 1))})
	}
	return e
}
