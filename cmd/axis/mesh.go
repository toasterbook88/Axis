package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/mesh"
	"github.com/toasterbook88/axis/internal/ui"
)

const (
	defaultBeaconPort = 42424
	defaultGossipPort = 42426
	meshLiveScanWait  = 5 * time.Second
)

func meshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mesh",
		Short: "Diagnose daemon discovery beacons and gossip mesh",
		Long: `Operator diagnostics over AXIS's two UDP planes.

Discovery beacons (default UDP 42424) feed snapshot assembly.
Gossip mesh (default UDP 42426) is an advisory daemon lifecycle.

axis mesh does not start a second long-lived listener. status and peers
read the running daemon. --live is the only path that opens a temporary
beacon scanner, and a bind failure is reported instead of "no peers".`,
	}
	cmd.PersistentFlags().String("cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon (Unix socket or TCP host:port)")
	cmd.PersistentFlags().String("format", "text", "Output format: text, json, or yaml")
	cmd.AddCommand(meshStatusCmd())
	cmd.AddCommand(meshPeersCmd())
	return cmd
}

func meshCacheAddr(cmd *cobra.Command) string {
	addr, _ := cmd.Flags().GetString("cache-addr")
	if addr == "" {
		addr, _ = cmd.InheritedFlags().GetString("cache-addr")
	}
	if addr == "" {
		return api.DefaultAddr()
	}
	return addr
}

func meshFormat(cmd *cobra.Command) string {
	format, _ := cmd.Flags().GetString("format")
	if format == "" {
		format, _ = cmd.InheritedFlags().GetString("format")
	}
	if format == "" {
		return "text"
	}
	return format
}

func meshStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show discovery and gossip-mesh health",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			report := buildMeshStatus(cmd.Context(), cfg, meshCacheAddr(cmd))
			format := meshFormat(cmd)
			if format == "json" || format == "yaml" {
				return printOutput(cmd.OutOrStdout(), report, format)
			}
			return writeMeshStatusText(cmd, report)
		},
	}
}

func meshPeersCmd() *cobra.Command {
	var live bool
	cmd := &cobra.Command{
		Use:   "peers",
		Short: "List daemon gossip peers, or scan beacons with --live",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if live {
				return runLiveBeaconScan(cmd, cfg)
			}
			return runDaemonMeshPeers(cmd)
		},
	}
	cmd.Flags().BoolVar(&live, "live", false, "Open a temporary UDP beacon scan instead of reading the daemon")
	return cmd
}

type meshStatusReport struct {
	Source            string         `json:"source"`
	DiscoveryEnabled  bool           `json:"discovery_enabled"`
	MeshEnabled       bool           `json:"mesh_enabled"`
	BeaconPort        int            `json:"beacon_port"`
	GossipPort        int            `json:"gossip_port"`
	BeaconIntervalSec int            `json:"beacon_interval_sec"`
	BeaconSignatures  string         `json:"beacon_signatures"`
	Daemon            string         `json:"daemon"`
	DiscoveryListener string         `json:"discovery_listener"`
	GossipListener    string         `json:"gossip_listener"`
	SharedUDPPort     bool           `json:"shared_udp_port,omitempty"`
	MeshPeerCount     int            `json:"mesh_peer_count"`
	MeshPeerCounts    map[string]int `json:"mesh_peer_counts,omitempty"`
	DaemonError       string         `json:"daemon_error,omitempty"`
	Warnings          []string       `json:"warnings,omitempty"`
	Peers             []mesh.Peer    `json:"peers,omitempty"`
}

