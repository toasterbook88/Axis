package console

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/toasterbook88/axis/internal/agent"
)

// ApprovalOverlay presents an interactive confirmation modal over the console.
// It captures keystrokes until the operator allows, denies, or auto-approves.
type ApprovalOverlay struct {
	tool        string
	description string
	score       int
	reply       chan<- agent.ConfirmResult

	done     bool
	decision agent.ConfirmResult
	explain  bool
}

// NewApprovalOverlay constructs a modal for operator confirmation.
func NewApprovalOverlay(tool, description string, score int, reply chan<- agent.ConfirmResult) *ApprovalOverlay {
	return &ApprovalOverlay{
		tool:        tool,
		description: description,
		score:       score,
		reply:       reply,
	}
}

// compile-time check that ApprovalOverlay satisfies Overlay
var _ Overlay = (*ApprovalOverlay)(nil)

func (o *ApprovalOverlay) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	if o.done {
		return nil, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return o, nil
	}

	switch key.String() {
	case "y", "Y":
		o.resolve(agent.ConfirmYes)
		return nil, nil

	case "n", "N", "esc", "ctrl+c":
		o.resolve(agent.ConfirmNo)
		return nil, nil

	case "a", "A":
		o.resolve(agent.ConfirmAlways)
		return nil, nil

	case "v", "V":
		o.resolve(agent.ConfirmNever)
		return nil, nil

	case "?":
		o.explain = !o.explain
		return o, nil
	}

	return o, nil
}

func (o *ApprovalOverlay) resolve(result agent.ConfirmResult) {
	if o.done {
		return
	}
	o.done = true
	o.decision = result
	if o.reply != nil {
		select {
		case o.reply <- result:
		default:
		}
	}
}

func (o *ApprovalOverlay) Done() bool {
	return o.done
}

func (o *ApprovalOverlay) Decision() agent.ConfirmResult {
	return o.decision
}

func (o *ApprovalOverlay) Render(width int) []Line {
	width = effectiveWidth(width)
	boxWidth := width - 2
	if boxWidth < 30 {
		boxWidth = 30
	}

	var lines []Line

	// Top border
	titlePrefix := "┌─ Tool Execution Approval ─"
	prefixRunes := utf8.RuneCountInString(titlePrefix)
	var borderTitle string
	if boxWidth > prefixRunes {
		borderTitle = fmt.Sprintf("%s%s", titlePrefix, strings.Repeat("─", boxWidth-prefixRunes))
	} else {
		borderTitle = string([]rune(titlePrefix)[:boxWidth])
	}
	lines = append(lines, Line{Text: borderTitle, Style: StyleAccent})

	// Tool name + Risk Badge
	riskStyle := StyleGood
	riskBadge := "[LOW RISK]"
	if o.score >= 70 {
		riskStyle = StyleBad
		riskBadge = "[HIGH RISK]"
	} else if o.score >= 40 {
		riskStyle = StyleBad
		riskBadge = "[CAUTION]"
	}

	lines = append(lines, Line{
		Text:  fmt.Sprintf("│ Tool: %s  %s", o.tool, riskBadge),
		Style: riskStyle,
	})

	if o.score > 0 {
		lines = append(lines, Line{
			Text:  fmt.Sprintf("│ Safety Score: %d/100", o.score),
			Style: StyleMuted,
		})
	}

	// Details / description lines
	lines = append(lines, Line{Text: "│ Details:", Style: StyleMuted})
	descLines := strings.Split(strings.TrimSpace(o.description), "\n")
	avail := boxWidth - 6
	if avail < 10 {
		avail = 10
	}
	for _, rawLine := range descLines {
		rawLine = strings.TrimSpace(rawLine)
		if rawLine == "" {
			continue
		}
		for _, wrapped := range wrap(rawLine, avail) {
			lines = append(lines, Line{Text: fmt.Sprintf("│   %s", wrapped), Style: StylePlain})
		}
	}

	// Explanation toggle
	if o.explain {
		lines = append(lines, Line{Text: "│", Style: StyleMuted})
		explanation := "│ Risk evaluates execution blast radius, file modifications, and network access."
		lines = append(lines, Line{Text: explanation, Style: StyleMuted})
	}

	// Keystroke prompt line
	lines = append(lines, Line{Text: "│", Style: StyleMuted})
	promptText := "│ [y]es  [n]o  [a]lways  ne[v]er  [?]explain"
	lines = append(lines, Line{Text: promptText, Style: StyleStrong})

	// Bottom border
	lines = append(lines, Line{Text: fmt.Sprintf("└%s", strings.Repeat("─", boxWidth-1)), Style: StyleAccent})

	return lines
}
