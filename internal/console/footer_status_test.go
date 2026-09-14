package console

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStatusFooterResponsiveWidths(t *testing.T) {
	cfg := StatusFooterConfig{
		Model:      func() string { return "qwen3.8-9b" },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
		Mode:       func() string { return "default" },
		Fleet:      func() string { return "10/10 ok" },
	}
	footer := NewStatusFooter(cfg)

	// Wide screen: >= 80 cols
	lines80 := footer.Render(100)
	if len(lines80) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines80))
	}
	text80 := lines80[0].Text
	if !strings.Contains(text80, "model: qwen3.8-9b") ||
		!strings.Contains(text80, "context: [") ||
		!strings.Contains(text80, "mode: default") ||
		!strings.Contains(text80, "fleet: 10/10 ok") {
		t.Fatalf("wide footer missing segments: %s", text80)
	}
	if utf8.RuneCountInString(text80) != 100 {
		t.Fatalf("wide footer rune count = %d; want 100", utf8.RuneCountInString(text80))
	}

	// Medium screen: 70 cols (mode dropped, compact context)
	lines70 := footer.Render(70)
	text70 := lines70[0].Text
	if !strings.Contains(text70, "model: qwen3.8-9b") ||
		!strings.Contains(text70, "fleet: 10/10 ok") ||
		strings.Contains(text70, "mode: default") {
		t.Fatalf("medium footer unexpected segments: %s", text70)
	}
	if utf8.RuneCountInString(text70) != 70 {
		t.Fatalf("medium footer rune count = %d; want 70", utf8.RuneCountInString(text70))
	}

	// Narrow screen: 45 cols
	lines45 := footer.Render(45)
	text45 := lines45[0].Text
	if !strings.Contains(text45, "model: qwen3.8-9b") ||
		strings.Contains(text45, "fleet:") {
		t.Fatalf("narrow footer unexpected segments: %s", text45)
	}
	if utf8.RuneCountInString(text45) != 45 {
		t.Fatalf("narrow footer rune count = %d; want 45", utf8.RuneCountInString(text45))
	}
}

func TestStatusFooterShowsRealUsageWithoutHijackingContextBar(t *testing.T) {
	// Occupancy (UsedTokens/MaxTokens) and session spend (UsageStats) are
	// different quantities. The bar stays on occupancy; in/out is a
	// separate labeled segment when the backend reported at least one turn.
	cfg := StatusFooterConfig{
		Model:      func() string { return "qwen3.8-9b" },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
		UsageStats: func() (int, int, int) { return 1200, 340, 2 },
		Mode:       func() string { return "default" },
		Fleet:      func() string { return "10/10 ok" },
	}
	text := NewStatusFooter(cfg).Render(100)[0].Text
	if !strings.Contains(text, "context: [") || !strings.Contains(text, "(9.2k/32k)") {
		t.Fatalf("context occupancy missing from footer: %s", text)
	}
	if !strings.Contains(text, "usage: 1.2k/340") {
		t.Fatalf("real usage segment missing: %s", text)
	}
	if strings.Contains(text, "mode: default") {
		t.Fatalf("mode should yield the column when usage is shown: %s", text)
	}
	if !strings.Contains(text, "fleet: 10/10 ok") {
		t.Fatalf("fleet missing when usage is shown: %s", text)
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
		Model:      func() string { return "qwen3.8-9b" },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
		UsageStats: func() (int, int, int) { return 0, 0, 0 },
		Mode:       func() string { return "default" },
		Fleet:      func() string { return "10/10 ok" },
	}
	text := NewStatusFooter(cfg).Render(100)[0].Text
	if strings.Contains(text, "usage:") {
		t.Fatalf("usage segment fabricated from zero turns: %s", text)
	}
	if !strings.Contains(text, "mode: default") {
		t.Fatalf("mode missing when usage is absent: %s", text)
	}
}

func TestStatusFooterHidesUsageOnMediumWidth(t *testing.T) {
	cfg := StatusFooterConfig{
		Model:      func() string { return "qwen3.8-9b" },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
		UsageStats: func() (int, int, int) { return 1200, 340, 2 },
		Fleet:      func() string { return "10/10 ok" },
	}
	text := NewStatusFooter(cfg).Render(70)[0].Text
	if strings.Contains(text, "usage:") {
		t.Fatalf("usage segment should wait for >=80 cols: %s", text)
	}
	if !strings.Contains(text, "fleet: 10/10 ok") {
		t.Fatalf("medium footer dropped fleet: %s", text)
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
