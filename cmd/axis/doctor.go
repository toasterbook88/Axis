package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
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
	sshExec := transport.NewSSHExecutor(
		node.Hostname,
		node.EffectiveSSHPort(),
		node.SSHUser,
		node.EffectiveTimeout(),
	)
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
func runBackendProbeScript(ctx context.Context, script string) doctorBackendStatus {
	out, err := exec.CommandContext(ctx, "bash", "-c", script).Output()
	if err != nil {
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

func runDoctor(cmd *cobra.Command, strict bool) error {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, ui.Bold("AXIS Doctor"))
	fmt.Fprintln(out)

	coreFailures := 0
	advisoryWarnings := 0

	// 1. Config check
	cfgPath := doctorConfigPath()
	fmt.Fprintf(out, "%s Config: %s\n", ui.Cyan("→"), cfgPath)
	cfg, err := loadDoctorConfig(cfgPath)
	if err != nil {
		ui.FprintError(out, fmt.Sprintf("Config: %v", err), "cp nodes.example.yaml ~/.axis/nodes.yaml")
		coreFailures++
	} else {
		fmt.Fprintf(out, "  %s Loaded %d node(s)\n", ui.StatusIcon(true), len(cfg.Nodes))

		// 2. SSH connectivity check per node
		fmt.Fprintln(out)
		fmt.Fprintf(out, "%s SSH connectivity\n", ui.Cyan("→"))
		for _, n := range cfg.Nodes {
			addr := net.JoinHostPort(n.Hostname, fmt.Sprintf("%d", n.EffectiveSSHPort()))
			sshCtx, cancel := context.WithTimeout(cmd.Context(), time.Duration(n.EffectiveTimeout())*time.Second)
			sshErr := doctorCheckNodeSSH(sshCtx, n)
			cancel()
			if sshErr != nil {
				fmt.Fprintf(out, "  %s %s (%s): %v\n", ui.StatusIcon(false), n.Name, addr, sshErr)
				coreFailures++
			} else {
				fmt.Fprintf(out, "  %s %s (%s)\n", ui.StatusIcon(true), n.Name, addr)
			}
		}
	}

	// 3. Daemon health check
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s Daemon cache\n", ui.Cyan("→"))
	daemonAddr := api.DefaultAddr()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	snap, _, daemonErr := fetchStatusSnapshot(ctx, daemonAddr)
	if daemonErr != nil || snap == nil {
		fmt.Fprintf(out, "  %s Not reachable at %s\n", ui.StatusIcon(false), daemonAddr)
		fmt.Fprintf(out, "    %s\n", ui.Dim("hint: start with: axis serve"))
		if strict {
			coreFailures++
		} else {
			advisoryWarnings++
		}
	} else {
		fmt.Fprintf(out, "  %s Reachable, %d node(s) cached\n",
			ui.StatusIcon(true), len(snap.Nodes))
	}

	// 4. Local AI backend health (advisory — not counted as core failures)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s AI backends (local)\n", ui.Cyan("→"))
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer probeCancel()
	for _, b := range []struct {
		name  string
		probe func(context.Context) doctorBackendStatus
	}{
		{"llama-server", doctorProbeLlamaServer},
		{"mlx", doctorProbeMLX},
	} {
		s := b.probe(probeCtx)
		switch {
		case s.Err != nil:
			fmt.Fprintf(out, "  %s %s: probe error: %v\n", ui.StatusIcon(false), b.name, s.Err)
			advisoryWarnings++
		case !s.Installed:
			fmt.Fprintf(out, "  %s %s: not installed\n", ui.Dim("–"), b.name)
		case !s.Running:
			fmt.Fprintf(out, "  %s %s: installed, not running\n", ui.StatusIcon(true), b.name)
		default:
			portStr := ""
			if s.Port > 0 {
				portStr = fmt.Sprintf(" on :%d", s.Port)
			}
			modelStr := formatResidentModelCount(s.ResidentCount)
			fmt.Fprintf(out, "  %s %s: running%s%s\n", ui.StatusIcon(true), b.name, portStr, modelStr)
		}
	}

	// 5. Binary info
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s Binary\n", ui.Cyan("→"))
	self, _ := os.Executable()
	fmt.Fprintf(out, "  %s %s\n", ui.Dim("path:"), self)
	fmt.Fprintf(out, "  %s %s\n", ui.Dim("version:"), Version)

	fmt.Fprintln(out)
	switch {
	case coreFailures > 0:
		ui.FprintWarning(out, "Some checks failed (see above)")
	case advisoryWarnings > 0:
		ui.FprintWarning(out, "Core checks passed with advisory warnings")
	default:
		ui.FprintSuccess(out, "All checks passed")
	}
	return nil
}
