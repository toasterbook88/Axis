package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"
)

var termMakeRaw = term.MakeRaw
var termRestore = term.Restore
var termGetSize = term.GetSize

// SelectOption represents a selectable line in the dropdown menu.
type SelectOption struct {
	ID       string
	Label    string
	Detail   string
	Disabled bool
}

// SelectResult captures the user's choice or cancellation status.
type SelectResult struct {
	ID       string
	Index    int
	Selected bool // false means operator cancellation
}

// TerminalIO abstracts basic TTY capabilities.
type TerminalIO interface {
	In() io.Reader
	Out() io.Writer
	IsTTY() bool
}

// StdTerminal is the default TerminalIO wrapper around os.Stdin and os.Stdout.
type StdTerminal struct {
	in  io.Reader
	out io.Writer
}

// NewStdTerminal constructs a standard terminal interface wrapper.
func NewStdTerminal(in io.Reader, out io.Writer) *StdTerminal {
	return &StdTerminal{in: in, out: out}
}

func (s *StdTerminal) In() io.Reader  { return s.in }
func (s *StdTerminal) Out() io.Writer { return s.out }
func (s *StdTerminal) IsTTY() bool {
	if fIn, ok := s.in.(*os.File); ok {
		if fOut, ok := s.out.(*os.File); ok {
			return fileIsTerminal(fIn) && fileIsTerminal(fOut)
		}
	}
	return false
}

// Selector defines the interface for prompting dropdown lists.
type Selector interface {
	Select(ctx context.Context, title string, options []SelectOption) (SelectResult, error)
}

// DefaultSelector implements Selector using ui.Select.
type DefaultSelector struct {
	Terminal TerminalIO
}

// NewDefaultSelector creates a default selector wrapper.
func NewDefaultSelector(term TerminalIO) *DefaultSelector {
	return &DefaultSelector{Terminal: term}
}

