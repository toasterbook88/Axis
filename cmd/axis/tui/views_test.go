package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/models"
)

// stripANSI removes ANSI escape codes so tests can assert on visible text.
func stripANSI(s string) string {
	var result strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') || s[i] == '@' {
				inEscape = false
			}
			continue
		}
		result.WriteByte(s[i])
	}
	return result.String()
}

func joinPlain(lines []string) string { return stripANSI(strings.Join(lines, "\n")) }

func TestTruncAndPadPlain(t *testing.T) {
	if got := truncPlain("node-with-long-name", 8, "…"); got != "node-wi…" {
		t.Fatalf("truncPlain = %q, want %q", got, "node-wi…")
	}
	if got := truncPlain("short", 10, "…"); got != "short" {
		t.Fatalf("truncPlain(short) = %q", got)
	}
	if got := truncPlain("abcdef", 1, "…"); got != "a" {
		t.Fatalf("truncPlain(width 1) = %q, want bare cut", got)
	}
	if got := truncPlain("abc", 0, "…"); got != "" {
		t.Fatalf("truncPlain(width 0) = %q", got)
	}
	if got := padPlain("ab", 6, "…"); got != "ab    " {
		t.Fatalf("padPlain = %q", got)
	}
}

func TestFitLineClipsStyledTextAndPads(t *testing.T) {
	styled := styleBad.Render("abcdefghij")
	if got := lipgloss.Width(fitLine(styled, 4)); got != 4 {
		t.Fatalf("fitLine clip width = %d, want 4", got)
	}
	if got := lipgloss.Width(fitLine("ab", 6)); got != 6 {
		t.Fatalf("fitLine pad width = %d, want 6", got)
	}
	if got := fitLine("ab", 0); got != "" {
		t.Fatalf("fitLine(width 0) = %q", got)
	}
}

func TestBarAndGaugeWidths(t *testing.T) {
	plain := lipgloss.NewStyle()
	for _, frac := range []float64{-1, 0, 0.01, 0.5, 1, 2.5, 50} {
		if got := lipgloss.Width(bar(frac, 10, unicodeGlyphs, plain)); got != 10 {
			t.Errorf("bar(%v) width = %d, want 10", frac, got)
		}
		if got := lipgloss.Width(gauge(frac, gaugeW, unicodeGlyphs, plain)); got != gaugeW {
			t.Errorf("gauge(%v) width = %d, want %d", frac, got, gaugeW)
		}
	}
	if bar(0.5, 0, unicodeGlyphs, plain) != "" {
		t.Fatal("zero-width bar must render nothing")
	}
	// A tiny non-zero value still shows one filled cell.
	if !strings.Contains(stripANSI(bar(0.01, 10, asciiGlyphs, plain)), "=") {
		t.Fatal("non-zero bar rendered empty")
	}
	if got := stripANSI(gauge(1.5, gaugeW, asciiGlyphs, plain)); !strings.HasSuffix(got, "150%") {
		t.Fatalf("oversubscribed gauge must show the real number: %q", got)
	}
}

func TestTableColumnsDropInPriorityOrder(t *testing.T) {
	nodes := []models.NodeFacts{{Name: "node-a"}}
	titles := func(cols []column) string {
		var s []string
		for _, c := range cols {
			s = append(s, c.title)
		}
		return strings.Join(s, ",")
	}
	if got := titles(tableColumns(nodes, 200)); got != "NODE,ROLE,STATE,LOAD,RAM,GPU,LINK" {
		t.Fatalf("wide columns = %s", got)
	}
	if got := titles(tableColumns(nodes, 70)); strings.Contains(got, "GPU") || strings.Contains(got, "LINK") {
		t.Fatalf("70-col columns kept GPU/LINK: %s", got)
	}
	if got := titles(tableColumns(nodes, 10)); got != "NODE" {
		t.Fatalf("tiny columns = %s, want NODE only", got)
	}
}

func TestRenderTableEmptySnapshot(t *testing.T) {
	m := NewModel()
	m.loading = false
	m.snapshot = &models.ClusterSnapshot{}
	if out := joinPlain(renderTable(m, 80, 3)); !strings.Contains(out, "No nodes in this snapshot.") {
		t.Fatalf("empty table = %q", out)
	}
}

func TestOverviewTab(t *testing.T) {
	battery := 77
	m := NewModel()
	node := models.NodeFacts{
		Name: "node-a",
		Resources: &models.Resources{
			CPUCores: 8, CPUModel: "Test CPU", RAMTotalMB: 16384, RAMFreeMB: 8192,
			BatteryPercent: &battery, PowerSource: "ac", Pressure: "high", ThermalState: "critical",
		},
	}
	out := joinPlain(overviewTab(m, node, 100))
	for _, want := range []string{"Test CPU · 8 cores", "50%  8.0 GB / 16 GB used", "high", "critical", "battery 77% · ac"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q in:\n%s", want, out)
		}
	}
}

