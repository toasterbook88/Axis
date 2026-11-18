package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/ui"
)

func TestSlashToolsListsDeferredMCPTools(t *testing.T) {
	reg := mcpclient.NewRegistry()
	reg.Add(&mcpclient.ServerConnection{
		Name:       "mock",
		InitResult: &mcp.InitializeResult{},
		Tools:      []mcp.Tool{{Name: "echo", Description: "echo text", InputSchema: mcp.ToolInputSchema{Type: "object"}}},
	})
	a := agent.New(agent.Config{
		Model:       "test-model",
		MCPRegistry: reg,
		Output:      io.Discard,
		ToolContext: agent.NewToolContext(&agent.RuntimeView{}, nil),
	})

	var w, errW bytes.Buffer
	session := &agentREPLSession{
		Agent:  a,
		Out:    &w,
		ErrOut: &errW,
	}
	handled, shouldExit, err := slashTools(session, nil)
	if err != nil || !handled || shouldExit {
		t.Fatalf("slashTools = (%v,%v,%v), want (true,false,nil)", handled, shouldExit, err)
	}
	out := ui.StripANSIAndControls(w.String())
	if !strings.Contains(out, "mcp_mock_echo") || !strings.Contains(out, "[deferred]") {
		t.Fatalf("slashTools output missing deferred tag for mcp_mock_echo:\n%s", out)
	}
	if !strings.Contains(out, "tool_search") || !strings.Contains(out, "tool_call") {
		t.Fatalf("slashTools output missing bridge tools:\n%s", out)
	}
}

func TestAgentCmdParsesEagerToolsAndNoDeferFlags(t *testing.T) {
	cmd := agentCmd()
	if err := cmd.ParseFlags([]string{"--eager-tools", "read_file, write_file", "--no-defer-tools"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	eager, err := cmd.Flags().GetStringSlice("eager-tools")
	if err != nil {
		t.Fatalf("GetStringSlice(eager-tools): %v", err)
	}
	if len(eager) != 2 || eager[0] != "read_file" || strings.TrimSpace(eager[1]) != "write_file" {
		t.Fatalf("eager-tools = %#v, want [read_file write_file]", eager)
	}
	noDefer, err := cmd.Flags().GetBool("no-defer-tools")
	if err != nil || !noDefer {
		t.Fatalf("GetBool(no-defer-tools) = (%v, %v), want (true, nil)", noDefer, err)
	}
}

func TestPlainAndConsoleStartupWireDeferralFlags(t *testing.T) {
	reg := mcpclient.NewRegistry()
	reg.Add(&mcpclient.ServerConnection{
		Name:       "mock",
		InitResult: &mcp.InitializeResult{},
		Tools:      []mcp.Tool{{Name: "echo", Description: "echo text", InputSchema: mcp.ToolInputSchema{Type: "object"}}},
	})

	catalogState := func(a *agent.Agent) map[string]bool {
		out := make(map[string]bool)
		for _, e := range a.ToolCatalog() {
			out[e.Def.Function.Name] = e.Deferred
		}
		return out
	}

	// 1. One-shot / --plain path via buildAgentSessionConfig with EagerTools allowlist.
	cfg := buildAgentSessionConfig(agentSessionParams{
		Model:       "m",
		Backend:     resumeTestBackend{},
		MaxTurns:    1,
		MaxTokens:   32768,
		EagerTools:  []string{"read_file"},
		MCPRegistry: reg,
		Output:      io.Discard,
		ToolContext: agent.NewToolContext(&agent.RuntimeView{}, nil),
	})
	aPlain := agent.New(cfg)
	plainCat := catalogState(aPlain)
	if deferred, ok := plainCat["read_file"]; !ok || deferred {
		t.Fatalf("plain session should keep read_file eager, cat=%v", plainCat)
	}
	if deferred, ok := plainCat["axis_status"]; !ok || !deferred {
		t.Fatalf("plain session should defer axis_status when EagerTools=[read_file], cat=%v", plainCat)
	}
	if _, hasBridge := plainCat["tool_search"]; !hasBridge {
		t.Fatalf("plain session missing tool_search, cat=%v", plainCat)
	}

	// 2. Interactive console path via consoleStartupJob.buildAgent (EagerTools + DisableToolDeferral precedence).
	jEager := &consoleStartupJob{
		in: agentStartupInputs{
			maxTurns:   1,
			maxTokens:  32768,
			eagerTools: []string{"read_file"},
		},
		out:      io.Discard,
		prog:     discardProgram{},
		launcher: newConsoleLauncher(nil, time.Second, time.Now),
		prelude: &consolePrelude{result: agentStartupBackendResult{
			ActiveTarget: ModelChoice{Model: "m"},
			Backend:      resumeTestBackend{},
		}},
	}
	aConsole := jEager.buildAgent(context.Background(), nil, reg)
	conCat := catalogState(aConsole)
	if deferred, ok := conCat["read_file"]; !ok || deferred {
		t.Fatalf("console startup should keep read_file eager, cat=%v", conCat)
	}
	if deferred, ok := conCat["axis_status"]; !ok || !deferred {
		t.Fatalf("console startup should defer axis_status when eagerTools=[read_file], cat=%v", conCat)
	}

	jNoDefer := &consoleStartupJob{
		in: agentStartupInputs{
			maxTurns:            1,
			maxTokens:           32768,
			eagerTools:          []string{"read_file"},
			disableToolDeferral: true,
		},
		out:      io.Discard,
		prog:     discardProgram{},
		launcher: newConsoleLauncher(nil, time.Second, time.Now),
		prelude: &consolePrelude{result: agentStartupBackendResult{
			ActiveTarget: ModelChoice{Model: "m"},
			Backend:      resumeTestBackend{},
		}},
	}
	aConsoleNoDefer := jNoDefer.buildAgent(context.Background(), nil, reg)
	noDeferCat := catalogState(aConsoleNoDefer)
	if _, hasBridge := noDeferCat["tool_search"]; hasBridge {
		t.Fatalf("console startup ignored disableToolDeferral precedence: tool_search present in %v", noDeferCat)
	}
	if deferred, ok := noDeferCat["mcp_mock_echo"]; !ok || deferred {
		t.Fatalf("console startup with disableToolDeferral should make mcp_mock_echo eager, cat=%v", noDeferCat)
	}
}
