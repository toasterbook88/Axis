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
				return printOutput(nf, format)
			default:
				printFactsText(cmd, nf)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	return cmd
}

func printFactsText(cmd *cobra.Command, nf *models.NodeFacts) {
	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "%s %s\n\n", ui.Bold("NODE FACTS"), ui.Cyan(nf.Name))

	kv := func(key, val string) {
		fmt.Fprintf(out, "  %s  %s\n", ui.Dim(fmt.Sprintf("%-14s", key)), val)
	}

	kv("hostname:", nf.Hostname)
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
		if r.RAMReservedMB > 0 {
			kv("ram reserved:", fmt.Sprintf("%d MB", r.RAMReservedMB))
		}
		if r.RAMAllocatableMB > 0 {
			kv("ram alloc:", fmt.Sprintf("%d MB", r.RAMAllocatableMB))
		}
		kv("disk:", fmt.Sprintf("%d GB free / %d GB total", r.DiskFreeGB, r.DiskTotalGB))
		kv("pressure:", formatPressure(r.Pressure))
		if len(r.GPUs) > 0 {
			kv("gpus:", strings.Join(r.GPUs, ", "))
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

	fmt.Fprintf(out, "\n  %s %s\n", ui.Dim("collected:"), nf.CollectedAt.Format(time.RFC3339))
}
