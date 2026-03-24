package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/snapshot"
)

type statusOutput struct {
	Source   string                  `json:"source" yaml:"source"`
	Snapshot *models.ClusterSnapshot `json:"snapshot" yaml:"snapshot"`
}

func statusCmd() *cobra.Command {
	var format string
	var cached bool
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Collect cluster snapshot from all configured nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			snap, source, err := collectStatusSnapshot(
				ctx,
				cached,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return fetchCachedSnapshot(ctx, cacheAddr)
				},
				discoverLiveSnapshot,
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return err
			}

			var payload any = snap
			if cached {
				payload = statusOutput{
					Source:   source,
					Snapshot: snap,
				}
			}

			if err := printOutput(payload, format); err != nil {
				fmt.Fprintf(os.Stderr, "output error: %v\n", err)
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or yaml")
	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr, "Address of the local AXIS daemon cache")
	return cmd
}

func collectStatusSnapshot(
	ctx context.Context,
	cached bool,
	cachedLoader func(context.Context) (*models.ClusterSnapshot, string, error),
	liveLoader func(context.Context) (*models.ClusterSnapshot, string, error),
) (*models.ClusterSnapshot, string, error) {
	if cached && cachedLoader != nil {
		snap, source, err := cachedLoader(ctx)
		if err == nil {
			return snap, source, nil
		}
	}

	return liveLoader(ctx)
}

func discoverLiveSnapshot(ctx context.Context) (*models.ClusterSnapshot, string, error) {
	cfgPath := config.DefaultConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, "", err
	}

	nodes := discovery.Discover(ctx, cfg)
	return snapshot.Build(nodes), "live", nil
}

func fetchCachedSnapshot(ctx context.Context, addr string) (*models.ClusterSnapshot, string, error) {
	baseURL := normalizeCacheAddr(addr)
	client := &http.Client{Timeout: 5 * time.Second}

	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/snapshot/meta", nil)
	if err != nil {
		return nil, "", err
	}
	metaResp, err := client.Do(metaReq)
	if err != nil {
		return nil, "", err
	}
	defer metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("cache metadata request failed: %s", metaResp.Status)
	}

	var meta daemon.Metadata
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		return nil, "", err
	}
	if !meta.Ready {
		return nil, "", fmt.Errorf("snapshot cache not ready")
	}

	snapReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/snapshot", nil)
	if err != nil {
		return nil, "", err
	}
	snapResp, err := client.Do(snapReq)
	if err != nil {
		return nil, "", err
	}
	defer snapResp.Body.Close()
	if snapResp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("snapshot cache request failed: %s", snapResp.Status)
	}

	var snap models.ClusterSnapshot
	if err := json.NewDecoder(snapResp.Body).Decode(&snap); err != nil {
		return nil, "", err
	}

	source := meta.Source
	if source == "" {
		source = "daemon-cache"
	}

	return &snap, source, nil
}

func normalizeCacheAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimRight(addr, "/")
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}
