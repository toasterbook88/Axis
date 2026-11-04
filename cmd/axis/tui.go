package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/toasterbook88/axis/cmd/axis/tui"
	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/console"
)

func tuiCmd() *cobra.Command {
	var (
		dashboardOnly bool
		model         string
		autonomy      string
	)
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the AXIS terminal workspace (agent + cluster)",
		Long: `Launch the full-screen AXIS workspace.

The agent conversation fills the main pane. It is the same in-process agent
as axis agent: streaming answers, tool cards, approvals, /model, /skills,
/mcp and @node completion. A sidebar shows live cluster health from the
cached snapshot, and the cluster dashboard is one key away (F2 or ctrl+x d).
ctrl+o opens the command palette.

Agent output is advisory. The sidebar and dashboard show only what the
cached snapshot observed.

Requires an interactive terminal (TTY).

Examples:
  axis tui                     # Workspace: agent + cluster sidebar
  axis tui --dashboard         # Cluster dashboard only, no agent
  axis tui --autonomy cluster  # Agent limited to AXIS cluster reads
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check both stdin AND stdout for TTY capability
			if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
				return fmt.Errorf("axis tui requires an interactive terminal")
			}
			if dashboardOnly {
				return runDashboard()
			}
			if _, err := agent.ParseAutonomyMode(autonomy); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			_, err := runAgentConsoleStartup(ctx, agentStartupInputs{
				model:     model,
				provider:  "auto",
				maxTurns:  25,
				maxTokens: 32768,
				timeout:   5 * time.Minute,
				autonomy:  autonomy,
				// The workspace owns the screen; nothing may write to it
				// outside the renderer.
				out:     io.Discard,
				program: newWorkspaceProgram,
			}, cmd.ErrOrStderr())
			return err
		},
	}
	cmd.Flags().BoolVar(&dashboardOnly, "dashboard", false, "Open only the cluster dashboard, without the agent")
	cmd.Flags().StringVarP(&model, "model", "m", "", "Agent model tag (default: agent.default_model, warm preferred, or ai.yaml role default)")
	cmd.Flags().StringVar(&autonomy, "autonomy", "default", "Agent autonomy mode: cluster, default, edit, full (see axis agent --help)")
	return cmd
}

// newWorkspaceProgram hosts the agent console, in embedded mode, inside the
// full-screen workspace on the alternate screen.
func newWorkspaceProgram(ctx context.Context, opts console.Options) consoleProgram {
	opts.Embedded = true
	ws := tui.NewWorkspace(console.NewModel(opts))
	return tea.NewProgram(ws, append(consoleOptions(ctx).teaOptions(), tea.WithAltScreen())...)
}

func runDashboard() error {
	p := tea.NewProgram(tui.NewModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}
