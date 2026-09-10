package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
)

func TestPipedObserverBadges(t *testing.T) {
	var buf bytes.Buffer
	o := &pipedToolObserver{w: &buf}

	o.ToolCalled("c1", "axis_facts", `{"node":"cranium"}`)
	o.ToolSucceeded("c1", "axis_facts", "5 nodes ok", 1843, 124*time.Millisecond)
	o.ToolFailed("c2", "remote_grep", errors.New("dial timeout"), 2*time.Second)
	o.ShellExecuting("c3", "nixos", "", "uptime")
	o.ToolSkipped("c4", "bash", "dry-run")

	got := buf.String()
	for _, want := range []string{
		"→ axis_facts {\"node\":\"cranium\"}",
		"✓ axis_facts (124ms, 1843 bytes)",
		"✗ remote_grep (dial timeout, 2s elapsed)",
		"  ▶ nixos: uptime",
		"  [dry-run] bash skipped: dry-run",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("badge missing %q:\n%s", want, got)
		}
	}
}

func TestPipedObserverVerboseGatesTurnEvents(t *testing.T) {
	var quiet, loud bytes.Buffer
	(&pipedToolObserver{w: &quiet}).TurnStarted(1, 25)
	o := &pipedToolObserver{w: &loud, verbose: true}
	o.TurnStarted(1, 25)
	o.CompactionSkipped(errors.New("budget"))
	o.MaxTurnsReached(25)

	if strings.Contains(quiet.String(), "turn") {
		t.Fatalf("non-verbose observer leaked turn events: %q", quiet.String())
	}
	for _, want := range []string{"turn 1/25", "compaction skipped: budget", "stopped at the 25-turn ceiling"} {
		if !strings.Contains(loud.String(), want) {
			t.Fatalf("verbose observer missing %q:\n%s", want, loud.String())
		}
	}
}

func TestPipedObserverClipsLongArgs(t *testing.T) {
	var buf bytes.Buffer
	o := &pipedToolObserver{w: &buf}
	o.ToolCalled("c1", "write_file", strings.Repeat("x", 200))

	want := "→ write_file " + strings.Repeat("x", 79) + "…\n"
	if got := buf.String(); got != want {
		t.Fatalf("long args not clipped exactly:\ngot  %q\nwant %q", got, want)
	}
}

func TestPipedObserverLocalTargetDefault(t *testing.T) {
	var buf bytes.Buffer
	o := &pipedToolObserver{w: &buf}
	o.ShellExecuting("c1", "", "", "uptime")
	if got := buf.String(); !strings.Contains(got, "  ▶ local: uptime") {
		t.Fatalf("default target missing:\n%s", got)
	}
}

func TestPipedObserverBlankArgsNoTrailingSpace(t *testing.T) {
	var buf bytes.Buffer
	o := &pipedToolObserver{w: &buf}
	o.ToolCalled("c1", "axis_facts", "   ")
	if got := buf.String(); strings.HasSuffix(got, "axis_facts \n") || strings.Contains(got, "axis_facts  ") {
		t.Fatalf("whitespace-only args left a trailing space:\n%q", got)
	}
}

func TestClipLineEdges(t *testing.T) {
	if got := clipLine("abc", 0); got != "" {
		t.Fatalf("n=0 → %q, want empty", got)
	}
	if got := clipLine("abc", 3); got != "abc" {
		t.Fatalf("exact-length → %q, want unchanged", got)
	}
	if got := clipLine("abcd", 3); got != "ab…" {
		t.Fatalf("overflow → %q, want ab…", got)
	}
}

func TestPipedObserverQuietStillPrintsCeiling(t *testing.T) {
	var buf bytes.Buffer
	(&pipedToolObserver{w: &buf}).MaxTurnsReached(25)
	if !strings.Contains(buf.String(), "25-turn ceiling") {
		t.Fatal("the turn-ceiling notice must print even when quiet")
	}
}

func TestPipedConfirmDeniesWithNotice(t *testing.T) {
	var buf bytes.Buffer
	confirm := pipedConfirm(&buf)
	if confirm("run_shell", "rm -rf /", 90) != agent.ConfirmNo {
		t.Fatal("piped confirm must deny")
	}
	if confirm("read_file", "read config", 10) != agent.ConfirmNo {
		t.Fatal("piped confirm must deny regardless of score — the approval flags compose on top")
	}
	if !strings.Contains(buf.String(), "not an interactive session") {
		t.Fatalf("denial notice missing:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "--auto-approve") || !strings.Contains(buf.String(), "--autonomy full") {
		t.Fatalf("notice must point at the escape hatches:\n%s", buf.String())
	}
}

func TestObserverForPipedMode(t *testing.T) {
	if observerForPipedMode(false, nil, false) != nil {
		t.Fatal("non-piped mode must keep the nil-observer stdout fallback")
	}
	o := observerForPipedMode(true, errWriterForTest{}, true)
	if _, ok := o.(*pipedToolObserver); !ok {
		t.Fatalf("piped mode must produce a pipedToolObserver, got %T", o)
	}
}

type errWriterForTest struct{}

func (errWriterForTest) Write(p []byte) (int, error) { return len(p), nil }

func TestConfirmForPipedMode(t *testing.T) {
	if confirmForPipedMode(false, nil) != nil {
		t.Fatal("non-piped mode must keep the agent's default StdinConfirm")
	}
	c := confirmForPipedMode(true, errWriterForTest{})
	if c == nil {
		t.Fatal("piped mode must produce the fail-closed confirm")
	}
	var buf bytes.Buffer
	if pipedConfirm(&buf)("t", "d", 50) != agent.ConfirmNo {
		t.Fatal("pipedConfirm must deny")
	}
}