func buildMeshStatus(ctx context.Context, cfg *config.Config, cacheAddr string) meshStatusReport {
	report := meshStatusReport{
		Source:            "config",
		BeaconPort:        discoveryBeaconPort(cfg),
		GossipPort:        meshGossipPort(cfg),
		BeaconIntervalSec: discoveryBeaconInterval(cfg),
		BeaconSignatures:  "disabled",
		Daemon:            "unavailable",
		DiscoveryListener: "unknown",
		GossipListener:    "unknown",
		MeshPeerCounts:    map[string]int{},
	}
	if cfg != nil && cfg.Discovery != nil && cfg.Discovery.Enabled {
		report.DiscoveryEnabled = true
	}
	report.MeshEnabled = cfg == nil || cfg.IsMeshEnabled()
	if cfg != nil && cfg.Discovery != nil && cfg.Discovery.Secret != "" {
		report.BeaconSignatures = "enabled (HMAC-SHA256)"
	}
	if report.BeaconPort == report.GossipPort {
		report.SharedUDPPort = true
		report.Warnings = append(report.Warnings, fmt.Sprintf("discovery and gossip share UDP %d; a bind failure on that port affects both planes", report.BeaconPort))
	}

	if bound, err := udpPortHeld(report.BeaconPort); err != nil {
		report.DiscoveryListener = "probe failed"
		report.Warnings = append(report.Warnings, fmt.Sprintf("discovery port :%d probe failed: %v", report.BeaconPort, err))
	} else if bound {
		report.DiscoveryListener = "bound"
	} else if report.DiscoveryEnabled {
		report.DiscoveryListener = "not listening"
		report.Warnings = append(report.Warnings, fmt.Sprintf("discovery enabled but UDP :%d is free", report.BeaconPort))
	} else {
		report.DiscoveryListener = "disabled"
	}

	if bound, err := udpPortHeld(report.GossipPort); err != nil {
		report.GossipListener = "probe failed"
		report.Warnings = append(report.Warnings, fmt.Sprintf("gossip port :%d probe failed: %v", report.GossipPort, err))
	} else if bound {
		report.GossipListener = "bound"
	} else if report.MeshEnabled {
		report.GossipListener = "not listening"
		report.Warnings = append(report.Warnings, fmt.Sprintf("mesh enabled at daemon start but UDP :%d is free; changing discovery.enabled does not start gossip until restart", report.GossipPort))
	} else {
		report.GossipListener = "disabled"
	}

	queryCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	peers, err := fetchDaemonMesh(queryCtx, cacheAddr)
	if err != nil {
		report.Daemon = "unavailable"
		report.DaemonError = err.Error()
		report.Warnings = append(report.Warnings, "daemon unavailable; status is local config plus UDP bind probes")
		return report
	}
	report.Daemon = "reachable"
	report.Source = "daemon"
	report.Peers = peers
	report.MeshPeerCount = len(peers)
	for _, p := range peers {
		report.MeshPeerCounts[p.State.String()]++
	}
	return report
}

func writeMeshStatusText(cmd *cobra.Command, report meshStatusReport) error {
	var rendered strings.Builder
	out := &rendered
	fmt.Fprintln(out, ui.Bold("AXIS Mesh Status"))
	fmt.Fprintln(out, strings.Repeat("─", 40))

	discLabel := ui.Red("DISABLED")
	if report.DiscoveryEnabled {
		discLabel = ui.Green("ENABLED")
	}
	meshLabel := ui.Red("DISABLED")
	if report.MeshEnabled {
		meshLabel = ui.Green("ENABLED")
	}
	fmt.Fprintln(out, "Discovery Beacons:     "+discLabel)
	fmt.Fprintln(out, "Gossip Mesh:           "+meshLabel)
	fmt.Fprintf(out, "UDP Beacon Port:       %d\n", report.BeaconPort)
	fmt.Fprintf(out, "UDP Gossip Port:       %d\n", report.GossipPort)
	fmt.Fprintf(out, "Beacon Interval:       %d seconds\n", report.BeaconIntervalSec)
	sig := ui.Yellow(report.BeaconSignatures)
	if strings.HasPrefix(report.BeaconSignatures, "enabled") {
		sig = ui.Green(report.BeaconSignatures)
	}
	fmt.Fprintf(out, "Beacon Signatures:     %s\n", sig)
	fmt.Fprintf(out, "Daemon:                %s\n", report.Daemon)
	fmt.Fprintf(out, "Discovery Listener:    %s\n", report.DiscoveryListener)
	fmt.Fprintf(out, "Gossip Listener:       %s\n", report.GossipListener)
	if report.MeshPeerCount > 0 {
		fmt.Fprintf(out, "Mesh Peers:            %d", report.MeshPeerCount)
		parts := make([]string, 0, len(report.MeshPeerCounts))
		for state, n := range report.MeshPeerCounts {
			parts = append(parts, fmt.Sprintf("%s=%d", state, n))
		}
		if len(parts) > 0 {
			fmt.Fprintf(out, " (%s)", strings.Join(parts, ", "))
		}
		fmt.Fprintln(out)
	}
	if report.DaemonError != "" {
		fmt.Fprintf(out, "Daemon Error:          %s\n", report.DaemonError)
	}
	for _, w := range report.Warnings {
		fmt.Fprintln(out, ui.Yellow("Warning: "+w))
	}
	fmt.Fprintln(out)
	_, err := fmt.Fprint(cmd.OutOrStdout(), rendered.String())
	return err
}

