package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
)

// scriptedBackend replays responses in order and repeats the last one.
type scriptedBackend struct {
	mu    sync.Mutex
	steps []chat.Message
	err   error
	calls int
}

func (s *scriptedBackend) ChatStream(_ context.Context, _ []chat.Message, _ []chat.ToolDef, w io.Writer) (chat.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return chat.Message{}, s.err
	}
	i := s.calls - 1
	if i >= len(s.steps) {
		i = len(s.steps) - 1
	}
	if w != nil && s.steps[i].Content != "" {
		_, _ = io.WriteString(w, s.steps[i].Content)
	}
	return s.steps[i], nil
}

func toolCall(id, name, args string) chat.Message {
	return chat.Message{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{{
		ID: id, Function: chat.ToolCallFunction{Name: name, Arguments: json.RawMessage(args)},
	}}}
}

func answer(text string) chat.Message {
	return chat.Message{Role: chat.RoleAssistant, Content: text}
}

type pipedRun struct {
	err    error
	stdout string
	stderr string
}

// runPiped drives runOneShotPiped the way `axis agent -p` does: piped
// confirm with a recorder, and io.Discard as the stream for --format json.
func runPiped(t *testing.T, be agent.ChatBackend, maxTurns int, dryRun bool, format string) pipedRun {
	return runPipedMode(t, be, maxTurns, dryRun, format, agent.AutonomyDefault)
}

func runPipedMode(t *testing.T, be agent.ChatBackend, maxTurns int, dryRun bool, format string, mode agent.AutonomyMode) pipedRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rec := &pipedApprovals{}
	agentOut := io.Writer(&stdout)
	if format == "json" {
		agentOut = io.Discard
	}
	a := agent.New(agent.Config{
		Backend: be, MaxTurns: maxTurns, MaxTokens: 100000, DryRun: dryRun,
		Autonomy: mode, ToolContext: &agent.ToolContext{},
		Output:   agentOut,
		Observer: observerForPipedMode(true, &stderr, false),
		Confirm:  confirmForPipedMode(true, &stderr, rec),
	})
	err := runOneShotPiped(context.Background(), a, "check the cluster", pipedRunOptions{
		Out: &stdout, ErrW: &stderr, Timeout: 5 * time.Second, Format: format, Approvals: rec,
	})
	return pipedRun{err: err, stdout: stdout.String(), stderr: stderr.String()}
}

type agentDoc struct {
	SchemaVersion string         `json:"schema_version"`
	Command       string         `json:"command"`
	OK            bool           `json:"ok"`
	Status        string         `json:"status"`
	Data          pipedRunResult `json:"data"`
}

func decodeAgentDoc(t *testing.T, stdout string) agentDoc {
	t.Helper()
	var doc agentDoc
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout carries more than one document:\n%s", stdout)
	}
	if doc.SchemaVersion != machineOutputSchemaV1 || doc.Command != "agent" {
		t.Fatalf("envelope = %+v", doc)
	}
	return doc
}

func TestClassifyPipedRun(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name    string
		err     error
		stop    agent.RunStop
		pending int
		outcome string
		code    int
	}{
		{"answered", nil, agent.RunStopAnswered, 0, pipedOutcomeAnswered, 0},
		{"ceiling", nil, agent.RunStopTurnCeiling, 0, pipedOutcomeTurnCeiling, 8},
		{"needs input after an answer", nil, agent.RunStopAnswered, 1, pipedOutcomeNeedsInput, 9},
		{"needs input outranks ceiling", nil, agent.RunStopTurnCeiling, 2, pipedOutcomeNeedsInput, 9},
		{"error outranks everything", boom, agent.RunStopFailed, 3, pipedOutcomeFailed, 4},
	} {
		outcome, code := classifyPipedRun(tc.err, tc.stop, tc.pending)
		if outcome != tc.outcome || code != tc.code {
			t.Errorf("%s: got (%s, %d), want (%s, %d)", tc.name, outcome, code, tc.outcome, tc.code)
		}
	}
}

func TestPipedExitCodesAreDistinct(t *testing.T) {
	seen := map[int]string{}
	for name, code := range map[string]int{
		"ok": ExitOK, "generic": ExitErrGeneric, "config": ExitErrConfigLoad, "nofit": ExitErrNoNodesFit,
		"cmdfail": ExitErrCommandFail, "context": ExitErrContextWrite, "io": ExitErrIO,
		"unlisted": ExitErrModelUnlisted, "ceiling": ExitAgentTurnCeiling, "needs": ExitAgentNeedsInput,
	} {
		if prev, dup := seen[code]; dup {
			t.Fatalf("exit code %d used by %s and %s", code, prev, name)
		}
		seen[code] = name
	}
}

