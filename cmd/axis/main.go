// Package main is the CLI entry point for AXIS.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Version is the hardcoded Phase 1 version string.
const Version = "0.1.0"

func main() {
	root := &cobra.Command{
		Use:   "axis",
		Short: "AXIS — cluster-aware AI execution substrate",
		RunE:  runRoot,
	}

	root.AddCommand(versionCmd())
	root.AddCommand(factsCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(taskCmd())
	root.AddCommand(mcpCmd())
	root.AddCommand(serveCmd())
	root.AddCommand(chatCmd())
	root.AddCommand(discoverCmd())
	root.AddCommand(contextCmd())
	root.AddCommand(scriptsCmd())
	root.AddCommand(skillsCmd())

	if err := root.Execute(); err != nil {
		os.Exit(ExitErrGeneric)
	}
}

func runRoot(cmd *cobra.Command, args []string) error {
	const logo = `
 █████╗ ██╗  ██╗██╗███████╗
██╔══██╗╚██╗██╔╝██║██╔════╝
███████║ ╚███╔╝ ██║███████╗
██╔══██║ ██╔██╗ ██║╚════██║
██║  ██║██╔╝ ██╗██║███████║
╚═╝  ╚═╝╚═╝  ╚═╝╚═╝╚══════╝
`
	out := cmd.OutOrStdout()
	cmd.SetOut(out)
	cmd.SetErr(out)

	fmt.Fprint(out, logo)
	fmt.Fprintf(out, "\nAXIS %s — cluster-aware AI execution substrate\n\n", Version)

	chat := chatCmd()
	return chat.RunE(chat, args)
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print AXIS version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("axis " + Version)
		},
	}
}

// printOutput marshals data to JSON or YAML and writes to stdout.
func printOutput(data interface{}, format string) error {
	switch format {
	case "yaml":
		out, err := yaml.Marshal(data)
		if err != nil {
			return err
		}
		fmt.Print(string(out))
	default:
		out, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	}
	return nil
}
