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
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
)

type serveDaemon interface {
	Start(context.Context)
	WatchConfig(context.Context, string)
	WaitStopped(context.Context)
	Snapshot() (*models.ClusterSnapshot, bool)
	Meta() daemon.Metadata
	Invalidate()
	RefreshNow(context.Context) error
}

var newServeDaemon = func(refreshInterval time.Duration) serveDaemon {
	return daemon.NewDefault(refreshInterval)
}

var serveHTTPAPI = func(ctx context.Context, addr string, d serveDaemon, token string) error {
	return api.ServeWithContext(ctx, addr, d, token)
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
			d.WatchConfig(ctx, config.DefaultConfigPath())

			protocol := "http"
			if auth.IsUnixAddr(addr) {
				protocol = "unix"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "AXIS HTTP API listening on %s://%s\n", protocol, addr)

			// ServeWithContext blocks until ctx is cancelled or a listen error.
			// On SIGTERM/SIGINT, ctx is cancelled, HTTP drains, then we wait for
			// the background refresh goroutines to finish.
			err = serveHTTPAPI(ctx, addr, d, token)

			drainCtx, cancel := context.WithTimeout(context.Background(), daemon.ShutdownDrainTimeout)
			defer cancel()
			d.WaitStopped(drainCtx)

			return err
		},
	}

	cmd.Flags().StringVar(&addr, "addr", api.DefaultAddr(), "Listen address for the local AXIS API (Unix socket or TCP host:port)")
	cmd.Flags().DurationVar(&refreshInterval, "refresh", time.Minute, "Background snapshot refresh interval")
	return cmd
}
