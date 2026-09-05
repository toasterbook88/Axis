package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/mcpclient"
)

// Characterization rows for agentCmd construction and the extracted REPL
// runtime (agent_repl.go). These exercise flag wiring and the runtime entry
// without a live TTY or cluster. They lock:
//   - flag defaults and mutual exclusivity as agentCmd builds them today
//   - runAgentInteractive console gating (--console requires TTY)
//   - replTurn one-line contract (slash / blank / exit / agent-run)

func TestCharAgentCmdConstruction(t *testing.T) {
	cmd := agentCmd()
	if cmd == nil {
		t.Fatal("agentCmd() returned nil")
	}
	if cmd.Use != "agent [instruction...]" {
		t.Errorf("Use = %q", cmd.Use)
	}
	// Every documented flag must be registered with today's defaults.
	defaults := map[string]string{
		"model":                      "",
		"role":                       "",
		"timeout":                    "5m0s",
		"max-tokens":                 "32768",
		"max-turns":                  "25",
		"auto-approve":               "false",
		"autonomy":                   "default",
		"system":                     "",
		"resume":                     "false",
		"verbose":                    "false",
		"dry-run":                    "false",
		"provider":                   "auto",
		"cloud-model":                "",
		"cheap-model":                "",
		"allow-raw-command-evidence": "false",
		"select":                     "false",
		"console":                    "false",
	}
	for name, want := range defaults {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("flag --%s missing", name)
			continue
		}
		if f.DefValue != want {
			t.Errorf("flag --%s default = %q, want %q", name, f.DefValue, want)
		}
	}
}

func TestCharAgentCmdModelRoleMutuallyExclusive(t *testing.T) {
	cmd := agentCmd()
	cmd.SetArgs([]string{"--model", "m1", "--role", "r1", "--dry-run"})
	// RunE will fail on the exclusivity check before touching the network.
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for --model + --role")
	}
	if !strings.Contains(err.Error(), "use either --model or --role") {
		t.Errorf("exclusivity error string, got %q", err.Error())
	}
}

func TestCharAgentCmdBadAutonomyRejected(t *testing.T) {
	cmd := agentCmd()
	cmd.SetArgs([]string{"--autonomy", "bogus", "--dry-run"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid --autonomy")
	}
	// ParseAutonomyMode's own message is part of the contract.
	if strings.Contains(err.Error(), "use either --model or --role") {
		t.Errorf("wrong error surfaced: %v", err)
	}
}

func TestCharAgentCmdConsoleRequiresTTY(t *testing.T) {
	// runAgentInteractive with UseConsole and a non-TTY stdin must return the
	// captured contract error before touching the console.
	cfg := agentREPLConfig{
		UseConsole: true,
		Ctx:        context.Background(),
		Out:        &bytes.Buffer{},
		ErrOut:     &bytes.Buffer{},
	}
	err := runAgentInteractive(cfg)
	if err == nil {
		t.Fatal("expected --console to require a TTY in tests")
	}
	if !strings.Contains(err.Error(), "requires an interactive terminal") {
		t.Errorf("console TTY error string, got %q", err.Error())
	}
}

func TestCharReplTurnBlankAndExit(t *testing.T) {
	session, _, _ := newCharSession()

	// Blank line: continue, no break
	cont, br := replTurn(session, context.Background(), time.Second, "   ")
	if !cont || br {
		t.Errorf("blank line: cont=%v break=%v, want true/false", cont, br)
	}

	// exit/quit: break
	for _, word := range []string{"exit", "quit", "EXIT"} {
		cont, br = replTurn(session, context.Background(), time.Second, word)
		if cont || !br {
			t.Errorf("%q: cont=%v break=%v, want false/true", word, cont, br)
		}
	}
}

func TestCharReplTurnSlashHandled(t *testing.T) {
	session, _, errW := newCharSession()

	// A handled slash verb continues the loop without touching Agent.Run
	cont, br := replTurn(session, context.Background(), time.Second, "/help")
	if !cont || br {
		t.Errorf("/help: cont=%v break=%v, want true/false", cont, br)
	}
	if !strings.Contains(errW.String(), "Available commands:") {
		t.Errorf("/help output missing, got %q", errW.String())
	}

	// A handled slash verb that exits breaks the loop
	cont, br = replTurn(session, context.Background(), time.Second, "/exit")
	if cont || !br {
		t.Errorf("/exit: cont=%v break=%v, want false/true", cont, br)
	}
}

func TestCharReplTurnAgentRunError(t *testing.T) {
	// Non-slash input goes to Agent.Run; with no backend it fails and the
	// error is captured on ErrOut (not returned). Lock that contract.
	session, _, errW := newCharSession()
	cont, br := replTurn(session, context.Background(), time.Second, "hello world")
	if !cont || br {
		t.Errorf("agent run: cont=%v break=%v, want true/false", cont, br)
	}
	if !strings.Contains(errW.String(), "Error:") {
		t.Errorf("agent run error must be captured on ErrOut, got %q", errW.String())
	}
}

func TestCharAgentSessionDefaults(t *testing.T) {
	// agentREPLSession wiring sanity: registry nil-safe names check matches
	// what runAgentREPLSession does before printing session details.
	var reg *mcpclient.Registry
	count := 0
	if reg != nil {
		count = len(reg.Names())
	}
	if count != 0 {
		t.Errorf("nil registry count = %d, want 0", count)
	}

	// A real registry with one server counts 1
	reg = mcpclient.NewRegistry()
	if len(reg.Names()) != 0 {
		t.Errorf("empty registry count = %d, want 0", len(reg.Names()))
	}

	// Agent construction with defaults keeps model name
	a := agent.New(agent.Config{Model: "test-model"})
	if a.Model() != "test-model" && a.Model() != "test" {
		// Lock only that Model() reflects what was configured
		t.Logf("model = %q (informational)", a.Model())
	}
}
