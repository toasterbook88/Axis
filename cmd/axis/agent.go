package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/secrets"
	"github.com/toasterbook88/axis/internal/ui"
	"golang.org/x/term"
)

var loadAgentShellRuntime = runtimectx.Load
var runGuardedAgentShell = execution.RunGuarded
var runDaemonGuardedAgentShell = daemon.RunGuardedStream
var fetchAgentDaemonMeta = daemon.FetchMeta
var signalAgentDaemonRefresh = func(ctx context.Context, trigger string) error {
	return refreshDaemonCacheWithTrigger(ctx, api.DefaultAddr(), trigger)
}
var agentDaemonExecutionAddr = api.DefaultAddr

func agentCmd() *cobra.Command {
	var (
		model                   string
		timeout                 time.Duration
		maxTokens               int
		maxTurns                int
		autoApprove             bool
		autonomy                string
		systemMsg               string
		resume                  bool
		verbose                 bool
		dryRun                  bool
		provider                string
		cloudModel              string
		cheapModel              string
		allowRawCommandEvidence bool
		selectModel             bool
	)

	cmd := &cobra.Command{
		Use:   "agent [instruction...]",
		Short: "Agentic tool-calling assistant",
		Long: "Run an AI agent that can call AXIS tools to answer cluster questions.\n\n" +
			"The agent uses the Ollama /api/chat endpoint with tool calling.\n" +
			"It can run read-only cluster queries (status, facts, placement) and\n" +
			"execute shell commands through guarded AXIS execution with operator confirmation.\n\n" +
			"Agent output is advisory only — it is a consumer of the fact plane,\n" +
			"never a source of cluster truth.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if _, err := agent.ParseAutonomyMode(autonomy); err != nil {
				return err
			}

			var a *agent.Agent
			defer func() {
				if a == nil {
					return
				}
				stats := a.Stats()
				if stats.TokensIn > 0 || stats.TokensOut > 0 {
					totalTokens := stats.TokensIn + stats.TokensOut
					w := cmd.ErrOrStderr()

					fmt.Fprintln(w)
					ui.WhiteColor.Fprintf(w, "  ┌────────────────────────────────────────────────────────┐\n")
					ui.WhiteColor.Fprintf(w, "  │                    SESSION SUMMARY                     │\n")
					ui.WhiteColor.Fprintf(w, "  ├────────────────────────────────────────────────────────┤\n")

					totalStr := fmt.Sprintf("%-36d", totalTokens)
					inStr := fmt.Sprintf("%-36d", stats.TokensIn)
					outStr := fmt.Sprintf("%-36d", stats.TokensOut)

					fmt.Fprintf(w, "  │  Tokens Consumed:  %s │\n", ui.Bold(totalStr))
					fmt.Fprintf(w, "  │    - Input:        %s │\n", ui.Dim(inStr))
					fmt.Fprintf(w, "  │    - Output:       %s │\n", ui.Dim(outStr))
					if stats.Cost > 0 {
						costStr := fmt.Sprintf("$%-35.4f", stats.Cost)
						fmt.Fprintf(w, "  │  Estimated Cost:   %s │\n", ui.Green(costStr))
					}
					ui.WhiteColor.Fprintf(w, "  └────────────────────────────────────────────────────────┘\n")
				}
			}()

			rt, err := runtimectx.Load(ctx)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s Could not load cluster context: %v\n", ui.Yellow("⚠"), err)
			}
			// Display / last-resort resolution (always non-empty when possible).
			currentModel := resolveChatModel(model, rt)
			// Startup request: explicit --model, else chat.default_model / warm preferred.
			// Empty means "pick from catalog" (first usable local, then priority cloud).
			startupRequestedModel := effectiveStartupRequestedModel(model, rt)
			if verbose && strings.TrimSpace(model) == "" {
				if startupRequestedModel != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "Resolved model: %s\n", startupRequestedModel)
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "Resolved model: %s\n", currentModel)
				}
			}
			w := cmd.OutOrStdout()
			errW := cmd.ErrOrStderr()

			fmt.Fprintln(errW, ui.Dim("advisory: agent output is not cluster truth — it uses tools to read the fact plane"))

			// Load runtime context for tools and safety.
			var cluster *chat.ClusterSummaryForPrompt
			var k *knowledge.ClusterKnowledge
			var initialView *agent.RuntimeView

			if rt != nil {
				if rt.Snapshot != nil {
					cluster = chat.BuildClusterSummary(rt.Snapshot)
					bestNode := ""
					if len(rt.Snapshot.Nodes) > 0 {
						bestNode = rt.Snapshot.Nodes[0].Name
					}
					k = knowledge.Build(rt.Snapshot, rt.State, bestNode)
				}
				initialView = &agent.RuntimeView{
					Config:    rt.Config,
					Snapshot:  rt.Snapshot,
					State:     rt.State,
					Ledger:    rt.Ledger,
					Skills:    rt.Skills,
					Knowledge: k,
				}
			}

			tc := agent.NewToolContext(initialView, func(ctx context.Context) (*agent.RuntimeView, error) {
				newRt, err := runtimectx.Load(ctx)
				if err != nil {
					return nil, err
				}
				if newRt == nil {
					return nil, fmt.Errorf("loaded runtime context is nil")
				}
				bestNode := ""
				if newRt.Snapshot != nil && len(newRt.Snapshot.Nodes) > 0 {
					bestNode = newRt.Snapshot.Nodes[0].Name
				}
				newK := knowledge.Build(newRt.Snapshot, newRt.State, bestNode)
				return &agent.RuntimeView{
					Config:    newRt.Config,
					Snapshot:  newRt.Snapshot,
					State:     newRt.State,
					Ledger:    newRt.Ledger,
					Skills:    newRt.Skills,
					Knowledge: newK,
				}, nil
			})

			// Load MCP servers and connect.
			var mcpReg *mcpclient.Registry
			if rt != nil && rt.Config != nil {
				mcpReg = mcpclient.NewRegistry()
				if len(rt.Config.MCPServers) > 0 {
					if verbose {
						fmt.Fprintln(errW, "Connecting to MCP servers...")
					}
					mcpReg.ConnectAll(ctx, rt.Config)
					defer mcpReg.Close()
				}
			}

			// Determine which backend to use.
			var backend agent.ChatBackend

			hasDefaultModel := false
			if rt != nil && rt.Config != nil && rt.Config.Chat != nil && strings.TrimSpace(rt.Config.Chat.DefaultModel) != "" {
				hasDefaultModel = true
			}

			choices := collectModelChoices(rt)
			var explicitTarget *ModelChoice

			if selectModel || (model == "" && !hasDefaultModel && term.IsTerminal(int(os.Stdin.Fd()))) {
				if len(choices) == 0 {
					return ExitCodeError{Code: ExitErrConfigLoad, Message: "no models found (neither local Ollama models nor enabled cloud providers)"}
				}
				var selectOptions []ui.SelectOption
				for _, choice := range choices {
					detail := fmt.Sprintf("%s - %s", choice.ProviderName, choice.ProviderKind)
					if choice.ProviderKind == "local" {
						if choice.Node != "" {
							detail = fmt.Sprintf("Remote node %s (%s) [%s]", choice.Node, choice.Endpoint, choice.Protocol)
						} else {
							detail = fmt.Sprintf("Local (%s) [%s]", choice.Endpoint, choice.Protocol)
						}
					}
					disabled := choice.Disabled
					if choice.DisabledReason != "" && disabled {
						detail += " (" + choice.DisabledReason + ")"
					}
					selectOptions = append(selectOptions, ui.SelectOption{
						ID:       choice.ID,
						Label:    choice.Model,
						Detail:   detail,
						Disabled: disabled,
					})
				}

				sel := &REPLSelector{
					terminal: ui.NewStdTerminal(os.Stdin, w),
					in:       &UnbufferedLineReader{reader: os.Stdin},
					out:      w,
				}
				res, err := sel.Select(ctx, "Select model to use for the AXIS Agent session:", selectOptions)
				if err != nil {
					return fmt.Errorf("select model: %w", err)
				}
				if !res.Selected {
					return fmt.Errorf("model selection aborted")
				}

				var chosen ModelChoice
				for _, c := range choices {
					if c.ID == res.ID {
						chosen = c
						break
					}
				}
				if chosen.Model == "" {
					return fmt.Errorf("selected model id %q not found", res.ID)
				}
				explicitTarget = &chosen
				// Interactive local/remote selection forces local provider mode for auto policy.
				if chosen.ProviderKind != "cloud" && strings.EqualFold(provider, "auto") {
					provider = "local"
				}
				if chosen.ProviderKind == "cloud" {
					provider = "cloud"
					if cloudModel == "" {
						cloudModel = chosen.Model
					}
				}
			}

			// Pass the effective requested model (flag / default_model / preferred), not the raw flag alone.
			activeTarget, cloudOpts, err := resolveStartupModelTarget(startupRequestedModel, provider, cloudModel, explicitTarget, rt, choices)
			if err != nil {
				return ExitCodeError{Code: ExitErrConfigLoad, Message: err.Error()}
			}

			backend, err = agent.BuildBackend(activeTarget, cloudOpts)
			if err != nil {
				return ExitCodeError{Code: ExitErrConfigLoad, Message: err.Error()}
			}

			// Multi-model routing: cheap model on the same cloud provider only.
			if cheapModel != "" && activeTarget.Protocol == agent.ProtocolCloud {
				cheapTarget, cheapOpts, cerr := resolveCheapCloudTarget(rt, activeTarget, cheapModel)
				if cerr == nil {
					cheapBackend, berr := agent.BuildBackend(cheapTarget, cheapOpts)
					if berr == nil {
						rb := agent.NewRoutingBackend(backend, cheapBackend, nil)
						if verbose {
							rb.SetVerbose(errW)
							fmt.Fprintf(errW, "Multi-model routing: primary=%q cheap=%q\n", activeTarget.Model, cheapTarget.Model)
						}
						backend = rb
					} else if verbose {
						fmt.Fprintf(errW, "Warning: cheap-model %q not available: %v\n", cheapModel, berr)
					}
				} else if verbose {
					fmt.Fprintf(errW, "Warning: cheap-model %q not available: %v\n", cheapModel, cerr)
				}
			}

			endpoint := activeTarget.Endpoint
			if endpoint == "" {
				endpoint = chat.DefaultEndpoint
			}

			cfg := agent.Config{
				Endpoint:                endpoint,
				Model:                   activeTarget.Model,
				Backend:                 backend,
				MaxTurns:                maxTurns,
				MaxTokens:               maxTokens,
				AutoApprove:             autoApprove,
				Autonomy:                agent.AutonomyMode(autonomy),
				Verbose:                 verbose,
				DryRun:                  dryRun,
				AllowRawCommandEvidence: allowRawCommandEvidence,
				BackendSecurityClass:    activeTarget.SecurityClass,
				Cluster:                 cluster,
				Knowledge:               k,
				ToolContext:             tc,
				Output:                  w,
				RunTask:                 guardedAgentTaskRunner(),
				MCPRegistry:             mcpReg,
			}

			a = agent.New(cfg)

			// Resume previous conversation if requested.
			historyPath, err := chat.PersistPath("agent")
			if err != nil {
				fmt.Fprintf(errW, "warning: cannot determine history path: %v\n", err)
			} else if resume {
				if err := a.Conversation().LoadFromFile(historyPath); err != nil {
					fmt.Fprintf(errW, "warning: could not resume conversation: %v\n", err)
				} else if n := a.Conversation().HistoryCount(); n > 0 {
					fmt.Fprintf(errW, "Resumed %d messages from previous session.\n", n)
				}
			}

			// Single-shot mode.
			if len(args) > 0 {
				instruction := strings.Join(args, " ")
				fmt.Fprintf(errW, "Agent [%s] — max %d turns\n\n", ui.Bold(activeTarget.Model), maxTurns)

				ctx2, cancel := agentRequestContext(ctx, timeout)
				defer cancel()
				if err := a.Run(ctx2, instruction); err != nil {
					fmt.Fprintf(errW, "error: Agent failed: %v\n", err)
					return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("agent failed: %v", err)}
				}
				fmt.Fprintln(w)
				if historyPath != "" {
					_ = a.Conversation().SaveToFile(historyPath)
				}
				return nil
			}

			// Interactive REPL with readline.
			ui.PrintLogo(errW, buildinfo.Version)

			mcpCount := 0
			if mcpReg != nil {
				mcpCount = len(mcpReg.Names())
			}
			printAgentSessionDetails(errW, activeTarget, autoApprove, autonomy, mcpCount, maxTurns)

			var completerItems []readline.PrefixCompleterInterface
			completerItems = append(completerItems,
				readline.PcItem("/help"),
				readline.PcItem("/clear"),
				readline.PcItem("/context"),
				readline.PcItem("/history"),
				readline.PcItem("/tools"),
				readline.PcItem("/models"),
				readline.PcItem("/mcp"),
				readline.PcItem("/nodes"),
				readline.PcItem("/reservations"),
				readline.PcItem("/skills"),
				readline.PcItem("/exit"),
				readline.PcItem("/quit"),
			)

			// Collect available models dynamically for /model <name> autocomplete
			var modelCompleterItems []readline.PrefixCompleterInterface
			for _, choice := range collectModelChoices(rt) {
				if !choice.Disabled {
					modelCompleterItems = append(modelCompleterItems, readline.PcItem(choice.Model))
				}
			}
			completerItems = append(completerItems, readline.PcItem("/model", modelCompleterItems...))

			completer := readline.NewPrefixCompleter(completerItems...)

			rlCfg := &readline.Config{
				Prompt:          ui.Cyan("✨ axis ❯ "),
				InterruptPrompt: "^C",
				EOFPrompt:       "exit",
				AutoComplete:    completer,
			}
			if historyPath != "" {
				rlCfg.HistoryFile = historyPath + ".line"
			}
			rl, err := readline.NewEx(rlCfg)
			if err != nil {
				return runPlainAgentREPL(ctx, a, w, errW, timeout, historyPath, mcpReg, activeTarget)
			}
			defer rl.Close()

			session := &agentREPLSession{
				Agent:        a,
				MCPRegistry:  mcpReg,
				Runtime:      loadAgentShellRuntime,
				Selector:     &REPLSelector{terminal: ui.NewStdTerminal(os.Stdin, w), in: rl, out: w},
				In:           rl,
				Out:          w,
				ErrOut:       errW,
				ActiveTarget: activeTarget,
			}

			for {
				line, err := session.In.Readline()
				if err != nil {
					break
				}
				instruction := strings.TrimSpace(line)
				if instruction == "" {
					continue
				}
				lower := strings.ToLower(instruction)
				if lower == "exit" || lower == "quit" {
					break
				}

				if strings.HasPrefix(instruction, "/") {
					handled, shouldExit, slashErr := handleREPLSlashCommand(session, instruction)
					if slashErr != nil {
						fmt.Fprintf(session.ErrOut, "\n%s %v\n", ui.Red("Error:"), slashErr)
					}
					if handled {
						if shouldExit {
							break
						}
						continue
					}
				}

				ctx2, cancel := agentRequestContext(ctx, timeout)
				if err := session.Agent.Run(ctx2, instruction); err != nil {
					fmt.Fprintf(session.ErrOut, "\n%s %v\n", ui.Red("Error:"), err)
				}
				cancel()
				fmt.Fprintln(session.Out)
			}

			if historyPath != "" && a.Conversation().HistoryCount() > 0 {
				if err := a.Conversation().SaveToFile(historyPath); err != nil {
					fmt.Fprintf(errW, "warning: could not save conversation: %v\n", err)
				} else {
					fmt.Fprintf(errW, "Saved %d messages to conversation history.\n", a.Conversation().HistoryCount())
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&model, "model", "m", "", "Ollama model (default: chat.default_model or best installed)")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 5*time.Minute, "Per-request timeout")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 4096, "Conversation token budget")
	cmd.Flags().IntVar(&maxTurns, "max-turns", 10, "Maximum agent loop iterations per query")
	cmd.Flags().BoolVar(&autoApprove, "auto-approve", false, "Auto-approve safe commands (safety score < 70)")
	cmd.Flags().StringVar(&autonomy, "autonomy", "default", "Autonomy mode: default (prompt for mutations), edit (auto-approve file edits, prompt commands), full (auto-approve all but safety-blocked)")
	cmd.Flags().StringVar(&systemMsg, "system", "", "Extra text appended to system prompt")
	cmd.Flags().BoolVar(&resume, "resume", false, "Resume previous conversation from history")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Emit trace output for tool calls and turns")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Plan tool calls without executing them")
	cmd.Flags().StringVar(&provider, "provider", "auto", "Inference provider to use (local, cloud, auto)")
	cmd.Flags().StringVar(&cloudModel, "cloud-model", "", "Model name for cloud provider")
	cmd.Flags().StringVar(&cheapModel, "cheap-model", "", "Cheap/fast model for simple turns (enables multi-model routing; uses the same cloud provider as --cloud-model)")
	cmd.Flags().BoolVar(&allowRawCommandEvidence, "allow-raw-command-evidence", false, "Include raw command text in local backend evidence")
	cmd.Flags().BoolVarP(&selectModel, "select", "s", false, "Interactively select the model to use on startup")
	return cmd
}

