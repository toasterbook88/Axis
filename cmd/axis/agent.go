package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
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
		roleFlag                string
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
		useConsole              bool
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
			// --role and --model are mutually exclusive.
			if strings.TrimSpace(model) != "" && strings.TrimSpace(roleFlag) != "" {
				return fmt.Errorf("use either --model or --role, not both")
			}
			// --role fills model from ai.yaml when --model is empty.
			if strings.TrimSpace(model) == "" && strings.TrimSpace(roleFlag) != "" {
				if fromRole := modelFromAIRoleFn(roleFlag); fromRole != "" {
					model = fromRole
				} else {
					return fmt.Errorf("unknown or empty ai.yaml role %q (axis ai roles)", roleFlag)
				}
			}
			// Display / last-resort resolution (always non-empty when possible).
			currentModel := resolveAgentModel(model, rt)
			// Startup request: explicit --model, else chat.default_model / warm preferred / ai role default.
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

			// Determine and configure backend via extracted setupAgentStartupBackend.
			startupBackend, err := setupAgentStartupBackend(agentStartupBackendParams{
				Model:                 model,
				StartupRequestedModel: startupRequestedModel,
				Provider:              provider,
				CloudModel:            cloudModel,
				CheapModel:            cheapModel,
				SelectModel:           selectModel,
				Verbose:               verbose,
				RT:                    rt,
				Ctx:                   ctx,
				In:                    os.Stdin,
				Out:                   w,
				ErrOut:                errW,
			})
			if err != nil {
				return err
			}
			activeTarget := startupBackend.ActiveTarget
			backend := startupBackend.Backend
			choices := startupBackend.Choices

			endpoint := activeTarget.Endpoint
			if endpoint == "" {
				endpoint = chat.DefaultEndpoint
			}

			cfg := buildAgentSessionConfig(agentSessionParams{
				Endpoint:                endpoint,
				Model:                   activeTarget.Model,
				Backend:                 backend,
				MaxTurns:                maxTurns,
				MaxTokens:               maxTokens,
				AutoApprove:             autoApprove,
				Autonomy:                agent.AutonomyMode(autonomy),
				SystemExtra:             systemMsg,
				Verbose:                 verbose,
				DryRun:                  dryRun,
				AllowRawCommandEvidence: allowRawCommandEvidence,
				BackendSecurityClass:    activeTarget.SecurityClass,
				Cluster:                 cluster,
				Knowledge:               k,
				ToolContext:             tc,
				Output:                  w,
				MCPRegistry:             mcpReg,
			})

			a = agent.New(cfg)

			// Resume previous conversation if requested.
			historyPath := chat.PersistPath("agent")
			if resume {
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
					_ = saveAgentConversation(a.Conversation(), historyPath, errW)
				}
				return nil
			}

			// REPL runtime extracted to agent_repl.go (runAgentInteractive).
			return runAgentInteractive(agentREPLConfig{
				Agent:        a,
				MCPRegistry:  mcpReg,
				ActiveTarget: activeTarget,
				Timeout:      timeout,
				HistoryPath:  historyPath,
				UseConsole:   useConsole,
				AutoApprove:  autoApprove,
				Autonomy:     autonomy,
				MaxTurns:     maxTurns,
				ModelChoices: choices,
				Ctx:          ctx,
				Out:          w,
				ErrOut:       errW,
			})
		},
	}

	cmd.Flags().StringVarP(&model, "model", "m", "", "Model tag (default: agent.default_model, warm preferred, or ai.yaml role default)")

	cmd.Flags().StringVar(&roleFlag, "role", "", "Inference role from ~/.axis/ai.yaml (sets model from that role when --model is empty)")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 5*time.Minute, "Per-request timeout")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 32768, "Conversation token budget")
	cmd.Flags().IntVar(&maxTurns, "max-turns", 25, "Maximum agent loop iterations per query")
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
	cmd.Flags().BoolVar(&useConsole, "console", false, "Experimental transcript console (interactive TTY only; tool approvals are denied)")
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
		_, shouldBreak := replTurn(session, ctx, timeout, line)
		if shouldBreak {
			break
		}
	}

	if historyPath != "" && a.Conversation().HistoryCount() > 0 {
		_ = saveAgentConversation(a.Conversation(), historyPath, errW)
	}

	return nil
}

func saveAgentConversation(conversation *chat.Conversation, historyPath string, errW io.Writer) error {
	if err := conversation.SaveToFile(historyPath); err != nil {
		fmt.Fprintf(errW, "warning: could not save conversation: %v\n", err)
		return err
	}
	return nil
}

// handleREPLSlashCommand is defined in agent_slash.go (dispatch table).

func agentRequestContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

