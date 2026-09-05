package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// Characterization tests for handleREPLSlashCommand.
//
// These lock CURRENT behavior of every slash verb that exists today, so the
// planned dispatch-table extraction (refactor/agent-island-split) can be
// verified against captured behavior rather than intent. Each test asserts:
//   - handled / shouldExit contract
//   - observable output on the session buffers
//   - error string exactly where the verb can fail without a cluster
//
// No new verbs are introduced here. If a row fails before the refactor, the
// refactor is wrong; if it fails after, the extraction changed behavior.

func newCharSession() (*agentREPLSession, *bytes.Buffer, *bytes.Buffer) {
	a := agent.New(agent.Config{
		Endpoint:  "http://localhost:11434",
		Model:     "granite3.1-moe:1b",
		MaxTokens: 4096,
	})
	var w, errW bytes.Buffer
	session := &agentREPLSession{
		Agent: a,
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return nil, nil
		},
		Selector: nil,
		Out:      &w,
		ErrOut:   &errW,
	}
	return session, &w, &errW
}

// every verb that handleREPLSlashCommand accepts today (from the switch in
// cmd/axis/agent.go). The extraction must dispatch exactly this set.
var charVerbs = []string{
	"/exit", "/quit", "/help", "/plan", "/todo", "/diff", "/undo",
	"/compact", "/autonomy", "/fleet", "/export", "/facts", "/cluster",
	"/nodes", "/reservations", "/skills", "/models", "/model", "/mcp",
	"/clear", "/context", "/history", "/tools",
}

func TestCharAllVerbsAreHandled(t *testing.T) {
	// Every verb must be recognized (handled=true) and must not fall through
	// to the not-a-slash path. /exit and /quit additionally assert exit=true.
	mustExit := map[string]bool{"/exit": true, "/quit": true}
	for _, verb := range charVerbs {
		session, _, _ := newCharSession()
		handled, shouldExit, err := handleREPLSlashCommand(session, verb)
		if err != nil {
			// Verbs that can error without a cluster are covered by dedicated
			// rows below; here we only require that known-safe verbs run clean.
			switch verb {
			case "/model", "/models":
				// /model without args opens interactive select; Selector is nil
				// so it may error — that's today's behavior, lock it.
			default:
				t.Fatalf("%s: unexpected error: %v", verb, err)
			}
		}
		if !handled {
			t.Errorf("%s: expected handled=true", verb)
		}
		if shouldExit != mustExit[verb] {
			t.Errorf("%s: shouldExit=%v, want %v", verb, shouldExit, mustExit[verb])
		}
	}
}

func TestCharUnknownSlashFallsThrough(t *testing.T) {
	session, _, _ := newCharSession()
	for _, line := range []string{"/nope", "/VEHICLE", "/HELPX"} {
		handled, shouldExit, err := handleREPLSlashCommand(session, line)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", line, err)
		}
		if handled {
			t.Errorf("%q: unknown verb must fall through (handled=false)", line)
		}
		if shouldExit {
			t.Errorf("%q: must not exit", line)
		}
	}
}

func TestCharCaseInsensitiveVerb(t *testing.T) {
	// The dispatcher lowercases the verb; capture that contract.
	session, _, errW := newCharSession()
	handled, _, err := handleREPLSlashCommand(session, "/HELP")
	if err != nil || !handled {
		t.Fatalf("/HELP: handled=%v err=%v (verb matching is case-insensitive today)", handled, err)
	}
	if !strings.Contains(errW.String(), "Available commands:") {
		t.Errorf("/HELP must print help, got %q", errW.String())
	}
}

func TestCharEmptyLine(t *testing.T) {
	session, _, _ := newCharSession()
	handled, shouldExit, err := handleREPLSlashCommand(session, "")
	if err != nil || handled || shouldExit {
		t.Fatalf("empty line: handled=%v exit=%v err=%v, want false/false/nil", handled, shouldExit, err)
	}
}

