package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/ui"
	"gopkg.in/yaml.v3"
)

func initCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Run the interactive cluster configuration wizard",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInitWizard(cmd)
		},
	}
	return cmd
}

func runInitWizard(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	errW := cmd.ErrOrStderr()

	fmt.Fprintln(out, ui.Bold("AXIS Interactive Setup Wizard"))
	fmt.Fprintln(out, "This wizard will guide you to configure your local and remote AXIS nodes.")
	fmt.Fprintln(out)

	rl, err := readline.NewEx(&readline.Config{
		Prompt: ui.Cyan(">>> "),
	})
	if err != nil {
		return fmt.Errorf("failed to initialize interactive prompt: %w", err)
	}
	defer rl.Close()

	// 1. Get local node name
	defaultName, _ := os.Hostname()
	if defaultName == "" {
		defaultName = "localhost"
	}
	fmt.Fprintf(out, "Enter a name for the local node [default: %s]: ", defaultName)
	line, err := rl.Readline()
	if err != nil {
		return err
	}
	localName := strings.TrimSpace(line)
	if localName == "" {
		localName = defaultName
	}

	// 2. Get SSH User
	defaultUser := os.Getenv("USER")
	if defaultUser == "" {
		defaultUser = "root"
	}
	fmt.Fprintf(out, "Enter the SSH user name for remote node connections [default: %s]: ", defaultUser)
	line, err = rl.Readline()
	if err != nil {
		return err
	}
	sshUser := strings.TrimSpace(line)
	if sshUser == "" {
		sshUser = defaultUser
	}

	// Create nodes slice starting with local node
	nodes := []config.NodeConfig{
		{
			Name:       localName,
			Hostname:   "localhost",
			SSHUser:    sshUser,
			Role:       "primary",
			TimeoutSec: 10,
		},
	}

	// 3. Ask to scan for neighbor AXIS nodes
	fmt.Fprint(out, "Would you like to scan the network for active AXIS gossip nodes? [Y/n]: ")
	line, err = rl.Readline()
	if err == nil {
		ans := strings.ToLower(strings.TrimSpace(line))
		if ans == "" || ans == "y" || ans == "yes" {
			fmt.Fprintln(out, "Scanning for mesh beacons (3 seconds)...")
			tempCfg := &config.Config{
				Discovery: &config.DiscoveryConfig{
					Enabled: true,
					UDPPort: 42424,
				},
			}
			registry := discovery.NewBeaconRegistry()
			scanCtx, scanCancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			discovery.WatchBeaconChanges(scanCtx, tempCfg, registry, nil)
			<-scanCtx.Done()
			scanCancel()

			discovered := registry.Snapshot()
			if len(discovered) == 0 {
				fmt.Fprintln(out, "No active gossip nodes detected.")
			} else {
				fmt.Fprintf(out, "Found %d discovered node(s):\n", len(discovered))
				for _, n := range discovered {
					// Don't add local localhost again
					if n.Hostname == "127.0.0.1" || n.Hostname == "localhost" {
						continue
					}
					fmt.Fprintf(out, "  - Name: %s, IP: %s, Role: %s\n", n.Name, n.Hostname, n.Role)
					fmt.Fprintf(out, "    Add this node to configuration? [Y/n]: ")
					line, err = rl.Readline()
					if err != nil {
						break
					}
					choice := strings.ToLower(strings.TrimSpace(line))
					if choice == "" || choice == "y" || choice == "yes" {
						n.SSHUser = sshUser // enforce configured SSH user
						nodes = append(nodes, n)
						fmt.Fprintf(out, "Added node %s.\n", n.Name)
					}
				}
			}
		}
	}

	// 4. Manual entry fallback
	for {
		fmt.Fprint(out, "\nWould you like to manually add a remote worker node? [y/N]: ")
		line, err = rl.Readline()
		if err != nil {
			return err
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		if ans != "y" && ans != "yes" {
			break
		}

		fmt.Fprint(out, "Enter remote node name (e.g. node-b): ")
		line, err = rl.Readline()
		if err != nil {
			return err
		}
		remoteName := strings.TrimSpace(line)
		if remoteName == "" {
			fmt.Fprintln(errW, "Node name cannot be empty")
			continue
		}

		fmt.Fprintf(out, "Enter remote node hostname or IP (e.g. 192.168.1.50): ")
		line, err = rl.Readline()
		if err != nil {
			return err
		}
		remoteHost := strings.TrimSpace(line)
		if remoteHost == "" {
			fmt.Fprintln(errW, "Hostname cannot be empty")
			continue
		}

		nodes = append(nodes, config.NodeConfig{
			Name:       remoteName,
			Hostname:   remoteHost,
			SSHUser:    sshUser,
			Role:       "worker",
			TimeoutSec: 10,
		})
		fmt.Fprintf(out, "Added node %s (%s) to config.\n\n", remoteName, remoteHost)
	}

	// 5. Save configuration
	cfg := &config.Config{
		Nodes:     nodes,
		Discovery: &config.DiscoveryConfig{Enabled: len(nodes) > 1},
	}

	home, _ := os.UserHomeDir()
	cfgDir := filepath.Join(home, ".axis")
	cfgPath := filepath.Join(cfgDir, "nodes.yaml")

	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if _, err := os.Stat(cfgPath); err == nil {
		bakPath := cfgPath + ".bak"
		fmt.Fprintf(out, "Existing config found. Backing up to %s\n", bakPath)
		_ = os.Rename(cfgPath, bakPath)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	fmt.Fprintf(out, "\n%s Configuration written successfully to %s\n\n", ui.Green("✓"), cfgPath)
	return nil
}
