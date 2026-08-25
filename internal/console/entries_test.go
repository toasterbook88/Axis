package console

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// at is a fixed clock so timestamped entries render deterministically.
var at = time.Date(2026, 8, 25, 21, 35, 0, 0, time.UTC)

// plain renders an entry as unstyled text, which is what golden assertions
// compare. Styling is Paint's job and is asserted separately.
func plain(e Entry, width int) []string { return PlainAll(e.Render(width)) }

// assertLines compares rendered output line-for-line.
func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("line count = %d, want %d\ngot:\n%s\nwant:\n%s",
			len(got), len(want), strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUserEntryRendersWithGutter(t *testing.T) {
	e := NewUserEntry(at, "which node has the most free VRAM?")
	assertLines(t, plain(e, 60), []string{
		"> which node has the most free VRAM?",
	})
}

func TestAgentEntryWrapsAndIndentsContinuations(t *testing.T) {
	e := NewAgentEntry(at, "node-a has 28 GB free of 48 GB and node-b has 16 GB free")
	got := plain(e, 30)

	want := []string{
		"* node-a has 28 GB free of 48",
		"  GB and node-b has 16 GB free",
	}
	assertLines(t, got, want)

	for _, line := range got {
		if visibleWidth(line) > 30 {
			t.Errorf("line %q exceeds width 30", line)
		}
	}
}

func TestConversationalEntriesCarryNoTimestamp(t *testing.T) {
	for _, e := range []Entry{
		NewUserEntry(at, "hello"),
		NewAgentEntry(at, "hello"),
		NewThinkingEntry(at, "hmm"),
		NewToolEntry(at, "call-1", "axis_status", ""),
	} {
		for _, line := range plain(e, 60) {
			if strings.Contains(line, "21:35:00") {
				t.Errorf("kind %s rendered a timestamp: %q", e.Kind(), line)
			}
		}
	}
}

func TestRuntimeEntriesCarryTimestamp(t *testing.T) {
	for _, e := range []Entry{
		NewNoticeEntry(at, "backend switched"),
		NewErrorEntry(at, "probe failed"),
		NewApprovalEntry(at, "bash", "node-a", 35, "", DecisionOnce),
	} {
		first := plain(e, 80)[0]
		if !strings.Contains(first, "21:35:00") {
			t.Errorf("kind %s missing timestamp: %q", e.Kind(), first)
		}
	}
}

func TestThinkingCollapsesByDefault(t *testing.T) {
	e := NewThinkingEntry(at, "one\ntwo\nthree\nfour")

	got := strings.Join(plain(e, 60), "\n")
	if !strings.Contains(got, "2 more lines") {
		t.Errorf("collapsed thinking did not summarize remainder:\n%s", got)
	}
	if strings.Contains(got, "four") {
		t.Errorf("collapsed thinking leaked hidden content:\n%s", got)
	}

	e.Expanded = true
	if !strings.Contains(strings.Join(plain(e, 60), "\n"), "four") {
		t.Error("expanded thinking did not reveal full text")
	}
}

func TestToolEntryRendersResultAndError(t *testing.T) {
	ok := NewToolEntry(at, "call-1", "axis_status", "--cached")
	ok.Result = "5 nodes"
	assertLines(t, plain(ok, 60), []string{
		"$ axis_status --cached",
		"  5 nodes",
	})

	failed := NewToolEntry(at, "call-2", "remote_grep", "")
	failed.Err = errors.New("dial timeout")
	assertLines(t, plain(failed, 60), []string{
		"$ remote_grep",
		"  error: dial timeout",
	})
}

func TestStreamEntryAppendsAndCloses(t *testing.T) {
	e := NewStreamEntry(at, "s1", "remote_tail_logs node-a")

	if !strings.Contains(plain(e, 60)[0], "(streaming)") {
		t.Error("open stream not marked streaming")
	}

	e.Append("line one")
	e.Append("line two")
	e.Close()

	got := plain(e, 60)
	if strings.Contains(got[0], "(streaming)") {
		t.Error("closed stream still marked streaming")
	}
	assertLines(t, got, []string{
		"| remote_tail_logs node-a",
		"  line one",
		"  line two",
	})
}

func TestStreamEntryDropsOldestBeyondLimit(t *testing.T) {
	e := NewStreamEntry(at, "s1", "tail")
	e.Limit = 3
	for _, line := range []string{"a", "b", "c", "d", "e"} {
		e.Append(line)
	}

	if len(e.Lines) != 3 {
		t.Fatalf("retained %d lines, want 3", len(e.Lines))
	}
	if e.Lines[0] != "c" {
		t.Errorf("oldest retained line = %q, want %q", e.Lines[0], "c")
	}
}

func TestApprovalAlwaysRendersNumericScore(t *testing.T) {
	// The 70-79 band is allowed but risky. A binary PASS/FAIL would hide it,
	// so the score is rendered numerically for every decision.
	e := NewApprovalEntry(at, "bash", "node-a", 74, "writes outside workspace", DecisionOnce)

	got := strings.Join(plain(e, 80), "\n")
	if !strings.Contains(got, "safety 74/100") {
		t.Errorf("numeric score missing:\n%s", got)
	}
	if !strings.Contains(got, "writes outside workspace") {
		t.Errorf("safety reason missing:\n%s", got)
	}
	if strings.Contains(got, "PASS") || strings.Contains(got, "FAIL") {
		t.Errorf("rendered a binary verdict:\n%s", got)
	}
}

func TestApprovalRendersTargetWhenPresent(t *testing.T) {
	with := NewApprovalEntry(at, "bash", "node-a", 10, "", DecisionOnce)
	if !strings.Contains(plain(with, 80)[0], "on node-a") {
		t.Error("target omitted from approval head")
	}

	without := NewApprovalEntry(at, "bash", "", 10, "", DecisionOnce)
	if strings.Contains(plain(without, 80)[0], " on ") {
		t.Error("empty target rendered a dangling preposition")
	}
}

func TestNarrowWidthNeverOverflows(t *testing.T) {
	entries := []Entry{
		NewUserEntry(at, "start a really quite long model on the compute node please"),
		NewAgentEntry(at, "supercalifragilisticexpialidocious antidisestablishmentarianism"),
		NewNoticeEntry(at, "backend switched to llama.cpp on node-a port 8000"),
		NewErrorEntry(at, "health check timed out after three seconds"),
	}

	// minWidth is the floor; anything narrower clamps rather than producing
	// single-character columns.
	for _, width := range []int{10, 20, 40, 80} {
		for _, e := range entries {
			for _, line := range e.Render(width) {
				if got := line.Width(); got > effectiveWidth(width) {
					t.Errorf("kind %s at width %d: line width %d exceeds %d: %q",
						e.Kind(), width, got, effectiveWidth(width), line.Plain())
				}
			}
		}
	}
}

func TestWrapSplitsWordsLongerThanColumn(t *testing.T) {
	got := wrap("aaaaaaaaaa", 4)
	assertLines(t, got, []string{"aaaa", "aaaa", "aa"})
}

func TestWrapPreservesBlankLines(t *testing.T) {
	assertLines(t, wrap("a\n\nb", 10), []string{"a", "", "b"})
}

func TestRenderIsDeterministic(t *testing.T) {
	e := NewAgentEntry(at, "node-a has 28 GB free of 48 GB across two devices")
	first := strings.Join(plain(e, 34), "\n")
	for i := range 3 {
		if got := strings.Join(plain(e, 34), "\n"); got != first {
			t.Fatalf("render %d differed:\n%s\nwant:\n%s", i, got, first)
		}
	}
}

func TestEveryKindHasAGutterMarker(t *testing.T) {
	kinds := []Kind{
		KindUser, KindAgent, KindThinking, KindTool,
		KindStream, KindDiff, KindApproval, KindNotice, KindError,
	}
	for _, k := range kinds {
		if gutter[k] == "" {
			t.Errorf("kind %s has no gutter marker", k)
		}
	}
	if len(gutter) != len(kinds) {
		t.Errorf("gutter has %d markers, want %d — a kind was added without a test",
			len(gutter), len(kinds))
	}
}

func TestWidthCountsTerminalCellsNotRunes(t *testing.T) {
	// A wide CJK glyph occupies two cells. Counting runes would let a line
	// overflow by its own width, which is the bug class the old TUI had.
	if got := visibleWidth("世界"); got != 4 {
		t.Errorf("visibleWidth(CJK) = %d, want 4", got)
	}
	// Escape sequences occupy no cells.
	if got := visibleWidth("\x1b[31mred\x1b[0m"); got != 3 {
		t.Errorf("visibleWidth(ANSI) = %d, want 3", got)
	}
}

func TestSplitNeverCutsAMultiByteRune(t *testing.T) {
	for _, width := range []int{1, 2, 3, 5} {
		for _, line := range wrap("日本語のテキストです", width) {
			if !utf8.ValidString(line) {
				t.Errorf("width %d produced invalid UTF-8: %q", width, line)
			}
		}
	}
}

func TestWideCharactersNeverOverflowTheColumn(t *testing.T) {
	for _, width := range []int{4, 6, 10} {
		for _, line := range wrap("世界世界世界世界", width) {
			if got := visibleWidth(line); got > width {
				t.Errorf("width %d: line %q occupies %d cells", width, line, got)
			}
		}
	}
}

func TestEntriesEmitNoEscapeSequences(t *testing.T) {
	// Entries are style-free by contract: ANSI enters only through Paint.
	// Without this, wrapping arithmetic would operate on invisible bytes.
	entries := []Entry{
		NewUserEntry(at, "hello"),
		NewAgentEntry(at, "hi"),
		NewToolEntry(at, "call-1", "axis_status", "--cached"),
		NewApprovalEntry(at, "bash", "node-a", 74, "risky", DecisionOnce),
		NewErrorEntry(at, "boom"),
	}
	for _, e := range entries {
		for _, line := range e.Render(80) {
			if strings.Contains(line.Plain(), "\x1b") {
				t.Errorf("kind %s emitted an escape sequence: %q", e.Kind(), line.Plain())
			}
		}
	}
}

func TestPaintAppliesStyleOnlyAtTheEnd(t *testing.T) {
	// Paint is a pure function of the line, so the same line always paints
	// the same way regardless of which entry produced it.
	l := Line{Gutter: "> ", Text: "hello", Style: StyleStrong}
	first := Paint(l)
	second := Paint(Line{Gutter: "> ", Text: "hello", Style: StyleStrong})
	if first != second {
		t.Errorf("Paint is not deterministic: %q vs %q", first, second)
	}
	if !strings.Contains(first, "hello") {
		t.Error("Paint dropped the text")
	}
}

func TestToolEntryCarriesCorrelationID(t *testing.T) {
	// Tools dispatch in parallel; without an id a completion cannot be
	// matched back to its call.
	e := NewToolEntry(at, "call-7", "axis_status", "")
	if e.ID != "call-7" {
		t.Errorf("ID = %q, want call-7", e.ID)
	}
}
