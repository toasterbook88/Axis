package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/ui"
)

func meshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mesh",
		Short: "Gossip mesh peer diagnostics",
	}
	cmd.AddCommand(meshStatusCmd())
	cmd.AddCommand(meshPeersCmd())
	return cmd
}

func meshStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local Gossip mesh network properties",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			var rendered strings.Builder
			out := &rendered
			flush := func() error {
				_, err := fmt.Fprint(cmd.OutOrStdout(), rendered.String())
				return err
			}
			fmt.Fprintln(out, ui.Bold("AXIS Gossip Mesh Status"))
			fmt.Fprintln(out, strings.Repeat("─", 40))

			if cfg.Discovery == nil || !cfg.Discovery.Enabled {
				fmt.Fprintln(out, "Gossip Mesh Discovery: "+ui.Red("DISABLED"))
				return flush()
			}

			fmt.Fprintln(out, "Gossip Mesh Discovery: "+ui.Green("ENABLED"))
			port := 42424
			if cfg.Discovery.UDPPort > 0 {
				port = cfg.Discovery.UDPPort
			}
			interval := 3
			if cfg.Discovery.BeaconInterval > 0 {
				interval = cfg.Discovery.BeaconInterval
			}

			fmt.Fprintf(out, "UDP Beacon Port:       %d\n", port)
			fmt.Fprintf(out, "Beacon Interval:       %d seconds\n", interval)

			secretStatus := ui.Yellow("disabled")
			if cfg.Discovery.Secret != "" {
				secretStatus = ui.Green("enabled (HMAC-SHA256)")
			}
			fmt.Fprintf(out, "Beacon Signatures:     %s\n", secretStatus)
			fmt.Fprintln(out)

			return flush()
		},
	}
}

func meshPeersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "peers",
		Short: "List discovered Gossip mesh neighbors",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			out := cmd.OutOrStdout()
			if cfg.Discovery == nil || !cfg.Discovery.Enabled {
				_, err := fmt.Fprintln(out, "Gossip Mesh Discovery is disabled.")
				return err
			}

			if _, err := fmt.Fprintln(out, "Listening for Gossip mesh beacons (5 seconds)..."); err != nil {
				return err
			}

			registry := discovery.NewBeaconRegistry()
			scanCtx, scanCancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			discovery.WatchBeaconChanges(scanCtx, cfg, registry, nil)
			<-scanCtx.Done()
			scanCancel()

			peers := registry.Snapshot()
			if len(peers) == 0 {
				_, err := fmt.Fprintln(out, "No active Gossip neighbors discovered.")
				return err
			}

			var rendered strings.Builder
			fmt.Fprintln(&rendered, "\nDiscovered Gossip Neighbors:")
			tbl := ui.NewTable("NAME", "HOSTNAME/IP", "ROLE", "PORT", "STABLE ID")
			for _, p := range peers {
				tbl.AddRow(
					ui.Cyan(p.Name),
					p.Hostname,
					p.Role,
					fmt.Sprintf("%d", p.SSHPort),
					p.StableID,
				)
			}
			tbl.Render(&rendered)
			fmt.Fprintln(&rendered)
			_, err = fmt.Fprint(out, rendered.String())
			return err
		},
	}
}
