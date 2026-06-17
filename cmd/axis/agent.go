package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/secrets"
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
		verbose     bool
		dryRun      bool
		provider    string
		cloudModel  string
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

			currentModel := resolveChatModel(model)
			if verbose && model == "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Resolved model: %s\n", currentModel)
			}
			w := cmd.OutOrStdout()
			errW := cmd.ErrOrStderr()

			fmt.Fprintln(errW, ui.Dim("advisory: agent output is not cluster truth — it uses tools to read the fact plane"))

			// Load runtime context for tools and safety.
			var cluster *chat.ClusterSummaryForPrompt
			var k *knowledge.ClusterKnowledge
			tc := &agent.ToolContext{}

			rt, err := runtimectx.Load(ctx)
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

			// Load MCP servers and connect.
			var mcpReg *mcpclient.Registry
			if rt != nil && rt.Config != nil {
				mcpReg = mcpclient.NewRegistry()
				augmentedMCPServers := make(map[string]config.MCPServerConfig)
				for k, v := range rt.Config.MCPServers {
					augmentedMCPServers[k] = v
				}

				// Dynamically add Cortex server if foundry is present
				if foundry, ok := rt.Config.FindNode("foundry"); ok {
					headers := make(map[string]string)
					token, err := secrets.ResolveOrEmpty("AXIS_CORTEX_SECRET", "~/.axis/cortex.token")
					if err == nil && token != "" {
						headers["Authorization"] = "Bearer " + token
					}
					augmentedMCPServers["cortex"] = config.MCPServerConfig{
						Transport: "http",
						URL:       fmt.Sprintf("http://%s:8200/mcp", foundry.Hostname),
						Headers:   headers,
					}
				}

				if len(augmentedMCPServers) > 0 {
					tempCfg := &config.Config{
						MCPServers: augmentedMCPServers,
					}
					if verbose {
						fmt.Fprintln(errW, "Connecting to MCP servers...")
					}
					mcpReg.ConnectAll(ctx, tempCfg)
					defer mcpReg.Close()
				}
			}

			// Determine which backend to use.
			var backend agent.ChatBackend
			resolvedModel := currentModel

			providerMode := strings.ToLower(strings.TrimSpace(provider))
			if providerMode == "" {
				providerMode = "auto"
			}

			var bestCloudProviderName string
			var bestCloudProvider config.AIProviderConfig
			var bestCloudAPIKey string

			if rt != nil && rt.Config != nil {
				var cloudProviders []struct {
					name string
					cfg  config.AIProviderConfig
					key  string
				}
				for pName, pCfg := range rt.Config.AIProviders {
					if pCfg.Enabled && strings.EqualFold(pCfg.Type, "cloud") {
						key, err := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
						if err == nil && key != "" {
							cloudProviders = append(cloudProviders, struct {
								name string
								cfg  config.AIProviderConfig
								key  string
							}{pName, pCfg, key})
						}
					}
				}

				if len(cloudProviders) > 0 {
					sort.Slice(cloudProviders, func(i, j int) bool {
						if cloudProviders[i].cfg.Priority != cloudProviders[j].cfg.Priority {
							return cloudProviders[i].cfg.Priority > cloudProviders[j].cfg.Priority
						}
						return cloudProviders[i].name < cloudProviders[j].name
					})
					bestCloudProviderName = cloudProviders[0].name
					bestCloudProvider = cloudProviders[0].cfg
					bestCloudAPIKey = cloudProviders[0].key
				}
			}

			useCloud := false
			if providerMode == "cloud" {
				if bestCloudProviderName == "" {
					return ExitCodeError{Code: ExitErrConfigLoad, Message: "no enabled cloud providers with valid API keys found in config"}
				}
				useCloud = true
			} else if providerMode == "auto" {
				if bestCloudProviderName != "" {
					useCloud = true
				}
			}

			if useCloud {
				targetModel := cloudModel
				if targetModel == "" {
					bestModel := ""
					bestCost := 0.0
					for _, m := range bestCloudProvider.Models {
						if m.Name == "" {
							continue
						}
						if bestModel == "" {
							bestModel = m.Name
						}
						if m.CostPer1K > 0 && (bestCost == 0 || m.CostPer1K < bestCost) {
							bestCost = m.CostPer1K
							bestModel = m.Name
						}
					}
					targetModel = bestModel
				}

				if targetModel == "" {
					return ExitCodeError{Code: ExitErrConfigLoad, Message: fmt.Sprintf("no models configured for cloud provider %q", bestCloudProviderName)}
				}

				var costPer1K float64
				for _, m := range bestCloudProvider.Models {
					if strings.EqualFold(m.Name, targetModel) {
						costPer1K = m.CostPer1K
						break
					}
					for _, alias := range m.Aliases {
						if strings.EqualFold(alias, targetModel) {
							costPer1K = m.CostPer1K
							break
						}
					}
				}

				backend = agent.NewCloudBackendWithKey(bestCloudProviderName, bestCloudProvider.Endpoint, bestCloudAPIKey, targetModel, costPer1K)
				resolvedModel = targetModel
				if verbose {
					fmt.Fprintf(errW, "Using cloud provider %q with model %q\n", bestCloudProviderName, targetModel)
				}
			}

			cfg := agent.Config{
				Endpoint:    chat.DefaultEndpoint,
				Model:       resolvedModel,
				Backend:     backend,
				MaxTurns:    maxTurns,
				MaxTokens:   maxTokens,
				AutoApprove: autoApprove,
				SystemExtra: systemMsg,
				Verbose:     verbose,
				DryRun:      dryRun,
				Cluster:     cluster,
				Knowledge:   k,
				ToolContext: tc,
				Output:      w,
				RunShell:    guardedAgentShellRunner(resolvedModel),
				MCPRegistry: mcpReg,
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
				fmt.Fprintf(errW, "Agent [%s] — max %d turns\n\n", ui.Bold(currentModel), maxTurns)

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
			fmt.Fprintf(errW, "AXIS Agent [%s] — max %d turns per query, type exit to quit\n\n", ui.Bold(currentModel), maxTurns)

			rlCfg := &readline.Config{
				Prompt:          ui.Cyan("✨ axis ❯ "),
				InterruptPrompt: "^C",
				EOFPrompt:       "exit",
			}
			if historyPath != "" {
				rlCfg.HistoryFile = historyPath + ".line"
			}
			rl, err := readline.NewEx(rlCfg)
			if err != nil {
				return runPlainAgentREPL(ctx, a, w, errW, timeout, historyPath, rt, verbose)
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

				if strings.HasPrefix(instruction, "/") {
					handled, shouldExit, slashErr := handleREPLSlashCommand(instruction, a, w, errW, rt, verbose)
					if slashErr != nil {
						fmt.Fprintf(errW, "\n%s %v\n", ui.Red("Error:"), slashErr)
					}
					if handled {
						if shouldExit {
							break
						}
						continue
					}
				}

				ctx2, cancel := agentRequestContext(ctx, timeout)
				if err := a.Run(ctx2, instruction); err != nil {
					fmt.Fprintf(errW, "\n%s %v\n", ui.Red("Error:"), err)
				}
				cancel()
				fmt.Fprintln(w)
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
	cmd.Flags().StringVar(&systemMsg, "system", "", "Extra text appended to system prompt")
	cmd.Flags().BoolVar(&resume, "resume", false, "Resume previous conversation from history")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Emit trace output for tool calls and turns")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Plan tool calls without executing them")
	cmd.Flags().StringVar(&provider, "provider", "auto", "Inference provider to use (local, cloud, auto)")
	cmd.Flags().StringVar(&cloudModel, "cloud-model", "", "Model name for cloud provider")
	return cmd
}

