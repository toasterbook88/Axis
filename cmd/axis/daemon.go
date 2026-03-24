package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/daemon"
)

func daemonCmd() *cobra.Command {
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Interact with the local AXIS daemon cache",
	}
	cmd.PersistentFlags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr, "Address of the local AXIS daemon cache")

	cmd.AddCommand(&cobra.Command{
		Use:   "invalidate",
		Short: "Invalidate the local AXIS daemon snapshot cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := invalidateDaemonCache(ctx, cacheAddr); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "AXIS daemon cache invalidated")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "refresh",
		Short: "Refresh the local AXIS daemon snapshot cache now",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
			defer cancel()

			if err := refreshDaemonCache(ctx, cacheAddr); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "AXIS daemon cache refreshed")
			return nil
		},
	})

	return cmd
}

func invalidateDaemonCache(ctx context.Context, addr string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, daemon.NormalizeAddr(addr)+"/invalidate", nil)
	if err != nil {
		return err
	}

	return doDaemonAction(req, "daemon invalidate failed")
}

func refreshDaemonCache(ctx context.Context, addr string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, daemon.NormalizeAddr(addr)+"/refresh", nil)
	if err != nil {
		return err
	}

	return doDaemonAction(req, "daemon refresh failed")
}

func doDaemonAction(req *http.Request, prefix string) error {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Errorf("%s: %s", prefix, resp.Status)
	}
	return fmt.Errorf("%s: %s: %s", prefix, resp.Status, msg)
}
