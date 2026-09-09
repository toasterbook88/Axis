package console

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// StatusFooterConfig configures the dynamic data providers for the statusline footer.
type StatusFooterConfig struct {
	// Model returns the name of the active model.
	Model func() string

	// UsedTokens returns the current estimated context tokens.
	UsedTokens func() int

	// MaxTokens returns the context token limit.
	MaxTokens func() int

	// Mode returns the autonomy mode (e.g. "default", "edit", "full").
	Mode func() string

	// Fleet returns the cluster fleet reachability summary (e.g. "10/10 ok").
	Fleet func() string
}

// StatusFooter renders a persistent 1-line situational awareness footer below the editor.
type StatusFooter struct {
	cfg StatusFooterConfig
}

// NewStatusFooter builds a StatusFooter with the given configuration.
func NewStatusFooter(cfg StatusFooterConfig) *StatusFooter {
	return &StatusFooter{cfg: cfg}
}

// FormatTokens converts raw token counts to readable notation (e.g. 9200 -> "9.2k").
func FormatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return fmt.Sprintf("%dk", n/1000)
}

// FormatContextBar generates a block progress bar and percentage.
func FormatContextBar(used, max, barWidth int) (bar string, percent int) {
	if barWidth <= 0 {
		barWidth = 10
	}
	if max <= 0 {
		return strings.Repeat("░", barWidth), 0
	}
	if used < 0 {
		used = 0
	}
	ratio := float64(used) / float64(max)
	if ratio > 1.0 {
		ratio = 1.0
	}
	percent = int(ratio * 100)
	filled := int(ratio * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	bar = strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	return bar, percent
}

// Render draws the 1-line responsive footer.
func (f *StatusFooter) Render(width int) []Line {
	width = effectiveWidth(width)

	model := "default"
	if f.cfg.Model != nil {
		if m := f.cfg.Model(); m != "" {
			model = m
		}
	}

	used := 0
	if f.cfg.UsedTokens != nil {
		used = f.cfg.UsedTokens()
	}

	max := 32768
	if f.cfg.MaxTokens != nil {
		if m := f.cfg.MaxTokens(); m > 0 {
			max = m
		}
	}

	mode := "default"
	if f.cfg.Mode != nil {
		if m := f.cfg.Mode(); m != "" {
			mode = m
		}
	}

	fleet := ""
	if f.cfg.Fleet != nil {
		fleet = f.cfg.Fleet()
	}

	bar, pct := FormatContextBar(used, max, 10)
	ctxFull := fmt.Sprintf("context: [%s] %d%% (%s/%s)", bar, pct, FormatTokens(used), FormatTokens(max))
	ctxMedium := fmt.Sprintf("context: [%s] %d%%", bar, pct)
	ctxCompact := fmt.Sprintf("context: %d%%", pct)

	var segments []string

	if width >= 80 {
		segments = append(segments, "model: "+model, ctxFull)
		if mode != "" {
			segments = append(segments, "mode: "+mode)
		}
		if fleet != "" {
			segments = append(segments, "fleet: "+fleet)
		}
	} else if width >= 60 {
		segments = append(segments, "model: "+model, ctxMedium)
		if fleet != "" {
			segments = append(segments, "fleet: "+fleet)
		}
	} else if width >= 40 {
		segments = append(segments, "model: "+model, ctxCompact)
	} else {
		segments = append(segments, "model: "+model)
	}

	content := "── " + strings.Join(segments, " ── ") + " ──"

	// Fill trailing space with dashes if room allows
	contentRunes := utf8.RuneCountInString(content)
	if contentRunes < width {
		content += strings.Repeat("─", width-contentRunes)
	} else if contentRunes > width {
		// Truncate cleanly to width
		runes := []rune(content)
		content = string(runes[:width])
	}

	return []Line{{
		Text:  content,
		Style: StyleMuted,
	}}
}
