package main

import "github.com/spf13/cobra"

func clusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "The fleet: status, summary",
		Long: `Look at every configured node.

  axis cluster status     live snapshot (opt in to cache with --cached)
  axis cluster summary    one-screen dashboard

This machine is axis node facts. Health checks are axis doctor.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(statusCmd())
	cmd.AddCommand(summaryCmd())
	return cmd
}
