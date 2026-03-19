package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/snapshot"
)

func statusCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Collect cluster snapshot from all configured nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			nodes := discovery.Discover(ctx, cfg)
			snap := snapshot.Build(nodes)

			if err := printOutput(snap, format); err != nil {
				fmt.Fprintf(os.Stderr, "output error: %v\n", err)
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or yaml")
	return cmd
}
