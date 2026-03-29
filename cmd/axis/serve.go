package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
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

var serveHTTPAPI = func(addr string, d serveDaemon) error {
	return api.Serve(addr, d)
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

			d := newServeDaemon(refreshInterval)
			d.Start(ctx)

			fmt.Fprintf(cmd.OutOrStdout(), "AXIS HTTP API listening on http://%s\n", addr)
			return serveHTTPAPI(addr, d)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", api.DefaultAddr, "Listen address for the local AXIS HTTP API")
	cmd.Flags().DurationVar(&refreshInterval, "refresh", time.Minute, "Background snapshot refresh interval")
	return cmd
}
