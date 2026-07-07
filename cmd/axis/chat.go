package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/transport"
	"github.com/toasterbook88/axis/internal/ui"
)

// Injected for testing.
var resolveDefaultChatModel = chat.ResolveDefaultModel
var formatChatCatalog = func(ctx context.Context, currentModel string) string {
	return chat.FormatModelCatalog(chat.BuildModelCatalog(ctx, currentModel))
}
var chatEndpoint = chat.DefaultEndpoint
var loadRuntimeContext = runtimectx.Load

func chatCmd() *cobra.Command {
	var (
		model      string
		timeout    time.Duration
		maxTokens  int
		useContext bool
		systemMsg  string
		format     string
		resume     bool
		verbose    bool
	)

	cmd := &cobra.Command{
		Use:   "chat [message...]",
		Short: "Cluster-aware chat assistant",
		Long: "Chat with a local LLM that understands your cluster.\n\n" +
			"Uses the Ollama /api/chat endpoint with structured messages.\n" +
			"Chat responses are advisory only and must not be treated as cluster truth\n" +
			"unless backed by `axis status`, `axis facts`, or a live probe.\n\n" +
			"Slash commands:\n" +
			"  /clear     — clear conversation history\n" +
			"  /status    — show cluster status summary\n" +
			"  /facts     — show local hardware facts\n" +
			"  /models    — list available models\n" +
			"  /model TAG — switch to a different model\n" +
			"  /help      — show this help\n",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			// Load runtime context for auto-routing and context injection.
			rt, _ := loadRuntimeContext(ctx)
			
			currentModel := resolveChatModel(model, rt)
			if verbose && model == "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Resolved model: %s\n", currentModel)
			}
			w := cmd.OutOrStdout()
			errW := cmd.ErrOrStderr()

			// Build optional cluster context.
			var cluster *chat.ClusterSummaryForPrompt
			if useContext && rt != nil && rt.Snapshot != nil {
				cluster = chat.BuildClusterSummary(rt.Snapshot)
			}

			// --- Intelligent Auto-Routing (Phase E) ---
			endpoint := chatEndpoint
			if rt != nil && rt.Snapshot != nil && rt.State != nil {
				reqs := placement.InferRequirements(fmt.Sprintf("ollama run %s", currentModel))
				decision := placement.SelectBestNode(reqs, rt.Snapshot.Nodes, rt.State)
				if decision.OK && !decision.IsLocal {
					if targetConfig, ok := rt.Config.FindNode(decision.Node); ok {
						executor := transport.NewSSHExecutor(targetConfig.Hostname, targetConfig.EffectiveSSHPort(), targetConfig.SSHUser, targetConfig.EffectiveTimeout())
						defer executor.Close()
						boundPort, stopForward, err := executor.ForwardLocal(ctx, 0, 11434)
						if err != nil {
							fmt.Fprintf(errW, "%s Failed to tunnel to %s: %v (falling back to local)\n", ui.Yellow("!"), decision.Node, err)
						} else {
							defer stopForward()
							endpoint = fmt.Sprintf("http://127.0.0.1:%d", boundPort)
							fmt.Fprintf(errW, "%s Auto-routed %s to %s (zero-latency inference tunnel active)\n", ui.Green("✓"), ui.Bold(currentModel), ui.Bold(decision.Node))
						}
					}
				} else if decision.OK && decision.IsLocal && verbose {
					fmt.Fprintf(errW, "Routed to local node (%s)\n", decision.Node)
				}
			}

			// Build client and conversation.
			client := chat.NewClient(endpoint, currentModel)
			conv := chat.NewConversation(maxTokens)
			sysPrompt := chat.BuildSystemPrompt(cluster, systemMsg)
			conv.Append(chat.Message{Role: chat.RoleSystem, Content: sysPrompt})

			// Resume previous conversation if requested.
			historyPath, err := chat.PersistPath("chat")
			if err != nil {
				fmt.Fprintf(errW, "warning: cannot determine history path: %v\n", err)
			} else if resume {
				if err := conv.LoadFromFile(historyPath); err != nil {
					fmt.Fprintf(errW, "warning: could not resume conversation: %v\n", err)
				} else if n := conv.HistoryCount(); n > 0 {
					fmt.Fprintf(errW, "Resumed %d messages from previous session.\n", n)
				}
			}

			fmt.Fprintln(errW, ui.Dim("advisory: chat output is not cluster truth — validate with axis status or axis facts"))

			// Single-shot mode.
			if len(args) > 0 {
				query := strings.Join(args, " ")
				conv.Append(chat.Message{Role: chat.RoleUser, Content: query})

				sp := ui.NewSpinner()
				sp.Start("Thinking...")

				ctx2, cancel := chatRequestContext(ctx, timeout)
				defer cancel()

				// When --format json, suppress streaming to stdout and
				// only print the structured response afterward.
				streamW := w
				if format == "json" {
					streamW = io.Discard
				}
				resp, err := client.ChatStream(ctx2, conv.Messages(), nil, streamW)
				sp.Stop("")

				if err != nil {
					fmt.Fprintf(errW, "error: Chat failed: %v\n", err)
					return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("chat failed: %v", err)}
				}
				if format == "json" {
					conv.Append(resp)
					printOutput(cmd.OutOrStdout(), resp, format)
				} else {
					fmt.Fprintln(w)
				}
				// Save conversation after single-shot.
				if historyPath != "" {
					_ = conv.SaveToFile(historyPath)
				}
				return nil
			}

			// Interactive REPL with readline.
			fmt.Fprintf(errW, "AXIS Chat [%s] — type /help for commands, exit to quit\n\n", ui.Bold(currentModel))

			cfg := &readline.Config{
				Prompt:          ui.Cyan(">>> "),
				InterruptPrompt: "^C",
				EOFPrompt:       "exit",
			}
			if historyPath != "" {
				cfg.HistoryFile = historyPath + ".line"
			}
			rl, err := readline.NewEx(cfg)
			if err != nil {
				// Fallback to plain scanner if readline fails (e.g., non-TTY).
				return runPlainREPL(ctx, client, conv, currentModel, w, errW, timeout, historyPath)
			}
			defer rl.Close()

			for {
				line, err := rl.Readline()
				if err != nil { // io.EOF or readline.ErrInterrupt
					break
				}
				query := strings.TrimSpace(line)
				if query == "" {
					continue
				}
				lower := strings.ToLower(query)
				if lower == "exit" || lower == "quit" {
					break
				}

				// Slash commands.
				if strings.HasPrefix(query, "/") {
					nextModel := handleSlashCommand(query, currentModel, conv, errW)
					if nextModel != "" {
						currentModel = nextModel
						client = chat.NewClient(chat.DefaultEndpoint, currentModel)
					}
					continue
				}

				conv.Append(chat.Message{Role: chat.RoleUser, Content: query})

				sp := ui.NewSpinner()
				sp.Start("Thinking...")

				ctx2, cancel := chatRequestContext(ctx, timeout)
				resp, err := client.ChatStream(ctx2, conv.Messages(), nil, w)
				sp.Stop("")
				cancel()

				if err != nil {
					fmt.Fprintf(errW, "\n%s\n", ui.Red("Error: ", err))
					continue
				}
				conv.Append(resp)
				fmt.Fprintln(w)
			}

			// Save conversation on exit.
			if historyPath != "" && conv.HistoryCount() > 0 {
				if err := conv.SaveToFile(historyPath); err != nil {
					fmt.Fprintf(errW, "warning: could not save conversation: %v\n", err)
				} else {
					fmt.Fprintf(errW, "Saved %d messages to conversation history.\n", conv.HistoryCount())
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&model, "model", "m", "", "Ollama model (default: chat.default_model or best installed)")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 2*time.Minute, "Per-request timeout")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 4096, "Conversation token budget")
	cmd.Flags().BoolVar(&useContext, "context", false, "Inject live cluster snapshot into system prompt")
	cmd.Flags().StringVar(&systemMsg, "system", "", "Extra text appended to system prompt")
	cmd.Flags().StringVar(&format, "format", "text", "Output format for single-shot mode (text, json)")
	cmd.Flags().BoolVar(&resume, "resume", false, "Resume previous conversation from history")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Print model resolution and other debug info")
	return cmd
}

