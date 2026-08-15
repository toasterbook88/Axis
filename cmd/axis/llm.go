package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func llmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "llm",
		Short: "Removed; use axis ai route",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("axis llm was removed in v0.15. Use: axis ai route")
		},
	}
}
