package main

import (
	"fmt"

	"github.com/spf13/cobra"
	axismcp "github.com/toasterbook88/axis/internal/mcp"
)

var serveMCPStdio = axismcp.ServeStdio

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Read-only MCP surfaces for AXIS cluster state and diagnostics",
	}
	cmd.AddCommand(mcpServeCmd())
	return cmd
}

func mcpServeCmd() *cobra.Command {
	var cached bool
	var cacheAddr string
	var transport string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start an ephemeral MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if transport != "stdio" {
				return fmt.Errorf("unsupported transport %q: only stdio is implemented", transport)
			}
			return serveMCPStdio(cached, cacheAddr)
		},
	}

	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", "127.0.0.1:42425", "Address of the local AXIS daemon cache")
	cmd.Flags().StringVar(&transport, "transport", "stdio", "MCP transport: stdio")
	return cmd
}
