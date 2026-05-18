package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/knowledge"
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
		model       string
		timeout     time.Duration
		maxTokens   int
		maxTurns    int
		autoApprove bool
		systemMsg   string
		resume      bool
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
			currentModel := resolveChatModel(model)
			w := cmd.OutOrStdout()
			errW := cmd.ErrOrStderr()

			fmt.Fprintln(errW, ui.Dim("advisory: agent output is not cluster truth — it uses tools to read the fact plane"))

			// Load runtime context for tools and safety.
			var cluster *chat.ClusterSummaryForPrompt
			var k *knowledge.ClusterKnowledge
			tc := &agent.ToolContext{}

			rt, err := runtimectx.Load(cmd.Context())
			if err != nil {
				fmt.Fprintf(errW, "%s Could not load cluster context: %v\n", ui.Yellow("⚠"), err)
			} else {
				tc.Snapshot = rt.Snapshot
				tc.State = rt.State
				if rt.Snapshot != nil {
					cluster = chat.BuildClusterSummary(rt.Snapshot)
					bestNode := ""
					if len(rt.Snapshot.Nodes) > 0 {
						bestNode = rt.Snapshot.Nodes[0].Name
					}
					k = knowledge.Build(rt.Snapshot, rt.State, bestNode)
				}
			}

			cfg := agent.Config{
				Endpoint:    chat.DefaultEndpoint,
				Model:       currentModel,
				MaxTurns:    maxTurns,
				MaxTokens:   maxTokens,
				AutoApprove: autoApprove,
				SystemExtra: systemMsg,
				Cluster:     cluster,
				Knowledge:   k,
				ToolContext: tc,
				Output:      w,
				RunShell:    guardedAgentShellRunner(currentModel),
			}

			a := agent.New(cfg)

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
				fmt.Fprintf(errW, "Agent [%s] — max %d turns\n\n", ui.Bold(currentModel), maxTurns)

				ctx, cancel := agentRequestContext(timeout)
				defer cancel()
				if err := a.Run(ctx, instruction); err != nil {
					fmt.Fprintf(errW, "error: Agent failed: %v\n", err)
					return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("agent failed: %v", err)}
				}
				fmt.Fprintln(w)
				_ = a.Conversation().SaveToFile(historyPath)
				return nil
			}

			// Interactive REPL with readline.
			fmt.Fprintf(errW, "AXIS Agent [%s] — max %d turns per query, type exit to quit\n\n", ui.Bold(currentModel), maxTurns)

			rl, err := readline.NewEx(&readline.Config{
				Prompt:          ui.Cyan("agent> "),
				HistoryFile:     historyPath + ".line",
				InterruptPrompt: "^C",
				EOFPrompt:       "exit",
			})
			if err != nil {
				return runPlainAgentREPL(a, w, errW, timeout, historyPath)
			}
			defer rl.Close()

			for {
				line, err := rl.Readline()
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

				ctx, cancel := agentRequestContext(timeout)
				if err := a.Run(ctx, instruction); err != nil {
					fmt.Fprintf(errW, "\n%s %v\n", ui.Red("Error:"), err)
				}
				cancel()
				fmt.Fprintln(w)
			}

			if n := a.Conversation().HistoryCount(); n > 0 {
				if err := a.Conversation().SaveToFile(historyPath); err != nil {
					fmt.Fprintf(errW, "warning: could not save conversation: %v\n", err)
				} else {
					fmt.Fprintf(errW, "Saved %d messages to conversation history.\n", n)
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
	cmd.Flags().StringVar(&systemMsg, "system", "", "Extra text appended to system prompt")
	cmd.Flags().BoolVar(&resume, "resume", false, "Resume previous conversation from history")
	return cmd
}

// runPlainAgentREPL is the fallback scanner-based REPL when readline is unavailable.
func runPlainAgentREPL(a *agent.Agent, w, errW io.Writer, timeout time.Duration, historyPath string) error {
	fmt.Fprintln(errW, ui.Yellow("Note: using plain input mode (no arrow keys or history)"))
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(errW, ui.Cyan("agent\u003e "))
		if !scanner.Scan() {
			break
		}
		instruction := strings.TrimSpace(scanner.Text())
		if instruction == "" {
			continue
		}
		lower := strings.ToLower(instruction)
		if lower == "exit" || lower == "quit" {
			break
		}
		ctx, cancel := agentRequestContext(timeout)
		if err := a.Run(ctx, instruction); err != nil {
			fmt.Fprintf(errW, "\n%s %v\n", ui.Red("Error:"), err)
		}
		cancel()
		fmt.Fprintln(w)
	}
	if n := a.Conversation().HistoryCount(); n > 0 {
		_ = a.Conversation().SaveToFile(historyPath)
	}
	return nil
}

func agentRequestContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}

func guardedAgentShellRunner(model string) agent.ShellRunner {
	return func(ctx context.Context, command string) (string, error) {
		rt, err := loadAgentShellRuntime(ctx)
		if err != nil {
			return "", fmt.Errorf("load runtime context for guarded execution: %w", err)
		}

		req := execution.GuardedExecutionRequest{
			Description:  command,
			Mode:         execution.ModeExec,
			Confirm:      execution.ConfirmWord,
			OwnerSurface: execution.OwnerSurfaceAgentRunShell,
			OwnerLabel:   strings.TrimSpace(model),
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
