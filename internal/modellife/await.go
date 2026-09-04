package modellife

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// AwaitOptions configures the readiness polling loop for a model instance.
type AwaitOptions struct {
	Timeout        time.Duration
	Interval       time.Duration
	ProbeFn        func(ctx context.Context) error
	SnapshotSource string
	PublicationID  string
	SnapshotAt     time.Time
}

// AwaitInstance polls until the target model instance is ready to serve
// or until the timeout or context deadline expires.
func AwaitInstance(ctx context.Context, instance models.ModelInstance, opts AwaitOptions) (models.ModelOperationReceipt, error) {
	startedAt := time.Now().UTC()
	receipt := models.ModelOperationReceipt{
		Schema:         "axis.model-operation/v1",
		ID:             models.GenerateID("mo"),
		Action:         models.ModelOperationAwait,
		InstanceID:     instance.ID,
		GenerationID:   instance.GenerationID,
		Node:           instance.Node,
		Engine:         instance.Engine,
		Port:           instance.Port,
		PID:            instance.PID,
		Model:          instance.Model,
		SnapshotSource: opts.SnapshotSource,
		PublicationID:  opts.PublicationID,
		SnapshotAt:     opts.SnapshotAt,
		StartedAt:      startedAt,
	}

	if instance.Port < 1 || instance.Port > 65535 {
		receipt.Status = models.ModelOperationRejected
		receipt.Disposition = "invalid_target"
		receipt.CompletedAt = time.Now().UTC()
		receipt.DurationMS = receipt.CompletedAt.Sub(startedAt).Milliseconds()
		receipt.Error = fmt.Sprintf("invalid port %d", instance.Port)
		return receipt, fmt.Errorf("%s", receipt.Error)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}

	probe := opts.ProbeFn
	if probe == nil {
		probe = func(probeCtx context.Context) error {
			return ProbeEndpoint(probeCtx, fmt.Sprintf("http://127.0.0.1:%d", instance.Port))
		}
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial probe attempt
	if err := probe(pollCtx); err == nil {
		receipt.Status = models.ModelOperationCompleted
		receipt.Disposition = "ready"
		receipt.CompletedAt = time.Now().UTC()
		receipt.DurationMS = receipt.CompletedAt.Sub(startedAt).Milliseconds()
		return receipt, nil
	} else {
		lastErr = err
	}

	for {
		select {
		case <-pollCtx.Done():
			receipt.CompletedAt = time.Now().UTC()
			receipt.DurationMS = receipt.CompletedAt.Sub(startedAt).Milliseconds()
			receipt.Status = models.ModelOperationFailed
			if errors.Is(pollCtx.Err(), context.DeadlineExceeded) {
				receipt.Disposition = "timeout"
				if lastErr != nil {
					receipt.Error = fmt.Sprintf("timed out after %s waiting for instance %s: %v", timeout, instance.ID, lastErr)
				} else {
					receipt.Error = fmt.Sprintf("timed out after %s waiting for instance %s", timeout, instance.ID)
				}
				return receipt, fmt.Errorf("%s", receipt.Error)
			}
			receipt.Disposition = "cancelled"
			receipt.Error = pollCtx.Err().Error()
			return receipt, pollCtx.Err()

		case <-ticker.C:
			if err := probe(pollCtx); err == nil {
				receipt.Status = models.ModelOperationCompleted
				receipt.Disposition = "ready"
				receipt.CompletedAt = time.Now().UTC()
				receipt.DurationMS = receipt.CompletedAt.Sub(startedAt).Milliseconds()
				return receipt, nil
			} else {
				lastErr = err
			}
		}
	}
}

// ProbeEndpoint performs a readiness probe against an HTTP base URL.
// It tries /health first (detecting loading status), falling back to /v1/models.
func ProbeEndpoint(ctx context.Context, baseURL string) error {
	baseURL = strings.TrimRight(baseURL, "/")
	client := &http.Client{Timeout: 5 * time.Second}

	healthReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
	if err == nil {
		resp, doErr := client.Do(healthReq)
		if doErr == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			if resp.StatusCode == http.StatusServiceUnavailable {
				return fmt.Errorf("model loading (503 Service Unavailable)")
			}
		}
	}

	modelsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(modelsReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("probe %s returned status %d", baseURL+"/v1/models", resp.StatusCode)
}
