package agent

import (
	"io"
	"sync"
	"testing"
)

// A surface that owns the terminal installs its own confirm via SetConfirm.
// Changing autonomy mode afterwards must re-wrap THAT confirm, not fall back
// to StdinConfirm (which would corrupt a raw-mode input loop).
func TestSetAutonomyPreservesInstalledConfirm(t *testing.T) {
	calls := 0
	installed := func(_, _ string, _ int) ConfirmResult {
		calls++
		return ConfirmNo
	}

	a := New(Config{Output: io.Discard})
	a.SetConfirm(installed)
	a.SetAutonomy(AutonomyFull)

	// Safety score below the full-autonomy threshold auto-approves without
	// reaching the base confirm; a mutating tool at high score must reach it.
	if got := a.confirm("read_file", "read", 0); got != ConfirmYes {
		t.Fatalf("full autonomy should auto-approve low-score reads, got %v", got)
	}
	if got := a.confirm("run_shell", "rm -rf /", 95); got != ConfirmNo {
		t.Fatalf("expected installed confirm to be consulted, got %v", got)
	}
	if calls != 1 {
		t.Fatalf("installed confirm called %d times, want 1", calls)
	}

	// Switching back to default unwraps to the installed confirm directly.
	a.SetAutonomy(AutonomyDefault)
	if got := a.confirm("write_file", "edit file", 10); got != ConfirmNo || calls != 2 {
		t.Fatalf("default mode should consult installed confirm, got %v calls=%d", got, calls)
	}
}

func TestWrapConfirmDefaultMode(t *testing.T) {
	a := New(Config{Output: io.Discard})
	base := func(_, _ string, _ int) ConfirmResult { return ConfirmNever }
	if got := a.wrapConfirm(base)("x", "y", 0); got != ConfirmNever {
		t.Fatalf("wrapConfirm in default mode must return the base result, got %v", got)
	}
}

// Concurrent SetConfirm/SetAutonomy (operator surface) against Autonomy() and
// confirm reads (dispatch path) must not race: all of them synchronize on
// dispatchMu.
func TestConfirmFieldsConcurrentAccess(t *testing.T) {
	a := New(Config{Output: io.Discard})
	fn := func(_, _ string, _ int) ConfirmResult { return ConfirmYes }

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				switch (i + j) % 4 {
				case 0:
					a.SetConfirm(fn)
				case 1:
					a.SetAutonomy(AutonomyEdit)
				case 2:
					_ = a.Autonomy()
				case 3:
					a.dispatchMu.Lock()
					_ = a.confirm("read_file", "read", 0)
					a.dispatchMu.Unlock()
				}
			}
		}(i)
	}
	wg.Wait()
}
