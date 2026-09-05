package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/ui"
)

// agentREPLConfig carries everything the REPL runtime needs from the cobra
// wiring. agentCmd assembles it; runAgentInteractive consumes it. Keeping the
// struct explicit makes the runtime entry testable without a live TTY.
type agentREPLConfig struct {
	Agent        *agent.Agent
	MCPRegistry  *mcpclient.Registry
	ActiveTarget ModelChoice
	Timeout      time.Duration
	HistoryPath  string
	UseConsole   bool
	AutoApprove  bool
	Autonomy     string
	MaxTurns     int
	ModelChoices []ModelChoice
	Ctx          context.Context
	Out, ErrOut  io.Writer
}

// runAgentInteractive is the named REPL runtime entry extracted from agentCmd.
// It owns: console attach and the REPL loop. Setup, one-turn processing, and
// shutdown are separate functions so no single function carries the old CC 83.
func runAgentInteractive(cfg agentREPLConfig) error {
	if cfg.UseConsole {
		if !consoleTTY() {
			return fmt.Errorf("--console requires an interactive terminal")
		}
		return runAgentConsole(cfg.Ctx, cfg.Agent, cfg.ErrOut, cfg.Timeout, cfg.HistoryPath, cfg.MCPRegistry, cfg.ActiveTarget)
	}
	return runAgentREPLSession(cfg)
}

// writeAgentREPLIntro prints the logo and session banner. Extracted so tests
// can lock Safety Gate and Max Turns without a live TTY/readline loop.
func writeAgentREPLIntro(cfg agentREPLConfig) {
	ui.PrintLogo(cfg.ErrOut, buildinfo.Version)
	mcpCount := 0
	if cfg.MCPRegistry != nil {
		mcpCount = len(cfg.MCPRegistry.Names())
	}
	printAgentSessionDetails(cfg.ErrOut, cfg.ActiveTarget, cfg.AutoApprove, cfg.Autonomy, mcpCount, cfg.MaxTurns)
}

// agentREPLCompleter is the slash-verb and /model tab-complete tree restored
// from the pre-extraction readline config. Nil AutoComplete is not equivalent:
// readline does not rebuild this tree.
func agentREPLCompleter(choices []ModelChoice) *readline.PrefixCompleter {
	completerItems := []readline.PrefixCompleterInterface{
		readline.PcItem("/help"),
		readline.PcItem("/plan"),
		readline.PcItem("/todo"),
		readline.PcItem("/diff"),
		readline.PcItem("/undo"),
		readline.PcItem("/compact"),
		readline.PcItem("/autonomy", readline.PcItem("default"), readline.PcItem("edit"), readline.PcItem("full")),
		readline.PcItem("/export"),
		readline.PcItem("/fleet"),
		readline.PcItem("/facts"),
		readline.PcItem("/cluster"),
		readline.PcItem("/clear"),
		readline.PcItem("/context"),
		readline.PcItem("/history"),
		readline.PcItem("/tools"),
		readline.PcItem("/models"),
		readline.PcItem("/mcp"),
		readline.PcItem("/reservations"),
		readline.PcItem("/skills"),
		readline.PcItem("/exit"),
		readline.PcItem("/quit"),
	}
	var modelCompleterItems []readline.PrefixCompleterInterface
	for _, choice := range choices {
		if !choice.Disabled {
			modelCompleterItems = append(modelCompleterItems, readline.PcItem(choice.Model))
		}
	}
	completerItems = append(completerItems, readline.PcItem("/model", modelCompleterItems...))
	return readline.NewPrefixCompleter(completerItems...)
}

// runAgentREPLSession runs the readline REPL extracted from the agentCmd RunE body.
func runAgentREPLSession(cfg agentREPLConfig) error {
	out, errW := cfg.Out, cfg.ErrOut

	writeAgentREPLIntro(cfg)

	rlCfg := &readline.Config{
		Prompt:          ui.Cyan("✨ axis ❯ "),
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
		AutoComplete:    agentREPLCompleter(cfg.ModelChoices),
	}
	// Agent history is already persisted by Conversation.SaveToFile.
	// Keep readline history in memory so its permissive file mode cannot
	// expose prompts to other local users.
	rl, err := readline.NewEx(rlCfg)
	if err != nil {
		return runPlainAgentREPL(cfg.Ctx, cfg.Agent, out, errW, cfg.Timeout, cfg.HistoryPath, cfg.MCPRegistry, cfg.ActiveTarget)
	}
	defer rl.Close()

	session := &agentREPLSession{
		Agent:        cfg.Agent,
		MCPRegistry:  cfg.MCPRegistry,
		Runtime:      loadAgentShellRuntime,
		Selector:     &REPLSelector{terminal: ui.NewStdTerminal(os.Stdin, out), in: rl, out: out},
		In:           rl,
		Out:          out,
		ErrOut:       errW,
		ActiveTarget: cfg.ActiveTarget,
	}

	for {
		line, err := session.In.Readline()
		if err != nil {
			break
		}
		// replTurn returns (continueLoop, breakLoop). breakLoop exits the REPL;
		// otherwise the loop continues regardless of continueLoop (both blank
		// lines and handled verbs fall through to the next Readline).
		_, shouldBreak := replTurn(session, cfg.Ctx, cfg.Timeout, line)
		if shouldBreak {
			break
		}
	}

	if cfg.HistoryPath != "" && cfg.Agent.Conversation().HistoryCount() > 0 {
		if err := saveAgentConversation(cfg.Agent.Conversation(), cfg.HistoryPath, errW); err == nil {
			fmt.Fprintf(errW, "Saved %d messages to conversation history.\n", cfg.Agent.Conversation().HistoryCount())
		}
	}
	return nil
}

// replTurn processes one REPL input line: slash dispatch or agent run.
// Returns (continueLoop, breakLoop). Extracted so the loop body is testable
// without a live TTY.
func replTurn(session *agentREPLSession, ctx context.Context, timeout time.Duration, line string) (bool, bool) {
	instruction := strings.TrimSpace(line)
	if instruction == "" {
		return true, false
	}
	lower := strings.ToLower(instruction)
	if lower == "exit" || lower == "quit" {
		return false, true
	}

	if strings.HasPrefix(instruction, "/") {
		handled, shouldExit, slashErr := handleREPLSlashCommand(session, instruction)
		if slashErr != nil {
			fmt.Fprintf(session.ErrOut, "\n%s %v\n", ui.Red("Error:"), slashErr)
		}
		if handled {
			if shouldExit {
				return false, true
			}
			return true, false
		}
	}

	ctx2, cancel := agentRequestContext(ctx, timeout)
	if err := session.Agent.Run(ctx2, instruction); err != nil {
		fmt.Fprintf(session.ErrOut, "\n%s %v\n", ui.Red("Error:"), err)
	}
	cancel()
	fmt.Fprintln(session.Out)
	return true, false
}
