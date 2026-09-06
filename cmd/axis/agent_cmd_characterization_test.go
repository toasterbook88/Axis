package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
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

func TestCharREPLSessionBannerReflectsFlags(t *testing.T) {
	var errW bytes.Buffer
	writeAgentREPLIntro(agentREPLConfig{
		ActiveTarget: ModelChoice{Model: "test-model", Endpoint: "http://127.0.0.1:11434"},
		AutoApprove:  true,
		Autonomy:     "full",
		MaxTurns:     50,
		ErrOut:       &errW,
	})
	got := ui.StripANSIAndControls(errW.String())
	if !strings.Contains(got, "Autonomy: full") {
		t.Errorf("banner must reflect --autonomy full, got %q", got)
	}
	if strings.Contains(got, "Strict Operator Approval") {
		t.Errorf("banner must not hard-code Strict Operator Approval when autonomy is full, got %q", got)
	}
	if !strings.Contains(got, "50") {
		t.Errorf("banner must reflect --max-turns 50, got %q", got)
	}
	if strings.Contains(got, "Max Turns") && strings.Contains(got, " 0") && !strings.Contains(got, "50") {
		t.Errorf("banner must not hard-code Max Turns 0, got %q", got)
	}
}

func TestCharREPLSessionBannerAutoApprove(t *testing.T) {
	var errW bytes.Buffer
	writeAgentREPLIntro(agentREPLConfig{
		ActiveTarget: ModelChoice{Model: "test-model"},
		AutoApprove:  true,
		MaxTurns:     25,
		ErrOut:       &errW,
	})
	got := ui.StripANSIAndControls(errW.String())
	if !strings.Contains(got, "Auto-Approve safe") {
		t.Errorf("banner must reflect --auto-approve, got %q", got)
	}
	if !strings.Contains(got, "25") {
		t.Errorf("banner must reflect max-turns 25, got %q", got)
	}
}

func TestCharREPLCompleterIncludesVerbsAndModels(t *testing.T) {
	completer := agentREPLCompleter([]ModelChoice{
		{Model: "qwen3:1.7b", Disabled: false},
		{Model: "disabled-model", Disabled: true},
	})
	if completer == nil {
		t.Fatal("agentREPLCompleter must not be nil")
	}
	tree := completer.Tree("")
	for _, want := range []string{"/help", "/plan", "/todo", "/autonomy", "default", "edit", "full", "/model", "/models", "/exit", "/quit", "qwen3:1.7b"} {
		if !strings.Contains(tree, want) {
			t.Errorf("completer tree missing %q\n%s", want, tree)
		}
	}
	if strings.Contains(tree, "disabled-model") {
		t.Errorf("disabled models must not be completer items\n%s", tree)
	}
}

func TestCharSetupAgentStartupBackendNoModelsError(t *testing.T) {
	prevLoad := inferenceAILoadFn
	inferenceAILoadFn = func(string) (*config.AIConfig, error) { return &config.AIConfig{}, nil }
	t.Cleanup(func() { inferenceAILoadFn = prevLoad })

	var out bytes.Buffer
	_, err := setupAgentStartupBackend(agentStartupBackendParams{
		SelectModel: true,
		RT:          &runtimectx.Context{Config: &config.Config{}},
		Out:         &out,
	})
	if err == nil {
		t.Fatal("expected error when no models are available and selectModel is true")
	}
	exitErr, ok := err.(ExitCodeError)
	if !ok || exitErr.Code != ExitErrConfigLoad {
		t.Errorf("expected ExitErrConfigLoad, got %v", err)
	}
}

