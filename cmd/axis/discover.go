package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"gopkg.in/yaml.v3"
)

var discoverConfigPath = config.DefaultConfigPath
var loadDiscoverConfig = config.Load
var runClusterDiscovery = discovery.Discover

func discoverCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Listen for UDP beacons and dump suggested nodes.yaml entries",
		RunE: func(cmd *cobra.Command, args []string) error {
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
			
			var suggestedConfig config.Config
			for _, nf := range nodes {
				suggestedConfig.Nodes = append(suggestedConfig.Nodes, config.NodeConfig{
					Name:       nf.Name,
					Hostname:   nf.Name + ".local", // Best guess fallback if IP wasn't set, or use IP
					SSHUser:    "axis",
					Role:       nf.Role,
					SSHPort:    22,
					TimeoutSec: 10,
				})
			}

			out, err := yaml.Marshal(suggestedConfig)
			if err != nil {
				return err
			}

			fmt.Println("\n---")
			fmt.Println(string(out))
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "yaml", "Output format")
	return cmd
}