func TestPipedConfirmRecordsDenials(t *testing.T) {
	var buf bytes.Buffer
	rec := &pipedApprovals{}
	c := pipedConfirm(&buf, rec)
	c("run_shell", "make deploy", 40)
	c("run_shell", "git push", 75)
	c("consult_model", "ask seat-a", 0)
	got := rec.list()
	if len(got) != 3 {
		t.Fatalf("recorded %d denials, want 3: %+v", len(got), got)
	}
	want := []pendingApproval{
		{Tool: "run_shell", Request: "make deploy", RiskScore: 40, Clears: "--autonomy full"},
		{Tool: "run_shell", Request: "git push", RiskScore: 75, Clears: "interactive approval"},
		{Tool: "consult_model", Request: "ask seat-a", RiskScore: 0, Clears: "interactive approval"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("denial %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPipedRunAnsweredExitsZero(t *testing.T) {
	be := &scriptedBackend{steps: []chat.Message{answer("all 5 nodes healthy")}}
	r := runPiped(t, be, 3, false, "text")
	if r.err != nil || ExitCode(r.err) != 0 {
		t.Fatalf("answered run returned %v", r.err)
	}
	if !strings.Contains(r.stdout, "all 5 nodes healthy") {
		t.Fatalf("answer missing from stdout: %q", r.stdout)
	}
}

func TestPipedRunTurnCeilingExitsEight(t *testing.T) {
	be := &scriptedBackend{steps: []chat.Message{toolCall("c1", "cluster_status", `{}`)}}
	r := runPiped(t, be, 2, true, "text")
	if got := ExitCode(r.err); got != ExitAgentTurnCeiling {
		t.Fatalf("exit code = %d (%v), want %d", got, r.err, ExitAgentTurnCeiling)
	}
	if !strings.Contains(r.stderr, "stopped at the 2-turn ceiling") {
		t.Fatalf("ceiling notice missing from stderr:\n%s", r.stderr)
	}
	if !strings.Contains(r.err.Error(), "2-turn ceiling without a final answer") {
		t.Fatalf("error message = %q", r.err.Error())
	}
}

func TestPipedRunTurnCeilingJSON(t *testing.T) {
	be := &scriptedBackend{steps: []chat.Message{toolCall("c1", "cluster_status", `{}`)}}
	r := runPiped(t, be, 2, true, "json")
	if got := ExitCode(r.err); got != ExitAgentTurnCeiling {
		t.Fatalf("exit code = %d, want %d", got, ExitAgentTurnCeiling)
	}
	doc := decodeAgentDoc(t, r.stdout)
	if doc.OK || doc.Status != "turn_ceiling" {
		t.Fatalf("envelope ok=%v status=%q", doc.OK, doc.Status)
	}
	d := doc.Data
	if d.Outcome != "turn_ceiling" || d.ExitCode != 8 || d.Turns != 2 || d.MaxTurns != 2 {
		t.Fatalf("data = %+v", d)
	}
	if d.PendingApprovals == nil || len(d.PendingApprovals) != 0 {
		t.Fatalf("pending_approvals must be an empty array, got %#v", d.PendingApprovals)
	}
	if !strings.Contains(r.stdout, `"pending_approvals":[]`) {
		t.Fatalf("pending_approvals not serialized as []: %s", r.stdout)
	}
}

func TestPipedRunAnsweredJSONKeepsStdoutClean(t *testing.T) {
	be := &scriptedBackend{steps: []chat.Message{answer("all 5 nodes healthy")}}
	r := runPiped(t, be, 3, false, "json")
	if r.err != nil {
		t.Fatalf("answered run returned %v", r.err)
	}
	doc := decodeAgentDoc(t, r.stdout) // fails if the stream leaked onto stdout
	if !doc.OK || doc.Status != "answered" || doc.Data.FinalText != "all 5 nodes healthy" || doc.Data.Turns != 1 || doc.Data.ExitCode != 0 {
		t.Fatalf("doc = %+v", doc)
	}
}

// A confirm the piped run cannot answer comes back to the caller as
// needs_input with the request, even though the model went on to answer.
func TestPipedRunNeedsInputJSON(t *testing.T) {
	be := &scriptedBackend{steps: []chat.Message{
		toolCall("c1", "run_shell", `{"command":"git push origin main"}`),
		answer("I could not push: it needs approval"),
	}}
	// --autonomy full still asks for a specific safety rule (git push), and
	// a piped run cannot answer that ask.
	r := runPipedMode(t, be, 5, false, "json", agent.AutonomyFull)
	if got := ExitCode(r.err); got != ExitAgentNeedsInput {
		t.Fatalf("exit code = %d (%v), want %d\nstderr:\n%s", got, r.err, ExitAgentNeedsInput, r.stderr)
	}
	doc := decodeAgentDoc(t, r.stdout)
	d := doc.Data
	if doc.OK || d.Outcome != "needs_input" || d.ExitCode != 9 || len(d.PendingApprovals) != 1 {
		t.Fatalf("doc = %+v", doc)
	}
	if p := d.PendingApprovals[0]; p.Tool != "run_shell" || !strings.Contains(p.Request, "git push") || p.Clears == "" {
		t.Fatalf("pending approval = %+v", p)
	}
	if !strings.Contains(d.FinalText, "needs approval") {
		t.Fatalf("final_text = %q", d.FinalText)
	}
}

func TestPipedRunFailedJSON(t *testing.T) {
	be := &scriptedBackend{err: errors.New("backend down")}
	r := runPiped(t, be, 3, false, "json")
	if got := ExitCode(r.err); got != ExitErrCommandFail {
		t.Fatalf("exit code = %d, want %d", got, ExitErrCommandFail)
	}
	doc := decodeAgentDoc(t, r.stdout)
	if doc.OK || doc.Status != "failed" || !strings.Contains(doc.Data.Error, "backend down") || doc.Data.ExitCode != 4 {
		t.Fatalf("doc = %+v", doc)
	}
	if !strings.Contains(r.stderr, "error: Agent failed") {
		t.Fatalf("failure notice missing from stderr:\n%s", r.stderr)
	}
}

func TestAgentCmdFormatValidation(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--format", "json", "hi"}, "--format json requires -p/--print"},
		{[]string{"-p", "--format", "yaml", "hi"}, `invalid --format "yaml"`},
	} {
		cmd := agentCmd()
		cmd.SetArgs(tc.args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SilenceUsage = true
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}