type LineReader interface {
	Readline() (string, error)
}

type UnbufferedLineReader struct {
	reader io.Reader
}

func (u *UnbufferedLineReader) Readline() (string, error) {
	var buf []byte
	b := make([]byte, 1)
	for {
		n, err := u.reader.Read(b)
		if n > 0 {
			if b[0] == '\n' {
				if len(buf) > 0 && buf[len(buf)-1] == '\r' {
					buf = buf[:len(buf)-1]
				}
				return string(buf), nil
			}
			buf = append(buf, b[0])
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return string(buf), nil
			}
			return "", err
		}
	}
}

type agentREPLSession struct {
	Agent        *agent.Agent
	MCPRegistry  *mcpclient.Registry
	Runtime      func(context.Context) (*runtimectx.Context, error)
	Selector     ui.Selector
	In           LineReader
	Out          io.Writer
	ErrOut       io.Writer
	ActiveTarget ModelChoice
}

type REPLSelector struct {
	terminal ui.TerminalIO
	in       LineReader
	out      io.Writer
}

func (s *REPLSelector) Select(ctx context.Context, title string, options []ui.SelectOption) (ui.SelectResult, error) {
	if s.terminal.IsTTY() {
		res, err := ui.Select(ctx, s.terminal, title, options)
		if err == nil {
			return res, nil
		}
	}

	if !s.terminal.IsTTY() {
		fmt.Fprintln(s.out, title)
		for _, opt := range options {
			status := ""
			if opt.Disabled {
				status = " (disabled)"
			}
			lbl := ui.StripANSIAndControls(opt.Label)
			det := ui.StripANSIAndControls(opt.Detail)
			if det != "" {
				fmt.Fprintf(s.out, "  - %s: %s%s\n", lbl, det, status)
			} else {
				fmt.Fprintf(s.out, "  - %s%s\n", lbl, status)
			}
		}
		return ui.SelectResult{Selected: false}, nil
	}

	fmt.Fprintln(s.out, title)
	for i, opt := range options {
		status := ""
		if opt.Disabled {
			status = " (disabled)"
		}
		fmt.Fprintf(s.out, "  [%d] %s - %s%s\n", i+1, opt.Label, opt.Detail, status)
	}

	for {
		fmt.Fprint(s.out, "Enter choice number: ")
		line, err := s.in.Readline()
		if err != nil {
			return ui.SelectResult{Selected: false}, err
		}
		line = strings.TrimSpace(line)
		var choice int
		_, err = fmt.Sscanf(line, "%d", &choice)
		if err != nil || choice < 1 || choice > len(options) || options[choice-1].Disabled {
			fmt.Fprintln(s.out, "Invalid choice, please try again.")
			continue
		}
		return ui.SelectResult{
			ID:       options[choice-1].ID,
			Index:    choice - 1,
			Selected: true,
		}, nil
	}
}

