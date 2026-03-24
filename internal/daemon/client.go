package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func NormalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimRight(addr, "/")
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

func FetchSnapshot(ctx context.Context, addr string) (*models.ClusterSnapshot, string, error) {
	baseURL := NormalizeAddr(addr)
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

	var meta Metadata
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
