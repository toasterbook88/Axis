package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/cortex"
	"github.com/toasterbook88/axis/internal/secrets"
	"github.com/toasterbook88/axis/internal/ui"
)

const cortexDefaultTimeout = 10 * time.Second
const cortexRecallTimeout = 45 * time.Second

func cortexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cortex",
		Short: "Interact with the Cortex cluster brain",
		Long: "Cortex is an optional coordination layer.\n" +
			"It provides distributed vector memory (Qdrant), a CI/CD event bus,\n" +
			"and cross-agent locking.\n\n" +
			"Target node is resolved from AXIS_CORTEX_NODE, role: cortex in nodes.yaml,\n" +
			"or a node named cortex (or foundry for backward compatibility).\n" +
			"Auth token: AXIS_CORTEX_SECRET env var or ~/.axis/cortex.token file.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(cortexStatusCmd())
	cmd.AddCommand(cortexEventsCmd())
	cmd.AddCommand(cortexRecallCmd())

	return cmd
}

// resolveCortexNode finds the target node for Cortex from configuration or environment.
// Precedence:
// 1. AXIS_CORTEX_NODE environment variable (explicit node name)
// 2. Node with role: cortex in nodes.yaml
// 3. Node named "cortex" in nodes.yaml
// 4. Node named "foundry" in nodes.yaml (backward compatibility)
func resolveCortexNode(cfg *config.Config) (config.NodeConfig, bool) {
	if cfg == nil {
		return config.NodeConfig{}, false
	}
	if envNode := strings.TrimSpace(os.Getenv("AXIS_CORTEX_NODE")); envNode != "" {
		if node, ok := cfg.FindNode(envNode); ok {
			return node, true
		}
	}
	for _, n := range cfg.Nodes {
		if strings.EqualFold(n.Role, "cortex") {
			return n, true
		}
	}
	if node, ok := cfg.FindNode("cortex"); ok {
		return node, true
	}
	if node, ok := cfg.FindNode("foundry"); ok {
		return node, true
	}
	return config.NodeConfig{}, false
}

// buildCortexClient resolves the Cortex target node from nodes.yaml and the auth token from
// secrets, then returns a ready-to-use cortex.Client with the given HTTP timeout.
// It returns an actionable error if no Cortex node is configured.
func buildCortexClient(timeout time.Duration) (*cortex.Client, error) {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return nil, fmt.Errorf("cortex: load config: %w", err)
	}

	node, ok := resolveCortexNode(cfg)
	if !ok {
		return nil, fmt.Errorf(
			"cortex: no Cortex node configured in %s\n"+
				"  set AXIS_CORTEX_NODE=<name>, or configure a node with role: cortex (or name: cortex)",
			config.DefaultConfigPath(),
		)
	}

	token, err := secrets.ResolveOrEmpty("AXIS_CORTEX_SECRET", "~/.axis/cortex.token")
	if err != nil {
		return nil, fmt.Errorf("cortex: resolve auth token: %w", err)
	}

	return cortex.NewClientWithTimeout(node.PrimaryHostname(), token, timeout), nil
}

func cortexStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Cortex brain health and memory count",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildCortexClient(cortexDefaultTimeout)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), cortexDefaultTimeout)
			defer cancel()

			status, err := client.Status(ctx)
			if err != nil {
				return fmt.Errorf("cortex unreachable: %w", err)
			}

			return printCortexStatus(cmd.OutOrStdout(), status)
		},
	}
}

func cortexEventsCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "events",
		Short: "List recent events from the Cortex event bus",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildCortexClient(cortexDefaultTimeout)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), cortexDefaultTimeout)
			defer cancel()

			events, err := client.Events(ctx, limit)
			if err != nil {
				return err
			}

			return printCortexEvents(cmd.OutOrStdout(), events)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Number of events to show")
	return cmd
}

func cortexRecallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recall <query>",
		Short: "Semantic search across Cortex cluster memories",
		Long: "Runs a semantic similarity search over the Qdrant cortex_memories\n" +
			"collection via the Cortex MCP recall tool.\n\n" +
			"Results are ranked by relevance score (1.0 = exact match).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildCortexClient(cortexRecallTimeout)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), cortexRecallTimeout)
			defer cancel()

			hits, err := client.Recall(ctx, args[0])
			if err != nil {
				return err
			}

			return printCortexRecall(cmd.OutOrStdout(), args[0], hits)
		},
	}
}

func printCortexStatus(w io.Writer, status *cortex.HealthResponse) error {
	var out strings.Builder
	fmt.Fprintf(&out, "\n  %s\n\n", ui.Bold("CORTEX BRAIN STATUS"))
	fmt.Fprintf(&out, "  %-16s %s\n", "Status:", ui.Green(status.Status))
	fmt.Fprintf(&out, "  %-16s %d\n", "MCP Tools:", status.MCPTools)
	fmt.Fprintf(&out, "  %-16s %d (Qdrant points)\n", "Memories:", status.Memories)
	fmt.Fprintln(&out)
	_, err := fmt.Fprint(w, out.String())
	return err
}

func printCortexEvents(w io.Writer, events []cortex.Event) error {
	var out strings.Builder
	fmt.Fprintf(&out, "\n  %s (last %d)\n\n", ui.Bold("CORTEX EVENT BUS"), len(events))

	if len(events) == 0 {
		fmt.Fprintf(&out, "  %s\n\n", ui.Dim("no events"))
	} else {
		for _, ev := range events {
			evType := strings.ToUpper(ev.Type)
			if strings.Contains(ev.Type, "failure") || strings.Contains(ev.Type, "error") {
				evType = ui.Red(evType)
			} else {
				evType = ui.Cyan(evType)
			}
			fmt.Fprintf(&out, "  [%s] %s  %s\n", ev.CreatedAt, evType, formatPayload(ev.Payload))
		}
		fmt.Fprintln(&out)
	}

	_, err := fmt.Fprint(w, out.String())
	return err
}

func printCortexRecall(w io.Writer, query string, hits []cortex.MemoryHit) error {
	var out strings.Builder
	fmt.Fprintf(&out, "\n  %s %q\n\n", ui.Bold("RECALL:"), query)

	if len(hits) == 0 {
		fmt.Fprintf(&out, "  %s\n\n", ui.Dim("no results"))
	} else {
		for i, hit := range hits {
			fmt.Fprintf(&out, "  [%d] score=%.3f\n      %s\n\n",
				i+1, hit.Score, hit.Content)
		}
	}

	_, err := fmt.Fprint(w, out.String())
	return err
}

// formatPayload renders an Event.Payload for terminal display.
// If the payload is a JSON string, the surrounding quotes are stripped.
// Object and array payloads are printed as-is (compact JSON).
func formatPayload(p json.RawMessage) string {
	var s string
	if err := json.Unmarshal(p, &s); err == nil {
		return s
	}
	return string(p)
}
