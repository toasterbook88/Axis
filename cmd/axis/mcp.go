package main

import (
	"fmt"

	"github.com/spf13/cobra"
	axismcp "github.com/toasterbook88/axis/internal/mcp"
)

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Read-only MCP surfaces for AXIS cluster state and diagnostics",
	}
	cmd.AddCommand(mcpServeCmd())
	return cmd
}

func mcpServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start an ephemeral MCP server",
		RunE:  runMCPServe,
	}

	cmd.Flags().String("transport", "stdio", "MCP transport: stdio")
	return cmd
}

func runMCPServe(cmd *cobra.Command, args []string) error {
	transport, _ := cmd.Flags().GetString("transport")
	if transport != "stdio" {
		return fmt.Errorf("unsupported transport %q: only stdio is implemented", transport)
	}
	return axismcp.ServeStdio()
}
