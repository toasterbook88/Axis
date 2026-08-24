package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/toasterbook88/axis/cmd/axis/tui"
)

func tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Launch the AXIS terminal user interface",
		Long: `Launch the AXIS cluster dashboard TUI.

The TUI provides an interactive, full-screen interface for monitoring
cluster health, exploring node details, managing reservations, and
executing common tasks.

Requires an interactive terminal (TTY). Will not work when piped or
redirected.

Examples:
  axis tui                    # Launch dashboard
  axis tui | cat              # Error: not a TTY
`,
		RunE: runTUI,
	}
}

func runTUI(cmd *cobra.Command, args []string) error {
	// Check both stdin AND stdout for TTY capability
	if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("axis tui requires an interactive terminal")
	}

	// Initialize and run the Bubble Tea program
	p := tea.NewProgram(tui.NewModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}