func TestOverviewTabCPUWithoutModelName(t *testing.T) {
	node := models.NodeFacts{Name: "n", Resources: &models.Resources{CPUCores: 4, Load1M: 1}}
	out := joinPlain(overviewTab(NewModel(), node, 100))
	if !strings.Contains(out, "CPU         4 cores · load 1.00 (25%)") {
		t.Fatalf("CPU line without model name:\n%s", out)
	}
	bare := joinPlain(overviewTab(NewModel(), models.NodeFacts{Name: "n", Resources: &models.Resources{}}, 100))
	if !strings.Contains(bare, "cores not observed") {
		t.Fatalf("CPU line without cores:\n%s", bare)
	}
}

func TestOverviewTabNilResourcesSaysNotObserved(t *testing.T) {
	out := joinPlain(overviewTab(NewModel(), models.NodeFacts{Name: "bare"}, 100))
	for _, want := range []string{"System      not observed", "Hardware    not observed", "Link        not observed"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q in:\n%s", want, out)
		}
	}
}

func TestModelsTab(t *testing.T) {
	node := models.NodeFacts{
		Ollama: &models.OllamaInfo{Installed: true, Running: true, Version: "0.1.0", Port: 11434, Models: []string{"model-a"}},
		ResidentModels: []models.ResidentModel{
			{Name: "model-b", Runtime: "ollama", Port: 11434, SizeVRAMMB: 4096},
		},
	}
	out := joinPlain(modelsTab(node, unicodeGlyphs))
	for _, want := range []string{"running 0.1.0", "port 11434", "model-a", "Runtime models", "model-b", "4.0 GB VRAM"} {
		if !strings.Contains(out, want) {
			t.Errorf("models tab missing %q in:\n%s", want, out)
		}
	}
	stopped := joinPlain(modelsTab(models.NodeFacts{Ollama: &models.OllamaInfo{Installed: true}}, unicodeGlyphs))
	if !strings.Contains(stopped, "installed, not running") {
		t.Fatalf("stopped ollama = %q", stopped)
	}
	if empty := joinPlain(modelsTab(models.NodeFacts{}, unicodeGlyphs)); !strings.Contains(empty, "No inference backends") {
		t.Fatalf("models tab empty-state = %q", empty)
	}
}

func TestStorageTab(t *testing.T) {
	node := models.NodeFacts{Resources: &models.Resources{Volumes: []models.Volume{
		{Mount: "/", Class: "nvme", TotalGB: 512, FreeGB: 128, Role: "root"},
		{Mount: "/mnt/x", TotalGB: 0},
	}}}
	out := joinPlain(storageTab(node, 100, unicodeGlyphs))
	for _, want := range []string{"75%", "128 GB free of 512 GB", "nvme", "root", "not observed", "class unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("storage tab missing %q in:\n%s", want, out)
		}
	}
	if empty := joinPlain(storageTab(models.NodeFacts{}, 100, unicodeGlyphs)); !strings.Contains(empty, "No storage volumes observed") {
		t.Fatalf("storage tab empty-state = %q", empty)
	}
}

func TestReservationsTabWithoutReservations(t *testing.T) {
	m := NewModel()
	m.snapshot = &models.ClusterSnapshot{}
	out := joinPlain(reservationsTab(m, models.NodeFacts{Name: "node-a"}))
	if !strings.Contains(out, "no RAM reservations recorded") || !strings.Contains(out, "axis reservations list") {
		t.Fatalf("reservations empty-state = %q", out)
	}
}

func TestInspectorOverflowSaysHowMuchIsHidden(t *testing.T) {
	m := NewModel()
	vols := make([]models.Volume, 20)
	for i := range vols {
		vols[i] = models.Volume{Mount: "/v", TotalGB: 10, FreeGB: 5}
	}
	m.snapshot = &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "node-a", Resources: &models.Resources{Volumes: vols}}}}
	m.activeTab = 2
	out := joinPlain(renderInspector(m, 80, 10))
	if !strings.Contains(out, "more line(s)") {
		t.Fatalf("overflowing inspector hides the overflow:\n%s", out)
	}
}

func TestInspectorTitleNarrowShowsActiveTabOnly(t *testing.T) {
	got := stripANSI(inspectorTitle(models.NodeFacts{Name: "node-a"}, 2, 30, unicodeGlyphs))
	if !strings.Contains(got, "3/4 Storage") {
		t.Fatalf("narrow title = %q", got)
	}
}
