package console

import (
	"strings"
	"testing"
)

func TestStatusFooterResponsiveWidths(t *testing.T) {
	cfg := StatusFooterConfig{
		Model:      func() string { return "mid-9b" },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
		Mode:       func() string { return "default" },
		Snapshot:   func() string { return "snap pub-1" },
	}
	footer := NewStatusFooter(cfg)

	// Wide screen: >= 80 cols
	lines80 := footer.Render(100, FooterState{})
	if len(lines80) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines80))
	}
	text80 := lines80[0].Text
	if !strings.Contains(text80, "model: mid-9b") ||
		!strings.Contains(text80, "context: [") ||
		!strings.Contains(text80, "mode: observe") ||
		!strings.Contains(text80, "snap pub-1") ||
		strings.Contains(text80, "fleet:") {
		t.Fatalf("wide footer missing segments: %s", text80)
	}
	if visibleWidth(text80) != 99 {
		t.Fatalf("wide footer width = %d; want 99", visibleWidth(text80))
	}

	// Medium screen: 70 cols (mode dropped, compact context)
	lines70 := footer.Render(70, FooterState{})
	text70 := lines70[0].Text
	if !strings.Contains(text70, "model: mid-9b") ||
		!strings.Contains(text70, "snap pub-1") ||
		strings.Contains(text70, "fleet:") ||
		strings.Contains(text70, "mode: observe") {
		t.Fatalf("medium footer unexpected segments: %s", text70)
	}
	if visibleWidth(text70) != 69 {
		t.Fatalf("medium footer width = %d; want 69", visibleWidth(text70))
	}

	// Narrow screen: 45 cols
	lines45 := footer.Render(45, FooterState{})
	text45 := lines45[0].Text
	if !strings.Contains(text45, "model: mid-9b") ||
		strings.Contains(text45, "fleet:") {
		t.Fatalf("narrow footer unexpected segments: %s", text45)
	}
	if visibleWidth(text45) != 44 {
		t.Fatalf("narrow footer width = %d; want 44", visibleWidth(text45))
	}
}

func TestStatusFooterShowsRealUsageWithoutHijackingContextBar(t *testing.T) {
	// Occupancy (UsedTokens/MaxTokens) and session spend (UsageStats) are
	// different quantities. The bar stays on occupancy; in/out is a
	// separate labeled segment when the backend reported at least one turn.
	cfg := StatusFooterConfig{
		Model:      func() string { return "mid-9b" },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
		UsageStats: func() (int, int, int) { return 1200, 340, 2 },
		Mode:       func() string { return "default" },
		Snapshot:   func() string { return "" },
	}
	text := NewStatusFooter(cfg).Render(120, FooterState{})[0].Text
	if !strings.Contains(text, "context: [") || !strings.Contains(text, "(9.2k/32k)") {
		t.Fatalf("context occupancy missing from footer: %s", text)
	}
	if !strings.Contains(text, "usage: 1.2k/340 · observe") {
		t.Fatalf("real usage+mode segment missing: %s", text)
	}
	if strings.Contains(text, "mode: observe") {
		t.Fatalf("mode should ride the usage segment, not take its own column: %s", text)
	}
	if strings.Contains(text, "fleet:") {
		t.Fatalf("footer showed a fleet fraction: %s", text)
	}
	if !strings.Contains(text, "no snapshot") {
		t.Fatalf("empty snapshot should read no snapshot: %s", text)
	}
	// 1200+340 vs 32768 would be ~4%. Occupancy 9200/32768 is ~28%.
	if strings.Contains(text, " 4%") {
		t.Fatalf("usage totals leaked into the occupancy bar: %s", text)
	}
	if !strings.Contains(text, " 28%") {
		t.Fatalf("occupancy percent missing: %s", text)
	}
}

func TestStatusFooterOmitsUsageWhenBackendReportedNone(t *testing.T) {
	cfg := StatusFooterConfig{
		Model:      func() string { return "mid-9b" },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
		UsageStats: func() (int, int, int) { return 0, 0, 0 },
		Mode:       func() string { return "edit" },
	}
	text := NewStatusFooter(cfg).Render(100, FooterState{})[0].Text
	if strings.Contains(text, "usage:") {
		t.Fatalf("usage segment fabricated from zero turns: %s", text)
	}
	if !strings.Contains(text, "mode: edit") {
		t.Fatalf("mode missing when usage is absent: %s", text)
	}
	if strings.Contains(text, "fleet:") {
		t.Fatalf("footer showed a fleet fraction: %s", text)
	}
}

