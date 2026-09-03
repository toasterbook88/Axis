package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/modelinventory"
	"github.com/toasterbook88/axis/internal/models"
)

var fetchModelInventorySnapshot = daemon.FetchSnapshot

var loadModelInventory = readModelInventory

func readModelInventory(ctx context.Context, live bool, cacheAddr string) (models.ModelInventory, error) {
	var (
		snap   *models.ClusterSnapshot
		source string
		err    error
	)
	if live {
		snap, err = loadModelSnapshot(ctx)
		source = "live"
	} else {
		snap, source, err = fetchModelInventorySnapshot(ctx, cacheAddr)
	}
	if err != nil {
		if live {
			return models.ModelInventory{}, fmt.Errorf("collect live model inventory: %w", err)
		}
		return models.ModelInventory{}, fmt.Errorf("load model inventory from daemon cache: %w (use --live for an explicit live collection)", err)
	}
	return modelinventory.FromSnapshot(snap, source), nil
}

func modelListCmd() *cobra.Command {
	var format, cacheAddr string
	var live bool
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List observed resident model instances from the daemon cache",
		Args:    cobra.NoArgs,
		PreRunE: validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelList(cmd, live, cacheAddr, format)
		},
	}
	addModelInventoryFlags(cmd, &format, &live, &cacheAddr)
	return cmd
}

func modelInspectCmd() *cobra.Command {
	var format, cacheAddr string
	var live bool
	cmd := &cobra.Command{
		Use:     "inspect <instance-id>",
		Short:   "Inspect one observed resident model instance",
		Args:    cobra.ExactArgs(1),
		PreRunE: validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelInspect(cmd, args[0], live, cacheAddr, format)
		},
	}
	addModelInventoryFlags(cmd, &format, &live, &cacheAddr)
	return cmd
}

func addModelInventoryFlags(cmd *cobra.Command, format *string, live *bool, cacheAddr *string) {
	cmd.Flags().StringVar(format, "format", "text", "Output format: text, json, or yaml")
	cmd.Flags().BoolVar(live, "live", false, "Perform an explicit live cluster collection instead of reading the daemon cache")
	cmd.Flags().StringVar(cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
}

func runModelList(cmd *cobra.Command, live bool, cacheAddr, format string) error {
	inventory, err := collectModelInventory(cmd.Context(), live, cacheAddr)
	if err != nil {
		return err
	}
	if format != "text" {
		return printOutput(cmd.OutOrStdout(), inventory, format)
	}
	return printModelInventoryText(cmd.OutOrStdout(), inventory)
}

func runModelInspect(cmd *cobra.Command, id string, live bool, cacheAddr, format string) error {
	inventory, err := collectModelInventory(cmd.Context(), live, cacheAddr)
	if err != nil {
		return err
	}
	for _, instance := range inventory.Instances {
		if instance.ID != id {
			continue
		}
		inspection := models.ModelInspection{
			Source: inventory.Source, PublicationID: inventory.PublicationID, ObservedAt: inventory.ObservedAt,
			Instance: instance, Warnings: inventory.Warnings,
		}
		if format != "text" {
			return printOutput(cmd.OutOrStdout(), inspection, format)
		}
		return printModelInspectionText(cmd.OutOrStdout(), inspection)
	}
	return fmt.Errorf("model instance %q not found in %s inventory", id, sourceOrLive(inventory.Source))
}

func collectModelInventory(parent context.Context, live bool, cacheAddr string) (models.ModelInventory, error) {
	timeout := 10 * time.Second
	if live {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	inventory, err := loadModelInventory(ctx, live, cacheAddr)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return models.ModelInventory{}, ctxErr
		}
		return models.ModelInventory{}, err
	}
	return inventory, nil
}

func printModelInventoryText(out io.Writer, inventory models.ModelInventory) error {
	var rendered bytes.Buffer
	fmt.Fprintf(&rendered, "MODEL INSTANCES (%d)\n", len(inventory.Instances))
	if len(inventory.Instances) == 0 {
		fmt.Fprintln(&rendered, "No resident model instances observed.")
	} else {
		table := tabwriter.NewWriter(&rendered, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "INSTANCE\tMODEL\tENGINE\tNODE\tNODE STATUS\tSTATE\tPORT\tPROCESSOR\tWEIGHTS\tRAM\tVRAM")
		for _, instance := range inventory.Instances {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				instance.ID, displayModelValue(instance.Model), displayModelValue(instance.Engine),
				displayModelValue(instance.Node), displayModelValue(string(instance.NodeStatus)), instance.State, displayModelPort(instance.Port),
				displayModelValue(instance.Processor), displayModelMemory(instance.WeightSizeMB), displayModelMemory(instance.SizeRAMMB),
				displayModelMemory(instance.SizeVRAMMB))
		}
		_ = table.Flush()
	}
	printModelInventoryAuthority(&rendered, inventory.Source, inventory.PublicationID, inventory.ObservedAt, inventory.Warnings)
	_, err := io.Copy(out, &rendered)
	return err
}

func printModelInspectionText(out io.Writer, inspection models.ModelInspection) error {
	var rendered bytes.Buffer
	instance := inspection.Instance
	fmt.Fprintln(&rendered, "MODEL INSTANCE")
	fmt.Fprintf(&rendered, "ID: %s\nModel: %s\nEngine: %s\nNode: %s\nNode status: %s\nState: %s\nPort: %s\nProcessor: %s\nWeights: %s\nRAM: %s\nVRAM: %s\n",
		instance.ID, displayModelValue(instance.Model), displayModelValue(instance.Engine),
		displayModelValue(instance.Node), displayModelValue(string(instance.NodeStatus)), instance.State, displayModelPort(instance.Port),
		displayModelValue(instance.Processor), displayModelMemory(instance.WeightSizeMB), displayModelMemory(instance.SizeRAMMB),
		displayModelMemory(instance.SizeVRAMMB))
	if instance.Source != "" {
		fmt.Fprintf(&rendered, "Resident source: %s\n", instance.Source)
	}
	if !instance.ExpiresAt.IsZero() {
		fmt.Fprintf(&rendered, "Expires: %s\n", instance.ExpiresAt.Format(time.RFC3339))
	}
	if !instance.ObservedAt.IsZero() {
		fmt.Fprintf(&rendered, "Node observed: %s\n", instance.ObservedAt.Format(time.RFC3339))
	}
	printModelInventoryAuthority(&rendered, inspection.Source, inspection.PublicationID, inspection.ObservedAt, inspection.Warnings)
	_, err := io.Copy(out, &rendered)
	return err
}

func printModelInventoryAuthority(out io.Writer, source, publicationID string, observedAt time.Time, warnings []models.Warning) {
	fmt.Fprintf(out, "Source: %s\n", sourceOrLive(source))
	if publicationID != "" {
		fmt.Fprintf(out, "Publication: %s\n", publicationID)
	}
	if !observedAt.IsZero() {
		fmt.Fprintf(out, "Snapshot observed: %s\n", observedAt.Format(time.RFC3339))
	}
	for _, warning := range warnings {
		prefix := warning.Kind
		if warning.Node != "" {
			prefix = warning.Node + "/" + warning.Kind
		}
		fmt.Fprintf(out, "Warning (%s): %s\n", prefix, warning.Message)
	}
}

func displayModelValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func displayModelPort(port int) string {
	if port == 0 {
		return "—"
	}
	return fmt.Sprintf("%d", port)
}

func displayModelMemory(sizeMB int64) string {
	if sizeMB == 0 {
		return "—"
	}
	return fmt.Sprintf("%d MiB", sizeMB)
}