// agentSessionParams holds the live operator inputs used to construct agent.Config.
// Extracted so tests can prove RunShell/RunOnNode/SystemExtra wiring without
// driving the full Cobra RunE path.
type agentSessionParams struct {
	Endpoint                string
	Model                   string
	Backend                 agent.ChatBackend
	MaxTurns                int
	MaxTokens               int
	AutoApprove             bool
	Autonomy                agent.AutonomyMode
	SystemExtra             string
	Verbose                 bool
	DryRun                  bool
	AllowRawCommandEvidence bool
	BackendSecurityClass    agent.BackendSecurityClass
	Cluster                 *chat.ClusterSummaryForPrompt
	Knowledge               *knowledge.ClusterKnowledge
	ToolContext             *agent.ToolContext
	Output                  io.Writer
	MCPRegistry             *mcpclient.Registry
}

// buildAgentSessionConfig assembles agent.Config for axis agent sessions.
// Always injects guarded shell runners so Layer 4 cannot be accidentally dropped.
func buildAgentSessionConfig(p agentSessionParams) agent.Config {
	model := p.Model
	return agent.Config{
		Endpoint:                p.Endpoint,
		Model:                   p.Model,
		Backend:                 p.Backend,
		MaxTurns:                p.MaxTurns,
		MaxTokens:               p.MaxTokens,
		AutoApprove:             p.AutoApprove,
		Autonomy:                p.Autonomy,
		SystemExtra:             p.SystemExtra,
		Verbose:                 p.Verbose,
		DryRun:                  p.DryRun,
		AllowRawCommandEvidence: p.AllowRawCommandEvidence,
		BackendSecurityClass:    p.BackendSecurityClass,
		Cluster:                 p.Cluster,
		Knowledge:               p.Knowledge,
		ToolContext:             p.ToolContext,
		Output:                  p.Output,
		RunShell:                guardedAgentShellRunner(model),
		RunOnNode: func(ctx context.Context, node, command string) (string, error) {
			return guardedAgentCommandRunner(model, node)(ctx, command)
		},
		RunTask:     guardedAgentTaskRunner(),
		MCPRegistry: p.MCPRegistry,
	}
}

// guardedAgentShellRunner runs local shell via guarded execution (local node pin).
func guardedAgentShellRunner(model string) agent.ShellRunner {
	return guardedAgentCommandRunner(model, "")
}

