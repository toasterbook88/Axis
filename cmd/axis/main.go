// Package main is the CLI entry point for AXIS.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/netutil"
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
		Long: `Look first, then act.

  axis cluster status     every node (live; --cached to opt in)
  axis node facts         this machine
  axis agent              ask questions (advisory)
  axis model start        llama-server on a named node (--node --weights --port)
  axis daemon status      local cache

axis status, axis facts, axis summary, and axis doctor still work.
axis chat and axis llm were removed; use axis agent and axis ai route.`,
		Example: "  axis cluster status\n  axis node facts\n  axis agent\n  axis model start --node storage --weights /mnt/models/a.gguf --port 8081",

		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			ui.Init(noColor)
			// Initialize and register Cortex client globally (optional cluster integration)
			if client, err := buildCortexClient(5 * time.Second); err == nil {
				events.SetCortexClient(client)
			}
			// Register webhooks globally if configured. Webhooks are an optional
			// advisory surface; a misconfigured URL must not block core fact-plane
			// commands, so degrade gracefully with a warning instead of aborting.
			if cfg, err := config.Load(config.DefaultConfigPath()); err == nil && cfg != nil {
				// Hydrate the SSRF allowlist before validating webhook URLs so
				// operator-approved internal targets (LAN/MCP/Thunderbolt) pass.
				for _, host := range cfg.AllowedInternalHosts {
					netutil.AllowInternalHost(host)
				}
				if err := events.SetWebhooks(cfg.Webhooks); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
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
	cmdLlm.Hidden = true
	cmdAI := aiCmd()
	cmdAI.GroupID = "ai"
	cmdCortex := cortexCmd()
	cmdCortex.GroupID = "ai"
	cmdCortex.Hidden = true
	cmdChat := chatCmd()
	cmdChat.GroupID = "ai"
	cmdChat.Hidden = true
	cmdAgent := agentCmd()
	cmdAgent.GroupID = "ai"
	cmdTUI := tuiCmd()
	cmdTUI.GroupID = "setup"
	cmdContext := contextCmd()
	cmdContext.GroupID = "meta"
	cmdContext.Hidden = true
	cmdProfile := profileCmd()
	cmdProfile.GroupID = "task"
	cmdProfile.Hidden = true
	cmdScripts := scriptsCmd()
	cmdScripts.GroupID = "meta"
	cmdScripts.Hidden = true
	cmdSkills := skillsCmd()
	cmdSkills.GroupID = "meta"
	cmdSkills.Hidden = true
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
	cmdObservations.Hidden = true
	cmdModel := modelCmd()
	cmdModel.GroupID = "ai"
	cmdMcp.Hidden = true
	cmdCluster := clusterCmd()
	cmdCluster.GroupID = "cluster"
	cmdNode := nodeCmd()
	cmdNode.GroupID = "cluster"

	root.AddCommand(cmdUpdate)
	root.AddCommand(cmdCluster)
	root.AddCommand(cmdNode)

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
	root.AddCommand(cmdAI)
	root.AddCommand(cmdCortex)
	root.AddCommand(cmdChat)
	root.AddCommand(cmdAgent)
	root.AddCommand(cmdTUI)
	root.AddCommand(cmdContext)
	root.AddCommand(cmdProfile)
	root.AddCommand(cmdScripts)
	root.AddCommand(cmdSkills)
	root.AddCommand(cmdCompletion)
	root.AddCommand(cmdDoctor)
	root.AddCommand(cmdSummary)
	root.AddCommand(cmdReservations)
	root.AddCommand(cmdObservations)
	root.AddCommand(cmdModel)

	ui.ApplyHelpTemplate(root)

	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print AXIS version and build info",
		RunE: func(cmd *cobra.Command, args []string) error {
			var rendered strings.Builder
			out := &rendered
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
			_, err := fmt.Fprint(cmd.OutOrStdout(), rendered.String())
			return err
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
		_, err = fmt.Fprint(out, string(b))
		return err
	default:
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(b))
		return err
	}
}

func printWarning(out io.Writer, err error) error {
	if err == nil {
		return nil
	}
	_, writeErr := fmt.Fprintf(out, "warning: %v\n", err)
	return writeErr
}