// runPlainREPL is the fallback scanner-based REPL when readline is unavailable.
func runPlainREPL(ctx context.Context, client *chat.Client, conv *chat.Conversation, currentModel string, w, errW io.Writer, timeout time.Duration, historyPath string) error {
	fmt.Fprintln(errW, ui.Yellow("Note: using plain input mode (no arrow keys or history)"))
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(errW, ui.Cyan(">>> "))
		if !scanner.Scan() {
			break
		}
		query := strings.TrimSpace(scanner.Text())
		if query == "" {
			continue
		}
		lower := strings.ToLower(query)
		if lower == "exit" || lower == "quit" {
			break
		}
		if strings.HasPrefix(query, "/") {
			nextModel := handleSlashCommand(query, currentModel, conv, errW)
			if nextModel != "" {
				currentModel = nextModel
				client = chat.NewClient(chat.DefaultEndpoint, currentModel)
			}
			continue
		}
		conv.Append(chat.Message{Role: chat.RoleUser, Content: query})
		sp := ui.NewSpinner()
		sp.Start("Thinking...")
		ctx2, cancel := chatRequestContext(ctx, timeout)
		resp, err := client.ChatStream(ctx2, conv.Messages(), nil, w)
		sp.Stop("")
		cancel()
		if err != nil {
			fmt.Fprintf(errW, "\n%s\n", ui.Red("Error: ", err))
			continue
		}
		conv.Append(resp)
		fmt.Fprintln(w)
	}
	if historyPath != "" && conv.HistoryCount() > 0 {
		_ = conv.SaveToFile(historyPath)
	}
	if err := scanner.Err(); err != nil {
		return ExitCodeError{Code: ExitErrIO, Message: fmt.Sprintf("input stream closed: %v", err)}
	}
	return nil
}

