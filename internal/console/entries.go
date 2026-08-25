// Package console renders the interactive agent surface.
//
// The surface is transcript-first: committed content is printed above the
// program into the terminal's own scrollback (tea.Println), while the
// ephemeral region — input editor, footer, overlays — is what View renders.
//
// Entries are the committed half, and they are style-free. Render returns
// Lines carrying plain text plus a semantic Style; escape sequences are
// applied once, at the end, by Paint. Keeping ANSI out of the layout path
// means wrapping arithmetic operates on exactly the characters the terminal
// will show, and golden tests assert on text rather than on color state.
package console

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/ui"
)

// Kind classifies a transcript entry. It drives the gutter marker and
// whether the entry carries a rendered timestamp.
type Kind string

const (
	KindUser     Kind = "user"     // operator input
	KindAgent    Kind = "agent"    // assistant message
	KindThinking Kind = "thinking" // reasoning block extracted from the stream
	KindTool     Kind = "tool"     // tool call and its result
	KindStream   Kind = "stream"   // long-running incremental output
	KindDiff     Kind = "diff"     // file write rendered as a diff
	KindApproval Kind = "approval" // resolved approval record
	KindNotice   Kind = "notice"   // runtime/system event
	KindError    Kind = "error"    // failure in any of the above
)

// Style is a semantic role, not a color. Mapping roles to escape sequences
// is Paint's job and happens exactly once.
type Style uint8

const (
	StylePlain  Style = iota // body text
	StyleStrong              // operator input
	StyleMuted               // secondary detail
	StyleAccent              // tool and subject names
	StyleGood                // succeeded, approved
	StyleBad                 // failed, denied, blocked
)

// Line is one rendered row: a gutter (marker, optional clock, or the
// indentation aligning a continuation) plus styled body text. Both fields are
// plain — no escape sequences — so Gutter+Text is the exact cell width.
type Line struct {
	Gutter string
	Text   string
	Style  Style
}

// Width returns the terminal cells the line occupies.
func (l Line) Width() int { return visibleWidth(l.Gutter) + visibleWidth(l.Text) }

// Plain returns the line as unstyled text, which is what golden tests and the
// non-TTY fallback consume.
func (l Line) Plain() string { return l.Gutter + l.Text }

// Paint renders a line for a terminal, applying its style to the body and
// muting the gutter. This is the only place escape sequences enter output.
func Paint(l Line) string {
	gutter := l.Gutter
	if strings.TrimSpace(gutter) != "" {
		gutter = ui.Dim(gutter)
	}
	return gutter + paintText(l.Text, l.Style)
}

func paintText(text string, style Style) string {
	if text == "" {
		return ""
	}
	switch style {
	case StyleStrong:
		return ui.Bold(text)
	case StyleMuted:
		return ui.Dim(text)
	case StyleAccent:
		return ui.Cyan(text)
	case StyleGood:
		return ui.Green(text)
	case StyleBad:
		return ui.Red(text)
	default:
		return text
	}
}

// PaintAll renders lines into a single string, one per row.
func PaintAll(lines []Line) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = Paint(l)
	}
	return strings.Join(out, "\n")
}

// PlainAll returns lines as unstyled text, one per row.
func PlainAll(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Plain()
	}
	return out
}

// gutter is the two-column marker prefixing an entry's first line.
// Continuation lines are indented to match. Markers stay ASCII so the
// transcript survives terminals without reliable Unicode.
var gutter = map[Kind]string{
	KindUser:     ">",
	KindAgent:    "*",
	KindThinking: "~",
	KindTool:     "$",
	KindStream:   "|",
	KindDiff:     "#",
	KindApproval: "?",
	KindNotice:   "-",
	KindError:    "!",
}

// timestamped reports whether a kind renders its clock time. Conversational
// turns do not: a timestamp on every line is noise. Runtime events do,
// because their age is part of the fact.
func timestamped(k Kind) bool {
	switch k {
	case KindApproval, KindNotice, KindError:
		return true
	default:
		return false
	}
}

