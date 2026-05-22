package main

import (
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/skills"
)

func skillsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skills",
		Short: "Show what AXIS has learned from real usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := skills.Load()
			if err != nil {
				if s == nil {
					return err
				}
				printWarning(err)
			}
			printOutput(cmd.OutOrStdout(), s, "json")
			return nil
		},
	}
}
