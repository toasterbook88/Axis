package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/scripts"
	"github.com/toasterbook88/axis/internal/ui"
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
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, ui.Bold("AVAILABLE SCRIPTS"))

			var categories []string
			for cat := range scripts.Registry {
				categories = append(categories, cat)
			}
			sort.Strings(categories)

			for _, cat := range categories {
				fmt.Fprintf(out, "\n%s\n", ui.Cyan("["+cat+"]"))
				tbl := ui.NewTable("NAME", "DESCRIPTION")
				for _, script := range scripts.Registry[cat] {
					tbl.AddRow(script.Name, script.Description)
				}
				tbl.Render(out)
			}
			fmt.Fprintf(out, "\n%s\n", ui.Dim("Run a script with: axis task run --script \"<script-name-or-keywords>\""))
		},
	}
}