// runPlainAgentREPL is the fallback scanner-based REPL when readline is unavailable.
func runPlainAgentREPL(ctx context.Context, a *agent.Agent, w, errW io.Writer, timeout time.Duration, historyPath string, rt *runtimectx.Context, verbose bool) error {
	fmt.Fprintln(errW, ui.Yellow("Note: using plain input mode (no arrow keys or history)"))
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(errW, ui.Cyan("✨ axis ❯ "))
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

		if strings.HasPrefix(instruction, "/") {
			handled, shouldExit, slashErr := handleREPLSlashCommand(instruction, a, w, errW, rt, verbose)
			if slashErr != nil {
				fmt.Fprintf(errW, "\n%s %v\n", ui.Red("Error:"), slashErr)
			}
			if handled {
				if shouldExit {
					break
				}
				continue
			}
		}

		ctx2, cancel := agentRequestContext(ctx, timeout)
		if err := a.Run(ctx2, instruction); err != nil {
			fmt.Fprintf(errW, "\n%s %v\n", ui.Red("Error:"), err)
		}
		cancel()
		fmt.Fprintln(w)
	}
	if historyPath != "" && a.Conversation().HistoryCount() > 0 {
		_ = a.Conversation().SaveToFile(historyPath)
	}
	if err := scanner.Err(); err != nil {
		return ExitCodeError{Code: ExitErrIO, Message: fmt.Sprintf("input stream closed: %v", err)}
	}
	return nil
}

