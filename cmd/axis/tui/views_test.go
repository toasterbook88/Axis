package tui

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

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

func TestRenderDetailsTabNilResources(t *testing.T) {
	out := renderDetailsTabEnhanced(models.NodeFacts{Name: "bare"})
	if !strings.Contains(out, "CPU: Unknown") {
		t.Fatalf("details tab should degrade for nil resources: %q", out)
	}
}
