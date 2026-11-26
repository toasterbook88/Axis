package main

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	axismcp "github.com/toasterbook88/axis/internal/mcp"
)

var (
	serveMCPStdio = axismcp.ServeStdio
	serveMCPHTTP  = axismcp.ServeStreamableHTTP
)

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Read-only MCP surfaces for AXIS cluster state and diagnostics",
	}
	cmd.AddCommand(mcpServeCmd())
	cmd.AddCommand(mcpClientCmd())
	return cmd
}

func mcpServeCmd() *cobra.Command {
	var cached bool
	var cacheAddr string
	var transport string
	var addr string
	var tokenEnv string
	var allowInsecureHTTP bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start an ephemeral MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch transport {
			case "stdio":
				return serveMCPStdio(cached, cacheAddr)
			case "http":
				ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
				defer stop()
				return serveMCPHTTP(ctx, addr, cached, cacheAddr, tokenEnv, allowInsecureHTTP)
			default:
				return fmt.Errorf("unsupported transport %q: supported transports are stdio, http", transport)
			}
		},
	}

	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
	cmd.Flags().StringVar(&transport, "transport", "stdio", "MCP transport: stdio or http")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:42426", "Listen address for Streamable HTTP transport (/mcp)")
	cmd.Flags().StringVar(&tokenEnv, "token-env", "", "Environment variable holding a Bearer token for Streamable HTTP transport (required)")
	cmd.Flags().BoolVar(&allowInsecureHTTP, "allow-insecure-http", false, "Allow a non-loopback Streamable HTTP listener without TLS. The bearer token is sent in cleartext")
	return cmd
}
