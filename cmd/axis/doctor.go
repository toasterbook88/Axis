package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/transport"
	"github.com/toasterbook88/axis/internal/ui"
)

var doctorConfigPath = config.DefaultConfigPath
var loadDoctorConfig = config.Load
var doctorCheckNodeSSH = func(ctx context.Context, node config.NodeConfig) error {
	spec := node.SSHDialSpec()
	sshExec := transport.NewSSHExecutorFromDial(spec.Host, spec.Port, spec.User, spec.DialTimeoutSec, spec.Fallbacks)
	defer sshExec.Close()
	return sshExec.Connect(ctx)
}

// doctorBackendStatus is the result of a local AI backend probe.
type doctorBackendStatus struct {
	Installed     bool
	Running       bool
	Port          int
	ResidentCount int
	Err           error
}

var doctorProbeLlamaServer = func(ctx context.Context) doctorBackendStatus {
	return runBackendProbeScript(ctx, facts.LlamaServerDiscoveryScript)
}

var doctorProbeMLX = func(ctx context.Context) doctorBackendStatus {
	return runBackendProbeScript(ctx, facts.MLXDiscoveryScript)
}

var doctorProbeOllama = func(ctx context.Context) doctorBackendStatus {
	return runBackendProbeScript(ctx, facts.OllamaDiscoveryScript)
}

// formatResidentModelCount returns a human-readable model count suffix.
func formatResidentModelCount(n int) string {
	switch n {
	case 0:
		return ", no models loaded"
	case 1:
		return ", 1 model loaded"
	default:
		return fmt.Sprintf(", %d models loaded", n)
	}
}

