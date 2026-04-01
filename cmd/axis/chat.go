package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
)

// Injected for testing.
var resolveDefaultChatModel = chat.ResolveDefaultModel
var formatChatCatalog = func(ctx context.Context, currentModel string) string {
	return chat.FormatModelCatalog(chat.BuildModelCatalog(ctx, currentModel))
}

func chatCmd() *cobra.Command {
	var (
		model      string
		timeout    time.Duration
		maxTokens  int
		useContext bool
		systemMsg  string
		format     string
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
			currentModel := resolveChatModel(model)
			w := cmd.OutOrStdout()
			errW := cmd.ErrOrStderr()

			// Build optional cluster context.
			var cluster *chat.ClusterSummaryForPrompt
			if useContext {
				if snap := loadSnapshotQuietly(cmd.Context()); snap != nil {
					cluster = chat.BuildClusterSummary(snap)
				}
			}

			// Build client and conversation.
			client := chat.NewClient(chat.DefaultEndpoint, currentModel)
			conv := chat.NewConversation(maxTokens)
			sysPrompt := chat.BuildSystemPrompt(cluster, systemMsg)
			conv.Append(chat.Message{Role: chat.RoleSystem, Content: sysPrompt})

			fmt.Fprintln(errW, ui.Dim("advisory: chat output is not cluster truth — validate with axis status or axis facts"))

			// Single-shot mode.
			if len(args) > 0 {
				query := strings.Join(args, " ")
				conv.Append(chat.Message{Role: chat.RoleUser, Content: query})

				sp := ui.NewSpinner()
				sp.Start("Thinking...")

				ctx, cancel := chatRequestContext(timeout)
				defer cancel()
				resp, err := client.ChatStream(ctx, conv.Messages(), nil, w)
				sp.Stop("")

				if err != nil {
					Fatal(ExitErrCommandFail, "Chat failed: %v", err)
				}
				fmt.Fprintln(w)
				if format == "json" {
					conv.Append(resp)
					printOutput(resp, format)
				}
				return nil
			}

			// Interactive REPL.
			fmt.Fprintf(errW, "AXIS Chat [%s] — type /help for commands, exit to quit\n\n", ui.Bold(currentModel))
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

				ctx, cancel := chatRequestContext(timeout)
				resp, err := client.ChatStream(ctx, conv.Messages(), nil, w)
				sp.Stop("")
				cancel()

				if err != nil {
					fmt.Fprintf(errW, "\n%s\n", ui.Red("Error: ", err))
					continue
				}
				conv.Append(resp)
				fmt.Fprintln(w)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&model, "model", "m", "", "Ollama model (default: best installed)")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 2*time.Minute, "Per-request timeout")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 4096, "Conversation token budget")
	cmd.Flags().BoolVar(&useContext, "context", false, "Inject live cluster snapshot into system prompt")
	cmd.Flags().StringVar(&systemMsg, "system", "", "Extra text appended to system prompt")
	cmd.Flags().StringVar(&format, "format", "text", "Output format for single-shot mode (text, json)")
	return cmd
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
			hostname, _ := os.Hostname()
			for _, n := range snap.Nodes {
				if n.Hostname == hostname || n.Name == hostname {
					if n.Resources != nil {
						fmt.Fprintf(w, "Node: %s (%s/%s)\n", n.Name, n.OS, n.Arch)
						fmt.Fprintf(w, "CPU: %d cores\n", n.Resources.CPUCores)
						fmt.Fprintf(w, "RAM: %d MB total, %d MB free\n", n.Resources.RAMTotalMB, n.Resources.RAMFreeMB)
					}
					break
				}
			}
		}
	case query == "/models":
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		fmt.Fprintln(w, formatChatCatalog(ctx, currentModel))
	case strings.HasPrefix(query, "/model "):
		next := strings.TrimSpace(strings.TrimPrefix(query, "/model "))
		if next == "" {
			fmt.Fprintln(w, "Usage: /model <tag>")
			return ""
		}
		fmt.Fprintf(w, "%s Switched to %s\n", ui.Green("✓"), ui.Bold(next))
		return next
	case query == "/model":
		fmt.Fprintln(w, "Usage: /model <tag>")
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

func chatRequestContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}

func resolveChatModel(requested string) string {
	if strings.TrimSpace(requested) != "" {
		return strings.TrimSpace(requested)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	return resolveDefaultChatModel(ctx)
}
