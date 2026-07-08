package agent

import (
	"testing"
)

func TestParseAutonomyMode(t *testing.T) {
	cases := []struct {
		in   string
		want AutonomyMode
		err  bool
	}{
		{"default", AutonomyDefault, false},
		{"edit", AutonomyEdit, false},
		{"full", AutonomyFull, false},
		{"", AutonomyDefault, false},
		{"bogus", AutonomyDefault, true},
	}
	for _, c := range cases {
		got, err := ParseAutonomyMode(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseAutonomyMode(%q): expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseAutonomyMode(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseAutonomyMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsFileEditTool(t *testing.T) {
	yes := []string{"write_file", "edit_file", "multi_edit"}
	no := []string{"run_shell", "read_file", "run_on_node", "spawn_subagent", "axis_run_task"}
	for _, n := range yes {
		if !isFileEditTool(n) {
			t.Errorf("isFileEditTool(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if isFileEditTool(n) {
			t.Errorf("isFileEditTool(%q) = true, want false", n)
		}
	}
}

// recorderConfirm is a fallback that records its call and returns ConfirmNo so
// we can detect when the autonomy policy delegated to the fallback.
type recorderConfirm struct {
	called   bool
	lastTool string
}

func (r *recorderConfirm) confirm(toolName, description string, score int) ConfirmResult {
	r.called = true
	r.lastTool = toolName
	return ConfirmNo
}

func TestAutonomyDefaultDelegatesMutatingToFallback(t *testing.T) {
	rec := &recorderConfirm{}
	fn := autonomyConfirm(AutonomyDefault, rec.confirm)
	// read-only always auto-approved
	if got := fn("read_file", "x", 0); got != ConfirmYes {
		t.Fatalf("read-only should auto-approve, got %v", got)
	}
	if rec.called {
		t.Fatalf("read-only should not hit fallback")
	}
	// file edit delegates to fallback in default mode
	rec.called = false
	if got := fn("edit_file", "x", 0); got != ConfirmNo {
		t.Fatalf("default mode edit_file should delegate to fallback (ConfirmNo), got %v", got)
	}
	if !rec.called {
		t.Fatalf("default mode should call fallback for edit_file")
	}
	// shell delegates too
	rec.called = false
	if got := fn("run_shell", "x", 50); got != ConfirmNo {
		t.Fatalf("default mode run_shell should delegate, got %v", got)
	}
	if !rec.called {
		t.Fatalf("default mode should call fallback for run_shell")
	}
}

func TestAutonomyEditAutoApprovesFileEdits(t *testing.T) {
	rec := &recorderConfirm{}
	fn := autonomyConfirm(AutonomyEdit, rec.confirm)
	// file edits auto-approved, no fallback
	if got := fn("edit_file", "x", 0); got != ConfirmYes {
		t.Fatalf("edit mode edit_file should auto-approve, got %v", got)
	}
	if rec.called {
		t.Fatalf("edit mode edit_file should not hit fallback")
	}
	if got := fn("multi_edit", "x", 0); got != ConfirmYes {
		t.Fatalf("edit mode multi_edit should auto-approve, got %v", got)
	}
	// shell low-risk auto-approved
	rec.called = false
	if got := fn("run_shell", "x", 50); got != ConfirmYes {
		t.Fatalf("edit mode low-risk shell should auto-approve, got %v", got)
	}
	if rec.called {
		t.Fatalf("edit mode low-risk shell should not hit fallback")
	}
	// shell high-risk delegates to fallback
	rec.called = false
	if got := fn("run_shell", "x", 75); got != ConfirmNo {
		t.Fatalf("edit mode high-risk shell should delegate (ConfirmNo), got %v", got)
	}
	if !rec.called {
		t.Fatalf("edit mode high-risk shell should hit fallback")
	}
}

func TestAutonomyFullAutoApprovesAllButSafetyBlocked(t *testing.T) {
	rec := &recorderConfirm{}
	fn := autonomyConfirm(AutonomyFull, rec.confirm)
	// file edit auto-approved
	if got := fn("edit_file", "x", 0); got != ConfirmYes {
		t.Fatalf("full mode edit_file should auto-approve, got %v", got)
	}
	// shell low-risk auto-approved
	rec.called = false
	if got := fn("run_shell", "x", 50); got != ConfirmYes {
		t.Fatalf("full mode low-risk shell should auto-approve, got %v", got)
	}
	if rec.called {
		t.Fatalf("full mode low-risk shell should not hit fallback")
	}
	// shell just under safety threshold auto-approved
	rec.called = false
	if got := fn("run_shell", "x", 79); got != ConfirmYes {
		t.Fatalf("full mode score<80 shell should auto-approve, got %v", got)
	}
	// shell safety-blocked (>=80) delegates to fallback
	rec.called = false
	if got := fn("run_shell", "x", 85); got != ConfirmNo {
		t.Fatalf("full mode safety-blocked shell should delegate (ConfirmNo), got %v", got)
	}
	if !rec.called {
		t.Fatalf("full mode safety-blocked shell should hit fallback")
	}
	// remote execution safety-blocked delegates too
	rec.called = false
	if got := fn("run_on_node", "x", 80); got != ConfirmNo {
		t.Fatalf("full mode safety-blocked run_on_node should delegate, got %v", got)
	}
	if !rec.called {
		t.Fatalf("full mode safety-blocked run_on_node should hit fallback")
	}
}

func TestAutonomyReadOnlyAlwaysApproved(t *testing.T) {
	rec := &recorderConfirm{}
	for _, mode := range []AutonomyMode{AutonomyDefault, AutonomyEdit, AutonomyFull} {
		rec.called = false
		fn := autonomyConfirm(mode, rec.confirm)
		if got := fn("grep_search", "x", 0); got != ConfirmYes {
			t.Fatalf("mode %v: read-only should auto-approve, got %v", mode, got)
		}
		if rec.called {
			t.Fatalf("mode %v: read-only should not hit fallback", mode)
		}
	}
}