// ModelChoice is an alias for agent.ModelTarget (canonical model identity).
type ModelChoice = agent.ModelTarget

// runPlainAgentREPL is the fallback scanner-based REPL when readline is unavailable.
func runPlainAgentREPL(ctx context.Context, a *agent.Agent, w, errW io.Writer, timeout time.Duration, historyPath string, mcpReg *mcpclient.Registry, activeTarget ModelChoice) error {
	fmt.Fprintln(errW, ui.Yellow("Note: using plain input mode (no arrow keys or history)"))
	inReader := &UnbufferedLineReader{reader: os.Stdin}

	session := &agentREPLSession{
		Agent:        a,
		MCPRegistry:  mcpReg,
		Runtime:      loadAgentShellRuntime,
		Selector:     &REPLSelector{terminal: ui.NewStdTerminal(os.Stdin, w), in: inReader, out: w},
		In:           inReader,
		Out:          w,
		ErrOut:       errW,
		ActiveTarget: activeTarget,
	}

	for {
		fmt.Fprint(session.ErrOut, ui.Cyan("✨ axis ❯ "))
		line, err := session.In.Readline()
		if err != nil {
			if err != io.EOF {
				return ExitCodeError{Code: ExitErrIO, Message: fmt.Sprintf("input stream closed: %v", err)}
			}
			break
		}
		instruction := strings.TrimSpace(line)
		if instruction == "" {
			continue
		}
		lower := strings.ToLower(instruction)
		if lower == "exit" || lower == "quit" {
			break
		}

		if strings.HasPrefix(instruction, "/") {
			handled, shouldExit, slashErr := handleREPLSlashCommand(session, instruction)
			if slashErr != nil {
				fmt.Fprintf(session.ErrOut, "\n%s %v\n", ui.Red("Error:"), slashErr)
			}
			if handled {
				if shouldExit {
					break
				}
				continue
			}
		}

		ctx2, cancel := agentRequestContext(ctx, timeout)
		if err := session.Agent.Run(ctx2, instruction); err != nil {
			fmt.Fprintf(session.ErrOut, "\n%s %v\n", ui.Red("Error:"), err)
		}
		cancel()
		fmt.Fprintln(session.Out)
	}

	if historyPath != "" && a.Conversation().HistoryCount() > 0 {
		_ = a.Conversation().SaveToFile(historyPath)
	}

	return nil
}

