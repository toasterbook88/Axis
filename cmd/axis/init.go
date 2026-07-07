package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/transport"
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

	ui.PrintLogo(out, Version)
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Bold("AXIS Interactive Setup Wizard"))
	fmt.Fprintln(out, "This wizard will guide you to configure your local and remote AXIS nodes.")
	fmt.Fprintln(out)

	rl, err := readline.NewEx(&readline.Config{
		Prompt: ui.Cyan(">>> "),
		Stdin:  io.NopCloser(cmd.InOrStdin()),
		Stdout: cmd.OutOrStdout(),
	})
	if err != nil {
		return fmt.Errorf("failed to initialize interactive prompt: %w", err)
	}
	defer rl.Close()

	// 1. Get local node name
	fmt.Fprintln(out, ui.Cyan("⬢ Local Configuration"))
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

	// 3. Tailscale Auto-Discovery
	fmt.Fprintln(out, ui.Cyan("\n⬢ Tailscale Auto-Discovery"))
	fmt.Fprint(out, "Would you like to scan for Tailscale peers? [Y/n]: ")
	line, err = rl.Readline()
	if err == nil {
		ans := strings.ToLower(strings.TrimSpace(line))
		if ans == "" || ans == "y" || ans == "yes" {
			spin := ui.NewSpinner()
			spin.Start("Scanning Tailscale network...")

			tsOut, err := exec.CommandContext(cmd.Context(), "tailscale", "status", "--json").Output()
			if err != nil {
				spin.Stop(fmt.Sprintf("%s Tailscale not found or not running.", ui.Red("✗")))
			} else {
				spin.Stop(fmt.Sprintf("%s Tailscale scan complete.", ui.Green("✓")))
				var tsStatus struct {
					Peer map[string]struct {
						HostName     string   `json:"HostName"`
						TailscaleIPs []string `json:"TailscaleIPs"`
						Online       bool     `json:"Online"`
						OS           string   `json:"OS"`
					} `json:"Peer"`
				}
				if err := json.Unmarshal(tsOut, &tsStatus); err == nil {
					found := 0
					for _, peer := range tsStatus.Peer {
						if len(peer.TailscaleIPs) == 0 {
							continue
						}
						found++
						fmt.Fprintf(out, "  - Found: %s (%s) [Online: %v, OS: %s]\n", peer.HostName, peer.TailscaleIPs[0], peer.Online, peer.OS)
						fmt.Fprintf(out, "    Add this node? [Y/n]: ")
						line, err = rl.Readline()
						if err != nil {
							break
						}
						choice := strings.ToLower(strings.TrimSpace(line))
						if choice == "" || choice == "y" || choice == "yes" {
							n := config.NodeConfig{
								Name:       peer.HostName,
								Hostname:   peer.TailscaleIPs[0],
								SSHUser:    sshUser,
								Role:       "worker",
								SSHPort:    22,
								TimeoutSec: 10,
							}
							if verifySSHConnectionFn(cmd.Context(), n.Hostname, n.EffectiveSSHPort(), n.SSHUser, n.EffectiveTimeout(), out) {
								nodes = append(nodes, n)
								fmt.Fprintf(out, "Added node %s.\n", n.Name)
							} else {
								fmt.Fprintf(out, "Add node %s anyway? [y/N]: ", n.Name)
								line, err = rl.Readline()
								if err == nil && (strings.ToLower(strings.TrimSpace(line)) == "y" || strings.ToLower(strings.TrimSpace(line)) == "yes") {
									nodes = append(nodes, n)
									fmt.Fprintf(out, "Added node %s.\n", n.Name)
								} else {
									fmt.Fprintf(out, "Skipped node %s.\n", n.Name)
								}
							}
						}
					}
					if found == 0 {
						fmt.Fprintln(out, "No active Tailscale peers found.")
					}
				}
			}
		}
	}

	// 4. Mesh Gossip Scan
	fmt.Fprintln(out, ui.Cyan("\n⬢ Mesh Gossip Scan"))
	fmt.Fprint(out, "Would you like to scan the network for active AXIS gossip nodes? [Y/n]: ")
	line, err = rl.Readline()
	if err == nil {
		ans := strings.ToLower(strings.TrimSpace(line))
		if ans == "" || ans == "y" || ans == "yes" {
			spin := ui.NewSpinner()
			spin.Start("Scanning for active AXIS gossip nodes (3 seconds)...")
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
			spin.Stop(fmt.Sprintf("%s Scan complete.", ui.Green("✓")))

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
						if verifySSHConnectionFn(cmd.Context(), n.Hostname, n.EffectiveSSHPort(), n.SSHUser, n.EffectiveTimeout(), out) {
							nodes = append(nodes, n)
							fmt.Fprintf(out, "Added node %s.\n", n.Name)
						} else {
							fmt.Fprintf(out, "Add node %s anyway? [y/N]: ", n.Name)
							line, err = rl.Readline()
							if err == nil {
								ans := strings.ToLower(strings.TrimSpace(line))
								if ans == "y" || ans == "yes" {
									nodes = append(nodes, n)
									fmt.Fprintf(out, "Added node %s.\n", n.Name)
								} else {
									fmt.Fprintf(out, "Skipped node %s.\n", n.Name)
								}
							}
						}
					}
				}
			}
		}
	}

	// 4. Manual entry fallback
	fmt.Fprintln(out, ui.Cyan("\n⬢ Remote Node Configuration"))
	for {
		fmt.Fprint(out, "Would you like to manually add a remote worker node? [y/N]: ")
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

		fmt.Fprintf(out, "Enter SSH port for remote node [default: 22]: ")
		line, err = rl.Readline()
		if err != nil {
			return err
		}
		portStr := strings.TrimSpace(line)
		sshPort := 22
		if portStr != "" {
			if p, pErr := strconv.Atoi(portStr); pErr == nil && p > 0 {
				sshPort = p
			} else {
				fmt.Fprintln(errW, "Invalid port, using default (22)")
			}
		}

		fmt.Fprintf(out, "Enter timeout in seconds for SSH operations [default: 10]: ")
		line, err = rl.Readline()
		if err != nil {
			return err
		}
		timeoutStr := strings.TrimSpace(line)
		timeoutSec := 10
		if timeoutStr != "" {
			if t, tErr := strconv.Atoi(timeoutStr); tErr == nil && t > 0 {
				timeoutSec = t
			} else {
				fmt.Fprintln(errW, "Invalid timeout, using default (10)")
			}
		}

		n := config.NodeConfig{
			Name:       remoteName,
			Hostname:   remoteHost,
			SSHUser:    sshUser,
			Role:       "worker",
			SSHPort:    sshPort,
			TimeoutSec: timeoutSec,
		}

		if verifySSHConnectionFn(cmd.Context(), n.Hostname, n.EffectiveSSHPort(), n.SSHUser, n.EffectiveTimeout(), out) {
			nodes = append(nodes, n)
			fmt.Fprintf(out, "Added node %s (%s) to config.\n\n", remoteName, remoteHost)
		} else {
			fmt.Fprintf(out, "Add node %s anyway? [y/N]: ", remoteName)
			line, err = rl.Readline()
			if err != nil {
				return err
			}
			ans := strings.ToLower(strings.TrimSpace(line))
			if ans == "y" || ans == "yes" {
				nodes = append(nodes, n)
				fmt.Fprintf(out, "Added node %s (%s) to config.\n\n", remoteName, remoteHost)
			} else {
				fmt.Fprintf(out, "Skipped node %s.\n\n", remoteName)
			}
		}
	}

	// 5. Save configuration
	var discoveryCfg *config.DiscoveryConfig
	if len(nodes) > 1 {
		discoveryCfg = &config.DiscoveryConfig{
			Enabled:        true,
			UDPPort:        42424,
			BeaconInterval: 3,
			Secret:         generateRandomSecret(),
		}
	}
	cfg := &config.Config{
		Nodes:     nodes,
		Discovery: discoveryCfg,
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

	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	fmt.Fprintf(out, "\n%s Configuration written successfully to %s\n", ui.Green("✓"), cfgPath)
	fmt.Fprintf(out, "  %s Run '%s' to view your cluster dashboard.\n\n", ui.Cyan("→"), ui.Bold("axis summary"))
	return nil
}

var verifySSHConnectionFn = verifySSHConnection

func verifySSHConnection(ctx context.Context, host string, port int, user string, timeoutSec int, out io.Writer) bool {
	spin := ui.NewSpinner()
	spin.Start(fmt.Sprintf("Verifying SSH connection to [%s]:%d as %s...", host, port, user))
	exec := transport.NewSSHExecutor(host, port, user, timeoutSec)
	defer exec.Close()

	dialCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	if err := exec.Connect(dialCtx); err != nil {
		spin.Stop(fmt.Sprintf("%s Connection failed: %v", ui.Red("✗"), err))
		return false
	}
	spin.Stop(fmt.Sprintf("%s Connection verified successfully!", ui.Green("✓")))
	return true
}

func generateRandomSecret() string {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "axis-default-gossip-secret"
	}
	return hex.EncodeToString(bytes)
}