func handleREPLSlashCommand(line string, a *agent.Agent, w, errW io.Writer, rt *runtimectx.Context, verbose bool) (bool, bool, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, false, nil
	}
	cmd := strings.ToLower(parts[0])
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
		fmt.Fprintln(errW, "  /exit, /quit   Quit the session")
		return true, false, nil

	case "/clear":
		a.Conversation().Clear()
		fmt.Fprintln(errW, "Conversation history cleared (system prompt kept).")
		return true, false, nil

	case "/context":
		tokens := a.ContextTokens()
		limit := a.MaxTokens()
		fmt.Fprintf(errW, "Conversation context:\n  Tokens used:  %d\n  Token budget: %d\n", tokens, limit)
		return true, false, nil

	case "/history":
		msgs := a.Conversation().Messages()
		fmt.Fprintf(errW, "Conversation History (%d message(s)):\n", len(msgs))
		for i, m := range msgs {
			short := m.Content
			if len(short) > 60 {
				short = short[:57] + "..."
			}
			short = strings.ReplaceAll(short, "\n", " ")
			fmt.Fprintf(errW, "  [%d] %s: %s\n", i, m.Role, short)
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					fmt.Fprintf(errW, "      → Tool call: %s\n", tc.Function.Name)
				}
			}
		}
		return true, false, nil

	case "/tools":
		fmt.Fprintf(errW, "Available Tools:\n  %s\n", a.ToolNames())
		return true, false, nil

	case "/model":
		if len(parts) < 2 {
			fmt.Fprintln(errW, "Error: /model requires a model name, e.g. /model llama3.2:3b or /model claude-3-5-sonnet")
			return true, false, nil
		}
		newModel := parts[1]
		var newBackend agent.ChatBackend
		var useCloud = false
		var bestCloudProviderName string
		var bestCloudProvider config.AIProviderConfig
		var bestCloudAPIKey string

		if rt != nil && rt.Config != nil {
			for pName, pCfg := range rt.Config.AIProviders {
				if pCfg.Enabled && strings.EqualFold(pCfg.Type, "cloud") {
					for _, m := range pCfg.Models {
						if strings.EqualFold(m.Name, newModel) {
							key, err := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
							if err == nil && key != "" {
								bestCloudProviderName = pName
								bestCloudProvider = pCfg
								bestCloudAPIKey = key
								useCloud = true
								break
							}
						}
						for _, alias := range m.Aliases {
							if strings.EqualFold(alias, newModel) {
								key, err := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
								if err == nil && key != "" {
									bestCloudProviderName = pName
									bestCloudProvider = pCfg
									bestCloudAPIKey = key
									useCloud = true
									break
								}
							}
						}
					}
				}
				if useCloud {
					break
				}
			}
		}

		if useCloud {
			var costPer1K float64
			for _, m := range bestCloudProvider.Models {
				if strings.EqualFold(m.Name, newModel) {
					costPer1K = m.CostPer1K
					break
				}
				for _, alias := range m.Aliases {
					if strings.EqualFold(alias, newModel) {
						costPer1K = m.CostPer1K
						break
					}
				}
			}
			newBackend = agent.NewCloudBackendWithKey(bestCloudProviderName, bestCloudProvider.Endpoint, bestCloudAPIKey, newModel, costPer1K)
			fmt.Fprintf(errW, "Switched to cloud provider %q, model %q\n", bestCloudProviderName, newModel)
		} else {
			endpoint := chat.DefaultEndpoint
			newBackend = chat.NewClient(endpoint, newModel)
			fmt.Fprintf(errW, "Switched to local Ollama model %q\n", newModel)
		}

		a.SetBackend(newBackend)
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
