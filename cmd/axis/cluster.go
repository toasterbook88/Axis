package main

import "github.com/spf13/cobra"

func clusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "See the cluster: status, summary, facts, doctor",
		Long: `Look first, then act. These are the same commands as the root aliases:

  axis cluster status     snapshot of every configured node
  axis cluster summary    one-screen dashboard
  axis cluster facts      this machine only
  axis cluster doctor     config, SSH, and daemon

Root still accepts axis status, axis summary, axis facts, and axis doctor.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(statusCmd())
	cmd.AddCommand(summaryCmd())
	cmd.AddCommand(factsCmd())
	cmd.AddCommand(doctorCmd())
	return cmd
}
