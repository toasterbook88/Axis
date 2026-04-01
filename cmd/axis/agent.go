package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
)

func agentCmd() *cobra.Command {
	var (
		model       string
		timeout     time.Duration
		maxTokens   int
		maxTurns    int
		autoApprove bool
		systemMsg   string
		format      string
	)

	cmd := &cobra.Command{
		Use:   "agent [instruction...]",
		Short: "Agentic tool-calling assistant",
		Long: "Run an AI agent that can call AXIS tools to answer cluster questions.\n\n" +
			"The agent uses the Ollama /api/chat endpoint with tool calling.\n" +
			"It can run read-only cluster queries (status, facts, placement) and\n" +
			"execute shell commands with operator confirmation.\n\n" +
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
			}

			a := agent.New(cfg)

			// Single-shot mode.
			if len(args) > 0 {
				instruction := strings.Join(args, " ")
				fmt.Fprintf(errW, "Agent [%s] — max %d turns\n\n", ui.Bold(currentModel), maxTurns)

				ctx, cancel := agentRequestContext(timeout)
				defer cancel()
				if err := a.Run(ctx, instruction); err != nil {
					Fatal(ExitErrCommandFail, "Agent failed: %v", err)
				}
				fmt.Fprintln(w)
				return nil
			}

			// Interactive REPL.
			fmt.Fprintf(errW, "AXIS Agent [%s] — max %d turns per query, type exit to quit\n\n", ui.Bold(currentModel), maxTurns)

			scanner := bufio.NewScanner(os.Stdin)
			for {
				fmt.Fprint(errW, ui.Cyan("agent> "))
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
			return nil
		},
	}

	cmd.Flags().StringVarP(&model, "model", "m", "", "Ollama model (default: best installed)")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 5*time.Minute, "Per-request timeout")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 4096, "Conversation token budget")
	cmd.Flags().IntVar(&maxTurns, "max-turns", 10, "Maximum agent loop iterations per query")
	cmd.Flags().BoolVar(&autoApprove, "auto-approve", false, "Auto-approve safe commands (safety score < 70)")
	cmd.Flags().StringVar(&systemMsg, "system", "", "Extra text appended to system prompt")
	cmd.Flags().StringVar(&format, "format", "text", "Output format (text, json)")
	return cmd
}

func agentRequestContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}
