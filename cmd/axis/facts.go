package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/ui"
)

var currentHostname = os.Hostname
var collectLocalFacts = func(ctx context.Context, hostname string) (*models.NodeFacts, error) {
	return facts.NewLocalCollector(hostname, "").Collect(ctx)
}

func factsCmd() *cobra.Command {
	var format string
	var verbose bool

	cmd := &cobra.Command{
		Use:   "facts",
		Short: "Collect and display local node facts",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			hostname, _ := currentHostname()
			nf, err := collectLocalFacts(ctx, hostname)
			if err != nil {
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
				printFactsText(cmd, nf, verbose)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show all network addresses (default hides local IPv6 and temporary addresses)")
	return cmd
}

func printFactsText(cmd *cobra.Command, nf *models.NodeFacts, verbose bool) {
	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "%s %s\n\n", ui.Bold("NODE FACTS"), ui.Cyan(nf.Name))

	kv := func(key, val string) {
		fmt.Fprintf(out, "  %s  %s\n", ui.Dim(fmt.Sprintf("%-14s", key)), val)
	}

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
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  %s\n", ui.Bold("Resources"))
		kv("cpu:", fmt.Sprintf("%d cores (%s)", r.CPUCores, r.CPUModel))
		kv("ram total:", fmt.Sprintf("%d MB", r.RAMTotalMB))
		kv("ram free:", fmt.Sprintf("%d MB", r.RAMFreeMB))
		if nf.RAMReservedMB > 0 {
			kv("ram reserved:", fmt.Sprintf("%d MB", nf.RAMReservedMB))
		}
		if nf.RAMAllocatableMB > 0 {
			kv("ram alloc:", fmt.Sprintf("%d MB", nf.RAMAllocatableMB))
		}
		kv("disk:", fmt.Sprintf("%d GB free / %d GB total", r.DiskFreeGB, r.DiskTotalGB))
		if r.StorageClass != "" {
			kv("storage:", r.StorageClass)
		}
		kv("pressure:", formatPressure(r.Pressure))
		if len(r.GPUs) > 0 {
			fmt.Fprintln(out)
			fmt.Fprintf(out, "  %s\n", ui.Bold("GPUs"))
			for _, g := range r.GPUs {
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

	if len(nf.Tools) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  %s\n", ui.Bold("Tools"))
		for _, t := range nf.Tools {
			ver := ""
			if t.Version != "" {
				ver = " " + ui.Dim(t.Version)
			}
			fmt.Fprintf(out, "    %s %s%s\n", ui.Green("✓"), t.Name, ver)
		}
	}

	if nf.Ollama != nil && nf.Ollama.Installed {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  %s\n", ui.Bold("Ollama"))
		kv("running:", fmt.Sprintf("%v", nf.Ollama.Running))
		if len(nf.Ollama.Models) > 0 {
			kv("models:", strings.Join(nf.Ollama.Models, ", "))
		}
	}

	if len(nf.Addresses) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  %s\n", ui.Bold("Addresses"))
		hiddenCount := 0
		for _, a := range nf.Addresses {
			show := verbose || a.Kind == "ipv4" || a.SpeedClass == "tailscale" || a.SpeedClass == "wireguard" || a.SpeedClass == "thunderbolt"
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

	fmt.Fprintf(out, "\n  %s %s\n", ui.Dim("collected:"), nf.CollectedAt.Format(time.RFC3339))
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