func runDaemonMeshPeers(cmd *cobra.Command) error {
	format := meshFormat(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
	defer cancel()

	peers, err := fetchDaemonMesh(ctx, meshCacheAddr(cmd))
	if err != nil {
		msg := "daemon unavailable"
		detail := err.Error()
		if format == "json" || format == "yaml" {
			return printOutput(cmd.OutOrStdout(), map[string]any{
				"source":   "daemon",
				"status":   msg,
				"error":    detail,
				"peers":    []mesh.Peer{},
				"warnings": []string{msg + ": " + detail},
			}, format)
		}
		_, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "daemon unavailable: %s\n", detail)
		if writeErr != nil {
			return writeErr
		}
		return nil
	}

	if format == "json" || format == "yaml" {
		status := "running with no peers"
		if len(peers) > 0 {
			status = "running"
		}
		return printOutput(cmd.OutOrStdout(), map[string]any{
			"source": "daemon",
			"status": status,
			"count":  len(peers),
			"peers":  peers,
		}, format)
	}

	if len(peers) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "running, 0 peers")
		return err
	}
	return printMeshPeers(cmd, peers)
}

func runLiveBeaconScan(cmd *cobra.Command, cfg *config.Config) error {
	out := cmd.OutOrStdout()
	format := meshFormat(cmd)
	if cfg == nil || cfg.Discovery == nil || !cfg.Discovery.Enabled {
		if format == "json" || format == "yaml" {
			return printOutput(out, map[string]any{
				"source": "scan",
				"status": "discovery disabled",
				"peers":  []config.NodeConfig{},
			}, format)
		}
		_, err := fmt.Fprintln(out, "discovery disabled")
		return err
	}

	if format == "text" {
		if _, err := fmt.Fprintln(out, "Listening for discovery beacons (5 seconds)..."); err != nil {
			return err
		}
	}

	registry := discovery.NewBeaconRegistry()
	scanCtx, scanCancel := context.WithTimeout(cmd.Context(), meshLiveScanWait)
	listenErr := discovery.WatchBeaconChanges(scanCtx, cfg, registry, nil)
	if listenErr != nil {
		scanCancel()
		if format == "json" || format == "yaml" {
			return printOutput(out, map[string]any{
				"source":   "scan",
				"status":   "listener failed",
				"error":    listenErr.Error(),
				"peers":    []config.NodeConfig{},
				"warnings": []string{"listener failed: " + listenErr.Error()},
			}, format)
		}
		_, writeErr := fmt.Fprintf(out, "listener failed: %v\n", listenErr)
		return writeErr
	}
	<-scanCtx.Done()
	scanCancel()
	if err := cmd.Context().Err(); err != nil {
		return err
	}

	peers := registry.Snapshot()
	if format == "json" || format == "yaml" {
		status := "running with no peers"
		if len(peers) > 0 {
			status = "scan complete"
		}
		return printOutput(out, map[string]any{
			"source": "scan",
			"status": status,
			"count":  len(peers),
			"peers":  peers,
		}, format)
	}
	if len(peers) == 0 {
		_, err := fmt.Fprintln(out, "running, 0 peers")
		return err
	}

	var rendered strings.Builder
	fmt.Fprintln(&rendered, "\nDiscovered Beacon Neighbors (source=scan, not gossip state):")
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
	_, err := fmt.Fprint(out, rendered.String())
	return err
}

func discoveryBeaconPort(cfg *config.Config) int {
	if cfg != nil && cfg.Discovery != nil && cfg.Discovery.UDPPort > 0 {
		return cfg.Discovery.UDPPort
	}
	return defaultBeaconPort
}

func meshGossipPort(cfg *config.Config) int {
	if cfg != nil && cfg.Discovery != nil && cfg.Discovery.UDPPort > 0 {
		return cfg.Discovery.UDPPort
	}
	return defaultGossipPort
}

func discoveryBeaconInterval(cfg *config.Config) int {
	if cfg != nil && cfg.Discovery != nil && cfg.Discovery.BeaconInterval > 0 {
		return cfg.Discovery.BeaconInterval
	}
	return 3
}

func udpPortHeld(port int) (bool, error) {
	pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true, nil
	}
	return false, pc.Close()
}
