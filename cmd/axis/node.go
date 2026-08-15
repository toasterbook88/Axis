package main

import "github.com/spf13/cobra"

func nodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "This machine: facts",
		Long: `Inspect this node. Default target is localhost.

  axis node facts     this machine only

Root still accepts axis facts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(factsCmd())
	return cmd
}
