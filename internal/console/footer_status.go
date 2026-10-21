package console

import (
	"fmt"
	"strings"
)

// StatusFooterConfig configures the dynamic data providers for the statusline footer.
type StatusFooterConfig struct {
	// Model returns the name of the active model.
	Model func() string

	// UsedTokens returns the current estimated context-window occupancy.
	// This is not session spend: occupancy can shrink on compact; spend cannot.
	UsedTokens func() int

	// MaxTokens returns the context token limit.
	MaxTokens func() int

	// UsageStats returns real session token usage (in, out, turns reported).
	// Turns == 0 means the backend never reported usage; the usage segment
	// is omitted rather than fabricating zeros.
	UsageStats func() (in, out, turns int)

	// Mode returns the autonomy mode (e.g. "default", "edit", "full").
	// The footer prints observe, edit, or exec. It never prints a fleet fraction.
	Mode func() string

	// Snapshot returns a real snapshot age or publication id.
	// Empty means the footer prints "no snapshot".
	Snapshot func() string
}

func chromeMode(mode string) string {
	switch mode {
	case "edit":
		return "edit"
	case "full", "exec":
		return "exec"
	default:
		return "observe"
	}
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

// Footer width tiers. Below footerMediumMin only the model segment fits; from
// there up the context and snapshot segments join; at footerFullMin the full
// content appears.
const (
	footerFullMin   = 72
	footerMediumMin = 44
)

// noModelSegment is shown when model resolution finished empty-handed.
const noModelSegment = "no model · /model or axis model start"

// modelSegment renders the model slot for the startup state.
func modelSegment(st FooterState, name string) string {
	switch st.Model {
	case ModelPending:
		return "model: " + st.Frame
	case ModelNone:
		return noModelSegment
	default:
		return "model: " + name
	}
}

// Render draws the 1-line responsive footer. The line is exactly width-1
// display columns: the last column stays empty so a full-width write cannot
// trigger a terminal auto-wrap.
func (f *StatusFooter) Render(width int, st FooterState) []Line {
	limit := width - 1
	if limit < 0 {
		limit = 0
	}

	model := "default"
	if f.cfg.Model != nil {
		if m := f.cfg.Model(); m != "" {
			model = m
		}
	}
	modelSeg := modelSegment(st, model)

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

	mode := chromeMode("")
	if f.cfg.Mode != nil {
		mode = chromeMode(f.cfg.Mode())
	}
	snap := "no snapshot"
	if f.cfg.Snapshot != nil {
		if s := strings.TrimSpace(f.cfg.Snapshot()); s != "" {
			snap = s
		}
	}

	usageIn, usageOut, usageTurns := 0, 0, 0
	if f.cfg.UsageStats != nil {
		usageIn, usageOut, usageTurns = f.cfg.UsageStats()
	}
	hasUsage := usageTurns > 0

	bar, pct := FormatContextBar(used, max, 10)
	ctxFull := fmt.Sprintf("context: [%s] %d%% (%s/%s)", bar, pct, FormatTokens(used), FormatTokens(max))
	ctxCompact := fmt.Sprintf("context: %d%%", pct)

	var segments []string

	if width >= footerFullMin {
		segments = append(segments, modelSeg, ctxFull)
		if hasUsage {
			// Real spend is not occupancy: keep the context bar on UsedTokens
			// and surface in/out separately. Mode rides the same segment so
			// autonomy stays visible after turn 1 without a third column.
			usageText := fmt.Sprintf("usage: %s/%s · %s", FormatTokens(usageIn), FormatTokens(usageOut), mode)
			segments = append(segments, usageText)
		} else if mode != "" {
			segments = append(segments, "mode: "+mode)
		}
		segments = append(segments, snap)
	} else if width >= footerMediumMin {
		segments = append(segments, modelSeg, ctxCompact, snap)
	} else {
		segments = append(segments, modelSeg)
	}

	// Drop whole trailing segments (snap first, then the rest, keeping the
	// model) until the line fits, so we never show a fragment like "no sn".
	join := func(segs []string) string { return "── " + strings.Join(segs, " ── ") + " ──" }
	content := join(segments)
	for len(segments) > 1 && visibleWidth(content) > limit {
		segments = segments[:len(segments)-1]
		content = join(segments)
	}

	// Only a model segment that alone overflows is clipped. Measure by display
	// width, not runes: wide runes take two columns.
	content = clipWidth(content, limit)
	if pad := limit - visibleWidth(content); pad > 0 {
		content += strings.Repeat("─", pad)
	}

	return []Line{{
		Text:  content,
		Style: StyleMuted,
	}}
}