// Select delegates option list prompting to ui.Select.
func (ds *DefaultSelector) Select(ctx context.Context, title string, options []SelectOption) (SelectResult, error) {
	return Select(ctx, ds.Terminal, title, options)
}

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// StripANSIAndControls removes ANSI escape sequences and C0/C1 control characters.
func StripANSIAndControls(s string) string {
	s = ansiRegex.ReplaceAllString(s, "")
	var sb strings.Builder
	for _, r := range s {
		if r < 32 && r != '\t' && r != '\n' && r != '\r' {
			continue
		}
		if r >= 127 && r <= 159 {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func sanitizeAndTruncate(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	s = StripANSIAndControls(s)
	if len(s) > limit {
		if limit > 3 {
			return s[:limit-3] + "..."
		}
		return s[:limit]
	}
	return s
}

func hideCursor(w io.Writer) {
	fmt.Fprint(w, "\033[?25l")
}

func showCursor(w io.Writer) {
	fmt.Fprint(w, "\033[?25h")
}

func clearLines(w io.Writer, numLines int) {
	for i := 0; i < numLines; i++ {
		fmt.Fprint(w, "\033[F\r\033[2K")
	}
}

// KeyAction represents the decoded control command.
type KeyAction int

const (
	ActionNone KeyAction = iota
	ActionUp
	ActionDown
	ActionEnter
	ActionCancel
)

// Select displays an interactive terminal dropdown menu using clean ANSI styling.
func Select(
	ctx context.Context,
	terminal TerminalIO,
	title string,
	options []SelectOption,
) (SelectResult, error) {
	if len(options) == 0 {
		return SelectResult{}, fmt.Errorf("select: no options provided")
	}

	w := terminal.Out()

	// Fallback when terminal is not a TTY (CI, piped inputs)
	if !terminal.IsTTY() {
		fmt.Fprintln(w, title)
		for _, opt := range options {
			status := ""
			if opt.Disabled {
				status = " (disabled)"
			}
			lbl := StripANSIAndControls(opt.Label)
			det := StripANSIAndControls(opt.Detail)
			if det != "" {
				fmt.Fprintf(w, "  - %s: %s%s\n", lbl, det, status)
			} else {
				fmt.Fprintf(w, "  - %s%s\n", lbl, status)
			}
		}
		return SelectResult{Selected: false}, nil
	}

	fIn, ok := terminal.In().(*os.File)
	if !ok {
		return SelectResult{Selected: false}, fmt.Errorf("stdin is not an *os.File")
	}
	inFd := int(fIn.Fd())

	oldState, err := termMakeRaw(inFd)
	if err != nil {
		return SelectResult{}, fmt.Errorf("failed to make raw terminal: %w", err)
	}
	defer termRestore(inFd, oldState)
	hideCursor(w)
	defer showCursor(w)

	defer func() {
		if r := recover(); r != nil {
			termRestore(inFd, oldState)
			showCursor(w)
			panic(r)
		}
	}()

	selected := 0
	for selected < len(options) && options[selected].Disabled {
		selected++
	}
	if selected >= len(options) {
		selected = 0
	}

	startIdx := 0
	pageSize := 10

	for {
		select {
		case <-ctx.Done():
			return SelectResult{Selected: false}, ctx.Err()
		default:
		}

		width, height, err := termGetSize(inFd)
		if err != nil || width <= 0 || height <= 0 {
			width = 80
			height = 24
		}

		pageSize = 10
		maxAvailHeight := height - 3
		if pageSize > maxAvailHeight {
			pageSize = maxAvailHeight
		}
		if pageSize < 1 {
			pageSize = 1
		}
		if pageSize > len(options) {
			pageSize = len(options)
		}

		if selected < startIdx {
			startIdx = selected
		} else if selected >= startIdx+pageSize {
			startIdx = selected - pageSize + 1
		}

		cleanTitle := sanitizeAndTruncate(title, width-1)
		fmt.Fprintf(w, "%s\r\n", cleanTitle)

		numLines := 0
		if startIdx > 0 {
			fmt.Fprintf(w, "  \033[90m▲ (%d more options above)\033[0m\r\n", startIdx)
			numLines++
		}

		for i := 0; i < pageSize; i++ {
			idx := startIdx + i
			if idx >= len(options) {
				break
			}
			opt := options[idx]

			prefix := "    "
			if idx == selected {
				prefix = "  \033[36m❯\033[0m "
			}

			displayStr := opt.Label
			if opt.Detail != "" {
				displayStr = fmt.Sprintf("%s - %s", opt.Label, opt.Detail)
			}
			if opt.Disabled {
				displayStr = fmt.Sprintf("%s (disabled)", displayStr)
			}

			limit := width - 6
			if limit < 1 {
				limit = 1
			}
			displayStr = sanitizeAndTruncate(displayStr, limit)

			if opt.Disabled {
				fmt.Fprintf(w, "%s\033[90;9m%s\033[0m\r\n", prefix, displayStr)
			} else {
				if idx == selected {
					fmt.Fprintf(w, "%s\033[1;36m%s\033[0m\r\n", prefix, displayStr)
				} else {
					fmt.Fprintf(w, "%s%s\r\n", prefix, displayStr)
				}
			}
			numLines++
		}

		if startIdx+pageSize < len(options) {
			moreBelow := len(options) - (startIdx + pageSize)
			fmt.Fprintf(w, "  \033[90m▼ (%d more options below)\033[0m\r\n", moreBelow)
			numLines++
		}

		action, err := readKey(terminal.In(), inFd, true)
		if err != nil {
			clearLines(w, numLines)
			fmt.Fprint(w, "\r\033[2K")
			return SelectResult{}, err
		}

		switch action {
		case ActionUp:
			orig := selected
			for {
				selected = (selected - 1 + len(options)) % len(options)
				if !options[selected].Disabled {
					break
				}
				if selected == orig {
					break
				}
			}
		case ActionDown:
			orig := selected
			for {
				selected = (selected + 1) % len(options)
				if !options[selected].Disabled {
					break
				}
				if selected == orig {
					break
				}
			}
		case ActionEnter:
			clearLines(w, numLines)
			fmt.Fprint(w, "\r\033[2K")
			if options[selected].Disabled {
				continue
			}
			cleanResult := sanitizeAndTruncate(options[selected].Label, width-len(title)-5)
			fmt.Fprintf(w, "%s \033[32m%s\033[0m\r\n", cleanTitle, cleanResult)
			return SelectResult{
				ID:       options[selected].ID,
				Index:    selected,
				Selected: true,
			}, nil
		case ActionCancel:
			clearLines(w, numLines)
			fmt.Fprint(w, "\r\033[2K")
			fmt.Fprintf(w, "%s \033[90m(cancelled)\033[0m\r\n", cleanTitle)
			return SelectResult{Selected: false}, nil
		}

		clearLines(w, numLines)
		fmt.Fprint(w, "\r\033[2K")
	}
}
