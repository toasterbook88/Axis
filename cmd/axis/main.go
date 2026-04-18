// Package main is the CLI entry point for AXIS.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/ui"
	"gopkg.in/yaml.v3"
)

// Version is the CLI-visible AXIS version string.
const Version = buildinfo.Version

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(ExitCode(err))
	}
}

func newRootCmd() *cobra.Command {
	var noColor bool

	root := &cobra.Command{
		Use:          "axis",
		Short:        "AXIS — snapshot-first cluster facts and deterministic placement",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: "AXIS is a snapshot-first operator CLI for cluster fact collection, " +
			"deterministic placement, and explicit local control surfaces.\n\n" +
			"Chat helpers are experimental and must not be treated as authoritative " +
			"cluster truth unless backed by a live snapshot or probe.",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			ui.Init(noColor)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	root.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable colored output")

	root.AddCommand(updateCmd())
	root.AddCommand(versionCmd())
	root.AddCommand(factsCmd())
	root.AddCommand(statusCmd())
	root.AddCommand(taskCmd())
	root.AddCommand(placementCmd())
	root.AddCommand(mcpCmd())
	root.AddCommand(serveCmd())
	root.AddCommand(daemonCmd())
	root.AddCommand(llmCmd())
	root.AddCommand(cortexCmd())
	root.AddCommand(chatCmd())
	root.AddCommand(agentCmd())
	root.AddCommand(contextCmd())
	root.AddCommand(profileCmd())
	root.AddCommand(scriptsCmd())
	root.AddCommand(skillsCmd())
	root.AddCommand(completionCmd())
	root.AddCommand(doctorCmd())

	ui.ApplyHelpTemplate(root)

	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print AXIS version and build info",
		Run: func(cmd *cobra.Command, args []string) {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "axis %s\n", Version)
			if buildinfo.Commit != "" {
				fmt.Fprintf(out, "  commit:   %s\n", buildinfo.Commit)
			}
			if buildinfo.Date != "" {
				fmt.Fprintf(out, "  built:    %s\n", buildinfo.Date)
			}
			goVer := buildinfo.GoVersion
			if goVer == "" {
				goVer = runtime.Version()
			}
			fmt.Fprintf(out, "  go:       %s\n", goVer)
			fmt.Fprintf(out, "  platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
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
