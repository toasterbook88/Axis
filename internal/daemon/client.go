package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/models"
)

const Version = buildinfo.Version

func NormalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimRight(addr, "/")
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

func FetchSnapshot(ctx context.Context, addr string) (*models.ClusterSnapshot, string, error) {
	token, err := auth.LoadOrGenerateToken()
	if err != nil {
		return nil, "", fmt.Errorf("loading api token: %w", err)
	}

	meta, err := fetchMetaWithToken(ctx, addr, token)
	if err != nil {
		return nil, "", err
	}
	if !meta.Ready {
		return nil, "", fmt.Errorf("snapshot cache not ready")
	}

	client, baseURLAddr := HttpClientForAddr(addr)
	baseURL := NormalizeAddr(baseURLAddr)
	snapReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/snapshot", nil)
	if err != nil {
		return nil, "", err
	}
	if token != "" {
		snapReq.Header.Set("Authorization", "Bearer "+token)
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

func FetchMeta(ctx context.Context, addr string) (Metadata, error) {
	token, err := auth.LoadOrGenerateToken()
	if err != nil {
		return Metadata{}, fmt.Errorf("loading api token: %w", err)
	}
	return fetchMetaWithToken(ctx, addr, token)
}

func HttpClientForAddr(addr string) (*http.Client, string) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	if auth.IsUnixAddr(addr) {
		client.Transport = &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", addr)
			},
		}
		return client, "http://localhost"
	}
	return client, addr
}

func fetchMetaWithToken(ctx context.Context, addr string, token string) (Metadata, error) {
	client, baseURLAddr := HttpClientForAddr(addr)
	baseURL := NormalizeAddr(baseURLAddr)

	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/snapshot/meta", nil)
	if err != nil {
		return Metadata{}, err
	}
	if token != "" {
		metaReq.Header.Set("Authorization", "Bearer "+token)
	}
	metaResp, err := client.Do(metaReq)
	if err != nil {
		return Metadata{}, err
	}
	defer metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK {
		return Metadata{}, fmt.Errorf("cache metadata request failed: %s", metaResp.Status)
	}

	var meta Metadata
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		return Metadata{}, err
	}
	return meta, nil
}
