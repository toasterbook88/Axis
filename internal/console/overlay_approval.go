package console

import (
	"fmt"
	"os"
	"strings"
	"time"
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
	why         string
	reply       chan<- agent.ConfirmResult

	done     bool
	decision agent.ConfirmResult
	explain  bool

	// deadline is when an unanswered box fails closed.
	deadline time.Time
	// now is the clock Render and the timeout use. Tests replace it.
	now func() time.Time
}

// ApprovalFailClosed is how long an unanswered approval may sit before it
// denies. The console wait and the painted countdown use this one deadline.
const ApprovalFailClosed = 600 * time.Second

// approvalFailClosed is the unexported alias used inside this package.
const approvalFailClosed = ApprovalFailClosed

// approvalTickMsg advances the countdown. deadline ties the tick to one box.
type approvalTickMsg struct {
	deadline time.Time
}

// NewApprovalOverlay constructs a modal for operator confirmation.
func NewApprovalOverlay(tool, description string, score int, reply chan<- agent.ConfirmResult) *ApprovalOverlay {
	return &ApprovalOverlay{
		tool:        tool,
		description: description,
		score:       score,
		reply:       reply,
		now:         time.Now,
		deadline:    time.Now().Add(approvalFailClosed),
	}
}

func (o *ApprovalOverlay) clock() time.Time {
	if o.now == nil {
		return time.Now()
	}
	return o.now()
}

// SetWhy records the safety-gate reason shown next to the score.
func (o *ApprovalOverlay) SetWhy(why string) {
	if o == nil {
		return
	}
	o.why = strings.TrimSpace(why)
}

func (o *ApprovalOverlay) remaining() time.Duration {
	left := o.deadline.Sub(o.clock())
	if left < 0 {
		return 0
	}
	return left.Round(time.Second)
}

// Arm starts the one-second countdown. The console calls it when the box opens.
func (o *ApprovalOverlay) Arm() tea.Cmd {
	if o == nil {
		return nil
	}
	if o.deadline.IsZero() {
		o.deadline = time.Now().Add(approvalFailClosed)
	}
	return o.tick()
}

func (o *ApprovalOverlay) tick() tea.Cmd {
	deadline := o.deadline
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return approvalTickMsg{deadline: deadline}
	})
}

// compile-time check that ApprovalOverlay satisfies Overlay
var _ Overlay = (*ApprovalOverlay)(nil)

func (o *ApprovalOverlay) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	if o.done {
		return nil, nil
	}

	if tick, ok := msg.(approvalTickMsg); ok {
		if o.done || !tick.deadline.Equal(o.deadline) {
			return o, nil
		}
		if !o.clock().Before(o.deadline) {
			o.resolve(agent.ConfirmNo)
			return nil, nil
		}
		return o, o.tick()
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

	case "enter":
		// Enter does not approve.
		return o, nil

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

	why := o.why
	if why == "" {
		why = "no gate finding"
	}
	lines = append(lines, Line{
		Text:  fmt.Sprintf("│ safety %d · %s", o.score, clipRunes(why, 72)),
		Style: StyleMuted,
	})
	if argv := clipRunes(strings.TrimSpace(o.description), 160); argv != "" {
		lines = append(lines, Line{
			Text:  "│ argv " + argv,
			Style: StylePlain,
		})
	}
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		cwd = "."
	}
	lines = append(lines, Line{
		Text:  fmt.Sprintf("│ cwd %s", clipRunes(cwd, 48)),
		Style: StyleMuted,
	})

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
	lines = append(lines, Line{
		Text:  fmt.Sprintf("│ timeout %s fail-closed", o.remaining()),
		Style: StyleMuted,
	})
	promptText := "│ [y] run  [n] deny  [esc] deny"
	lines = append(lines, Line{Text: promptText, Style: StyleStrong})

	// Bottom border
	lines = append(lines, Line{Text: fmt.Sprintf("└%s", strings.Repeat("─", boxWidth-1)), Style: StyleAccent})

	return lines
}