// Entry is one committed group of lines in the transcript. Render must be
// deterministic for a given width: the same entry always produces the same
// lines. Entries never write to a terminal and never emit escape sequences.
type Entry interface {
	Kind() Kind
	At() time.Time
	Render(width int) []Line
}

// base carries the fields every entry shares.
type base struct {
	kind Kind
	at   time.Time
}

func (b base) Kind() Kind     { return b.kind }
func (b base) At() time.Time  { return b.at }
func (b base) marker() string { return gutter[b.kind] }

// prefix is the gutter for an entry's first line.
func (b base) prefix() string {
	if timestamped(b.kind) {
		return fmt.Sprintf("%s %s ", b.marker(), b.at.Format("15:04:05"))
	}
	return b.marker() + " "
}

// indent is the gutter for continuation lines: blanks matching prefix.
func (b base) indent() string {
	return strings.Repeat(" ", visibleWidth(b.prefix()))
}

// minWidth is the narrowest terminal the transcript will lay out for. Below
// it, wrapping produces single-character columns, so we clamp instead.
const minWidth = 20

// effectiveWidth clamps a caller-supplied width to something layoutable.
func effectiveWidth(width int) int {
	if width < minWidth {
		return minWidth
	}
	return width
}

// renderBody wraps body beneath the gutter of b. The first line carries the
// marker; the rest are indented to the same column.
func renderBody(b base, width int, body string, style Style) []Line {
	return layout(b, width, body, style, true)
}

// renderContinuation lays out text indented under an entry's gutter with no
// marker of its own.
func renderContinuation(b base, width int, body string, style Style) []Line {
	return layout(b, width, body, style, false)
}

func layout(b base, width int, body string, style Style, lead bool) []Line {
	if body == "" {
		return nil
	}
	width = effectiveWidth(width)
	indent := b.indent()

	avail := width - visibleWidth(indent)
	if avail < 1 {
		avail = 1
	}

	var out []Line
	for i, text := range wrap(body, avail) {
		g := indent
		if lead && i == 0 {
			g = b.prefix()
		}
		out = append(out, Line{Gutter: g, Text: text, Style: style})
	}
	return out
}

// wrap breaks text into lines no wider than width, preserving existing
// newlines as hard breaks. Words longer than width are split at rune
// boundaries.
func wrap(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapLine(para, width)...)
	}
	return out
}