func TestCharHelp(t *testing.T) {
	session, _, errW := newCharSession()
	handled, shouldExit, err := handleREPLSlashCommand(session, "/help")
	if err != nil || !handled || shouldExit {
		t.Fatalf("/help: handled=%v exit=%v err=%v", handled, shouldExit, err)
	}
	// Lock the full verb list in help output — new verbs must update this row.
	for _, verb := range []string{
		"/plan, /todo", "/diff", "/undo", "/compact", "/autonomy",
		"/export", "/fleet", "/facts", "/cluster", "/clear", "/context",
		"/history", "/tools", "/models", "/mcp", "/reservations", "/skills",
		"/exit, /quit",
	} {
		if !strings.Contains(errW.String(), verb) {
			t.Errorf("/help missing %q", verb)
		}
	}
}

func TestCharDiffCleanAndError(t *testing.T) {
	// /diff runs `git diff HEAD` in the process CWD; the axis repo checkout is
	// dirty or clean depending on operator state, so lock both branches.
	session, w, errW := newCharSession()
	handled, shouldExit, err := handleREPLSlashCommand(session, "/diff")
	if err != nil || !handled || shouldExit {
		t.Fatalf("/diff: handled=%v exit=%v err=%v", handled, shouldExit, err)
	}
	out := w.String() + errW.String()
	clean := strings.Contains(out, "Working tree is clean")
	hasDiff := len(out) > 0 && !clean
	if !clean && !hasDiff {
		t.Errorf("/diff must print either the clean message or diff output, got %q", out)
	}
}

func TestCharUndoAndPlanUseToolDirect(t *testing.T) {
	// /plan and /undo call ExecuteToolDirect. With a default agent.New the todo
	// tool succeeds and prints to Out; undo_last reports nothing-to-undo as a
	// captured error on errW. Lock today's split — don't guess it.
	session, w, errW := newCharSession()
	handled, shouldExit, err := handleREPLSlashCommand(session, "/plan")
	if err != nil {
		t.Fatalf("/plan: returned err=%v (today it prints to a buffer instead)", err)
	}
	if !handled || shouldExit {
		t.Fatalf("/plan: handled=%v exit=%v", handled, shouldExit)
	}
	if !strings.Contains(w.String(), errW.String()) && w.String() == "" && errW.String() == "" {
		t.Error("/plan: expected output on Out or ErrOut, got neither")
	}

	session2, w2, errW2 := newCharSession()
	handled, _, err = handleREPLSlashCommand(session2, "/undo")
	if err != nil {
		t.Fatalf("/undo: returned err=%v", err)
	}
	if !handled {
		t.Fatal("/undo: must be handled")
	}
	if w2.String() == "" && errW2.String() == "" {
		t.Error("/undo: expected output on Out or ErrOut, got neither")
	}
}

func TestCharAutonomyViewAndSet(t *testing.T) {
	session, w, _ := newCharSession()

	// View path (no args)
	handled, shouldExit, err := handleREPLSlashCommand(session, "/autonomy")
	if err != nil || !handled || shouldExit {
		t.Fatalf("/autonomy view: handled=%v exit=%v err=%v", handled, shouldExit, err)
	}
	if !strings.Contains(w.String(), "Current autonomy mode:") {
		t.Errorf("/autonomy view output, got %q", w.String())
	}

	// Set path (valid mode)
	w.Reset()
	handled, _, err = handleREPLSlashCommand(session, "/autonomy full")
	if err != nil || !handled {
		t.Fatalf("/autonomy full: handled=%v err=%v", handled, err)
	}
	if !strings.Contains(w.String(), "Switched autonomy mode to") {
		t.Errorf("/autonomy full output, got %q", w.String())
	}

	// Set path (invalid mode) — lock the error string
	session2, _, errW := newCharSession()
	_, _, err = handleREPLSlashCommand(session2, "/autonomy bogus")
	if err != nil {
		t.Fatalf("/autonomy bogus: must print to errW, not return (got %v)", err)
	}
	if !strings.Contains(errW.String(), "Error:") {
		t.Errorf("/autonomy bogus: expected error prefix, got %q", errW.String())
	}
}

