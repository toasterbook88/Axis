// Package main is the CLI entry point for AXIS.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/ui"
	"gopkg.in/yaml.v3"
)

// Version is the CLI-visible AXIS version string.
const Version = buildinfo.Version

func main() {
	root := newRootCmd()
	err := root.Execute()
	_ = events.FlushEvents(1 * time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(ExitCode(err))
	}
}

func newRootCmd() *cobra.Command {
	var noColor bool

	root := &cobra.Command{
		Use:           "axis",
		Short:         "AXIS — snapshot-first cluster facts and deterministic placement",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: "AXIS is a snapshot-first operator CLI for cluster fact collection, " +
			"deterministic placement, and explicit local control surfaces.\n\n" +
			"Chat helpers are experimental and must not be treated as authoritative " +
			"cluster truth unless backed by a live snapshot or probe.",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			ui.Init(noColor)
			// Initialize and register Cortex client globally (optional cluster integration)
			if client, err := buildCortexClient(5 * time.Second); err == nil {
				events.SetCortexClient(client)
			}
			// Register webhooks globally if configured
			if cfg, err := config.Load(config.DefaultConfigPath()); err == nil && cfg != nil {
				if err := events.SetWebhooks(cfg.Webhooks); err != nil {
					return err
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	root.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable colored output")

	root.AddGroup(&cobra.Group{
		ID:    "cluster",
		Title: "CLUSTER OPERATIONS",
	})
	root.AddGroup(&cobra.Group{
		ID:    "task",
		Title: "TASK MANAGEMENT",
	})
	root.AddGroup(&cobra.Group{
		ID:    "setup",
		Title: "SETUP & DAEMON",
	})
	root.AddGroup(&cobra.Group{
		ID:    "ai",
		Title: "AI ASSISTANCE & MCP",
	})
	root.AddGroup(&cobra.Group{
		ID:    "meta",
		Title: "METADATA & UTILITIES",
	})

	cmdUpdate := updateCmd()
	cmdUpdate.GroupID = "meta"
	cmdVersion := versionCmd()
	cmdVersion.GroupID = "meta"
	cmdInit := initCmd()
	cmdInit.GroupID = "setup"
	cmdMesh := meshCmd()
	cmdMesh.GroupID = "setup"
	cmdFacts := factsCmd()
	cmdFacts.GroupID = "cluster"
	cmdStatus := statusCmd()
	cmdStatus.GroupID = "cluster"
	cmdTask := taskCmd()
	cmdTask.GroupID = "task"
	cmdPlacement := placementCmd()
	cmdPlacement.GroupID = "task"
	cmdMcp := mcpCmd()
	cmdMcp.GroupID = "ai"
	cmdServe := serveCmd()
	cmdServe.GroupID = "setup"
	cmdDaemon := daemonCmd()
	cmdDaemon.GroupID = "setup"
	cmdLlm := llmCmd()
	cmdLlm.GroupID = "ai"
	cmdCortex := cortexCmd()
	cmdCortex.GroupID = "ai"
	cmdChat := chatCmd()
	cmdChat.GroupID = "ai"
	cmdAgent := agentCmd()
	cmdAgent.GroupID = "ai"
	cmdContext := contextCmd()
	cmdContext.GroupID = "meta"
	cmdProfile := profileCmd()
	cmdProfile.GroupID = "task"
	cmdScripts := scriptsCmd()
	cmdScripts.GroupID = "meta"
	cmdSkills := skillsCmd()
	cmdSkills.GroupID = "meta"
	cmdCompletion := completionCmd()
	cmdCompletion.GroupID = "meta"
	cmdDoctor := doctorCmd()
	cmdDoctor.GroupID = "cluster"
	cmdSummary := summaryCmd()
	cmdSummary.GroupID = "cluster"
	cmdReservations := reservationsCmd()
	cmdReservations.GroupID = "task"
	cmdObservations := observationsCmd()
	cmdObservations.GroupID = "task"

	root.AddCommand(cmdUpdate)
	root.AddCommand(cmdVersion)
	root.AddCommand(cmdInit)
	root.AddCommand(cmdMesh)
	root.AddCommand(cmdFacts)
	root.AddCommand(cmdStatus)
	root.AddCommand(cmdTask)
	root.AddCommand(cmdPlacement)
	root.AddCommand(cmdMcp)
	root.AddCommand(cmdServe)
	root.AddCommand(cmdDaemon)
	root.AddCommand(cmdLlm)
	root.AddCommand(cmdCortex)
	root.AddCommand(cmdChat)
	root.AddCommand(cmdAgent)
	root.AddCommand(cmdContext)
	root.AddCommand(cmdProfile)
	root.AddCommand(cmdScripts)
	root.AddCommand(cmdSkills)
	root.AddCommand(cmdCompletion)
	root.AddCommand(cmdDoctor)
	root.AddCommand(cmdSummary)
	root.AddCommand(cmdReservations)
	root.AddCommand(cmdObservations)

	ui.ApplyHelpTemplate(root)

	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print AXIS version and build info",
		Run: func(cmd *cobra.Command, args []string) {
			out := cmd.OutOrStdout()
			ui.PrintLogo(out, Version)
			fmt.Fprintln(out)
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

// printOutput marshals data to JSON or YAML and writes to out.
func printOutput(out io.Writer, data interface{}, format string) error {
	switch format {
	case "yaml":
		b, err := yaml.Marshal(data)
		if err != nil {
			return err
		}
		fmt.Fprint(out, string(b))
	default:
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(b))
	}
	return nil
}

func printWarning(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: %v\n", err)
}