// runBackendProbeScript executes a discovery script and parses the minimal
// fields needed for the doctor health display. The full payload types live in
// internal/facts; this helper only extracts what doctor needs.
//
// script is always one of the exported package-level constants from
// internal/facts (LlamaServerDiscoveryScript, MLXDiscoveryScript).
// It is passed as a parameter rather than selected via an allowlist so that
// tests can inject a substitute without spawning a real shell.
func runBackendProbeScript(ctx context.Context, script string) doctorBackendStatus {
	out, err := exec.CommandContext(ctx, "bash", "-c", script).Output() //nolint:gosec // script is always a package-level constant
	if err != nil {
		// Surface stderr so the advisory message is actionable (e.g. "bash:
		// command not found") rather than an opaque "exit status 1".
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return doctorBackendStatus{Err: fmt.Errorf("%w: %s", err, exitErr.Stderr)}
		}
		return doctorBackendStatus{Err: err}
	}
	var payload struct {
		Installed      bool              `json:"installed"`
		Running        bool              `json:"running"`
		Port           int               `json:"port"`
		ResidentModels []json.RawMessage `json:"resident_models"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return doctorBackendStatus{Err: err}
	}
	return doctorBackendStatus{
		Installed:     payload.Installed,
		Running:       payload.Running,
		Port:          payload.Port,
		ResidentCount: len(payload.ResidentModels),
	}
}

func doctorCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate configuration, SSH connectivity, and daemon health",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd, strict)
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "treat daemon cache availability as a required check")
	return cmd
}

// doctorProbeRemoteShell measures remote login-shell cost and default shell.
// Returns a human message and whether it should be a warning.
// Overridable in tests to avoid real SSH.
var doctorProbeRemoteShell = doctorProbeRemoteShellImpl

func doctorProbeRemoteShellImpl(ctx context.Context, node config.NodeConfig) (string, bool) {
	spec := node.SSHDialSpec()
	exec := transport.NewSSHExecutorFromDial(spec.Host, spec.Port, spec.User, spec.DialTimeoutSec, spec.Fallbacks)
	defer exec.Close()
	if err := exec.Connect(ctx); err != nil {
		return "", false
	}
	start := time.Now()
	// Intentionally NOT bash-wrapped: measure real login-shell cost for bare true.
	out, err := exec.Run(ctx, "echo SHELL=$SHELL; true")
	elapsed := time.Since(start)
	if err != nil {
		return fmt.Sprintf("shell probe failed: %v", err), true
	}
	shell := strings.TrimSpace(out)
	warn := elapsed > 400*time.Millisecond
	msg := fmt.Sprintf("%s; bare session %.0fms", shell, elapsed.Seconds()*1000)
	if warn {
		msg += " (slow login shell — multi-probe collects may exceed short timeouts)"
	}
	return msg, warn
}

func runDoctor(cmd *cobra.Command, strict bool) error {
	out := cmd.OutOrStdout()
	var checks []DoctorCheck

	// 1. Config check
	cfgPath := doctorConfigPath()
	cfg, err := loadDoctorConfig(cfgPath)
	if err != nil {
		checks = append(checks, DoctorCheck{
			Name:    "Configuration File",
			Status:  "fail",
			Message: fmt.Sprintf("Failed to load: %v", err),
			Fix:     "cp nodes.example.yaml ~/.axis/nodes.yaml",
		})
	} else {
		checks = append(checks, DoctorCheck{
			Name:    "Configuration File",
			Status:  "pass",
			Message: fmt.Sprintf("Loaded %d node(s) from %s (membership %s)", len(cfg.Nodes), cfgPath, cfg.MembershipFingerprint()),
		})

		// 1.25 Seed hygiene: mDNS hostnames are fragile across OS resolvers.
		for _, n := range cfg.Nodes {
			for _, h := range n.DialHostnames() {
				if strings.HasSuffix(strings.ToLower(h), ".local") {
					checks = append(checks, DoctorCheck{
						Name:    fmt.Sprintf("Seed hostname: %s", n.Name),
						Status:  "warn",
						Message: fmt.Sprintf("%s uses mDNS (.local); prefer stable LAN/Tailscale IPs", h),
						Fix:     "set hostname to a fixed IP (LAN or Tailscale) in nodes.yaml",
					})
					break
				}
			}
		}

		// 1.5 MCP server config validation
		for name, mcpCfg := range cfg.MCPServers {
			switch strings.ToLower(mcpCfg.Transport) {
			case "stdio":
				if len(mcpCfg.Command) == 0 {
					checks = append(checks, DoctorCheck{
						Name:    fmt.Sprintf("MCP Server %q Config", name),
						Status:  "warn",
						Message: "missing command",
					})
				} else {
					cmdPath := mcpCfg.Command[0]
					if _, err := os.Stat(cmdPath); os.IsNotExist(err) {
						if _, lookErr := exec.LookPath(cmdPath); lookErr != nil {
							checks = append(checks, DoctorCheck{
								Name:    fmt.Sprintf("MCP Server %q Command", name),
								Status:  "warn",
								Message: fmt.Sprintf("command not found: %s", cmdPath),
							})
						} else {
							checks = append(checks, DoctorCheck{
								Name:    fmt.Sprintf("MCP Server %q Command", name),
								Status:  "pass",
								Message: fmt.Sprintf("%s (found in PATH)", cmdPath),
							})
						}
					} else {
						checks = append(checks, DoctorCheck{
							Name:    fmt.Sprintf("MCP Server %q Command", name),
							Status:  "pass",
							Message: cmdPath,
						})
					}
				}
			case "http":
				if mcpCfg.URL == "" {
					checks = append(checks, DoctorCheck{
						Name:    fmt.Sprintf("MCP Server %q Config", name),
						Status:  "warn",
						Message: "missing url",
					})
				} else {
					checks = append(checks, DoctorCheck{
						Name:    fmt.Sprintf("MCP Server %q URL", name),
						Status:  "pass",
						Message: mcpCfg.URL,
					})
				}
			default:
				checks = append(checks, DoctorCheck{
					Name:    fmt.Sprintf("MCP Server %q Transport", name),
					Status:  "warn",
					Message: fmt.Sprintf("unsupported transport: %s", mcpCfg.Transport),
				})
			}
		}

		// 2. SSH connectivity + lightweight mesh probes per node
		for _, n := range cfg.Nodes {
			spec := n.SSHDialSpec()
			host := spec.Host
			addr := net.JoinHostPort(host, fmt.Sprintf("%d", spec.Port))
			sshCtx, cancel := context.WithTimeout(cmd.Context(), time.Duration(spec.DialTimeoutSec)*time.Second)
			sshErr := doctorCheckNodeSSH(sshCtx, n)
			cancel()
			if sshErr != nil {
				checks = append(checks, DoctorCheck{
					Name:    fmt.Sprintf("SSH: %s", n.Name),
					Status:  "fail",
					Message: fmt.Sprintf("unreachable (%s): %v", addr, sshErr),
					Fix:     "verify pubkey auth, known_hosts, and dial address (LAN vs Tailscale)",
				})
				continue
			}
			checks = append(checks, DoctorCheck{
				Name:    fmt.Sprintf("SSH: %s", n.Name),
				Status:  "pass",
				Message: fmt.Sprintf("connected to %s (dial %ds / collect %ds)", addr, n.EffectiveDialTimeout(), n.EffectiveCollectTimeout()),
			})

			// Remote shell cost: slow login shells (fish+conda) break multi-probe collects.
			meshCtx, meshCancel := context.WithTimeout(cmd.Context(), time.Duration(n.EffectiveDialTimeout()+5)*time.Second)
			shellMsg, shellWarn := doctorProbeRemoteShell(meshCtx, n)
			meshCancel()
			if shellWarn {
				checks = append(checks, DoctorCheck{
					Name:    fmt.Sprintf("Remote shell: %s", n.Name),
					Status:  "warn",
					Message: shellMsg,
					Fix:     "AXIS forces bash for fact scripts; prefer one-shot collect (default) or raise collect_timeout_sec",
				})
			} else if shellMsg != "" {
				checks = append(checks, DoctorCheck{
					Name:    fmt.Sprintf("Remote shell: %s", n.Name),
					Status:  "pass",
					Message: shellMsg,
				})
			}
		}
	}

	// 3. Daemon health check
	daemonAddr := api.DefaultAddr()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	snap, _, daemonErr := fetchStatusSnapshot(ctx, daemonAddr)
	cancel()
	if daemonErr != nil || snap == nil {
		status := "warn"
		if strict {
			status = "fail"
		}
		checks = append(checks, DoctorCheck{
			Name:    "Daemon Cache",
			Status:  status,
			Message: fmt.Sprintf("Not reachable at %s", daemonAddr),
			Fix:     "start with: axis serve",
		})
	} else {
		if len(snap.Nodes) == 0 {
			checks = append(checks, DoctorCheck{
				Name:    "Daemon Cache",
				Status:  "warn",
				Message: "Reachable, 0 nodes cached",
				Fix:     "check ~/.axis/nodes.yaml and run axis status",
			})
		} else {
			checks = append(checks, DoctorCheck{
				Name:    "Daemon Cache",
				Status:  "pass",
				Message: fmt.Sprintf("Reachable, %d node(s) cached", len(snap.Nodes)),
			})
		}
	}

	// 4. Local AI backend health
	for _, b := range []struct {
		name  string
		probe func(context.Context) doctorBackendStatus
	}{
		{"ollama", doctorProbeOllama},
		{"llama-server", doctorProbeLlamaServer},
		{"mlx", doctorProbeMLX},
	} {
		bCtx, bCancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		s := b.probe(bCtx)
		bCancel()
		switch {
		case s.Err != nil:
			checks = append(checks, DoctorCheck{
				Name:    fmt.Sprintf("AI Backend: %s", b.name),
				Status:  "warn",
				Message: fmt.Sprintf("probe error: %v", s.Err),
			})
		case !s.Installed:
			checks = append(checks, DoctorCheck{
				Name:    fmt.Sprintf("AI Backend: %s", b.name),
				Status:  "pass",
				Message: "not installed",
			})
		case !s.Running:
			checks = append(checks, DoctorCheck{
				Name:    fmt.Sprintf("AI Backend: %s", b.name),
				Status:  "pass",
				Message: "installed, not running",
			})
		default:
			portStr := ""
			if s.Port > 0 {
				portStr = fmt.Sprintf(" on :%d", s.Port)
			}
			modelStr := formatResidentModelCount(s.ResidentCount)
			checks = append(checks, DoctorCheck{
				Name:    fmt.Sprintf("AI Backend: %s", b.name),
				Status:  "pass",
				Message: fmt.Sprintf("running%s%s", portStr, modelStr),
			})
		}
	}

	// Print visual report using RenderDoctorReport
	fmt.Fprint(out, RenderDoctorReport(checks))

	// Print binary path info
	self, _ := os.Executable()
	ui.DimColor.Fprintf(out, "  Binary Path: %s\n", self)
	ui.DimColor.Fprintf(out, "  Version:     %s\n\n", Version)

	// Active Remediation for missing config
	if err != nil && ui.StdinIsTerminal() && ui.StdoutIsTerminal() {
		fmt.Fprint(out, "No configuration found. Would you like to run the setup wizard (axis init) now? [y/N]: ")
		var answer string
		_, _ = fmt.Fscanln(cmd.InOrStdin(), &answer)
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer == "y" || answer == "yes" {
			// Trigger setup wizard (we will run the init command logic)
			fmt.Fprintln(out, "\nStarting setup wizard...")
			return runInitWizard(cmd)
		}
	}

	return nil
}
