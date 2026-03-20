package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
)

func factsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "facts",
		Short: "Collect and display local node facts",
		RunE:  runFacts,
	}

	cmd.Flags().String("format", "json", "Output format: json or yaml")
	return cmd
}

func runFacts(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("format")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hostname, _ := os.Hostname()
	collector := facts.NewLocalCollector(hostname, "")

	nf, err := collector.Collect(ctx)
	if err != nil {
		// Should not happen — LocalCollector tolerates failures —
		// but guard against it.
		nf = &models.NodeFacts{
			Name:        hostname,
			Status:      models.StatusError,
			Error:       err.Error(),
			CollectedAt: time.Now().UTC(),
		}
	}

	if err := printOutput(nf, format); err != nil {
		fmt.Fprintf(os.Stderr, "output error: %v\n", err)
		return err
	}
	return nil
}
