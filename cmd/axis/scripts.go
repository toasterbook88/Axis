package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/scripts"
)

func scriptsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scripts",
		Short: "Manage and list battle-tested node scripts (Mole alternative)",
	}
	cmd.AddCommand(scriptsListCmd())
	return cmd
}

func scriptsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all available fallback scripts",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("AVAILABLE MOLE-STYLE SCRIPTS:")
			
			var categories []string
			for cat := range scripts.Registry {
				categories = append(categories, cat)
			}
			sort.Strings(categories)

			for _, cat := range categories {
				fmt.Printf("\n[%s]\n", cat)
				for _, script := range scripts.Registry[cat] {
					fmt.Printf("  %-18s %s\n", script.Name, script.Description)
				}
			}
			fmt.Println("\nRun a script with: axis task run \"<script-name-or-keywords>\"")
		},
	}
}
