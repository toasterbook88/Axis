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
		Run: func(cmd *cobra.Command, args []string) {
			const logo = `
    ___   _  __ _____ 
   /   | | |/ //_  _/ ____
  / /| | |   /  / /  / __/
 / ___ |/   | _/ /_ _\ \  
/_/  |_/_/|_|/____//___/  
`
			fmt.Print(logo)
			fmt.Printf("\nAXIS %s — cluster-aware AI execution substrate\n\n", Version)
			cmd.Usage()
		},
	}

	root.AddCommand(versionCmd())
	root.AddCommand(factsCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(taskCmd())
	root.AddCommand(chatCmd())

	if err := root.Execute(); err != nil {
		os.Exit(ExitErrGeneric)
	}
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
