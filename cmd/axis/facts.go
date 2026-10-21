package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/ui"
)

var currentHostname = os.Hostname

// localNodeLabels resolves the local node's operator-assigned labels from
// config. A missing config file yields empty labels (unconfigured machine is
// normal for `axis facts`); a config that exists but fails to load is an
// error — silently dropping labels would make the node lose eligibility
// with no signal.
var localNodeLabels = func(hostname string) (map[string]string, error) {
	cfgPath := os.Getenv("AXIS_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	if _, statErr := os.Stat(cfgPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("statting config for node labels: %w", statErr)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("loading config for node labels: %w", err)
	}
	for i := range cfg.Nodes {
		if cfg.Nodes[i].IsLocal() {
			return cfg.Nodes[i].Labels, nil
		}
	}
	if nc, ok := cfg.FindNode(hostname); ok {
		return nc.Labels, nil
	}
	return map[string]string{}, nil
}

var collectLocalFacts = func(ctx context.Context, hostname string) (*models.NodeFacts, error) {
	labels, err := localNodeLabels(hostname)
	if err != nil {
		return nil, err
	}
	return facts.NewLocalCollector(hostname, "", labels).Collect(ctx)
}

const factsHelpText = `Collect and display local facts in organized sections.

By default, show machine, resources, storage, network, tools, and inference facts.
Use --hardware (also -hw) for detailed hardware specifications and live load.
Hardware reports cover CPU, memory, GPUs, storage, network adapters, system
firmware, sensors, and attached devices on macOS and Linux.

Hardware detail depends on device support, available tools, and permissions.
Unavailable readings include a reason. Rates use a reported sampling interval.
Hardware collection is read-only, excludes unique device identifiers, and never
invokes sudo. Watch mode retains inventory and refreshes live measurements.
JSON watch output is one report per line; YAML watch output uses document separators.`

func factsCmd() *cobra.Command {
	var format string
	var verbose bool
	var hardware, watch bool
	var interval time.Duration

	cmd := &cobra.Command{
		Use:     "facts",
		Short:   "Collect and display facts about this computer",
		Long:    factsHelpText,
		Example: "  axis facts\n  axis facts --hardware\n  axis facts -hw\n  axis facts --hardware --format json\n  axis facts --hardware --watch --interval 2s",
		Args:    cobra.NoArgs,
		// Local facts have no need for advisory Cortex/webhook initialization.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			noColor, _ := cmd.Flags().GetBool("no-color")
			ui.Init(noColor)
			return nil
		},
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(&format, "text", "json", "yaml")(cmd, args); err != nil {
				return err
			}
			return validateHardwareFactsFlags(cmd, hardware, watch, interval)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if hardware {
				return runHardwareFacts(cmd, format, watch, interval)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			hostname, _ := currentHostname()
			nf, err := collectLocalFacts(ctx, hostname)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				nf = &models.NodeFacts{
					Name:        hostname,
					Status:      models.StatusError,
					Error:       err.Error(),
					CollectedAt: time.Now().UTC(),
				}
			}

			switch format {
			case "json", "yaml":
				return printOutput(cmd.OutOrStdout(), nf, format)
			default:
				return printFactsText(cmd, nf, verbose)
			}
		},
	}

	cmd.Flags().BoolVar(&hardware, "hardware", false, "Show detailed hardware specifications and live load (also -hw)")
	cmd.Flags().BoolVar(&watch, "watch", false, "Refresh hardware measurements; requires --hardware")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "Hardware sampling interval (100ms to 1m)")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show all network addresses (default hides local IPv6 and temporary addresses)")
	return cmd
}

func validateHardwareFactsFlags(cmd *cobra.Command, hardware, watch bool, interval time.Duration) error {

	if watch && !hardware {
		return fmt.Errorf("--watch requires --hardware")
	}
	if cmd.Flags().Changed("interval") && !hardware {
		return fmt.Errorf("--interval requires --hardware")
	}
	if interval < 100*time.Millisecond || interval > time.Minute {
		return fmt.Errorf("--interval must be between 100ms and 1m")
	}
	return nil
}

