package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
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
		model                   string
		timeout                 time.Duration
		maxTokens               int
		maxTurns                int
		autoApprove             bool
		systemMsg               string
		resume                  bool
		verbose                 bool
		dryRun                  bool
		provider                string
		cloudModel              string
		allowRawCommandEvidence bool
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
			var initialView *agent.RuntimeView

			rt, err := runtimectx.Load(ctx)
			if err != nil {
				fmt.Fprintf(errW, "%s Could not load cluster context: %v\n", ui.Yellow("⚠"), err)
			} else {
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
			switch providerMode {
			case "cloud":
				if bestCloudProviderName == "" {
					return ExitCodeError{Code: ExitErrConfigLoad, Message: "no enabled cloud providers with valid API keys found in config"}
				}
				useCloud = true
			case "auto":
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

				var err error
				backend, err = agent.NewCloudBackendWithKey(bestCloudProvider.Kind, bestCloudProviderName, bestCloudProvider.Endpoint, bestCloudAPIKey, targetModel, costPer1K)
				if err != nil {
					return ExitCodeError{Code: ExitErrConfigLoad, Message: fmt.Sprintf("invalid cloud backend config: %v", err)}
				}
				resolvedModel = targetModel
				if verbose {
					fmt.Fprintf(errW, "Using cloud provider %q with model %q\n", bestCloudProviderName, targetModel)
				}
			}
			securityClass := agent.BackendLocal
			if useCloud {
				securityClass = agent.BackendRemote
			}

			cfg := agent.Config{
				Endpoint:                chat.DefaultEndpoint,
				Model:                   resolvedModel,
				Backend:                 backend,
				MaxTurns:                maxTurns,
				MaxTokens:               maxTokens,
				AutoApprove:             autoApprove,
				SystemExtra:             systemMsg,
				Verbose:                 verbose,
				DryRun:                  dryRun,
				AllowRawCommandEvidence: allowRawCommandEvidence,
				BackendSecurityClass:    securityClass,
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
				return runPlainAgentREPL(ctx, a, w, errW, timeout, historyPath, rt)
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
					handled, shouldExit, slashErr := handleREPLSlashCommand(instruction, a, w, errW, rt)
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
	cmd.Flags().BoolVar(&allowRawCommandEvidence, "allow-raw-command-evidence", false, "Include raw command text in local backend evidence")
	return cmd
}

// runPlainAgentREPL is the fallback scanner-based REPL when readline is unavailable.
func runPlainAgentREPL(ctx context.Context, a *agent.Agent, w, errW io.Writer, timeout time.Duration, historyPath string, rt *runtimectx.Context) error {
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
			handled, shouldExit, slashErr := handleREPLSlashCommand(instruction, a, w, errW, rt)
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

func handleREPLSlashCommand(line string, a *agent.Agent, w, errW io.Writer, rt *runtimectx.Context) (bool, bool, error) {
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
		fmt.Fprintln(errW, "  /models        List available models and switch interactively")
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
			reqCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL+"/v2/reservations", nil)
			if reqErr == nil {
				req.Header.Set("Authorization", "Bearer "+token)
				resp, respErr := client.Do(req)
				if respErr == nil && resp.StatusCode == 200 {
					defer resp.Body.Close()
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
			cancel()
		}

		if !daemonFetched {
			if err != nil {
				return true, false, fmt.Errorf("failed to load cluster status fallback: %w", err)
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
		return true, false, nil

	case "/models":
		freshRt, err := runtimectx.Load(context.Background())
		if err != nil {
			return true, false, fmt.Errorf("failed to load cluster status: %w", err)
		}

		type modelItem struct {
			name      string
			source    string
			modelType string // local | cloud
		}
		var items []modelItem

		// Gather local models from snapshot nodes
		if freshRt.Snapshot != nil {
			seenModels := make(map[string]bool)
			for _, n := range freshRt.Snapshot.Nodes {
				if n.Ollama != nil && n.Ollama.Installed {
					for _, mName := range n.Ollama.Models {
						key := n.Name + ":" + mName
						if !seenModels[key] {
							seenModels[key] = true
							items = append(items, modelItem{
								name:      mName,
								source:    fmt.Sprintf("Local (Ollama on %s)", n.Name),
								modelType: "local",
							})
						}
					}
				}
			}
		}

		// Gather cloud models
		if freshRt.Config != nil {
			for pName, pCfg := range freshRt.Config.AIProviders {
				if pCfg.Enabled {
					for _, m := range pCfg.Models {
						items = append(items, modelItem{
							name:      m.Name,
							source:    fmt.Sprintf("Cloud (%s)", pName),
							modelType: "cloud",
						})
					}
				}
			}
		}

		if len(items) == 0 {
			fmt.Fprintln(w, "No models found (neither local Ollama models nor enabled cloud providers).")
			return true, false, nil
		}

		fmt.Fprintln(w, "\nAvailable Models:")
		tbl := ui.NewTable("INDEX", "MODEL NAME", "PROVIDER/SOURCE", "TYPE")
		for idx, item := range items {
			tbl.AddRow(
				fmt.Sprintf("  [%d]", idx+1),
				item.name,
				item.source,
				item.modelType,
			)
		}
		tbl.Render(w)
		fmt.Fprintln(w)

		fmt.Fprint(w, "Select a model by index number (or press Enter to cancel): ")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text == "" {
				fmt.Fprintln(w, "Cancelled.")
				return true, false, nil
			}
			var index int
			if _, err := fmt.Sscanf(text, "%d", &index); err != nil || index < 1 || index > len(items) {
				fmt.Fprintf(errW, "Invalid index: %q\n", text)
				return true, false, nil
			}
			selected := items[index-1]
			err := switchAgentModel(a, freshRt, selected.name, errW)
			if err != nil {
				return true, false, err
			}
		}
		return true, false, nil

	case "/model":
		if len(parts) < 2 {
			fmt.Fprintln(errW, "Error: /model requires a model name, e.g. /model llama3.2:3b or /model claude-3-5-sonnet")
			return true, false, nil
		}
		newModel := parts[1]
		err := switchAgentModel(a, rt, newModel, errW)
		if err != nil {
			return true, false, err
		}
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
			Description:   command,
			Mode:          execution.ModeExec,
			Confirm:       execution.ConfirmWord,
			RequestedNode: localNodeName,
			OwnerSurface:  execution.OwnerSurfaceAgentRunShell,
			OwnerLabel:    strings.TrimSpace(model),
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

func switchAgentModel(a *agent.Agent, rt *runtimectx.Context, newModel string, errW io.Writer) error {
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

		var err error
		newBackend, err = agent.NewCloudBackendWithKey(bestCloudProvider.Kind, bestCloudProviderName, bestCloudProvider.Endpoint, bestCloudAPIKey, newModel, costPer1K)
		if err != nil {
			return fmt.Errorf("invalid cloud backend config: %w", err)
		}
	}

	var securityClass agent.BackendSecurityClass
	if useCloud {
		securityClass = agent.BackendRemote
		fmt.Fprintf(errW, "Switched to cloud provider %q, model %q\n", bestCloudProviderName, newModel)
	} else {
		securityClass = agent.BackendLocal
		endpoint := chat.DefaultEndpoint
		newBackend = chat.NewClient(endpoint, newModel)
		fmt.Fprintf(errW, "Switched to local Ollama model %q\n", newModel)
	}

	a.SetBackend(newBackend, securityClass)
	return nil
}