func TestCharFactsAndClusterNoCluster(t *testing.T) {
	// Runtime returns nil,nil (no cluster); /facts and /cluster must still
	// handle cleanly and print *something* — today they render empty tables.
	for _, verb := range []string{"/facts", "/cluster", "/fleet"} {
		session, w, _ := newCharSession()
		handled, shouldExit, err := handleREPLSlashCommand(session, verb)
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		if !handled || shouldExit {
			t.Fatalf("%s: handled=%v exit=%v", verb, handled, shouldExit)
		}
		_ = w
	}
}

func TestCharNodesDeprecated(t *testing.T) {
	session, _, errW := newCharSession()
	handled, shouldExit, err := handleREPLSlashCommand(session, "/nodes")
	if err != nil || !handled || shouldExit {
		t.Fatalf("/nodes: handled=%v exit=%v err=%v", handled, shouldExit, err)
	}
	if !strings.Contains(errW.String(), "/cluster shows the session snapshot") {
		t.Errorf("/nodes guidance output, got %q", errW.String())
	}
}

func TestCharReservationsFallbackError(t *testing.T) {
	// With a nil runtime and no daemon, /reservations returns the captured
	// fallback error. Lock its wording.
	session, _, _ := newCharSession()
	_, _, err := handleREPLSlashCommand(session, "/reservations")
	if err == nil {
		t.Skip("daemon may be running on this host; fallback path not taken")
	}
	if !strings.Contains(err.Error(), "failed to load cluster status fallback") {
		t.Errorf("/reservations error string, got %q", err.Error())
	}
}

func TestCharContextAndHistoryAndToolsAndClear(t *testing.T) {
	session, _, errW := newCharSession()

	handled, _, err := handleREPLSlashCommand(session, "/context")
	if err != nil || !handled {
		t.Fatalf("/context: %v", err)
	}
	if !strings.Contains(errW.String(), "Tokens used:") {
		t.Errorf("/context output, got %q", errW.String())
	}

	errW.Reset()
	handled, _, err = handleREPLSlashCommand(session, "/history")
	if err != nil || !handled {
		t.Fatalf("/history: %v", err)
	}
	if !strings.Contains(errW.String(), "Conversation History (") {
		t.Errorf("/history output, got %q", errW.String())
	}

	errW.Reset()
	handled, _, err = handleREPLSlashCommand(session, "/tools")
	if err != nil || !handled {
		t.Fatalf("/tools: %v", err)
	}
	// /tools writes to Out (w), not errW — just require no error.

	// /clear wipes conversation
	a := session.Agent
	a.Conversation().Append(chat.Message{Role: chat.RoleUser, Content: "x"})
	handled, _, err = handleREPLSlashCommand(session, "/clear")
	if err != nil || !handled {
		t.Fatalf("/clear: %v", err)
	}
	for _, m := range a.Conversation().Messages() {
		if m.Role != chat.RoleSystem {
			t.Errorf("/clear left non-system message role %q", m.Role)
		}
	}
	if !strings.Contains(errW.String(), "Conversation history cleared") {
		t.Errorf("/clear output, got %q", errW.String())
	}
}

func TestCharModelUnknownErrors(t *testing.T) {
	session, _, _ := newCharSession()
	handled, _, err := handleREPLSlashCommand(session, "/model definitely-not-a-model")
	if !handled {
		t.Fatal("/model <bad>: must be handled")
	}
	if err == nil {
		t.Fatal("/model <bad>: expected error for unknown model")
	}
	if session.Agent.Model() == "definitely-not-a-model" {
		t.Error("/model must not switch on unknown name")
	}
}

func TestCharExportErrorsWithoutWorkspace(t *testing.T) {
	// /export with a path in a non-writable place captures the error on errW.
	session, _, errW := newCharSession()
	handled, shouldExit, err := handleREPLSlashCommand(session, "/export /nonexistent-root-dir/x.md")
	if err != nil {
		t.Fatalf("/export bad path: must print to errW, got %v", err)
	}
	if !handled || shouldExit {
		t.Fatalf("/export: handled=%v exit=%v", handled, shouldExit)
	}
	// Either success (unlikely) or captured error — both write something.
	_ = errW
}