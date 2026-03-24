package main

import (
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/skills"
)

func skillsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skills",
		Short: "Show what AXIS has learned from real usage",
		Run: func(cmd *cobra.Command, args []string) {
			s := skills.Load()
			printOutput(s, "json")
		},
	}
}
