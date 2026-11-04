package console

import "testing"

func TestSpinnerFrame(t *testing.T) {
	want := []string{"─", "╲", "│", "╱"}
	for i, w := range want {
		if got := spinnerFrame(i, true); got != w {
			t.Fatalf("spinnerFrame(%d,true)=%q want %q", i, got, w)
		}
	}
	if got := spinnerFrame(5, false); got != "\\" {
		t.Fatalf("spinnerFrame(5,false)=%q want backslash", got)
	}
	if got := spinnerFrame(-1, false); got != "/" {
		t.Fatalf("spinnerFrame(-1,false)=%q want slash", got)
	}
}

func TestEnsureTick_SingleChain(t *testing.T) {
	m := NewModel(Options{})
	if m.ensureTick() == nil {
		t.Fatal("first call must start a tick")
	}
	if m.ensureTick() != nil {
		t.Fatal("second call must not start another chain")
	}
}

func TestSpinnerTick_StopsWhenIdleAndClearsTicking(t *testing.T) {
	m := NewModel(Options{})
	m.state = turnRunning
	m.ensureTick()
	m.state = turnIdle
	out, cmd := m.Update(spinnerTickMsg{})
	if cmd != nil {
		t.Fatal("idle tick must not schedule another")
	}
	if out.(Model).ticking {
		t.Fatal("ticking must clear when the chain ends")
	}
	got := out.(Model)
	if got.ensureTick() == nil {
		t.Fatal("a new chain must be startable after the old one ended")
	}
}

func TestSpinnerTick_ContinuesWhileAnimating(t *testing.T) {
	m := NewModel(Options{})
	m.state = turnRunning
	m.ensureTick()
	out, cmd := m.Update(spinnerTickMsg{})
	if cmd == nil || !out.(Model).ticking {
		t.Fatal("running turn must keep exactly one chain alive")
	}
}
