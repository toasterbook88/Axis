// Package main is the CLI entry point for AXIS.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"gopkg.in/yaml.v3"
)

// Version is the CLI-visible AXIS version string.
const Version = buildinfo.Version

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(ExitErrGeneric)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "axis",
		Short: "AXIS — snapshot-first cluster facts and deterministic placement",
		Long: "AXIS is a snapshot-first operator CLI for cluster fact collection, " +
			"deterministic placement, and explicit local control surfaces.\n\n" +
			"Chat helpers are experimental and must not be treated as authoritative " +
			"cluster truth unless backed by a live snapshot or probe.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	root.AddCommand(versionCmd())
	root.AddCommand(factsCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(taskCmd())
	root.AddCommand(mcpCmd())
	root.AddCommand(serveCmd())
	root.AddCommand(daemonCmd())
	root.AddCommand(chatCmd())
	root.AddCommand(contextCmd())
	root.AddCommand(scriptsCmd())
	root.AddCommand(skillsCmd())
	return root
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

func printWarning(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: %v\n", err)
}