// handleSlashCommand processes a slash command and returns a new model name
// if the model was switched, or empty string otherwise.
func handleSlashCommand(input, currentModel string, conv *chat.Conversation, w io.Writer) string {
	query := strings.TrimSpace(input)
	switch {
	case query == "/clear":
		conv.Clear()
		fmt.Fprintln(w, ui.Green("✓ Conversation cleared"))
	case query == "/status":
		snap := loadSnapshotQuietly(context.Background())
		if snap == nil {
			fmt.Fprintln(w, ui.Yellow("No cluster snapshot available"))
		} else {
			summary := chat.BuildClusterSummary(snap)
			if summary != nil {
				fmt.Fprintf(w, "Nodes: %d total, %d reachable\n", summary.NodeCount, summary.ReachableCount)
				fmt.Fprintf(w, "RAM: %d MB total, %d MB free\n", summary.TotalRAMMB, summary.FreeRAMMB)
				fmt.Fprintf(w, "Status: %s\n", summary.Status)
				if len(summary.Tools) > 0 {
					fmt.Fprintf(w, "Tools: %s\n", strings.Join(summary.Tools, ", "))
				}
			}
		}
	case query == "/facts":
		snap := loadSnapshotQuietly(context.Background())
		if snap == nil {
			fmt.Fprintln(w, ui.Yellow("No cluster snapshot available"))
		} else {
			if n, ok := models.FindLocalNode(snap.Nodes); ok {
				fmt.Fprintf(w, "Node: %s (%s/%s)\n", n.Name, n.OS, n.Arch)
				if n.Resources != nil {
					fmt.Fprintf(w, "CPU: %d cores\n", n.Resources.CPUCores)
					fmt.Fprintf(w, "RAM: %d MB total, %d MB free\n", n.Resources.RAMTotalMB, n.Resources.RAMFreeMB)
				}
			} else {
				fmt.Fprintln(w, ui.Yellow("Local node not found in snapshot"))
			}
		}
	case query == "/models":
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		fmt.Fprintln(w, formatChatCatalog(ctx, currentModel))
	case strings.HasPrefix(query, "/model"):
		parts := strings.Fields(query)
		if len(parts) < 2 {
			fmt.Fprintln(w, "Usage: /model <tag>")
			return ""
		}
		next := parts[1]
		fmt.Fprintf(w, "%s Switched to %s\n", ui.Green("✓"), ui.Bold(next))
		return next
	case query == "/help":
		fmt.Fprintln(w, "Slash commands:")
		fmt.Fprintln(w, "  /clear     — clear conversation history")
		fmt.Fprintln(w, "  /status    — show cluster status summary")
		fmt.Fprintln(w, "  /facts     — show local hardware facts")
		fmt.Fprintln(w, "  /models    — list available models")
		fmt.Fprintln(w, "  /model TAG — switch to a different model")
		fmt.Fprintln(w, "  /help      — show this help")
	default:
		fmt.Fprintf(w, "%s Unknown command: %s (try /help)\n", ui.Yellow("?"), query)
	}
	return ""
}

// loadSnapshotQuietly loads a cluster snapshot without printing errors.
func loadSnapshotQuietly(ctx context.Context) *models.ClusterSnapshot {
	rt, err := runtimectx.Load(ctx)
	if err != nil || rt.Snapshot == nil {
		return nil
	}
	return rt.Snapshot
}

func chatRequestContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func resolveChatModel(requested string, rt *runtimectx.Context) string {
	return resolveChatModelFromPath(requested, config.DefaultConfigPath(), rt)
}

// resolveChatModelFromPath is the testable core of resolveChatModel. cfgPath
// allows tests to inject a temporary config file without touching the real
// ~/.axis/nodes.yaml.
func resolveChatModelFromPath(requested, cfgPath string, rt *runtimectx.Context) string {
	// Explicit --model flag always wins.
	if strings.TrimSpace(requested) != "" {
		return strings.TrimSpace(requested)
	}
	// Operator-configured default in nodes.yaml chat.default_model.
	if cfg, err := config.Load(cfgPath); err == nil {
		if cfg.Chat != nil && strings.TrimSpace(cfg.Chat.DefaultModel) != "" {
			return strings.TrimSpace(cfg.Chat.DefaultModel)
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: failed to load chat config from %s: %v\n", cfgPath, err)
	}
	// Auto-detect: pick the best available resident model across all nodes (warm cache awareness)
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
	// Fallback: pick the best available locally installed model.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	return resolveDefaultChatModel(ctx)
}