func handleREPLSlashCommand(session *agentREPLSession, line string) (bool, bool, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, false, nil
	}
	cmd := strings.ToLower(parts[0])

	a := session.Agent
	w := session.Out
	errW := session.ErrOut
	rt, _ := session.Runtime(context.Background())
	switch cmd {
	case "/exit", "/quit":
		return true, true, nil

	case "/help":
		fmt.Fprintln(errW, "Available commands:")
		fmt.Fprintln(errW, "  /help          Show this help message")
		fmt.Fprintln(errW, "  /clear         Clear conversation history (keep system prompt)")
		fmt.Fprintln(errW, "  /context       Show conversation token usage and limit")
		fmt.Fprintln(errW, "  /history       Show conversation turn summary")
		fmt.Fprintln(errW, "  /tools         List available tools")
		fmt.Fprintln(errW, "  /model <name>  Switch LLM model mid-session")
		fmt.Fprintln(errW, "  /models        List available models and switch interactively")
		fmt.Fprintln(errW, "  /mcp           Manage and view connected MCP servers")
		fmt.Fprintln(errW, "  /nodes         Show cluster nodes status")
		fmt.Fprintln(errW, "  /reservations  Show active ledger reservations")
		fmt.Fprintln(errW, "  /skills        Show learned skills from history")
		fmt.Fprintln(errW, "  /exit, /quit   Quit the session")
		return true, false, nil

	case "/nodes":
		ctx2, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cacheAddr := api.DefaultAddr()
		snap, source, err := collectStatusSnapshot(
			ctx2,
			true,  // cached
			false, // cachedOnly
			func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
				return fetchStatusSnapshot(ctx, cacheAddr)
			},
			loadStatusLiveSnapshot,
		)
		cancel()
		if err != nil {
			return true, false, fmt.Errorf("failed to load cluster status: %w", err)
		}
		if snap == nil || len(snap.Nodes) == 0 {
			fmt.Fprintln(errW, ui.Yellow("No nodes found in cluster snapshot."))
			return true, false, nil
		}
		var listItems []NodeListItem
		for _, n := range snap.Nodes {
			var ramTotal, ramFree int
			var pressure string
			var gpus []string
			if n.Resources != nil {
				ramTotal = int(n.Resources.RAMTotalMB)
				ramFree = int(n.Resources.RAMFreeMB)
				pressure = string(n.Resources.Pressure)
				for _, g := range n.Resources.GPUs {
					gpus = append(gpus, g.Model)
				}
			}
			listItems = append(listItems, NodeListItem{
				Name:     n.Name,
				Status:   string(n.Status),
				OS:       n.OS,
				Arch:     n.Arch,
				RAMTotal: ramTotal,
				RAMFree:  ramFree,
				Pressure: pressure,
				GPUs:     gpus,
				IsLocal:  models.IsLocalNode(n),
				Reserved: n.RAMReservedMB,
			})
		}
		fmt.Fprintf(w, "Snapshot Source: %s\n", source)
		if !snap.Timestamp.IsZero() {
			fmt.Fprintf(w, "Snapshot Age: %v\n", time.Since(snap.Timestamp).Round(time.Second))
		}
		fmt.Fprint(w, RenderNodeTable(listItems))
		return true, false, nil

	case "/reservations":
		freshRt, err := runtimectx.Load(context.Background())
		var items []ReservationListItem
		daemonFetched := false
		cacheAddr := api.DefaultAddr()
		client, baseURLAddr := daemon.HttpClientForAddr(cacheAddr)
		baseURL := daemon.NormalizeAddr(baseURLAddr)
		if token, tokenErr := auth.LoadOrGenerateToken(); tokenErr == nil {
			ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, reqErr := http.NewRequestWithContext(ctx2, http.MethodGet, baseURL+"/v2/reservations", nil)
			if reqErr == nil {
				req.Header.Set("Authorization", "Bearer "+token)
				resp, respErr := client.Do(req)
				if respErr == nil {
					defer resp.Body.Close()
					if resp.StatusCode == 200 {
						var result struct {
							Entries []reservation.Entry `json:"reservations"`
						}
						if json.NewDecoder(resp.Body).Decode(&result) == nil {
							daemonFetched = true
							now := time.Now()
							limits := reservation.DefaultLimits()
							for _, e := range result.Entries {
								items = append(items, ReservationListItem{
									ID:      e.ID,
									Node:    e.Node,
									RAMMB:   e.RAMMB,
									Owner:   e.OwnerSurface,
									Age:     now.Sub(e.CreatedAt),
									IsStale: e.ClassifyLiveness(now, limits) != reservation.LivenessActive,
								})
							}
						}
					}
				}
			}
		}

		if !daemonFetched {
			if err != nil {
				return true, false, fmt.Errorf("failed to load cluster status fallback: %w", err)
			}
			if freshRt == nil {
				return true, false, fmt.Errorf("failed to load cluster status fallback: runtime context is nil")
			}

			if freshRt.Ledger != nil {
				now := time.Now()
				limits := reservation.DefaultLimits()
				for _, e := range freshRt.Ledger.Entries() {
					items = append(items, ReservationListItem{
						ID:      e.ID,
						Node:    e.Node,
						RAMMB:   e.RAMMB,
						Owner:   e.OwnerSurface,
						Age:     now.Sub(e.CreatedAt),
						IsStale: e.ClassifyLiveness(now, limits) != reservation.LivenessActive,
					})
				}
			}
		}

		fmt.Fprint(w, RenderReservationTable(items))
		return true, false, nil

	case "/skills":
		freshRt, err := runtimectx.Load(context.Background())
		if err != nil {
			return true, false, fmt.Errorf("failed to load skills: %w", err)
		}
		if freshRt == nil {
			return true, false, fmt.Errorf("failed to load skills: runtime context is nil")
		}
		if freshRt.Skills == nil || len(freshRt.Skills.Skills) == 0 {
			fmt.Fprintln(w, "\nLearned skills:")
			fmt.Fprintln(w, ui.DimColor.Sprint("  No learned skills yet\n"))
			return true, false, nil
		}
		tbl := ui.NewTable("ID", "DESCRIPTION", "COMMAND", "SUCCESS", "BEST NODE", "LAST USED")
		for _, s := range freshRt.Skills.Skills {
			bestNode := s.PreferredNode
			if bestNode == "" && len(s.NodeCount) > 0 {
				maxVal := 0
				for n, val := range s.NodeCount {
					if val > maxVal {
						maxVal = val
						bestNode = n
					} else if val == maxVal {
						if bestNode == "" || n < bestNode {
							bestNode = n
						}
					}
				}
			}
			if bestNode == "" {
				bestNode = "-"
			}
			tbl.AddRow(
				s.ID,
				s.Description,
				s.Command,
				fmt.Sprintf("%d", s.SuccessCount),
				bestNode,
				s.LastUsed.Format(time.RFC3339),
			)
		}
		fmt.Fprintln(w, "\nLearned skills:")
		tbl.Render(w)
		fmt.Fprintln(w)

		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return true, false, nil
		}

		var skillOptions []ui.SelectOption
		skillOptions = append(skillOptions, ui.SelectOption{
			ID:     "none",
			Label:  "Cancel (do not run any skill)",
			Detail: "",
		})
		for _, s := range freshRt.Skills.Skills {
			skillOptions = append(skillOptions, ui.SelectOption{
				ID:     s.ID,
				Label:  s.Description,
				Detail: fmt.Sprintf("Command: %s", s.Command),
			})
		}

		res, err := session.Selector.Select(context.Background(), "Execute a learned skill:", skillOptions)
		if err != nil {
			return true, false, err
		}
		if !res.Selected || res.ID == "none" {
			return true, false, nil
		}

		var chosenCommand string
		for _, s := range freshRt.Skills.Skills {
			if s.ID == res.ID {
				chosenCommand = s.Command
				break
			}
		}

		if chosenCommand != "" {
			fmt.Fprintf(w, "\nRunning skill command: %s\n", ui.Bold(chosenCommand))
			ctx2, cancel := agentRequestContext(context.Background(), 5*time.Minute)
			defer cancel()
			if err := session.Agent.Run(ctx2, chosenCommand); err != nil {
				fmt.Fprintf(session.ErrOut, "\n%s %v\n", ui.Red("Error:"), err)
			}
			fmt.Fprintln(session.Out)
		}
		return true, false, nil

	case "/models", "/model":
		choices := collectModelChoices(rt)

		if cmd == "/model" && len(parts) >= 2 {
			ref := strings.Join(parts[1:], " ")
			chosen, err := findModelTargetByRef(choices, ref)
			if err != nil {
				return true, false, err
			}
			if err := switchAgentToModelChoice(session, chosen); err != nil {
				return true, false, err
			}
			return true, false, nil
		}

		if len(choices) == 0 {
			fmt.Fprintln(w, "No models found (neither local Ollama models nor enabled cloud providers).")
			return true, false, nil
		}

		var selectOptions []ui.SelectOption
		for _, choice := range choices {
			detail := fmt.Sprintf("%s - %s", choice.ProviderName, choice.ProviderKind)
			if choice.ProviderKind == "local" {
				if choice.Node != "" {
					detail = fmt.Sprintf("Remote node %s [%s] (%s)", choice.Node, choice.ProviderName, choice.Endpoint)
				} else {
					detail = fmt.Sprintf("Local node [%s] (%s)", choice.ProviderName, choice.Endpoint)
				}
			}

			disabled := choice.Disabled
			if choice.ProviderKind == "local" && choice.Node != "" && choice.Endpoint == "" {
				disabled = true
				detail += " (unsupported: no valid IP/hostname)"
			} else if choice.ProviderKind == "local" && disabled {
				detail += " (unreachable)"
			}

			selectOptions = append(selectOptions, ui.SelectOption{
				ID:       choice.ID,
				Label:    choice.Model,
				Detail:   detail,
				Disabled: disabled,
			})
		}

		res, err := session.Selector.Select(context.Background(), "Select active model for task routing:", selectOptions)
		if err != nil {
			return true, false, err
		}
		if !res.Selected {
			return true, false, nil
		}

		var chosen ModelChoice
		for _, c := range choices {
			if c.ID == res.ID {
				chosen = c
				break
			}
		}

		err = switchAgentToModelChoice(session, chosen)
		if err != nil {
			return true, false, err
		}
		return true, false, nil

	case "/mcp":
		mcpReg := session.MCPRegistry
		if mcpReg == nil || len(mcpReg.Names()) == 0 {
			fmt.Fprintln(errW, "No MCP servers configured or connected.")
			return true, false, nil
		}

		for {
			names := mcpReg.Names()
			var serverOptions []ui.SelectOption
			for _, name := range names {
				s := mcpReg.Get(name)
				status := "[not initialized]"
				if s.Err != nil {
					status = "[failed]"
				} else if s.InitResult != nil {
					status = "[ready]"
				}
				serverOptions = append(serverOptions, ui.SelectOption{
					ID:     name,
					Label:  name,
					Detail: fmt.Sprintf("Transport: %s %s", s.Transport, status),
				})
			}

			serverIdx, err := session.Selector.Select(context.Background(), "Select an MCP Server:", serverOptions)
			if err != nil {
				return true, false, err
			}
			if !serverIdx.Selected {
				return true, false, nil
			}

			sc := mcpReg.Get(names[serverIdx.Index])

			for {
				actions := []ui.SelectOption{
					{ID: "tools", Label: "List Tools", Detail: "Show all tools exposed by this server"},
					{ID: "resources", Label: "List Resources", Detail: "Show all data resources exposed by this server"},
					{ID: "diagnostics", Label: "Show Server Status & Diagnostics", Detail: "Run a live ping and show connection details"},
					{ID: "back", Label: "Back", Detail: "Return to the server menu"},
				}

				actionIdx, err := session.Selector.Select(context.Background(), fmt.Sprintf("MCP Server %q Actions:", sc.Name), actions)
				if err != nil {
					return true, false, err
				}
				if !actionIdx.Selected || actionIdx.ID == "back" {
					break
				}

				switch actionIdx.ID {
				case "tools":
					tools := sc.CachedTools()
					if len(tools) == 0 {
						fmt.Fprintln(w, "\nNo tools exposed by this server.")
					} else {
						fmt.Fprintf(w, "\nTools exposed by %s:\n", sc.Name)
						tbl := ui.NewTable("TOOL NAME", "SAFETY", "DESCRIPTION")
						for _, t := range tools {
							name := sanitizeDiagnosticsText(t.Name)
							desc := sanitizeDiagnosticsText(t.Description)

							safety := ui.YellowColor.Sprint("Execute")
							if agent.IsReadOnlyTool(name) || agent.IsReadOnlyTool("mcp_"+sc.Name+"_"+name) {
								safety = ui.GreenColor.Sprint("Read-Only")
							}

							tbl.AddRow(name, safety, desc)
						}
						tbl.Render(w)
						fmt.Fprintln(w)
					}
				case "resources":
					resources := sc.CachedResources()
					if len(resources) == 0 {
						fmt.Fprintln(w, "\nNo resources exposed by this server.")
					} else {
						fmt.Fprintf(w, "\nResources exposed by %s:\n", sc.Name)
						tbl := ui.NewTable("RESOURCE NAME", "URI", "DESCRIPTION")
						for _, r := range resources {
							name := sanitizeDiagnosticsText(r.Name)
							uri := sanitizeDiagnosticsText(r.URI)
							desc := sanitizeDiagnosticsText(r.Description)
							tbl.AddRow(name, uri, desc)
						}
						tbl.Render(w)
						fmt.Fprintln(w)
					}
				case "diagnostics":
					fmt.Fprintf(w, "\nMCP Server Details: %s\n", sanitizeDiagnosticsText(sc.Name))
					fmt.Fprintf(w, "  Transport: %s\n", sanitizeDiagnosticsText(sc.Transport))
					if sc.InitResult != nil {
						fmt.Fprintf(w, "  Initial Handshake: \033[32msuccess\033[0m\n")
						fmt.Fprintf(w, "    Protocol Version: %s\n", sanitizeDiagnosticsText(sc.InitResult.ProtocolVersion))
						if sc.InitResult.ServerInfo.Name != "" {
							sName := sanitizeDiagnosticsText(sc.InitResult.ServerInfo.Name)
							sVer := sanitizeDiagnosticsText(sc.InitResult.ServerInfo.Version)
							fmt.Fprintf(w, "    Server Name:      %s (%s)\n", sName, sVer)
						}
					} else {
						fmt.Fprintf(w, "  Initial Handshake: \033[31mfailed / not initialized\033[0m\n")
					}

					pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
					var pingErr error
					var pingDur time.Duration
					if sc.Client != nil {
						start := time.Now()
						pingErr = sc.Client.Ping(pingCtx)
						pingDur = time.Since(start)
					} else {
						pingErr = fmt.Errorf("client is nil")
					}
					pingCancel()

					fmt.Fprintf(w, "  Live Probe (Ping):\n")
					if pingErr == nil {
						fmt.Fprintf(w, "    Status:   \033[32mconnected\033[0m (latency: %v)\n", pingDur)
					} else {
						pErrStr := sanitizeDiagnosticsText(pingErr.Error())
						fmt.Fprintf(w, "    Status:   \033[31mfailed / unreachable\033[0m (%s)\n", pErrStr)
					}

					if !sc.ConnectedAt().IsZero() {
						fmt.Fprintf(w, "    Handshake Time:   %s\n", sc.ConnectedAt().Format(time.RFC3339))
					}
					fmt.Fprintf(w, "    Probe Time:       %s\n", time.Now().Format(time.RFC3339))
					fmt.Fprintln(w)
				}
			}
		}

	case "/clear":
		a.Conversation().Clear()
		fmt.Fprintln(errW, "Conversation history cleared (system prompt kept).")
		return true, false, nil

	case "/context":
		tokens := a.ContextTokens()
		limit := a.MaxTokens()
		pct := 0.0
		if limit > 0 {
			pct = float64(tokens) / float64(limit)
		}

		barWidth := 20
		filled := int(pct * float64(barWidth))
		if filled > barWidth {
			filled = barWidth
		}
		if filled < 0 {
			filled = 0
		}

		barStr := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

		var coloredBar string
		if pct <= 0.60 {
			coloredBar = ui.GreenColor.Sprint(barStr)
		} else if pct <= 0.85 {
			coloredBar = ui.YellowColor.Sprint(barStr)
		} else {
			coloredBar = ui.RedColor.Sprint(barStr)
		}

		fmt.Fprintf(errW, "\nConversation Context Budget:\n")
		fmt.Fprintf(errW, "  [%s] %.1f%% (Tokens used: %d / %d budget)\n\n", coloredBar, pct*100, tokens, limit)
		return true, false, nil

	case "/history":
		msgs := a.Conversation().Messages()
		fmt.Fprintf(errW, "\nConversation History (%d message(s)):\n", len(msgs))
		for i, m := range msgs {
			short := m.Content
			runes := []rune(short)
			if len(runes) > 60 {
				short = string(runes[:57]) + "..."
			} else {
				short = string(runes)
			}
			short = strings.ReplaceAll(short, "\n", " ")

			var roleLabel string
			switch strings.ToLower(m.Role) {
			case "user":
				roleLabel = ui.CyanColor.Sprint("user")
			case "assistant":
				roleLabel = ui.GreenColor.Sprint("assistant")
			case "system":
				roleLabel = ui.WhiteColor.Sprint("system")
			case "tool":
				roleLabel = ui.YellowColor.Sprint("tool")
			default:
				roleLabel = m.Role
			}

			fmt.Fprintf(errW, "  [%d] %s: %s\n", i, roleLabel, short)
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					fmt.Fprintf(errW, "      %s %s\n", ui.Dim("→ Tool call:"), ui.Bold(tc.Function.Name))
				}
			}
		}
		fmt.Fprintln(errW)
		return true, false, nil

	case "/tools":
		defs := a.ToolDefs()
		if len(defs) == 0 {
			fmt.Fprintln(w, "\nNo tools registered.")
			return true, false, nil
		}
		tbl := ui.NewTable("TOOL NAME", "SAFETY", "DESCRIPTION")
		for _, d := range defs {
			safety := ui.YellowColor.Sprint("Execute")
			if agent.IsReadOnlyTool(d.Function.Name) {
				safety = ui.GreenColor.Sprint("Read-Only")
			}
			tbl.AddRow(d.Function.Name, safety, d.Function.Description)
		}
		fmt.Fprintln(w, "\nAvailable Tools:")
		tbl.Render(w)
		fmt.Fprintln(w)
		return true, false, nil
	}
	return false, false, nil
}

func agentRequestContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func guardedAgentShellRunner(model string) agent.ShellRunner {
	return func(ctx context.Context, command string) (string, error) {
		rt, err := loadAgentShellRuntime(ctx)
		if err != nil {
			return "", fmt.Errorf("load runtime context for guarded execution: %w", err)
		}

		var localNodeName string
		if rt.Snapshot != nil {
			if localNode, ok := models.FindLocalNode(rt.Snapshot.Nodes); ok {
				localNodeName = localNode.Name
			}
		}
		if localNodeName == "" {
			return "", fmt.Errorf("local node resolution failed: could not identify canonical local node name from snapshot")
		}

		req := execution.GuardedExecutionRequest{
			Description:      command,
			Mode:             execution.ModeExec,
			Confirm:          execution.ConfirmWord,
			RequestedNode:    localNodeName,
			OwnerSurface:     execution.OwnerSurfaceAgentRunShell,
			OwnerLabel:       strings.TrimSpace(model),
			Events:           events.GuardedExecutionSink{},
			BuildContextJSON: knowledge.ExecutionContextJSON,
			OnStateChange: func(_ context.Context, trigger string, _ execution.GuardedExecutionResult) {
				scheduleAgentDaemonRefresh(trigger)
			},
		}

		if resp, usedDaemon, err := tryGuardedAgentShellViaDaemon(ctx, rt, req); usedDaemon {
			if err != nil {
				return "", fmt.Errorf("daemon guarded execution: %w", err)
			}
			return marshalGuardedExecutionPayload(resp, nil)
		}

		resp, runErr := runGuardedAgentShell(ctx, rt, req)
		return marshalGuardedExecutionPayload(resp, runErr)
	}
}

func tryGuardedAgentShellViaDaemon(ctx context.Context, rt *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.GuardedExecutionResult, bool, error) {
	addr := agentDaemonExecutionAddr()
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if _, err := fetchAgentDaemonMeta(probeCtx, addr); err != nil {
		return execution.GuardedExecutionResult{}, false, nil
	}

	resp, err := runDaemonGuardedAgentShell(ctx, addr, req, execution.LocalExecutionOrigin(rt))
	return resp, true, err
}

func marshalGuardedExecutionPayload(resp execution.GuardedExecutionResult, runErr error) (string, error) {
	if runErr != nil && resp.Error == "" {
		resp.Error = runErr.Error()
	}
	if resp.Error != "" {
		resp.OK = false
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("marshal guarded execution result: %w", err)
	}
	return string(payload), nil
}

func scheduleAgentDaemonRefresh(trigger string) {
	scheduleBestEffortDaemonRefresh("agent", trigger, signalAgentDaemonRefresh)
}

func guardedAgentTaskRunner() agent.TaskRunner {
	return func(ctx context.Context, prepared execution.PreparedExecution) (string, error) {
		prepared.Request.OnStateChange = func(_ context.Context, trigger string, _ execution.GuardedExecutionResult) {
			scheduleAgentDaemonRefresh(trigger)
		}
		resp, runErr := execution.RunPreparedExecution(ctx, prepared)
		return marshalGuardedExecutionPayload(resp, runErr)
	}
}

// resolveNodeEndpoint determines the reachable HTTP endpoint for a given node
// and port. If port is 0, it defaults to 11434. It prioritizes the explicitly
// configured SSHTarget (since that is the known dial route), followed by
// private LAN addresses, over other overlay/docker interfaces.
func resolveNodeEndpoint(n models.NodeFacts, port int) (string, error) {
	if port <= 0 {
		port = 11434
	}

	if models.IsLocalNode(n) {
		return fmt.Sprintf("http://localhost:%d", port), nil
	}

	var targetHost string

	// 1. Prefer the configured dial target if it's a parseable IP.
	if n.SSHTarget != "" {
		if _, err := netip.ParseAddr(n.SSHTarget); err == nil {
			targetHost = n.SSHTarget
		}
	}

	// 2. Fallback to searching addresses, preferring private LAN.
	if targetHost == "" {
		for _, addr := range n.Addresses {
			if addr.Scope != "link-local" && addr.Address != "" {
				if ip, err := netip.ParseAddr(addr.Address); err == nil {
					if isPrivateLAN(ip) {
						targetHost = addr.Address
						break
					}
					// keep first valid address if no LAN found
					if targetHost == "" {
						targetHost = addr.Address
					}
				}
			}
		}
	}

	// 3. Absolute fallback to hostname (machine name).
	if targetHost == "" {
		targetHost = n.Hostname
	}

	if targetHost == "" {
		return "", fmt.Errorf("remote node %s has no valid network address or hostname", n.Name)
	}

	return fmt.Sprintf("http://%s:%d", targetHost, port), nil
}

