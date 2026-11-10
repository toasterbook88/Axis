package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var allBgTaskStates = []bgTaskState{bgTaskRunning, bgTaskSucceeded, bgTaskFailed}

// TestBgTaskTransitionTable checks every from -> to pair, legal and illegal.
func TestBgTaskTransitionTable(t *testing.T) {
	legal := map[[2]bgTaskState]bool{
		{bgTaskRunning, bgTaskSucceeded}: true,
		{bgTaskRunning, bgTaskFailed}:    true,
	}
	for _, from := range allBgTaskStates {
		for _, to := range allBgTaskStates {
			want := legal[[2]bgTaskState{from, to}]
			if got := from.canTransition(to); got != want {
				t.Errorf("canTransition(%s -> %s) = %v, want %v", from.checkLabel(), to.checkLabel(), got, want)
			}
		}
	}
}

// TestBgTaskFinishFirstOutcomeWins drives finish from every start state.
func TestBgTaskFinishFirstOutcomeWins(t *testing.T) {
	cases := []struct {
		name    string
		start   bgTaskState
		runErr  error
		wantOK  bool
		wantEnd bgTaskState
	}{
		{"running+nil", bgTaskRunning, nil, true, bgTaskSucceeded},
		{"running+err", bgTaskRunning, errors.New("boom"), true, bgTaskFailed},
		{"succeeded+nil", bgTaskSucceeded, nil, false, bgTaskSucceeded},
		{"succeeded+err", bgTaskSucceeded, errors.New("boom"), false, bgTaskSucceeded},
		{"failed+nil", bgTaskFailed, nil, false, bgTaskFailed},
		{"failed+err", bgTaskFailed, errors.New("boom"), false, bgTaskFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &backgroundTask{state: tc.start}
			if got := task.finish(tc.runErr); got != tc.wantOK {
				t.Fatalf("finish ok = %v, want %v", got, tc.wantOK)
			}
			if got := task.currentState(); got != tc.wantEnd {
				t.Fatalf("state = %s, want %s", got.checkLabel(), tc.wantEnd.checkLabel())
			}
		})
	}
}

// legacyLabels reproduces the pre-refactor done/failed derivation used by
// formatTaskStatus and listBackgroundTasks, as the behavior oracle.
func legacyLabels(done, failed bool) (check, list string) {
	running, errored := !done, failed && done
	check, list = "running", "running"
	if !running {
		if errored {
			check, list = "failed", "failed"
		} else {
			check, list = "completed", "done"
		}
	}
	return check, list
}

// TestBgTaskLabelsMatchLegacy proves the typed state reports the same words
// the old bool pair did for every reachable legacy combination.
func TestBgTaskLabelsMatchLegacy(t *testing.T) {
	cases := []struct {
		done, failed bool
		state        bgTaskState
	}{
		{false, false, bgTaskRunning},
		{true, false, bgTaskSucceeded},
		{true, true, bgTaskFailed},
	}
	for _, tc := range cases {
		wantCheck, wantList := legacyLabels(tc.done, tc.failed)
		if got := tc.state.checkLabel(); got != wantCheck {
			t.Errorf("checkLabel(done=%v failed=%v) = %q, want %q", tc.done, tc.failed, got, wantCheck)
		}
		if got := tc.state.listLabel(); got != wantList {
			t.Errorf("listLabel(done=%v failed=%v) = %q, want %q", tc.done, tc.failed, got, wantList)
		}
	}
	// The one illegal legacy combination (failed but not done) read as running.
	if c, l := legacyLabels(false, true); c != "running" || l != "running" {
		t.Fatalf("legacy failed-not-done = %q/%q, want running/running", c, l)
	}
}

// TestBgTaskEndToEndOutputUnchanged runs commands through the tools and
// checks the exact state words agents see. bg-1 succeeds; bg-2 exits 3 on the
// default runner, which returns a nil error for a non-zero exit, so it is
// reported as completed (pre-existing behavior, pinned here); bg-3 uses a
// runner that returns an error and is reported as failed.
func TestBgTaskEndToEndOutputUnchanged(t *testing.T) {
	a := newAsyncTestAgent(t)
	for _, cmd := range []string{`{"command":"echo ok-out"}`, `{"command":"echo bad-out; exit 3"}`} {
		if _, err := a.dispatchRunBackground(context.Background(), json.RawMessage(cmd)); err != nil {
			t.Fatalf("run %s: %v", cmd, err)
		}
	}
	waitAllBgTasks(t, a, "bg-1", "bg-2")
	a.SetRunShell(func(context.Context, string) (string, error) {
		return "err-out", errors.New("runner failed")
	})
	if _, err := a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"echo x"}`)); err != nil {
		t.Fatalf("run err: %v", err)
	}
	waitAllBgTasks(t, a, "bg-3")

	want := map[string]struct{ prefix, body string }{
		"bg-1": {"Task bg-1: completed\n", "ok-out"},
		"bg-2": {"Task bg-2: completed\n", "[exit error] exit status 3"},
		"bg-3": {"Task bg-3: failed\n", "[error: runner failed]"},
	}
	for id, w := range want {
		out, err := a.dispatchCheckTask(context.Background(), json.RawMessage(`{"id":"`+id+`"}`))
		if err != nil || !strings.HasPrefix(out, w.prefix) || !strings.Contains(out, w.body) {
			t.Fatalf("check %s = %q, %v", id, out, err)
		}
	}
	list := a.listBackgroundTasks()
	for _, frag := range []string{"bg-1  [done]", "bg-2  [done]", "bg-3  [failed]"} {
		if !strings.Contains(list, frag) {
			t.Fatalf("list missing %q: %q", frag, list)
		}
	}
}

func waitAllBgTasks(t *testing.T, a *Agent, ids ...string) {
	t.Helper()
	for _, id := range ids {
		task, ok := a.backgroundTasks.get(id)
		if !ok {
			t.Fatalf("missing %s", id)
		}
		deadline := time.Now().Add(5 * time.Second)
		for task.currentState() == bgTaskRunning {
			if time.Now().After(deadline) {
				t.Fatalf("%s still running", id)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
