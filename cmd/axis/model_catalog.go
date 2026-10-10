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
	"github.com/toasterbook88/axis/internal/modelinventory"
	"github.com/toasterbook88/axis/internal/models"
)

var loadModelCatalog = readModelCatalog

func readModelCatalog(ctx context.Context, live bool, cacheAddr string) (models.ModelCatalog, error) {
	snap, source, err := readModelSnapshot(ctx, live, cacheAddr, "model catalog")
	if err != nil {
		return models.ModelCatalog{}, err
	}
	return modelinventory.Catalog(snap, source), nil
}

func modelCatalogCmd() *cobra.Command {
	var format, cacheAddr string
	var live bool
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "List every observed model per node: loaded and installed, with capabilities and locality",
		Long: `List every model the cluster snapshot observed, one row per model per node.

Each node reports its own models, so the catalog does not depend on which node
you run it from. STATE is loaded only with a load signal (Ollama /api/ps, or a
llama.cpp process started with -m), listed when a server names the model without
a load signal (e.g. MLX), or installed (on disk, not loaded). LOCALITY is
on-node, or cloud-proxy when the node forwards requests
off the cluster. Empty CAPABILITIES means the engine did not report them.
Nodes the snapshot could not observe are listed after the table.`,
		Args:    cobra.NoArgs,
		PreRunE: validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			catalog, err := collectModelCatalog(cmd.Context(), live, cacheAddr)
			if err != nil {
				return err
			}
			if format != "text" {
				return printOutput(cmd.OutOrStdout(), catalog, format)
			}
			return printModelCatalogText(cmd.OutOrStdout(), catalog)
		},
	}
	addModelInventoryFlags(cmd, &format, &live, &cacheAddr)
	return cmd
}

func collectModelCatalog(parent context.Context, live bool, cacheAddr string) (models.ModelCatalog, error) {
	timeout := 10 * time.Second
	if live {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	catalog, err := loadModelCatalog(ctx, live, cacheAddr)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return models.ModelCatalog{}, ctxErr
		}
		return models.ModelCatalog{}, err
	}
	return catalog, nil
}

func printModelCatalogText(out io.Writer, catalog models.ModelCatalog) error {
	var rendered bytes.Buffer
	fmt.Fprintf(&rendered, "MODEL CATALOG (%d)\n", len(catalog.Entries))
	if len(catalog.Entries) == 0 {
		fmt.Fprintln(&rendered, "No models observed.")
	} else {
		table := tabwriter.NewWriter(&rendered, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "MODEL\tNODE\tENGINE\tSTATE\tLOCALITY\tCAPABILITIES\tPARAMS\tQUANT\tPORT")
		for _, e := range catalog.Entries {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				e.Model, e.Node, displayModelValue(e.Engine), e.State, e.Locality,
				displayModelValue(strings.Join(e.Capabilities, ",")),
				displayModelValue(e.ParameterSize), displayModelValue(e.Quantization),
				displayModelPort(e.Port))
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	for _, n := range catalog.Unobserved {
		line := fmt.Sprintf("Not observed: %s (%s)", n.Node, n.Status)
		if n.Reason != "" {
			line += ": " + n.Reason
		}
		fmt.Fprintln(&rendered, line)
	}
	printModelInventoryAuthority(&rendered, catalog.Source, catalog.PublicationID, catalog.ObservedAt, catalog.Warnings)
	_, err := out.Write(rendered.Bytes())
	return err
}
