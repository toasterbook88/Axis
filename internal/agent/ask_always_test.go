package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

// Full autonomy asks before a command a specific safety rule flags, through
// the real shell gate, and keeps auto-approving unknown commands.
// See docs/superpowers/specs/2026-11-05-full-autonomy-named-ask-design.md.

type askRecorder struct {
	asked  []string
	answer ConfirmResult
}

func (r *askRecorder) confirm(_, desc string, _ int) ConfirmResult {
	r.asked = append(r.asked, desc)
	return r.answer
}

func fullAutonomyShellAgent(t *testing.T, rec *askRecorder) (*Agent, *[]string) {
	t.Helper()
	var ran []string
	a := New(Config{
		Output:      io.Discard,
		Confirm:     rec.confirm,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
		RunShell: func(_ context.Context, cmd string) (string, error) {
			ran = append(ran, cmd)
			return "ok", nil
		},
	})
	a.SetAutonomy(AutonomyFull)
	return a, &ran
}

func runShellCmd(a *Agent, cmd string) error {
	args, _ := json.Marshal(shellArgs{Command: cmd})
	_, err := a.dispatchShell(context.Background(), args)
	return err
}

func TestFullAutonomyShellAsksForSpecificRisks(t *testing.T) {
	for _, cmd := range []string{
		"git push origin main",
		"git push origin main > /dev/null",
		"ssh -o StrictHostKeyChecking=no h true",
		"cd node_modules/x && git checkout v1",
	} {
		rec := &askRecorder{answer: ConfirmNo}
		a, ran := fullAutonomyShellAgent(t, rec)
		if err := runShellCmd(a, cmd); err == nil || len(rec.asked) != 1 || len(*ran) != 0 {
			t.Errorf("%q: err %v, asked %d, ran %d; want declined after one ask", cmd, err, len(rec.asked), len(*ran))
		}
	}
	for _, cmd := range []string{"foo --bar", "ls > /dev/null", "ls"} {
		rec := &askRecorder{answer: ConfirmNo}
		a, ran := fullAutonomyShellAgent(t, rec)
		if err := runShellCmd(a, cmd); err != nil || len(rec.asked) != 0 || len(*ran) != 1 {
			t.Errorf("%q: err %v, asked %d, ran %d; want run without asking", cmd, err, len(rec.asked), len(*ran))
		}
	}
}

// runTaskAgent is a full-autonomy agent over a minimal guarded-execution
// fixture (one complete local node), as in TestRunTaskConfirmDoesNotHoldDispatchMu.
func runTaskAgent(rec *askRecorder) (*Agent, *int) {
	view := &RuntimeView{
		Config: &config.Config{Nodes: []config.NodeConfig{{Name: "local", Hostname: "localhost", SSHUser: "me"}}},
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{{
				Name: "local", Hostname: "localhost", Status: models.StatusComplete,
				Resources: &models.Resources{RAMTotalMB: 8192, RAMFreeMB: 4096, Pressure: "low", CPUCores: 8},
			}},
			Summary: models.ClusterSummary{TotalNodes: 1, ReachableNodes: 1},
		},
		State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{},
	}
	runs := 0
	a := &Agent{
		safety:      func(string) (bool, string, int) { return true, "", 0 },
		output:      io.Discard,
		toolContext: NewToolContext(view, nil),
		runTask: func(context.Context, execution.PreparedExecution) (string, error) {
			runs++
			return "ok", nil
		},
	}
	a.SetConfirm(rec.confirm)
	a.SetAutonomy(AutonomyFull)
	return a, &runs
}

// axis_run_task asks for a specific risk under full autonomy, shows the
// rule's reason, and asks every time ("always" has no session effect there).
func TestFullAutonomyRunTaskAsksForSpecificRisks(t *testing.T) {
	rec := &askRecorder{answer: ConfirmAlways}
	a, runs := runTaskAgent(rec)
	for i := 0; i < 2; i++ {
		if _, err := a.dispatchRunTask(context.Background(), json.RawMessage(`{"description":"git push origin main"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.asked) != 2 || *runs != 2 {
		t.Fatalf("asked %d, ran %d; want an ask before each of two runs", len(rec.asked), *runs)
	}
	if strings.Count(rec.asked[0], "git-push") != 1 || !strings.Contains(rec.asked[0], "pushes") {
		t.Fatalf("confirm text %q should show the rule and its description once", rec.asked[0])
	}

	// The two logged shapes ask; ls with or without a null redirect does not.
	for _, cmd := range []string{"ssh -o StrictHostKeyChecking=no h true", "cd node_modules/x && git checkout v1"} {
		r := &askRecorder{answer: ConfirmNo}
		c, _ := runTaskAgent(r)
		_, _ = c.dispatchRunTask(context.Background(), json.RawMessage(mustJSON(t, map[string]any{"description": cmd})))
		if len(r.asked) != 1 {
			t.Errorf("%q through axis_run_task asked %d times, want 1", cmd, len(r.asked))
		}
	}
	for _, cmd := range []string{"foo --bar", "ls", "ls > /dev/null"} {
		plain := &askRecorder{answer: ConfirmNo}
		b, plainRuns := runTaskAgent(plain)
		if _, err := b.dispatchRunTask(context.Background(), json.RawMessage(mustJSON(t, map[string]any{"description": cmd}))); err != nil || len(plain.asked) != 0 || *plainRuns != 1 {
			t.Errorf("%q: err %v, asked %d, ran %d; want run without asking", cmd, err, len(plain.asked), *plainRuns)
		}
	}
}

// Edit and default modes keep asking at 70, as before.
func TestEditAndDefaultModesAskAtSeventy(t *testing.T) {
	for _, mode := range []AutonomyMode{AutonomyEdit, AutonomyDefault} {
		rec := &recorderConfirm{}
		if got := autonomyConfirm(mode, rec.confirm)("run_shell", "x", 70); got != ConfirmNo || !rec.called {
			t.Errorf("%s mode at score 70: %v (fallback %v), want the fallback", mode, got, rec.called)
		}
	}
}

// "Always" approves run_shell for the session, including later specific
// risks: the operator's explicit choice.
func TestFullAutonomySessionAlwaysCoversLaterAsks(t *testing.T) {
	rec := &askRecorder{answer: ConfirmAlways}
	a, ran := fullAutonomyShellAgent(t, rec)
	for i := 0; i < 2; i++ {
		if err := runShellCmd(a, "git push origin main"); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.asked) != 1 || len(*ran) != 2 {
		t.Fatalf("asked %d, ran %d; want one ask and two runs", len(rec.asked), len(*ran))
	}
}
