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

var currentHostname = os.Hostname
var collectLocalFacts = func(ctx context.Context, hostname string) (*models.NodeFacts, error) {
	return facts.NewLocalCollector(hostname, "").Collect(ctx)
}

func factsCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "facts",
		Short: "Collect and display local node facts",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			hostname, _ := currentHostname()
			nf, err := collectLocalFacts(ctx, hostname)
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
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or yaml")
	return cmd
}