func TestStatusFooterHidesUsageOnMediumWidth(t *testing.T) {
	cfg := StatusFooterConfig{
		Model:      func() string { return "mid-9b" },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
		UsageStats: func() (int, int, int) { return 1200, 340, 2 },
		Snapshot:   func() string { return "snap pub-1" },
	}
	text := NewStatusFooter(cfg).Render(70, FooterState{})[0].Text
	if strings.Contains(text, "usage:") {
		t.Fatalf("usage segment should wait for >=80 cols: %s", text)
	}
	if !strings.Contains(text, "snap pub-1") || strings.Contains(text, "fleet:") {
		t.Fatalf("medium footer chrome: %s", text)
	}
}

func TestFormatTokensAndBar(t *testing.T) {
	if got := FormatTokens(500); got != "500" {
		t.Fatalf("FormatTokens(500) = %q; want '500'", got)
	}
	if got := FormatTokens(2400); got != "2.4k" {
		t.Fatalf("FormatTokens(2400) = %q; want '2.4k'", got)
	}
	if got := FormatTokens(32768); got != "32k" {
		t.Fatalf("FormatTokens(32768) = %q; want '32k'", got)
	}

	bar, pct := FormatContextBar(25, 100, 10)
	if pct != 25 {
		t.Fatalf("pct = %d; want 25", pct)
	}
	if !strings.HasPrefix(bar, "██") {
		t.Fatalf("bar prefix unexpected: %s", bar)
	}
}

func tierFooter(model string) *StatusFooter {
	return NewStatusFooter(StatusFooterConfig{
		Model:      func() string { return model },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
	})
}

func TestStatusFooter_Tiers(t *testing.T) {
	f := tierFooter("example-model")
	for _, w := range []int{30, 43, 44, 71, 72, 120} {
		text := f.Render(w, FooterState{})[0].Text
		if got := visibleWidth(text); got != w-1 {
			t.Errorf("width %d: visible width %d, want %d: %q", w, got, w-1, text)
		}
		switch {
		case w >= 72:
			if !strings.Contains(text, "context: [") {
				t.Errorf("width %d: want full context bar: %q", w, text)
			}
		case w >= 44:
			// At 44 the snap segment is clipped away by design; it only
			// fits once the line is wide enough to hold it whole.
			if !strings.Contains(text, "context: ") || strings.Contains(text, "[") {
				t.Errorf("width %d: want compact context, no bar: %q", w, text)
			}
			if w == 71 && !strings.Contains(text, "no snapshot") {
				t.Errorf("width %d: want snap segment: %q", w, text)
			}
		default:
			if !strings.Contains(text, "model: example-model") || strings.Contains(text, "context") {
				t.Errorf("width %d: want model only: %q", w, text)
			}
		}
	}
}

func TestStatusFooter_ModelStates(t *testing.T) {
	f := tierFooter("example-model")
	pending := f.Render(120, FooterState{Frame: "╲", Model: ModelPending})[0].Text
	if !strings.Contains(pending, "model: ╲") || strings.Contains(pending, "example-model") {
		t.Errorf("pending: %q", pending)
	}
	none := f.Render(120, FooterState{Model: ModelNone})[0].Text
	if !strings.Contains(none, "no model · /model or axis model start") {
		t.Errorf("none: %q", none)
	}
}

func TestStatusFooter_WideRunesNeverOverflow(t *testing.T) {
	f := tierFooter("模型-example")
	for w := 20; w <= 120; w++ {
		for _, st := range []ModelState{ModelReady, ModelPending, ModelNone} {
			text := f.Render(w, FooterState{Frame: "─", Model: st})[0].Text
			if got := visibleWidth(text); got > w-1 {
				t.Fatalf("width %d state %d: visible width %d > %d: %q", w, st, got, w-1, text)
			}
		}
	}
}
