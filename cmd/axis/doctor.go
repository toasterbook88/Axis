package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/ui"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Validate configuration, SSH connectivity, and daemon health",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd)
		},
	}
}

func runDoctor(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, ui.Bold("AXIS Doctor"))
	fmt.Fprintln(out)

	allOK := true

	// 1. Config check
	cfgPath := config.DefaultConfigPath()
	fmt.Fprintf(out, "%s Config: %s\n", ui.Cyan("→"), cfgPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		ui.FprintError(out, fmt.Sprintf("Config: %v", err), "cp nodes.example.yaml ~/.axis/nodes.yaml")
		allOK = false
	} else {
		fmt.Fprintf(out, "  %s Loaded %d node(s)\n", ui.StatusIcon(true), len(cfg.Nodes))

		// 2. SSH connectivity check per node
		fmt.Fprintln(out)
		fmt.Fprintf(out, "%s SSH connectivity\n", ui.Cyan("→"))
		for _, n := range cfg.Nodes {
			addr := fmt.Sprintf("%s:%d", n.Hostname, n.EffectiveSSHPort())
			conn, dialErr := net.DialTimeout("tcp", addr, 3*time.Second)
			if dialErr != nil {
				fmt.Fprintf(out, "  %s %s (%s): %v\n", ui.StatusIcon(false), n.Name, addr, dialErr)
				allOK = false
			} else {
				conn.Close()
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
	} else {
		fmt.Fprintf(out, "  %s Reachable, %d node(s) cached\n",
			ui.StatusIcon(true), len(snap.Nodes))
	}

	// 4. Binary info
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s Binary\n", ui.Cyan("→"))
	self, _ := os.Executable()
	fmt.Fprintf(out, "  %s %s\n", ui.Dim("path:"), self)
	fmt.Fprintf(out, "  %s %s\n", ui.Dim("version:"), Version)

	fmt.Fprintln(out)
	if allOK {
		ui.FprintSuccess(out, "All checks passed")
	} else {
		ui.FprintWarning(out, "Some checks failed (see above)")
	}
	return nil
}