func wrapLine(line string, width int) []string {
	var (
		out     []string
		current string
	)
	flush := func() {
		out = append(out, current)
		current = ""
	}
	for _, word := range strings.Fields(line) {
		switch {
		case current == "" && visibleWidth(word) <= width:
			current = word
		case current != "" && visibleWidth(current)+1+visibleWidth(word) <= width:
			current += " " + word
		default:
			if current != "" {
				flush()
			}
			// A word wider than the column is split rather than allowed to
			// overflow. The split walks runes and measures cells, so it can
			// never cut a multi-byte character or miscount a wide one.
			for visibleWidth(word) > width {
				head, rest := splitToWidth(word, width)
				if head == "" {
					break // degenerate width; emit the remainder whole
				}
				out = append(out, head)
				word = rest
			}
			current = word
		}
	}
	if current != "" {
		flush()
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}

// splitToWidth cuts s at the last rune boundary fitting within width cells,
// returning the head and the remainder.
func splitToWidth(s string, width int) (head, rest string) {
	if width < 1 {
		return "", s
	}
	cells := 0
	for i, r := range s {
		w := visibleWidth(string(r))
		if cells+w > width {
			return s[:i], s[i:]
		}
		cells += w
	}
	return s, ""
}

// visibleWidth counts the terminal cells s occupies. lipgloss.Width defers to
// ansi.StringWidth, which ignores escape sequences and measures grapheme
// clusters, so wide CJK characters and emoji count as the cells they take
// rather than as one rune each.
func visibleWidth(s string) int { return lipgloss.Width(s) }

// UserEntry is a submitted operator message.
type UserEntry struct {
	base
	Text string
}

// NewUserEntry records operator input at t.
func NewUserEntry(t time.Time, text string) *UserEntry {
	return &UserEntry{base: base{kind: KindUser, at: t}, Text: text}
}

func (e *UserEntry) Render(width int) []Line {
	return renderBody(e.base, width, e.Text, StyleStrong)
}

// AgentEntry is an assistant message.
type AgentEntry struct {
	base
	Text string
}

// NewAgentEntry records an assistant message at t.
func NewAgentEntry(t time.Time, text string) *AgentEntry {
	return &AgentEntry{base: base{kind: KindAgent, at: t}, Text: text}
}

func (e *AgentEntry) Render(width int) []Line {
	return renderBody(e.base, width, e.Text, StylePlain)
}

// ThinkingEntry is a reasoning block lifted out of the response stream. It
// renders collapsed to a summary unless Expanded is set.
type ThinkingEntry struct {
	base
	Text     string
	Expanded bool
}

// NewThinkingEntry records a reasoning block at t.
func NewThinkingEntry(t time.Time, text string) *ThinkingEntry {
	return &ThinkingEntry{base: base{kind: KindThinking, at: t}, Text: text}
}

// collapsedThinkingLines is how much of a reasoning block shows when collapsed.
const collapsedThinkingLines = 2

func (e *ThinkingEntry) Render(width int) []Line {
	body := e.Text
	if !e.Expanded {
		lines := strings.Split(strings.TrimSpace(e.Text), "\n")
		if len(lines) > collapsedThinkingLines {
			body = strings.Join(lines[:collapsedThinkingLines], "\n") +
				fmt.Sprintf("\n... %d more lines", len(lines)-collapsedThinkingLines)
		}
	}
	return renderBody(e.base, width, body, StyleMuted)
}

// ToolEntry is a tool call and its outcome. Result is a rendered summary, not
// raw output; large output belongs in a StreamEntry.
type ToolEntry struct {
	base

	// ID correlates a call with its result. Tools dispatch concurrently, so
	// without it a completion cannot be mapped back to its invocation.
	ID string

	Name    string
	Summary string
	Result  string
	Err     error
}

// NewToolEntry records a tool call at t.
func NewToolEntry(t time.Time, id, name, summary string) *ToolEntry {
	return &ToolEntry{base: base{kind: KindTool, at: t}, ID: id, Name: name, Summary: summary}
}

func (e *ToolEntry) Render(width int) []Line {
	head := e.Name
	if e.Summary != "" {
		head += " " + e.Summary
	}
	out := renderBody(e.base, width, head, StyleAccent)

	switch {
	case e.Err != nil:
		out = append(out, renderContinuation(e.base, width, "error: "+e.Err.Error(), StyleBad)...)
	case e.Result != "":
		out = append(out, renderContinuation(e.base, width, e.Result, StyleMuted)...)
	}
	return out
}

// StreamEntry accumulates incremental output from a long-running tool such as
// remote_tail_logs. It is the one entry that mutates after creation.
type StreamEntry struct {
	base
	ID    string
	Label string
	Lines []string
	Done  bool

	// Limit caps retained lines so a chatty log tail cannot grow the
	// transcript without bound.
	Limit int
}

// defaultStreamLimit bounds retained lines for a stream entry.
const defaultStreamLimit = 200

// NewStreamEntry opens a stream labelled label at t.
func NewStreamEntry(t time.Time, id, label string) *StreamEntry {
	return &StreamEntry{
		base:  base{kind: KindStream, at: t},
		ID:    id,
		Label: label,
		Limit: defaultStreamLimit,
	}
}

// Append adds one line, dropping the oldest when at the limit.
func (e *StreamEntry) Append(line string) {
	limit := e.Limit
	if limit <= 0 {
		limit = defaultStreamLimit
	}
	e.Lines = append(e.Lines, line)
	if len(e.Lines) > limit {
		e.Lines = e.Lines[len(e.Lines)-limit:]
	}
}

// Close marks the stream finished.
func (e *StreamEntry) Close() { e.Done = true }

func (e *StreamEntry) Render(width int) []Line {
	head := e.Label
	if !e.Done {
		head += " (streaming)"
	}
	out := renderBody(e.base, width, head, StyleAccent)
	for _, line := range e.Lines {
		out = append(out, renderContinuation(e.base, width, line, StyleMuted)...)
	}
	return out
}

// DiffEntry renders a proposed or applied file write.
type DiffEntry struct {
	base
	Path string
	Diff string
}

// NewDiffEntry records a file write at t. diff is pre-rendered by the caller
// so this entry stays a pure layout concern.
func NewDiffEntry(t time.Time, path, diff string) *DiffEntry {
	return &DiffEntry{base: base{kind: KindDiff, at: t}, Path: path, Diff: diff}
}

func (e *DiffEntry) Render(width int) []Line {
	out := renderBody(e.base, width, e.Path, StyleStrong)
	for _, line := range strings.Split(strings.TrimRight(e.Diff, "\n"), "\n") {
		style := StylePlain
		switch {
		case strings.HasPrefix(line, "+"):
			style = StyleGood
		case strings.HasPrefix(line, "-"):
			style = StyleBad
		}
		out = append(out, renderContinuation(e.base, width, line, style)...)
	}
	return out
}

// Decision is how an approval resolved. There are two grant tiers, not three:
// internal/agent's ConfirmAlways is scoped to the running process, so
// "session" is the widest grant this architecture offers.
type Decision string

const (
	DecisionOnce    Decision = "once"
	DecisionSession Decision = "session"
	DecisionDenied  Decision = "denied"
	DecisionBlocked Decision = "blocked" // refused by safety, never offered
)

// ApprovalEntry is the settled record of an approval. The live prompt is an
// overlay; this is what it leaves behind in the transcript.
type ApprovalEntry struct {
	base
	Tool     string
	Target   string
	Score    int
	Reason   string
	Decision Decision
}

// NewApprovalEntry records a resolved approval at t.
func NewApprovalEntry(t time.Time, tool, target string, score int, reason string, d Decision) *ApprovalEntry {
	return &ApprovalEntry{
		base:     base{kind: KindApproval, at: t},
		Tool:     tool,
		Target:   target,
		Score:    score,
		Reason:   reason,
		Decision: d,
	}
}

func (e *ApprovalEntry) Render(width int) []Line {
	head := fmt.Sprintf("%s %s", e.Tool, e.Decision)
	if e.Target != "" {
		head = fmt.Sprintf("%s on %s %s", e.Tool, e.Target, e.Decision)
	}

	style := StyleGood
	if e.Decision == DecisionDenied || e.Decision == DecisionBlocked {
		style = StyleBad
	}
	out := renderBody(e.base, width, head, style)

	// The score is always rendered numerically with its reason: a binary
	// pass/fail would hide the risky-but-allowed band below the hard block.
	detail := fmt.Sprintf("safety %d/100", e.Score)
	if e.Reason != "" {
		detail += " - " + e.Reason
	}
	return append(out, renderContinuation(e.base, width, detail, StyleMuted)...)
}

// NoticeEntry is a runtime or lifecycle event surfaced to the operator.
type NoticeEntry struct {
	base
	Text string
}

// NewNoticeEntry records a runtime event at t.
func NewNoticeEntry(t time.Time, text string) *NoticeEntry {
	return &NoticeEntry{base: base{kind: KindNotice, at: t}, Text: text}
}

func (e *NoticeEntry) Render(width int) []Line {
	return renderBody(e.base, width, e.Text, StyleMuted)
}

// ErrorEntry is a failure surfaced to the operator.
type ErrorEntry struct {
	base
	Text string
}

// NewErrorEntry records a failure at t.
func NewErrorEntry(t time.Time, text string) *ErrorEntry {
	return &ErrorEntry{base: base{kind: KindError, at: t}, Text: text}
}

func (e *ErrorEntry) Render(width int) []Line {
	return renderBody(e.base, width, e.Text, StyleBad)
}