func isPrivateLAN(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	if ip.Is4() {
		return ip.IsPrivate()
	}
	// IPv6 ULA (fc00::/7)
	b := ip.As16()
	return b[0]&0xfe == 0xfc
}

func switchAgentToModelChoice(session *agentREPLSession, choice ModelChoice) error {
	opts, err := cloudOptsForTarget(session.Runtime, &choice)
	if err != nil {
		return err
	}
	backend, err := agent.BuildBackend(choice, opts)
	if err != nil {
		return err
	}
	session.Agent.SetBackend(backend, choice.SecurityClass)
	session.Agent.SetModel(choice.Model)
	session.ActiveTarget = choice
	errW := session.ErrOut
	switch choice.Protocol {
	case agent.ProtocolCloud:
		fmt.Fprintf(errW, "Switched to cloud provider %q, model %q\n", choice.ProviderName, choice.Model)
	case agent.ProtocolOpenAI:
		if choice.Node != "" {
			fmt.Fprintf(errW, "Switched to %s model %q on node %q (%s)\n", choice.ProviderName, choice.Model, choice.Node, choice.Endpoint)
		} else {
			fmt.Fprintf(errW, "Switched to %s model %q (%s)\n", choice.ProviderName, choice.Model, choice.Endpoint)
		}
	default:
		if choice.Node != "" {
			fmt.Fprintf(errW, "Switched to model %q on remote node %q (%s)\n", choice.Model, choice.Node, choice.Endpoint)
		} else {
			fmt.Fprintf(errW, "Switched to local Ollama model %q (%s)\n", choice.Model, choice.Endpoint)
		}
	}
	return nil
}

// cloudOptsForTarget resolves cloud credentials when target.Protocol is cloud.
// target is a pointer so provider-name casing normalization is retained by callers.
func cloudOptsForTarget(loadRT func(context.Context) (*runtimectx.Context, error), target *ModelChoice) (agent.CloudBackendOptions, error) {
	var opts agent.CloudBackendOptions
	if target == nil {
		return opts, fmt.Errorf("cloud target is nil")
	}
	if target.Protocol != agent.ProtocolCloud {
		return opts, nil
	}
	if loadRT == nil {
		return opts, fmt.Errorf("runtime loader required for cloud model %q", target.Model)
	}
	rt, err := loadRT(context.Background())
	if err != nil {
		return opts, fmt.Errorf("load runtime for cloud provider: %w", err)
	}
	if rt == nil || rt.Config == nil {
		return opts, fmt.Errorf("cloud provider %q not configured", target.ProviderName)
	}
	pCfg, ok := rt.Config.AIProviders[target.ProviderName]
	if !ok {
		for name, p := range rt.Config.AIProviders {
			if strings.EqualFold(name, target.ProviderName) {
				pCfg = p
				ok = true
				target.ProviderName = name
				break
			}
		}
	}
	if !ok {
		return opts, fmt.Errorf("cloud provider %q not configured", target.ProviderName)
	}
	key, err := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
	if err != nil || key == "" {
		return opts, fmt.Errorf("API key for cloud provider %q not found or empty", target.ProviderName)
	}
	opts.APIKey = key
	opts.ProviderKind = pCfg.Kind
	if m, found := findModelInProvider(pCfg, target.Model); found {
		opts.CostPer1K = m.CostPer1K
		// Prefer the canonical configured name for telemetry consistency.
		target.Model = m.Name
	}
	return opts, nil
}

// findModelInProvider matches a model name or alias within a provider config.
func findModelInProvider(pCfg config.AIProviderConfig, name string) (config.AIModelConfig, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.AIModelConfig{}, false
	}
	for _, m := range pCfg.Models {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
		for _, alias := range m.Aliases {
			if strings.EqualFold(alias, name) {
				return m, true
			}
		}
	}
	return config.AIModelConfig{}, false
}

// credentialedCloudProvider is an enabled cloud provider with a resolvable API key.
type credentialedCloudProvider struct {
	name string
	cfg  config.AIProviderConfig
	key  string
}

// listCredentialedCloudProviders returns enabled cloud providers that have a
// non-empty API key, ordered by Priority descending then provider name ascending.
func listCredentialedCloudProviders(rt *runtimectx.Context) []credentialedCloudProvider {
	if rt == nil || rt.Config == nil {
		return nil
	}
	var out []credentialedCloudProvider
	for pName, pCfg := range rt.Config.AIProviders {
		if !pCfg.Enabled || !strings.EqualFold(pCfg.Type, "cloud") {
			continue
		}
		key, err := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
		if err != nil || key == "" {
			continue
		}
		out = append(out, credentialedCloudProvider{name: pName, cfg: pCfg, key: key})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].cfg.Priority != out[j].cfg.Priority {
			return out[i].cfg.Priority > out[j].cfg.Priority
		}
		return out[i].name < out[j].name
	})
	return out
}

// cheapestModelInProvider picks the lowest positive CostPer1K model; if none
// have a positive cost, the first named model is used.
func cheapestModelInProvider(pCfg config.AIProviderConfig) (config.AIModelConfig, bool) {
	var best config.AIModelConfig
	var found bool
	bestCost := 0.0
	for _, m := range pCfg.Models {
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		if !found {
			best = m
			found = true
			if m.CostPer1K > 0 {
				bestCost = m.CostPer1K
			}
			continue
		}
		if m.CostPer1K > 0 && (bestCost == 0 || m.CostPer1K < bestCost) {
			best = m
			bestCost = m.CostPer1K
		}
	}
	return best, found
}

func cloudChoiceFromProvider(p credentialedCloudProvider, m config.AIModelConfig) ModelChoice {
	return ModelChoice{
		ID:            fmt.Sprintf("cloud:%s:%s", p.name, m.Name),
		Model:         m.Name,
		Protocol:      agent.ProtocolCloud,
		ProviderName:  p.name,
		ProviderKind:  "cloud",
		Endpoint:      p.cfg.Endpoint,
		SecurityClass: agent.BackendRemote,
	}
}

func cloudOptsFromProvider(p credentialedCloudProvider, m config.AIModelConfig) agent.CloudBackendOptions {
	return agent.CloudBackendOptions{
		ProviderKind: p.cfg.Kind,
		APIKey:       p.key,
		CostPer1K:    m.CostPer1K,
	}
}

// listValidCloudModelChoices returns provider-qualified model ids for error messages.
func listValidCloudModelChoices(providers []credentialedCloudProvider) []string {
	var ids []string
	for _, p := range providers {
		for _, m := range p.cfg.Models {
			if strings.TrimSpace(m.Name) == "" {
				continue
			}
			ids = append(ids, fmt.Sprintf("%s:%s", p.name, m.Name))
		}
	}
	sort.Strings(ids)
	return ids
}

// resolveCloudStartupTarget selects a cloud ModelTarget using credential-valid
// providers ordered by Priority. When requestedModel is non-empty it must match
// a configured model (name or alias) or an error is returned.
func resolveCloudStartupTarget(rt *runtimectx.Context, requestedModel string) (ModelChoice, agent.CloudBackendOptions, error) {
	providers := listCredentialedCloudProviders(rt)
	if len(providers) == 0 {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("no enabled cloud providers with valid API keys found in config")
	}
	req := strings.TrimSpace(requestedModel)
	if req != "" {
		// Support "provider:model" qualified form as well as bare model name.
		if prov, model, ok := splitProviderModel(req); ok {
			for _, p := range providers {
				if !strings.EqualFold(p.name, prov) {
					continue
				}
				if m, found := findModelInProvider(p.cfg, model); found {
					return cloudChoiceFromProvider(p, m), cloudOptsFromProvider(p, m), nil
				}
			}
		}
		for _, p := range providers {
			if m, found := findModelInProvider(p.cfg, req); found {
				return cloudChoiceFromProvider(p, m), cloudOptsFromProvider(p, m), nil
			}
		}
		valid := listValidCloudModelChoices(providers)
		if len(valid) == 0 {
			return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("cloud model %q not found; no models configured on credentialed providers", req)
		}
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf(
			"cloud model %q not found among credentialed providers; valid choices: %s",
			req, strings.Join(valid, ", "))
	}

	// No explicit model: highest-priority provider, then cheapest model on that provider.
	p := providers[0]
	m, ok := cheapestModelInProvider(p.cfg)
	if !ok {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("no models configured for cloud provider %q", p.name)
	}
	return cloudChoiceFromProvider(p, m), cloudOptsFromProvider(p, m), nil
}

