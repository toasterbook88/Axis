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
)

func serveCmd() *cobra.Command {
	var addr string
	var refreshInterval time.Duration

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the local AXIS HTTP API with background snapshot refresh",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			d := daemon.NewDefault(refreshInterval)
			d.Start(ctx)

			fmt.Fprintf(cmd.OutOrStdout(), "AXIS HTTP API listening on http://%s\n", addr)
			return api.Serve(addr, d)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", api.DefaultAddr, "Listen address for the local AXIS HTTP API")
	cmd.Flags().DurationVar(&refreshInterval, "refresh", time.Minute, "Background snapshot refresh interval")
	return cmd
}