func TestCharSetupAgentStartupBackendExplicitModel(t *testing.T) {
	res, err := setupAgentStartupBackend(agentStartupBackendParams{
		Model:                 "mock-model",
		StartupRequestedModel: "mock-model",
		Provider:              "local",
		RT:                    &runtimectx.Context{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ActiveTarget.Model != "mock-model" {
		t.Errorf("ActiveTarget.Model = %q, want %q", res.ActiveTarget.Model, "mock-model")
	}
	if res.Backend == nil {
		t.Error("Backend must not be nil")
	}
}

func TestCharSetupAgentStartupBackendNonTTYSelectAborts(t *testing.T) {
	rt := &runtimectx.Context{
		Config: &config.Config{
			AIProviders: map[string]config.AIProviderConfig{
				"mock-prov": {
					Enabled: true, Type: "cloud", Kind: "anthropic", Priority: 50,
					APIKeyEnv: "AXIS_TEST_MOCK_KEY",
					Models:    []config.AIModelConfig{{Name: "mock-choice", CostPer1K: 0.01}},
				},
			},
		},
	}
	t.Setenv("AXIS_TEST_MOCK_KEY", "secret-key")

	var out bytes.Buffer
	_, err := setupAgentStartupBackend(agentStartupBackendParams{
		SelectModel: true,
		RT:          rt,
		Out:         &out,
	})
	if err == nil {
		t.Fatal("expected selection to abort in non-TTY test environment")
	}
	if !strings.Contains(err.Error(), "model selection aborted") {
		t.Errorf("error = %q, want 'model selection aborted'", err.Error())
	}
	if !strings.Contains(out.String(), "Select model to use for the AXIS Agent session:") {
		t.Errorf("expected menu header in out, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "mock-choice") {
		t.Errorf("expected choice in out, got: %s", out.String())
	}
}

func TestCharSetupAgentStartupBackendCloudAndCheapRouting(t *testing.T) {
	t.Setenv("AXIS_TEST_CLOUD_ROUTING", "test-key")
	rt := &runtimectx.Context{
		Config: &config.Config{
			AIProviders: map[string]config.AIProviderConfig{
				"anthropic": {
					Enabled: true, Type: "cloud", Kind: "anthropic", Priority: 50,
					APIKeyEnv: "AXIS_TEST_CLOUD_ROUTING",
					Models: []config.AIModelConfig{
						{Name: "claude-3-opus", CostPer1K: 0.015},
						{Name: "claude-3-haiku", CostPer1K: 0.00025},
					},
				},
			},
		},
	}

	var errW bytes.Buffer
	res, err := setupAgentStartupBackend(agentStartupBackendParams{
		Provider:   "cloud",
		CloudModel: "claude-3-opus",
		CheapModel: "claude-3-haiku",
		Verbose:    true,
		RT:         rt,
		ErrOut:     &errW,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ActiveTarget.Model != "claude-3-opus" {
		t.Errorf("ActiveTarget.Model = %q, want 'claude-3-opus'", res.ActiveTarget.Model)
	}
	if res.ActiveTarget.Protocol != agent.ProtocolCloud {
		t.Errorf("ActiveTarget.Protocol = %v, want ProtocolCloud", res.ActiveTarget.Protocol)
	}
	if res.Backend == nil {
		t.Error("Backend must not be nil")
	}
	if !strings.Contains(errW.String(), `Multi-model routing: primary="claude-3-opus" cheap="claude-3-haiku"`) {
		t.Errorf("expected multi-model routing log in ErrOut, got: %s", errW.String())
	}
}

func TestCharSetupAgentStartupBackendCheapModelWarning(t *testing.T) {
	t.Setenv("AXIS_TEST_CLOUD_ROUTING", "test-key")
	rt := &runtimectx.Context{
		Config: &config.Config{
			AIProviders: map[string]config.AIProviderConfig{
				"anthropic": {
					Enabled: true, Type: "cloud", Kind: "anthropic", Priority: 50,
					APIKeyEnv: "AXIS_TEST_CLOUD_ROUTING",
					Models: []config.AIModelConfig{
						{Name: "claude-3-opus", CostPer1K: 0.015},
					},
				},
			},
		},
	}

	var errW bytes.Buffer
	res, err := setupAgentStartupBackend(agentStartupBackendParams{
		Provider:   "cloud",
		CloudModel: "claude-3-opus",
		CheapModel: "bogus-cheap-model",
		Verbose:    true,
		RT:         rt,
		ErrOut:     &errW,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ActiveTarget.Model != "claude-3-opus" {
		t.Errorf("ActiveTarget.Model = %q, want 'claude-3-opus'", res.ActiveTarget.Model)
	}
	if !strings.Contains(errW.String(), `Warning: cheap-model "bogus-cheap-model" not available`) {
		t.Errorf("expected warning in ErrOut, got: %s", errW.String())
	}
}
