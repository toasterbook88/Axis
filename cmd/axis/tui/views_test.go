package tui

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "node-a", "node-a"},
		{"color escape", "\x1b[38;5;42m●\x1b[0m", "●"},
		{"escape in middle", "a\x1b[31mred\x1b[0mb", "aredb"},
		{"cursor move", "\x1b[2Jscreen", "screen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripANSI(tc.in); got != tc.want {
				t.Fatalf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestVisualWidthIgnoresANSI(t *testing.T) {
	styled := "\x1b[38;5;42m●\x1b[0m" // status icon
	if got := visualWidth(styled); got != 1 {
		t.Fatalf("visualWidth(styled ●) = %d, want 1", got)
	}
	if got := visualWidth("node-a"); got != 6 {
		t.Fatalf("visualWidth(plain) = %d, want 6", got)
	}
}

func TestPadVisualWidth(t *testing.T) {
	got := padVisualWidth("ab", 6)
	if got != "ab    " {
		t.Fatalf("padVisualWidth = %q, want %q", got, "ab    ")
	}

	// Styled cells pad by visual width, not byte length.
	styled := "\x1b[31mab\x1b[0m" // 2 visible runes
	padded := padVisualWidth(styled, 6)
	if !strings.HasPrefix(padded, "\x1b[31mab\x1b[0m") {
		t.Fatalf("padVisualWidth(styled) = %q, want ANSI prefix preserved", padded)
	}
	if got := visualWidth(padded); got != 6 {
		t.Fatalf("visualWidth(padded styled) = %d, want 6", got)
	}
}

func TestTruncateVisual(t *testing.T) {
	got := truncateVisual("node-with-long-name", 8)
	if got != "node-wi…" {
		t.Fatalf("truncateVisual = %q, want %q", got, "node-wi…")
	}
	if got := truncateVisual("short", 10); got != "short" {
		t.Fatalf("truncateVisual(short) = %q, want unchanged", got)
	}
}

func TestRenderProgressBar(t *testing.T) {
	if got := renderProgressBar(0.5, 4); got != "[██░░]" {
		t.Fatalf("renderProgressBar(0.5, 4) = %q, want %q", got, "[██░░]")
	}
	if got := renderProgressBar(1.0, 3); got != "[███]" {
		t.Fatalf("renderProgressBar(1.0, 3) = %q, want %q", got, "[███]")
	}
	if got := renderProgressBar(0.0, 3); got != "[░░░]" {
		t.Fatalf("renderProgressBar(0.0, 3) = %q, want %q", got, "[░░░]")
	}
	// Out-of-range percentages clamp, never overflow the bar width.
	if got := renderProgressBar(2.0, 3); got != "[███]" {
		t.Fatalf("renderProgressBar(2.0, 3) = %q, want clamped %q", got, "[███]")
	}
	// Non-positive width falls back to a sane default.
	if got := visualWidth(renderProgressBar(1.0, 0)); got == 0 {
		t.Fatal("renderProgressBar(1.0, 0) rendered empty bar")
	}
}

func TestRenderRowPlainCellsAlign(t *testing.T) {
	row := renderRow([]string{"node-a", "primary", "ok"}, normalRowStyle)
	// First column is padded to 15 visual columns (styles add a 1-space
	// pad on each side, so node-a itself carries 9 trailing pad spaces).
	if !strings.Contains(row, "node-a         ") {
		t.Fatalf("renderRow first column not padded to width 15: %q", row)
	}
}

func TestRenderRowExtraCellsDoNotPanic(t *testing.T) {
	// More cells than configured columns must render, not panic.
	row := renderRow([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, normalRowStyle)
	if !strings.Contains(row, "h") || !strings.Contains(row, "i") {
		t.Fatalf("renderRow dropped extra cells: %q", row)
	}
}

func TestRenderFleetTable(t *testing.T) {
	m := NewModel()
	m.loading = false
	m.snapshot = &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{Name: "node-a", Role: "primary", Status: models.StatusComplete},
			{Name: "node-b", Role: "worker", Status: models.StatusUnreachable},
		},
	}

	out := renderFleetTable(m)
	if !strings.Contains(out, "node-a") || !strings.Contains(out, "node-b") {
		t.Fatalf("fleet table missing node names: %q", out)
	}
	if !strings.Contains(out, "NODE") {
		t.Fatalf("fleet table missing header row: %q", out)
	}

	m.snapshot = nil
	if got := renderFleetTable(m); got != "No nodes discovered." {
		t.Fatalf("renderFleetTable(nil snapshot) = %q, want %q", got, "No nodes discovered.")
	}
}

func TestRenderDetailsTab(t *testing.T) {
	battery := 77
	node := models.NodeFacts{
		Name: "node-a",
		Resources: &models.Resources{
			CPUCores:       8,
			CPUModel:       "Test CPU",
			RAMTotalMB:     16384,
			RAMFreeMB:      8192,
			BatteryPercent: &battery,
			PowerSource:    "AC",
			Pressure:       "warning",
			ThermalState:   "critical",
		},
	}

	out := renderDetailsTabEnhanced(node)
	for _, want := range []string{"Test CPU (8 cores)", "16 GB total, 8 GB free", "Pressure: warning", "Thermal: critical", "Battery: 77% (AC)"} {
		if !strings.Contains(out, want) {
			t.Errorf("details tab missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderDetailsTabNilResources(t *testing.T) {
	out := renderDetailsTabEnhanced(models.NodeFacts{Name: "bare"})
	if !strings.Contains(out, "CPU: Unknown") {
		t.Fatalf("details tab should degrade for nil resources: %q", out)
	}
}

func TestRenderBackendsTab(t *testing.T) {
	node := models.NodeFacts{
		Ollama: &models.OllamaInfo{
			Installed: true,
			Running:   true,
			Version:   "0.1.0",
			Port:      11434,
			Models:    []string{"llama3"},
		},
		ResidentModels: []models.ResidentModel{
			{Name: "qwen", Runtime: "ollama", Port: 11434, SizeVRAMMB: 4096},
		},
	}

	out := renderBackendsTabEnhanced(node)
	for _, want := range []string{"Ollama: 0.1.0", "Running", "llama3", "qwen on ollama:11434 (4096 MB VRAM)"} {
		if !strings.Contains(out, want) {
			t.Errorf("backends tab missing %q in:\n%s", want, out)
		}
	}

	empty := renderBackendsTabEnhanced(models.NodeFacts{})
	if !strings.Contains(empty, "No AI backends detected") {
		t.Fatalf("backends tab empty-state = %q", empty)
	}
}

func TestRenderStorageTab(t *testing.T) {
	node := models.NodeFacts{
		Resources: &models.Resources{
			Volumes: []models.Volume{
				{Mount: "/", Class: "nvme", TotalGB: 512, FreeGB: 128, Role: "root"},
			},
		},
	}
	out := renderStorageTabEnhanced(node)
	if !strings.Contains(out, "/ (nvme, 128/512 GB, 25% free) [root]") {
		t.Fatalf("storage tab = %q", out)
	}

	empty := renderStorageTabEnhanced(models.NodeFacts{})
	if !strings.Contains(empty, "No storage mounts detected") {
		t.Fatalf("storage tab empty-state = %q", empty)
	}
}
