package console

import (
	"fmt"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/ui"
)

func joinText(lines []Line) string { return strings.Join(PlainAll(lines), "\n") }

func TestPickTier(t *testing.T) {
	short := func(startTier) int { return 10 }
	cases := []struct {
		w, h int
		want startTier
	}{
		{120, 40, tierFull}, {72, 16, tierFull}, {71, 40, tierMedium},
		{44, 16, tierMedium}, {43, 40, tierCompact}, {120, 15, tierCompact},
		{80, 0, tierFull}, {30, 0, tierCompact}, {0, 0, tierCompact},
	}
	for _, c := range cases {
		if got := pickTier(c.w, c.h, short); got != c.want {
			t.Errorf("pickTier(%d,%d)=%v want %v", c.w, c.h, got, c.want)
		}
	}
	// A tier taller than the terminal falls to the next one down.
	rows := func(tier startTier) int { return map[startTier]int{tierFull: 20, tierMedium: 17, tierCompact: 3}[tier] }
	for _, c := range []struct {
		w, h int
		want startTier
	}{
		{120, 20, tierFull}, {120, 19, tierMedium}, {120, 17, tierMedium}, {120, 16, tierCompact},
		{60, 17, tierMedium}, {60, 16, tierCompact}, {120, 0, tierFull},
	} {
		if got := pickTier(c.w, c.h, rows); got != c.want {
			t.Errorf("pickTier(%d,%d) with tall tiers=%v want %v", c.w, c.h, got, c.want)
		}
	}
}

// The splash never needs more rows than the terminal has, except the compact
// floor, which is as small as the screen gets.
func TestHeroScreen_FitsHeight(t *testing.T) {
	var status []StatusLine
	for i := 0; i < 6; i++ {
		status = append(status, StatusLine{Key: fmt.Sprint(i), State: StatusInfo, Text: "status"})
	}
	for _, unicode := range []bool{true, false} {
		for _, w := range []int{50, 100} {
			for h := 1; h <= 40; h++ {
				lines := HeroScreen{}.Render(w, h, StartState{Version: "0.19.6", Unicode: unicode, Status: status})
				if len(lines) > h && len(lines) > 3 {
					t.Errorf("unicode=%v %dx%d: splash is %d rows", unicode, w, h, len(lines))
				}
			}
		}
	}
}