// splitProviderModel parses "provider:model" (exactly one colon separating non-empty parts).
func splitProviderModel(ref string) (provider, model string, ok bool) {
	ref = strings.TrimSpace(ref)
	i := strings.Index(ref, ":")
	if i <= 0 || i >= len(ref)-1 {
		return "", "", false
	}
	// Avoid treating model tags like "llama3.2:latest" as provider-qualified when
	// the left side is not a known provider — callers try provider match first,
	// then bare-name match. Here we only split; matchers verify provider exists.
	return ref[:i], ref[i+1:], true
}

// resolveCheapCloudTarget builds a cheap-routing target on the same provider as
// primary, re-resolving cost from config. The cheap model must be configured on
// that provider (name or alias).
func resolveCheapCloudTarget(rt *runtimectx.Context, primary ModelChoice, cheapModel string) (ModelChoice, agent.CloudBackendOptions, error) {
	cheapModel = strings.TrimSpace(cheapModel)
	if cheapModel == "" {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("cheap model name is empty")
	}
	if primary.Protocol != agent.ProtocolCloud || primary.ProviderName == "" {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("cheap-model requires an active cloud provider")
	}
	if rt == nil || rt.Config == nil {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("runtime config required for cheap-model")
	}
	pCfg, ok := rt.Config.AIProviders[primary.ProviderName]
	if !ok {
		for name, p := range rt.Config.AIProviders {
			if strings.EqualFold(name, primary.ProviderName) {
				pCfg = p
				ok = true
				primary.ProviderName = name
				break
			}
		}
	}
	if !ok {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("cloud provider %q not configured", primary.ProviderName)
	}
	m, found := findModelInProvider(pCfg, cheapModel)
	if !found {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf(
			"cheap-model %q is not configured on provider %q", cheapModel, primary.ProviderName)
	}
	key, err := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
	if err != nil || key == "" {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("API key for cloud provider %q not found or empty", primary.ProviderName)
	}
	target := ModelChoice{
		ID:            fmt.Sprintf("cloud:%s:%s", primary.ProviderName, m.Name),
		Model:         m.Name,
		Protocol:      agent.ProtocolCloud,
		ProviderName:  primary.ProviderName,
		ProviderKind:  "cloud",
		Endpoint:      pCfg.Endpoint,
		SecurityClass: agent.BackendRemote,
	}
	opts := agent.CloudBackendOptions{
		ProviderKind: pCfg.Kind,
		APIKey:       key,
		CostPer1K:    m.CostPer1K,
	}
	return target, opts, nil
}

// effectiveStartupRequestedModel returns the model name that should drive startup
// selection when --model is empty: chat.default_model, then warm-resident preferred.
// Returns "" when neither is available so the catalog can pick first usable local.
func effectiveStartupRequestedModel(flag string, rt *runtimectx.Context) string {
	if s := strings.TrimSpace(flag); s != "" {
		return s
	}
	// Prefer runtime config when available (tests inject rt.Config).
	if rt != nil && rt.Config != nil && rt.Config.Chat != nil {
		if s := strings.TrimSpace(rt.Config.Chat.DefaultModel); s != "" {
			return s
		}
	} else {
		if cfg, err := config.Load(config.DefaultConfigPath()); err == nil && cfg.Chat != nil {
			if s := strings.TrimSpace(cfg.Chat.DefaultModel); s != "" {
				return s
			}
		}
	}
	if rt != nil && rt.Snapshot != nil {
		var allInstalled []string
		for _, node := range rt.Snapshot.Nodes {
			for _, m := range node.ResidentModels {
				allInstalled = append(allInstalled, m.Name)
			}
		}
		if best, ok := chat.ChoosePreferredModel(allInstalled); ok {
			return best
		}
	}
	return ""
}

func syntheticLocalOllamaTarget(name string) ModelChoice {
	return ModelChoice{
		ID:            "local:ollama:" + name,
		Model:         name,
		Protocol:      agent.ProtocolOllama,
		ProviderName:  "ollama",
		ProviderKind:  "local",
		Endpoint:      chat.DefaultEndpoint,
		SecurityClass: agent.BackendLocal,
	}
}

// findModelTargetByRef resolves /model <ref> against the catalog.
// Prefer exact ID, then unique model name among non-disabled choices.
func findModelTargetByRef(choices []ModelChoice, ref string) (ModelChoice, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ModelChoice{}, fmt.Errorf("model name is empty")
	}
	for _, c := range choices {
		if c.ID == ref && !c.Disabled {
			return c, nil
		}
	}
	var matches []ModelChoice
	for _, c := range choices {
		if !c.Disabled && strings.EqualFold(c.Model, ref) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		var ids []string
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		return ModelChoice{}, fmt.Errorf("model %q is ambiguous; specify an id: %s", ref, strings.Join(ids, ", "))
	}
	return ModelChoice{}, fmt.Errorf("model %q not found in catalog; use /models to list choices or ollama pull %s", ref, ref)
}

// resolveStartupModelTarget picks the active ModelTarget for a new agent session.
// explicitTarget, when non-nil, is the interactive selection (always wins).
// requestedModel should be the effective operator request (explicit --model,
// chat.default_model, or warm preferred) — not only the raw flag.
//
// Policy for provider=auto: prefer reachable local/remote ollama (or openai-local)
// over cloud; cloud only when no usable local target exists. Explicit --provider
// local/cloud and interactive selection are never overridden by auto-cloud.
// Explicit --cloud-model always requires a credentialed configured match (no silent fallback).
func resolveStartupModelTarget(
	requestedModel, providerFlag, cloudModelFlag string,
	explicit *ModelChoice,
	rt *runtimectx.Context,
	choices []ModelChoice,
) (ModelChoice, agent.CloudBackendOptions, error) {
	providerMode := strings.ToLower(strings.TrimSpace(providerFlag))
	if providerMode == "" {
		providerMode = "auto"
	}

	if explicit != nil && explicit.Model != "" {
		opts, err := cloudOptsForTarget(func(context.Context) (*runtimectx.Context, error) { return rt, nil }, explicit)
		return *explicit, opts, err
	}

	// Catalog helpers
	usable := func(c ModelChoice) bool { return !c.Disabled && c.Model != "" }
	firstLocal := func() (ModelChoice, bool) {
		for _, c := range choices {
			if usable(c) && c.ProviderKind == "local" && c.Protocol == agent.ProtocolOllama {
				return c, true
			}
		}
		for _, c := range choices {
			if usable(c) && c.ProviderKind == "local" {
				return c, true
			}
		}
		return ModelChoice{}, false
	}
	matchLocalModel := func(name string) (ModelChoice, bool) {
		var matches []ModelChoice
		for _, c := range choices {
			if usable(c) && c.ProviderKind == "local" && strings.EqualFold(c.Model, name) {
				matches = append(matches, c)
			}
		}
		if len(matches) == 1 {
			return matches[0], true
		}
		if len(matches) > 1 {
			// Prefer local node ollama
			for _, c := range matches {
				if c.Node == "" && c.Protocol == agent.ProtocolOllama {
					return c, true
				}
			}
			return matches[0], true
		}
		return ModelChoice{}, false
	}

	reqModel := strings.TrimSpace(requestedModel)
	cloudReq := strings.TrimSpace(cloudModelFlag)

	switch providerMode {
	case "cloud":
		// Only --cloud-model is an explicit cloud model request. When empty, select the
		// highest-priority credentialed provider and its cheapest configured model.
		// Do not treat chat.default_model / local preferred names as cloud model names.
		return resolveCloudStartupTarget(rt, cloudReq)

	case "local":
		if reqModel != "" {
			if t, ok := matchLocalModel(reqModel); ok {
				return t, agent.CloudBackendOptions{}, nil
			}
			// Explicit local model not in catalog: bind to default local ollama endpoint
			return syntheticLocalOllamaTarget(reqModel), agent.CloudBackendOptions{}, nil
		}
		if t, ok := firstLocal(); ok {
			return t, agent.CloudBackendOptions{}, nil
		}
		// Fallback local default name
		name := resolveChatModel("", rt)
		return syntheticLocalOllamaTarget(name), agent.CloudBackendOptions{}, nil

	default: // auto
		// Explicit --cloud-model is operator intent: resolve cloud or fail (never ignore).
		if cloudReq != "" {
			return resolveCloudStartupTarget(rt, cloudReq)
		}
		// Effective requested model (flag / default_model / preferred) matching local catalog
		if reqModel != "" {
			if t, ok := matchLocalModel(reqModel); ok {
				return t, agent.CloudBackendOptions{}, nil
			}
			// Honor operator/default name even when not yet in the snapshot catalog.
			return syntheticLocalOllamaTarget(reqModel), agent.CloudBackendOptions{}, nil
		}
		// Prefer any usable local target from the catalog
		if t, ok := firstLocal(); ok {
			return t, agent.CloudBackendOptions{}, nil
		}
		// Else credentialed cloud by priority + cheapest model on that provider
		if t, opts, err := resolveCloudStartupTarget(rt, ""); err == nil {
			return t, opts, nil
		}
		// Last resort: local ollama with resolved default name
		name := resolveChatModel("", rt)
		return syntheticLocalOllamaTarget(name), agent.CloudBackendOptions{}, nil
	}
}

