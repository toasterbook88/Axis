package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
)

const Version = buildinfo.Version

const daemonRequestTimeout = 5 * time.Second

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

	if meta.Stale {
		appendSnapshotWarningIfMissing(&snap, staleCacheWarning(meta))
	}

	source := meta.Source
	if source == "" {
		source = "daemon-cache"
	}

	return &snap, source, nil
}

func staleCacheWarning(meta Metadata) models.Warning {
	message := "daemon cache is stale; run axis daemon refresh or restart axis serve"
	if meta.CacheAgeSec > 0 {
		message = fmt.Sprintf("daemon cache is stale (%ds old); run axis daemon refresh or restart axis serve", meta.CacheAgeSec)
	}
	if len(meta.StaleNodes) > 0 {
		message = fmt.Sprintf("%s; stale nodes: %s", message, strings.Join(meta.StaleNodes, ", "))
	}
	return models.Warning{
		Kind:    "cache",
		Message: message,
	}
}

func appendSnapshotWarningIfMissing(snap *models.ClusterSnapshot, warning models.Warning) {
	if snap == nil {
		return
	}
	for _, existing := range snap.Warnings {
		if existing.Kind == warning.Kind && existing.Message == warning.Message && existing.Node == warning.Node {
			return
		}
	}
	snap.Warnings = append(snap.Warnings, warning)
}

func FetchMeta(ctx context.Context, addr string) (Metadata, error) {
	token, err := auth.LoadOrGenerateToken()
	if err != nil {
		return Metadata{}, fmt.Errorf("loading api token: %w", err)
	}
	return fetchMetaWithToken(ctx, addr, token)
}

// RunGuarded preserves the simple final-result helper surface for callers that
// do not need streamed callbacks. It reuses the same streamed /run transport as
// other local execution callers so long-running executions are bounded by the
// caller context rather than the short metadata timeout.
func RunGuarded(ctx context.Context, addr string, req execution.GuardedExecutionRequest, origin models.ExecutionOrigin) (execution.GuardedExecutionResult, error) {
	return RunGuardedStream(ctx, addr, req, origin)
}

// RunGuardedStream executes a guarded request through the local AXIS HTTP /run
// surface using the NDJSON streaming contract.
func RunGuardedStream(ctx context.Context, addr string, req execution.GuardedExecutionRequest, origin models.ExecutionOrigin) (execution.GuardedExecutionResult, error) {
	token, err := auth.LoadOrGenerateToken()
	if err != nil {
		return execution.GuardedExecutionResult{}, fmt.Errorf("loading api token: %w", err)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return execution.GuardedExecutionResult{}, fmt.Errorf("marshal run request: %w", err)
	}

	client, baseURLAddr := runHTTPClientForAddr(addr)
	baseURL := NormalizeAddr(baseURLAddr)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/run?stream=1", bytes.NewReader(body))
	if err != nil {
		return execution.GuardedExecutionResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", RunStreamContentType)
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	if !origin.Normalized().IsZero() {
		if err := auth.SetForwardedExecutionOriginHeaders(httpReq.Header, origin, token, time.Now()); err != nil {
			return execution.GuardedExecutionResult{}, fmt.Errorf("sign forwarded execution origin: %w", err)
		}
	}

	httpResp, err := client.Do(httpReq)
	if err != nil {
		return execution.GuardedExecutionResult{}, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			return execution.GuardedExecutionResult{}, fmt.Errorf("run stream request failed: %s", httpResp.Status)
		}
		return execution.GuardedExecutionResult{}, fmt.Errorf("run stream request failed: %s: %s", httpResp.Status, msg)
	}

	dec := json.NewDecoder(httpResp.Body)
	var (
		final     execution.GuardedExecutionResult
		seenFinal bool
	)
	for {
		var event RunStreamEvent
		if err := dec.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			return execution.GuardedExecutionResult{}, fmt.Errorf("decode run stream event: %w", err)
		}
		switch event.Type {
		case RunStreamEventReady:
			if event.Result != nil && req.OnReady != nil {
				req.OnReady(*event.Result)
			}
		case RunStreamEventStateChange:
			if req.OnStateChange != nil {
				resp := execution.GuardedExecutionResult{}
				if event.Result != nil {
					resp = *event.Result
				}
				req.OnStateChange(ctx, event.Trigger, resp)
			}
		case RunStreamEventStdout:
			if req.Stdout != nil && event.Text != "" {
				if _, err := io.WriteString(req.Stdout, event.Text); err != nil {
					return execution.GuardedExecutionResult{}, err
				}
			}
		case RunStreamEventStderr:
			if req.Stderr != nil && event.Text != "" {
				if _, err := io.WriteString(req.Stderr, event.Text); err != nil {
					return execution.GuardedExecutionResult{}, err
				}
			}
		case RunStreamEventResult:
			if event.Result == nil {
				return execution.GuardedExecutionResult{}, fmt.Errorf("run stream result event missing payload")
			}
			final = *event.Result
			seenFinal = true
		}
	}
	if !seenFinal {
		return execution.GuardedExecutionResult{}, fmt.Errorf("run stream ended without final result")
	}
	if final.Blocked {
		return final, nil
	}
	if final.Error != "" {
		return final, errors.New(final.Error)
	}
	return final, nil
}

func HttpClientForAddr(addr string) (*http.Client, string) {
	return httpClientForAddrWithTimeout(addr, daemonRequestTimeout)
}

func runHTTPClientForAddr(addr string) (*http.Client, string) {
	return httpClientForAddrWithTimeout(addr, 0)
}

func httpClientForAddrWithTimeout(addr string, timeout time.Duration) (*http.Client, string) {
	client := &http.Client{
		Timeout: timeout,
	}
	if auth.IsUnixAddr(addr) {
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", addr)
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