func printFactsText(cmd *cobra.Command, nf *models.NodeFacts, verbose bool) error {
	var rendered strings.Builder
	out := &rendered

	fmt.Fprintf(out, "%s %s\n\n", ui.Bold("NODE FACTS"), ui.Cyan(nf.Name))

	kv := func(key, val string) {
		fmt.Fprintf(out, "  %s  %s\n", ui.Dim(fmt.Sprintf("%-14s", key)), val)
	}

	fmt.Fprintf(out, "  %s\n", ui.Bold("Machine"))
	kv("collected:", nf.CollectedAt.Format(time.RFC3339))
	kv("hostname:", nf.Hostname)
	if nf.Identity != nil && nf.Identity.StableID != "" {
		val := nf.Identity.StableID
		if nf.Identity.Source != "" {
			val = fmt.Sprintf("%s (%s)", val, nf.Identity.Source)
		}
		kv("identity:", val)
	}
	kv("os:", fmt.Sprintf("%s %s", nf.OS, nf.OSVersion))
	kv("arch:", nf.Arch)
	kv("status:", string(nf.Status))

	if nf.Resources != nil {
		r := nf.Resources
		fmt.Fprintf(out, "\n  %s\n", ui.Bold("Resources"))
		if r.CPUCores > 0 {
			kv("cpu:", fmt.Sprintf("%d logical CPUs (%s)", r.CPUCores, r.CPUModel))
		} else {
			kv("cpu:", "unavailable")
		}
		kv("ram total:", fmt.Sprintf("%d MB", r.RAMTotalMB))
		kv("ram free:", fmt.Sprintf("%d MB", r.RAMFreeMB))
		if nf.RAMReservedMB > 0 {
			kv("ram reserved:", fmt.Sprintf("%d MB", nf.RAMReservedMB))
		}
		if nf.RAMAllocatableMB > 0 {
			kv("ram alloc:", fmt.Sprintf("%d MB", nf.RAMAllocatableMB))
		}
		fmt.Fprintf(out, "\n  %s\n", ui.Bold("Storage"))
		kv("disk:", fmt.Sprintf("%d GB free / %d GB total", r.DiskFreeGB, r.DiskTotalGB))
		if r.StorageClass != "" {
			kv("storage:", r.StorageClass)
		}
		if len(r.Volumes) > 0 {
			fmt.Fprintf(out, "\n  %s\n", ui.Bold("Volumes"))
			for _, v := range r.Volumes {
				line := fmt.Sprintf("    %s %s  %d GB free / %d GB  %s %s %s",
					ui.Green("✓"), v.Mount, v.FreeGB, v.TotalGB, v.Kind, v.Bus, v.Role)
				if v.Removable {
					line += " removable"
				}
				if v.LinkMbit > 0 {
					line += fmt.Sprintf(" %d Mbit", v.LinkMbit)
				}
				if v.Owner != "" {
					line += "  owner " + v.Owner
					if v.OwnerMount != "" {
						line += ":" + v.OwnerMount
					}
				}
				fmt.Fprintln(out, line)
			}

		}

		fmt.Fprintf(out, "\n  %s\n", ui.Bold("Live load"))
		kv("pressure:", formatPressure(r.Pressure))
		if r.ThermalState != "" {
			kv("thermal:", formatThermal(r.ThermalState))
		}
		if r.PowerSource != "" {
			kv("power:", r.PowerSource)
		}
		if r.BatteryPercent != nil {
			kv("battery:", fmt.Sprintf("%d%%", *r.BatteryPercent))
		}
		kv("load:", fmt.Sprintf("%.2f / %.2f / %.2f", r.Load1M, r.Load5M, r.Load15M))
		if r.MemoryTopology != "" {
			kv("memory:", string(r.MemoryTopology))
		}
	}

	if nf.Resources != nil && len(nf.Resources.GPUs) > 0 {
		fmt.Fprintf(out, "\n  %s\n", ui.Bold("GPUs"))
		for _, g := range nf.Resources.GPUs {
			detail := formatGPUBaseName(g)
			if g.VRAMMB > 0 {
				detail = fmt.Sprintf("%s — %d MB VRAM", detail, g.VRAMMB)
			}
			if len(g.Capabilities) > 0 {
				detail = fmt.Sprintf("%s [%s]", detail, strings.Join(g.Capabilities, ", "))
			}
			fmt.Fprintf(out, "    %s %s\n", ui.Green("✓"), detail)
		}
	}

	if len(nf.Tools) > 0 {
		fmt.Fprintf(out, "\n  %s\n", ui.Bold("Tools"))
		for _, t := range nf.Tools {
			ver := ""
			if t.Version != "" {
				ver = " " + ui.Dim(t.Version)
			}
			fmt.Fprintf(out, "    %s %s%s\n", ui.Green("✓"), t.Name, ver)
		}
	}

	if len(nf.DiskWeights) > 0 || nf.DiskWeightsTruncated || nf.LocalEndpoints != nil || nf.Ollama != nil {
		fmt.Fprintf(out, "\n  %s\n", ui.Bold("Inference"))
	}

	if len(nf.DiskWeights) > 0 || nf.DiskWeightsTruncated {
		fmt.Fprintln(out)
		title := "Disk weights"
		if nf.DiskWeightsTruncated {
			title += " (truncated)"
		}
		fmt.Fprintf(out, "  %s\n", ui.Bold(title))
		for _, w := range nf.DiskWeights {
			fmt.Fprintf(out, "    %s %s  %s  %s  %s\n",
				ui.Green("✓"), w.Name, formatDiskWeightBytes(w.Bytes), w.Format, w.Path)
		}
	}

	if le := nf.LocalEndpoints; le != nil && (len(le.Confirmed) > 0 || len(le.Candidates) > 0 || le.Truncated || le.CachedSweep || le.SkippedNotAllowlisted > 0 || len(le.SkippedExcluded) > 0) {
		fmt.Fprintln(out)
		title := "Local endpoints"
		if le.Truncated {
			title += " (truncated)"
		}
		if le.CachedSweep {
			title += " (cached sweep)"
		}
		fmt.Fprintf(out, "  %s\n", ui.Bold(title))
		if le.SkippedNotAllowlisted > 0 {
			fmt.Fprintf(out, "    %d listener(s) not allowlisted\n", le.SkippedNotAllowlisted)
		}
		if len(le.SkippedExcluded) > 0 {
			ports := make([]string, 0, len(le.SkippedExcluded))
			for _, port := range le.SkippedExcluded {
				ports = append(ports, fmt.Sprintf("%d", port))
			}
			fmt.Fprintf(out, "    explicitly excluded port(s): %s\n", strings.Join(ports, ", "))
		}
		for _, ep := range le.Confirmed {
			fmt.Fprintf(out, "    %s :%d  %s  %s  %d model(s), listed\n", ui.Green("✓"), ep.Port, ep.Engine, ep.Bind, len(ep.Models))
		}
		for _, c := range le.Candidates {
			fmt.Fprintf(out, "    %s :%d  %s  %s\n", ui.Yellow("?"), c.Port, c.Process, c.Reason)
		}
	}

	if nf.Ollama != nil && nf.Ollama.Installed {
		fmt.Fprintf(out, "\n  %s\n", ui.Bold("Ollama"))
		kv("running:", fmt.Sprintf("%v", nf.Ollama.Running))
		if len(nf.Ollama.Models) > 0 {
			kv("models:", strings.Join(nf.Ollama.Models, ", "))
		}
	}

	if len(nf.Addresses) > 0 {
		fmt.Fprintf(out, "\n  %s\n", ui.Bold("Network addresses"))
		hiddenCount := 0
		for _, a := range nf.Addresses {
			show := verbose || a.Kind == "ipv4" || (a.Kind == "ipv6" && a.Scope != "link-local") || a.SpeedClass == "tailscale" || a.SpeedClass == "wireguard" || a.SpeedClass == "thunderbolt"
			if !show {
				hiddenCount++
				continue
			}
			detail := a.Address
			if a.Interface != "" {
				detail = fmt.Sprintf("%s (%s)", a.Address, a.Interface)
			}
			if a.SpeedClass != "" && a.SpeedClass != "unknown" {
				detail = fmt.Sprintf("%s [%s]", detail, a.SpeedClass)
			}
			fmt.Fprintf(out, "    %s %s\n", ui.Green("✓"), detail)
		}
		if hiddenCount > 0 {
			ui.DimColor.Fprintf(out, "    (... and %d more addresses, use --verbose to show all)\n", hiddenCount)
		}
	}

	if nf.Error != "" {
		kv("error:", nf.Error)
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), rendered.String())
	return err
}

func formatDiskWeightBytes(n int64) string {
	const (
		mib = 1024 * 1024
		gib = 1024 * mib
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.0f MiB", float64(n)/float64(mib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatThermal(state string) string {
	switch state {
	case "nominal":
		return ui.Green("nominal")
	case "fair":
		return ui.Yellow("fair")
	case "serious":
		return ui.Red("serious")
	case "critical":
		return ui.Red("critical")
	default:
		return state
	}
}