var probeEndpointFn = func(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func collectModelChoices(rt *runtimectx.Context) []ModelChoice {
	var choices []ModelChoice
	if rt == nil {
		return choices
	}

	if rt.Snapshot != nil {
		// Identify unique remote endpoints to probe concurrently
		type probeResult struct {
			endpoint string
			ok       bool
		}
		endpointToNodes := make(map[string][]models.NodeFacts)
		for _, n := range rt.Snapshot.Nodes {
			// Probe Ollama instances
			if n.Ollama != nil && n.Ollama.Installed && !models.IsLocalNode(n) {
				endpoint, err := resolveNodeEndpoint(n, n.Ollama.Port)
				if err == nil && endpoint != "" {
					endpointToNodes[endpoint+"/api/tags"] = append(endpointToNodes[endpoint+"/api/tags"], n)
				}
			}
			// Probe MLX/llama.cpp resident models
			for _, rm := range n.ResidentModels {
				if (rm.Runtime == "mlx" || rm.Runtime == "llama.cpp") && !models.IsLocalNode(n) && rm.Port > 0 {
					endpoint, err := resolveNodeEndpoint(n, rm.Port)
					if err == nil && endpoint != "" {
						// OpenAI-compatible /v1/models probe
						endpointToNodes[endpoint+"/v1/models"] = append(endpointToNodes[endpoint+"/v1/models"], n)
					}
				}
			}
		}

		ch := make(chan probeResult, len(endpointToNodes))
		var wg sync.WaitGroup
		for ep := range endpointToNodes {
			wg.Add(1)
			go func(endpoint string) {
				defer wg.Done()
				ok := probeEndpointFn(endpoint)
				ch <- probeResult{endpoint: endpoint, ok: ok}
			}(ep)
		}

		// Wait in background and close channel when done
		go func() {
			wg.Wait()
			close(ch)
		}()

		probeMap := make(map[string]bool)
		for res := range ch {
			probeMap[res.endpoint] = res.ok
		}

		seen := make(map[string]bool)
		for _, n := range rt.Snapshot.Nodes {
			var nodeLabel string
			if models.IsLocalNode(n) {
				nodeLabel = ""
			} else {
				nodeLabel = n.Name
			}

			// Add Ollama models
			if n.Ollama != nil && n.Ollama.Installed {
				endpoint, err := resolveNodeEndpoint(n, n.Ollama.Port)
				disabled := false
				reason := ""
				if err != nil {
					disabled = true
					reason = "no valid endpoint"
					endpoint = ""
				} else if !models.IsLocalNode(n) && !probeMap[endpoint+"/api/tags"] {
					disabled = true
					reason = "unreachable"
				}
				// Local security: process-local. Remote node HTTP is still cluster LAN.
				sec := agent.BackendLocal
				if !models.IsLocalNode(n) {
					sec = agent.BackendRemote
				}
				for _, mName := range n.Ollama.Models {
					key := n.Name + ":ollama:" + mName
					if !seen[key] {
						seen[key] = true
						choices = append(choices, ModelChoice{
							ID:             key,
							Model:          mName,
							Protocol:       agent.ProtocolOllama,
							ProviderName:   "ollama",
							ProviderKind:   "local",
							Node:           nodeLabel,
							Endpoint:       endpoint,
							SecurityClass:  sec,
							Disabled:       disabled,
							DisabledReason: reason,
						})
					}
				}
			}

			// Add Resident Models (llama.cpp / MLX / etc) — OpenAI-compatible protocol
			for _, rm := range n.ResidentModels {
				if rm.Runtime == "ollama" {
					continue // already covered above
				}
				endpoint, err := resolveNodeEndpoint(n, rm.Port)
				disabled := false
				reason := ""
				if err != nil || rm.Port <= 0 {
					disabled = true
					reason = "no valid endpoint"
					endpoint = ""
				} else if !models.IsLocalNode(n) && !probeMap[endpoint+"/v1/models"] {
					disabled = true
					reason = "unreachable"
				}
				sec := agent.BackendLocal
				if !models.IsLocalNode(n) {
					sec = agent.BackendRemote
				}
				key := n.Name + ":" + rm.Runtime + ":" + rm.Name
				if !seen[key] {
					seen[key] = true
					choices = append(choices, ModelChoice{
						ID:             key,
						Model:          rm.Name,
						Protocol:       agent.ProtocolOpenAI,
						ProviderName:   rm.Runtime,
						ProviderKind:   "local",
						Node:           nodeLabel,
						Endpoint:       endpoint,
						SecurityClass:  sec,
						Disabled:       disabled,
						DisabledReason: reason,
					})
				}
			}
		}
	}

	if rt.Config != nil {
		for pName, pCfg := range rt.Config.AIProviders {
			if pCfg.Enabled && strings.EqualFold(pCfg.Type, "cloud") {
				key, keyErr := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
				disabled := keyErr != nil || key == ""
				reason := ""
				if disabled {
					reason = "API key not found"
				}
				for _, m := range pCfg.Models {
					if m.Name == "" {
						continue
					}
					choices = append(choices, ModelChoice{
						ID:             fmt.Sprintf("cloud:%s:%s", pName, m.Name),
						Model:          m.Name,
						Protocol:       agent.ProtocolCloud,
						ProviderName:   pName,
						ProviderKind:   "cloud",
						Node:           "",
						Endpoint:       pCfg.Endpoint,
						SecurityClass:  agent.BackendRemote,
						Disabled:       disabled,
						DisabledReason: reason,
					})
				}
			}
		}
	}

	sort.Slice(choices, func(i, j int) bool {
		if choices[i].ProviderKind != choices[j].ProviderKind {
			return choices[i].ProviderKind < choices[j].ProviderKind
		}
		if choices[i].ProviderName != choices[j].ProviderName {
			return choices[i].ProviderName < choices[j].ProviderName
		}
		return choices[i].Model < choices[j].Model
	})

	return choices
}

var tokenRegex = regexp.MustCompile(`(?i)(bearer|token|key|auth|password|secret|credential)[=:\s]+[A-Za-z0-9\-_./\+=]+`)
var urlSecretRegex = regexp.MustCompile(`(?i)(token|key|password|pass|secret|auth)=[^&\s]+`)

func redactSecrets(s string) string {
	s = tokenRegex.ReplaceAllString(s, "$1=[REDACTED]")
	s = urlSecretRegex.ReplaceAllString(s, "$1=[REDACTED]")
	return s
}

func sanitizeDiagnosticsText(s string) string {
	s = ui.StripANSIAndControls(s)
	return redactSecrets(s)
}

func printAgentSessionDetails(w io.Writer, target ModelChoice, autoApprove bool, autonomy string, mcpCount int, maxTurns int) {
	safetyStr := "Strict Operator Approval"
	if autoApprove {
		safetyStr = "Auto-Approve safe (<70)"
	}
	switch agent.AutonomyMode(autonomy) {
	case agent.AutonomyEdit:
		safetyStr = "Autonomy: edit (auto-approve edits)"
	case agent.AutonomyFull:
		safetyStr = "Autonomy: full (auto-approve all but safety-blocked)"
	}
	mcpStr := "None connected"
	if mcpCount > 0 {
		mcpStr = fmt.Sprintf("%d connected", mcpCount)
	}

	printRow := func(label, value string, isBold bool) {
		valPlain := ui.StripANSIAndControls(value)
		pad := 38 - len(valPlain)
		if pad < 0 {
			pad = 0
		}
		valDisp := value
		if isBold {
			valDisp = ui.Bold(value)
		}
		fmt.Fprintf(w, "  │  %s:  %s%s │\n", label, valDisp, strings.Repeat(" ", pad))
	}

	endpoint := target.Endpoint
	if endpoint == "" {
		endpoint = "(default)"
	}
	provider := target.DisplayProvider()
	if target.Protocol != "" {
		provider = fmt.Sprintf("%s [%s]", provider, target.Protocol)
	}

	ui.WhiteColor.Fprintln(w, "  ┌────────────────────────────────────────────────────────┐")
	ui.WhiteColor.Fprintln(w, "  │                     SESSION ACTIVE                     │")
	ui.WhiteColor.Fprintln(w, "  ├────────────────────────────────────────────────────────┤")
	printRow("Active Model", target.Model, true)
	printRow("Provider    ", provider, false)
	printRow("Endpoint    ", endpoint, false)
	if target.Node != "" {
		printRow("Node        ", target.Node, false)
	}
	printRow("Safety Gate ", safetyStr, false)
	printRow("MCP Servers ", mcpStr, false)
	printRow("Max Turns   ", fmt.Sprintf("%d", maxTurns), false)
	ui.WhiteColor.Fprintln(w, "  └────────────────────────────────────────────────────────┘")
	fmt.Fprintln(w)
}
