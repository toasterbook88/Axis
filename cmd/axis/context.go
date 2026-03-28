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
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := state.Load()
			if err != nil {
				if st == nil {
					return err
				}
				printWarning(err)
			}
			out, _ := json.MarshalIndent(st, "", "  ")
			fmt.Println(string(out))
			return nil
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
