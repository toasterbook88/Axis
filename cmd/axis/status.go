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
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Collect cluster snapshot from all configured nodes",
		RunE:  runStatus,
	}

	cmd.Flags().String("format", "json", "Output format: json or yaml")
	return cmd
}

func runStatus(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("format")
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
}
