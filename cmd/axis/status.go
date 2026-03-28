package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

type statusOutput struct {
	Source   string                  `json:"source" yaml:"source"`
	Snapshot *models.ClusterSnapshot `json:"snapshot" yaml:"snapshot"`
}

func statusCmd() *cobra.Command {
	var format string
	var cached bool
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Collect cluster snapshot from all configured nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			snap, source, err := collectStatusSnapshot(
				ctx,
				cached,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return daemon.FetchSnapshot(ctx, cacheAddr)
				},
				discoverLiveSnapshot,
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return err
			}

			var payload any = snap
			if cached {
				payload = statusOutput{
					Source:   source,
					Snapshot: snap,
				}
			}

			if err := printOutput(payload, format); err != nil {
				fmt.Fprintf(os.Stderr, "output error: %v\n", err)
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or yaml")
	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr, "Address of the local AXIS daemon cache")
	return cmd
}

func collectStatusSnapshot(
	ctx context.Context,
	cached bool,
	cachedLoader func(context.Context) (*models.ClusterSnapshot, string, error),
	liveLoader func(context.Context) (*models.ClusterSnapshot, string, error),
) (*models.ClusterSnapshot, string, error) {
	if cached && cachedLoader != nil {
		snap, source, err := cachedLoader(ctx)
		if err == nil {
			return snap, source, nil
		}
	}

	return liveLoader(ctx)
}

func discoverLiveSnapshot(ctx context.Context) (*models.ClusterSnapshot, string, error) {
	rt, err := runtimectx.Load(ctx)
	if err != nil {
		return nil, "", err
	}
	return rt.Snapshot, "live", nil
}
