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
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
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
	return &cobra.Command{
		Use:   "chat",
		Short: "Removed; use axis agent",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("axis chat was removed in v0.15. Use: axis agent")
		},
	}
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
