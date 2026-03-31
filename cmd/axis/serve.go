package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
)

type serveDaemon interface {
	Start(context.Context)
	Snapshot() (*models.ClusterSnapshot, bool)
	Meta() daemon.Metadata
	Invalidate()
	RefreshNow(context.Context) error
}

var newServeDaemon = func(refreshInterval time.Duration) serveDaemon {
	return daemon.NewDefault(refreshInterval)
}

var serveHTTPAPI = func(addr string, d serveDaemon, token string) error {
	return api.Serve(addr, d, token)
}

func serveCmd() *cobra.Command {
	var addr string
	var refreshInterval time.Duration

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the local AXIS HTTP API with background snapshot refresh",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			token, err := auth.LoadOrGenerateToken()
			if err != nil {
				return fmt.Errorf("failed to load/generate API token: %w", err)
			}

			d := newServeDaemon(refreshInterval)
			d.Start(ctx)

			protocol := "http"
			if auth.IsUnixAddr(addr) {
				protocol = "unix"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "AXIS HTTP API listening on %s://%s\n", protocol, addr)
			return serveHTTPAPI(addr, d, token)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", api.DefaultAddr(), "Listen address for the local AXIS API (Unix socket or TCP host:port)")
	cmd.Flags().DurationVar(&refreshInterval, "refresh", time.Minute, "Background snapshot refresh interval")
	return cmd
}
