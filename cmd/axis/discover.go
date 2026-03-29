package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"gopkg.in/yaml.v3"
)

var discoverConfigPath = config.DefaultConfigPath
var loadDiscoverConfig = config.Load
var runClusterDiscovery = discovery.Discover

func discoverCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Experimental UDP discovery helper with manual-review output",
		Long: "Experimental UDP-assisted discovery helper.\n\n" +
			"The emitted config is a suggestion only. AXIS will include only observed " +
			"host facts and explicit placeholders; review every field before use.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "yaml" && format != "json" {
				return fmt.Errorf("unsupported format %q (use yaml or json)", format)
			}

			cfgPath := discoverConfigPath()
			cfg, err := loadDiscoverConfig(cfgPath)
			if err != nil {
				// If no config, create a blank one to allow pure discovery
				cfg = &config.Config{
					Discovery: &config.DiscoveryConfig{
						Enabled: true,
					},
				}
			}

			// Force discovery on
			if cfg.Discovery == nil {
				cfg.Discovery = &config.DiscoveryConfig{}
			}
			cfg.Discovery.Enabled = true

			fmt.Fprintln(cmd.ErrOrStderr(), "warning: axis discover is experimental; review every suggested field before use")
			fmt.Println("Listening for UDP beacons on port 42424 for 30 seconds...")

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// We bypass the full facts collection and just run the native UDP listener
			// to dump raw node configs based on beacons.
			// Wait, actually we just run standard discovery, which now includes UDP internally.
			// Actually, if we just call discovery.Discover with a 30s context, it will run SSH facts too.
			// Let's just run discovery.Discover directly. It currently waits 8s.
			// To make `discover` specifically wait 30s and only yield configs, we should export a
			// ListenUDP function in internal/discovery/udp.go.

			// For simplicity and to reuse exactly what we built: Let's discover the full cluster
			// because AXIS rule: "Always run SSH facts collection on discovered nodes — never trust UDP alone."

			nodes := runClusterDiscovery(ctx, cfg)

			suggestedConfig := suggestedDiscoverConfig(nodes)

			fmt.Println("\n---")
			if format == "yaml" {
				out, err := yaml.Marshal(suggestedConfig)
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
			return printOutput(suggestedConfig, "json")
		},
	}
	cmd.Flags().StringVar(&format, "format", "yaml", "Output format")
	return cmd
}

func suggestedDiscoverConfig(nodes []models.NodeFacts) config.Config {
	var suggested config.Config
	for _, nf := range nodes {
		suggested.Nodes = append(suggested.Nodes, config.NodeConfig{
			Name:     nf.Name,
			Hostname: suggestedDiscoverHostname(nf),
			SSHUser:  "",
			Role:     nf.Role,
		})
	}
	return suggested
}

func suggestedDiscoverHostname(nf models.NodeFacts) string {
	if host := strings.TrimSpace(nf.Hostname); host != "" {
		return host
	}
	for _, addr := range nf.Addresses {
		if candidate := strings.TrimSpace(addr.Address); candidate != "" {
			return candidate
		}
	}
	return ""
}