func TestHeroScreen_Full(t *testing.T) {
	lines := HeroScreen{}.Render(100, 40, StartState{Version: "0.19.6", Unicode: true})
	plain := PlainAll(lines)
	all := strings.Join(plain, "\n")
	for _, want := range []string{
		"agent · v0.19.6",
		"Tips for getting started:",
		"1. Ask about the fleet, models, or placement.",
		"2. /help for commands, /model to switch models.",
		"3. ! runs a shell command, @ completes a node name.",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	logo := ui.LogoLines(ui.ColorNone)
	for i := 0; i < 5; i++ {
		if lines[i].Text != logo[i] {
			t.Errorf("row %d = %q want %q", i, lines[i].Text, logo[i])
		}
	}
	if lines[5].Text != logo[5]+"  agent · v0.19.6" {
		t.Errorf("row 6 = %q", lines[5].Text)
	}
}

func TestHeroScreen_Medium(t *testing.T) {
	all := joinText(HeroScreen{}.Render(60, 40, StartState{Version: "0.19.6", Unicode: true}))
	if !strings.Contains(all, "2. /help · /model") || !strings.Contains(all, "1. Ask about the fleet or models.") {
		t.Errorf("medium tips missing:\n%s", all)
	}
	if strings.Contains(all, "Tips for getting started:") {
		t.Errorf("medium must not carry full tips")
	}
	if !strings.Contains(all, "agent · v0.19.6") {
		t.Errorf("medium needs version line")
	}
}

func TestHeroScreen_Compact(t *testing.T) {
	lines := HeroScreen{}.Render(40, 40, StartState{Version: "0.19.6", Unicode: true})
	if lines[0].Text != "▲ AXIS v0.19.6" {
		t.Errorf("first = %q", lines[0].Text)
	}
	if lines[1].Text != "/help for commands" {
		t.Errorf("second = %q", lines[1].Text)
	}
	if strings.Contains(joinText(lines), "█") {
		t.Error("compact must not draw the logo")
	}
	a := HeroScreen{}.Render(40, 40, StartState{Version: "0.19.6", Unicode: false})
	if a[0].Text != "^ AXIS v0.19.6" {
		t.Errorf("ascii first = %q", a[0].Text)
	}
}

func TestHeroScreen_CompactStatusPick(t *testing.T) {
	st := []StatusLine{
		{Key: "a", State: StatusRunning, Text: "connecting MCP…"},
		{Key: "b", State: StatusFailed, Text: "boom"},
		{Key: "c", State: StatusFailed, Text: "second"},
	}
	all := joinText(HeroScreen{}.Render(40, 10, StartState{Version: "1", Unicode: true, Frame: "─", Status: st}))
	if !strings.Contains(all, "boom") || strings.Contains(all, "second") || strings.Contains(all, "connecting") {
		t.Errorf("want only first failed:\n%s", all)
	}
	all = joinText(HeroScreen{}.Render(40, 10, StartState{Version: "1", Unicode: true, Frame: "─", Status: st[:1]}))
	if !strings.Contains(all, "─ connecting MCP…") {
		t.Errorf("want running line:\n%s", all)
	}
	all = joinText(HeroScreen{}.Render(40, 10, StartState{Version: "1", Unicode: true, Status: []StatusLine{{State: StatusDone, Text: "x"}}}))
	if strings.Contains(all, "x") && strings.Contains(all, "✓") {
		t.Errorf("compact shows only failed/running:\n%s", all)
	}
}

func TestHeroScreen_ASCIIFull(t *testing.T) {
	st := []StatusLine{
		{State: StatusRunning, Text: "finding a model…"},
		{State: StatusDone, Text: "example-model ready"},
		{State: StatusInfo, Text: "advisory — x"},
	}
	for _, w := range []int{100, 60, 40} {
		lines := HeroScreen{}.Render(w, 40, StartState{Version: "0.19.6", Unicode: false, Frame: "-", Status: st})
		for i, l := range lines {
			for _, r := range l.Text {
				if r > 0x7E {
					t.Fatalf("w=%d row %d has non-ASCII %q: %q", w, i, r, l.Text)
				}
			}
			for _, r := range l.Painted {
				if r > 0x7E {
					t.Fatalf("w=%d row %d Painted non-ASCII %q", w, i, r)
				}
			}
		}
	}
	lines := HeroScreen{}.Render(100, 40, StartState{Version: "0.19.6", Unicode: false})
	logo := ui.ASCIILogoLines()
	for i := 0; i < 4; i++ {
		if lines[i].Text != logo[i] {
			t.Errorf("row %d = %q want %q", i, lines[i].Text, logo[i])
		}
	}
	if lines[4].Text != logo[4]+"  agent - v0.19.6" {
		t.Errorf("row 5 = %q", lines[4].Text)
	}
}

func TestAsciiFallback(t *testing.T) {
	got := asciiFallback("a·b…c—d✓e▲f❯g─h")
	if got != "a-b...c-doke^f>g-h" {
		t.Errorf("got %q", got)
	}
	if got := asciiFallback("模型"); got != "??" {
		t.Errorf("leftover non-ASCII should become ?, got %q", got)
	}
	if asciiFallback("plain") != "plain" || asciiFallback("") != "" {
		t.Error("plain passthrough")
	}
}

func TestHeroScreen_NeverExceedsWidth(t *testing.T) {
	long := strings.Repeat("x", 200)
	st := []StatusLine{
		{State: StatusRunning, Text: long},
		{State: StatusDone, Text: "模型模型模型模型模型模型模型模型模型 ready"},
		{State: StatusFailed, Text: long},
		{State: StatusInfo, Text: long},
	}
	for _, uni := range []bool{true, false} {
		for w := 0; w <= 140; w++ {
			for _, h := range []int{0, 10, 15, 16, 40} {
				for _, ver := range []string{"", "0.19.6", long} {
					lines := HeroScreen{}.Render(w, h, StartState{Version: ver, Unicode: uni, Frame: "╲", Status: st})
					for _, l := range lines {
						if l.Width() > max(w-1, 0) {
							t.Fatalf("w=%d h=%d uni=%v: width %d > %d: %q", w, h, uni, l.Width(), w-1, l.Text)
						}
						if strings.ContainsAny(l.Text, "\n\r\x1b") {
							t.Fatalf("control char in %q", l.Text)
						}
					}
				}
			}
		}
	}
}

func TestHeroScreen_NilStatusAndEmptyVersion(t *testing.T) {
	for _, w := range []int{80, 60, 30} {
		if len(HeroScreen{}.Render(w, 40, StartState{})) == 0 {
			t.Errorf("w=%d empty render", w)
		}
	}
}

func TestHeroScreen_UnknownHeight(t *testing.T) {
	lines := HeroScreen{}.Render(80, 0, StartState{Version: "0.19.6", Unicode: true})
	if !strings.Contains(joinText(lines), "Tips for getting started:") {
		t.Error("height 0 should render the full tier")
	}
}

func TestHeroScreen_StatusStates(t *testing.T) {
	st := []StatusLine{
		{State: StatusRunning, Text: "finding a model…"},
		{State: StatusDone, Text: "example-model ready"},
		{State: StatusFailed, Text: "MCP: 1 of 2 failed (node-a)"},
		{State: StatusInfo, Text: "no snapshot"},
	}
	all := joinText(HeroScreen{}.Render(100, 40, StartState{Version: "1", Unicode: true, Frame: "╲", Status: st}))
	for _, want := range []string{"╲ finding a model…", "✓ example-model ready", "MCP: 1 of 2 failed (node-a)", "no snapshot"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	all = joinText(HeroScreen{}.Render(100, 40, StartState{Version: "1", Unicode: false, Frame: "/", Status: st}))
	for _, want := range []string{"/ finding a model...", "ok example-model ready"} {
		if !strings.Contains(all, want) {
			t.Errorf("ascii missing %q in:\n%s", want, all)
		}
	}
}

func TestHeroScreen_NoColorHasNoEscapes(t *testing.T) {
	ui.Init(true)
	t.Cleanup(func() { ui.Init(false) })
	st := []StatusLine{{State: StatusDone, Text: "m ready"}, {State: StatusFailed, Text: "bad"}}
	for _, w := range []int{100, 60, 40} {
		out := PaintAll(HeroScreen{}.Render(w, 40, StartState{Version: "1", Unicode: true, Depth: ui.ColorNone, Status: st}))
		if strings.Contains(out, "\x1b[") {
			t.Errorf("w=%d escapes with color off: %q", w, out)
		}
	}
}

func TestHeroScreen_PaintedKeepsPlainMeasure(t *testing.T) {
	lines := HeroScreen{}.Render(100, 40, StartState{Version: "1", Unicode: true, Depth: ui.ColorTrue})
	if lines[0].Painted == "" || !strings.Contains(lines[0].Painted, "38;2;") {
		t.Errorf("logo row should be painted: %q", lines[0].Painted)
	}
	if Paint(lines[0]) != lines[0].Painted {
		t.Error("Paint must return Painted verbatim")
	}
	if strings.Contains(lines[0].Text, "\x1b") {
		t.Error("Text must stay plain")
	}
}

func TestHeroScreen_256HasNoTruecolor(t *testing.T) {
	st := []StatusLine{{State: StatusDone, Text: "m ready"}}
	for _, w := range []int{100, 60, 40} {
		out := PaintAll(HeroScreen{}.Render(w, 40, StartState{Version: "1", Unicode: true, Depth: ui.Color256, Status: st}))
		if strings.Contains(out, "38;2;") {
			t.Errorf("w=%d truecolor leaked at 256: %q", w, out)
		}
	}
}
