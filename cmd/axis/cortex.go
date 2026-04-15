package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/cortex"
	"github.com/toasterbook88/axis/internal/secrets"
	"github.com/toasterbook88/axis/internal/ui"
)

const cortexDefaultTimeout = 10 * time.Second

func cortexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cortex",
		Short: "Interact with the Cortex cluster brain on Foundry",
		Long: "Cortex is an optional coordination layer running on Foundry.\n" +
			"It provides distributed vector memory (Qdrant), a CI/CD event bus,\n" +
			"and cross-agent locking.\n\n" +
			"Requires a node named \"foundry\" in nodes.yaml.\n" +
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

// buildCortexClient resolves Foundry from nodes.yaml and the auth token from
// secrets, then returns a ready-to-use cortex.Client.
// It returns an actionable error if foundry is not configured.
func buildCortexClient() (*cortex.Client, error) {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return nil, fmt.Errorf("cortex: load config: %w", err)
	}

	node, ok := cfg.FindNode("foundry")
	if !ok {
		return nil, fmt.Errorf(
			"cortex: no node named \"foundry\" found in nodes.yaml\n" +
				"  add a node with name: foundry, hostname: <ip>, ssh_user: <user>",
		)
	}

	token, err := secrets.ResolveOrEmpty("AXIS_CORTEX_SECRET", "~/.axis/cortex.token")
	if err != nil {
		return nil, fmt.Errorf("cortex: resolve auth token: %w", err)
	}

	return cortex.NewClient(node.Hostname, token), nil
}

func cortexStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Cortex brain health and memory count",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildCortexClient()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), cortexDefaultTimeout)
			defer cancel()

			status, err := client.Status(ctx)
			if err != nil {
				return fmt.Errorf("cortex unreachable: %w", err)
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "\n  %s\n\n", ui.Bold("CORTEX BRAIN STATUS"))
			fmt.Fprintf(w, "  %-16s %s\n", "Status:", ui.Green(status.Status))
			fmt.Fprintf(w, "  %-16s %d\n", "MCP Tools:", status.MCPTools)
			fmt.Fprintf(w, "  %-16s %d (Qdrant points)\n", "Memories:", status.Memories)
			fmt.Fprintln(w)
			return nil
		},
	}
}

func cortexEventsCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "events",
		Short: "List recent events from the Cortex event bus",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildCortexClient()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), cortexDefaultTimeout)
			defer cancel()

			events, err := client.Events(ctx, limit)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "\n  %s (last %d)\n\n", ui.Bold("CORTEX EVENT BUS"), len(events))

			if len(events) == 0 {
				fmt.Fprintf(w, "  %s\n\n", ui.Dim("no events"))
				return nil
			}

			for _, ev := range events {
				color := ui.Cyan
				evType := strings.ToUpper(ev.Type)
				if strings.Contains(ev.Type, "failure") || strings.Contains(ev.Type, "error") {
					color = ui.Red
					evType = ui.Red(evType)
				} else {
					evType = color(evType)
				}
				fmt.Fprintf(w, "  [%s] %s  %s\n", ev.CreatedAt, evType, formatPayload(ev.Payload))
			}
			fmt.Fprintln(w)
			return nil
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
			client, err := buildCortexClient()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			hits, err := client.Recall(ctx, args[0])
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "\n  %s %q\n\n", ui.Bold("RECALL:"), args[0])

			if len(hits) == 0 {
				fmt.Fprintf(w, "  %s\n\n", ui.Dim("no results"))
				return nil
			}

			for i, hit := range hits {
				fmt.Fprintf(w, "  [%d] score=%.3f\n      %s\n\n",
					i+1, hit.Score, hit.Content)
			}
			return nil
		},
	}
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
