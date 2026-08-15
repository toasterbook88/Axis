package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func chatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "chat",
		Short: "Removed; use axis agent",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("axis chat was removed in v0.15. Use: axis agent")
		},
	}
}