// guardedAgentCommandRunner builds a ShellRunner that executes through Layer 4.
// When requestedNode is empty, the canonical local node is resolved from the snapshot.
// When set, RequestedNode is pinned (agent run_on_node) with OwnerSurfaceAgentRunOnNode.
func guardedAgentCommandRunner(model, requestedNode string) agent.ShellRunner {
	return func(ctx context.Context, command string) (string, error) {
		rt, err := loadAgentShellRuntime(ctx)
		if err != nil {
			return "", fmt.Errorf("load runtime context for guarded execution: %w", err)
		}
		if rt == nil {
			return "", fmt.Errorf("runtime context unavailable for guarded execution (nil loader result)")
		}

		nodeName := strings.TrimSpace(requestedNode)
		ownerSurface := execution.OwnerSurfaceAgentRunShell
		if nodeName != "" {
			ownerSurface = execution.OwnerSurfaceAgentRunOnNode
		} else {
			if rt.Snapshot != nil {
				if localNode, ok := models.FindLocalNode(rt.Snapshot.Nodes); ok {
					nodeName = localNode.Name
				}
			}
			if nodeName == "" {
				return "", fmt.Errorf("local node resolution failed: could not identify canonical local node name from snapshot")
			}
		}

		req := execution.GuardedExecutionRequest{
			Description:      command,
			Mode:             execution.ModeExec,
			Confirm:          execution.ConfirmWord,
			RequestedNode:    nodeName,
			OwnerSurface:     ownerSurface,
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

// Model resolution, catalog choices, and startup backend helpers extracted to agent_startup_model.go.

var tokenRegex = regexp.MustCompile(`(?i)(bearer|token|key|auth|password|secret|credential)[=:\s]+[A-Za-z0-9\-_./\+=]+`)
var urlSecretRegex = regexp.MustCompile(`(?i)(token|key|password|pass|secret|auth)=[^&\s]+`)

func printAgentFacts(w, errW io.Writer, rt *runtimectx.Context) {
	if rt == nil || rt.Snapshot == nil {
		fmt.Fprintln(errW, ui.Yellow("No cluster snapshot available"))
		return
	}
	n, ok := models.FindLocalNode(rt.Snapshot.Nodes)
	if !ok {
		fmt.Fprintln(errW, ui.Yellow("Local node not found in snapshot"))
		return
	}
	fmt.Fprintf(w, "Node: %s (%s/%s)\n", n.Name, n.OS, n.Arch)
	if n.Resources != nil {
		fmt.Fprintf(w, "CPU: %d cores\n", n.Resources.CPUCores)
		fmt.Fprintf(w, "RAM: %d MB total, %d MB free\n", n.Resources.RAMTotalMB, n.Resources.RAMFreeMB)
	}
	if len(n.ResidentModels) == 0 {
		fmt.Fprintln(w, "Residents: none")
		return
	}
	fmt.Fprintln(w, "Residents:")
	for _, rm := range n.ResidentModels {
		fmt.Fprintf(w, "  %s  runtime=%s  port=%d\n", rm.Name, rm.Runtime, rm.Port)
	}
}

func printAgentCluster(w, errW io.Writer, rt *runtimectx.Context) {
	if rt == nil || rt.Snapshot == nil || len(rt.Snapshot.Nodes) == 0 {
		fmt.Fprintln(errW, ui.Yellow("No nodes found in session snapshot."))
		return
	}
	var listItems []NodeListItem
	for _, n := range rt.Snapshot.Nodes {
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
	if rt.Snapshot.Timestamp.IsZero() {
		fmt.Fprintln(w, "Snapshot Source: session-snapshot (age unknown)")
	} else {
		fmt.Fprintf(w, "Snapshot Source: session-snapshot (age %s)\n", time.Since(rt.Snapshot.Timestamp).Round(time.Minute))
	}

	fmt.Fprint(w, RenderNodeTable(listItems))
}

func agentStatusStrip(target ModelChoice) string {
	model := strings.TrimSpace(target.Model)
	if model == "" {
		model = "(none)"
	}
	ep := strings.TrimSpace(target.Endpoint)
	ep = strings.TrimPrefix(ep, "http://")
	ep = strings.TrimPrefix(ep, "https://")
	if i := strings.Index(ep, "/"); i >= 0 {
		ep = ep[:i]
	}
	if ep == "" {
		ep = "(default)"
	}
	status := "probed"
	if target.Disabled {
		status = "stale"
	}
	return fmt.Sprintf("[model: %s] [endpoint: %s] [status: %s]", model, ep, status)
}

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
	fmt.Fprintln(w, agentStatusStrip(target))
	fmt.Fprintln(w)
}

func exportAgentWorklog(a *agent.Agent, customPath string) (string, error) {
	if a == nil || a.Conversation() == nil {
		return "", fmt.Errorf("no active conversation to export")
	}

	exportPath := strings.TrimSpace(customPath)
	if exportPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir := filepath.Join(home, ".axis", "worklogs")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
		exportPath = filepath.Join(dir, fmt.Sprintf("session-%s.md", time.Now().Format("2006-01-02T150405")))
	} else {
		if strings.HasPrefix(exportPath, "~/") {
			home, _ := os.UserHomeDir()
			exportPath = filepath.Join(home, exportPath[2:])
		}
		dir := filepath.Dir(exportPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
	}

	var sb strings.Builder
	sb.WriteString("# AXIS Agent Session Worklog\n\n")
	sb.WriteString(fmt.Sprintf("- **Generated**: %s\n", time.Now().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("- **Model**: %s\n", a.Model()))
	sb.WriteString(fmt.Sprintf("- **Autonomy Mode**: %s\n", a.Autonomy()))
	sb.WriteString(fmt.Sprintf("- **Tokens**: %d / %d\n\n", a.ContextTokens(), a.MaxTokens()))
	sb.WriteString("---\n\n")

	msgs := a.Conversation().Messages()
	for i, m := range msgs {
		switch m.Role {
		case chat.RoleSystem:
			if i == 0 {
				sb.WriteString("### System Prompt\n\n```text\n")
				sb.WriteString(m.Content)
				sb.WriteString("\n```\n\n")
			} else {
				sb.WriteString(fmt.Sprintf("### System Context\n\n%s\n\n", m.Content))
			}
		case chat.RoleUser:
			sb.WriteString(fmt.Sprintf("## Operator\n\n%s\n\n", m.Content))
		case chat.RoleAssistant:
			sb.WriteString("## AXIS Agent\n\n")
			if m.Content != "" {
				sb.WriteString(m.Content)
				sb.WriteString("\n\n")
			}
			if len(m.ToolCalls) > 0 {
				sb.WriteString("**Tool Invocations:**\n")
				for _, tc := range m.ToolCalls {
					sb.WriteString(fmt.Sprintf("- `%s(%s)`\n", tc.Function.Name, string(tc.Function.Arguments)))
				}
				sb.WriteString("\n")
			}
		case chat.RoleTool:
			sb.WriteString(fmt.Sprintf("### Tool Result (`%s`)\n\n```\n%s\n```\n\n", m.ToolCallID, m.Content))
		}
	}

	if err := os.WriteFile(exportPath, []byte(sb.String()), 0600); err != nil {
		return "", err
	}
	return exportPath, nil
}
