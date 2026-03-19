package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/state"
)

func contextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Show or edit placement memory state",
	}
	
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show the current cluster placement state",
		Run: func(cmd *cobra.Command, args []string) {
			st, _ := state.Load()
			out, _ := json.MarshalIndent(st, "", "  ")
			fmt.Println(string(out))
		},
	})
	
	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear the cluster placement memory",
		Run: func(cmd *cobra.Command, args []string) {
			os.Remove(state.Path())
			fmt.Println("Cleared cluster state.")
		},
	})
	
	return cmd
}
