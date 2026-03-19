package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
)

func serveCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the local AXIS HTTP API and execution surface",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "AXIS HTTP API listening on http://%s\n", addr)
			return api.Serve(addr)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", api.DefaultAddr, "Listen address for the local AXIS HTTP API")
	return cmd
}
