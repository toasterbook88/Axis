package console

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// PagerOverlay shows a tool cell's full body. q or esc closes it.
// It is the expand path for a collapsed tool cell, not a second transcript.
type PagerOverlay struct {
	title string
	lines []string
	top   int
	rows  int
	done  bool
}

// NewPagerOverlay splits body into pager rows. title is the tool name.
func NewPagerOverlay(title, body string) *PagerOverlay {
	body = strings.TrimRight(body, "\n")
	var lines []string
	if body != "" {
		lines = strings.Split(body, "\n")
	}
	return &PagerOverlay{title: title, lines: lines, rows: 12}
}

func (p *PagerOverlay) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	if p.done {
		return nil, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	switch key.String() {
	case "q", "esc", "ctrl+c":
		p.done = true
		return nil, nil
	case "j", "down":
		if p.top+1 < len(p.lines) {
			p.top++
		}
	case "k", "up":
		if p.top > 0 {
			p.top--
		}
	}
	return p, nil
}

func (p *PagerOverlay) Render(width int) []Line {
	width = effectiveWidth(width)
	out := []Line{{
		Text:  fmt.Sprintf("pager %s  %d lines  q closes", p.title, len(p.lines)),
		Style: StyleAccent,
	}}
	if len(p.lines) == 0 {
		out = append(out, Line{Text: "(empty)", Style: StyleMuted})
		return out
	}
	end := p.top + p.rows
	if end > len(p.lines) {
		end = len(p.lines)
	}
	for _, line := range p.lines[p.top:end] {
		out = append(out, Line{Text: clipRunes(line, width), Style: StylePlain})
	}
	return out
}

func (p *PagerOverlay) Done() bool { return p.done }

// pagerBody is the full tool result a pager shows, including the advisory
// line a placement cell adds. It is not the collapsed transcript form.
func pagerBody(e *ToolEntry) string {
	if e == nil {
		return ""
	}
	result := e.Result
	if e.Name == "axis_place" && !strings.Contains(result, "advisory") {
		if result != "" {
			result += "\n"
		}
		result += "advisory — not reserved"
	}
	return result
}
