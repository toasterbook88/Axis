package llmrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/secrets"
)

const (
	defaultProbeTimeout   = 3 * time.Second
	maxProbeResponseBytes = 1 << 20
)

// BackendProbe is the result of probing a configured AI backend.
type BackendProbe struct {
	Backend string        `json:"backend" yaml:"backend"`
	Kind    string        `json:"kind" yaml:"kind"`
	BaseURL string        `json:"base_url" yaml:"base_url"`
	OK      bool          `json:"ok" yaml:"ok"`
	Latency time.Duration `json:"latency" yaml:"latency"`
	Message string        `json:"message,omitempty" yaml:"message,omitempty"`
	Models  []string      `json:"models,omitempty" yaml:"models,omitempty"`
	Node    string        `json:"node,omitempty" yaml:"node,omitempty"`
	Enabled bool          `json:"enabled" yaml:"enabled"`
}

// ProbeBackend checks reachability and lists models when possible.
// It never panics; failures set OK=false with Message.
func ProbeBackend(ctx context.Context, b config.AIBackendConfig, client *http.Client) BackendProbe {
	out := BackendProbe{
		Backend: b.Name,
		Kind:    b.Kind,
		BaseURL: b.BaseURL,
		Node:    b.Node,
		Enabled: b.IsEnabled(),
	}
	if !b.IsEnabled() {
		out.Message = "disabled"
		return out
	}
	if client == nil {
		client = &http.Client{Timeout: defaultProbeTimeout}
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), defaultProbeTimeout)
		defer cancel()
	}

	start := time.Now()
	url, err := probeURL(b)
	if err != nil {
		out.Message = err.Error()
		out.Latency = time.Since(start)
		return out
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		out.Message = err.Error()
		out.Latency = time.Since(start)
		return out
	}
	if key := resolveBackendAPIKey(b); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := client.Do(req)
	out.Latency = time.Since(start)
	if err != nil {
		out.Message = err.Error()
		return out
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeResponseBytes))
	if err != nil {
		out.Message = err.Error()
		return out
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return out
	}

	models, parseErr := parseModelList(b.Kind, body)
	if parseErr != nil {
		// Reachable but body unexpected — still mark OK for openai hubs that vary.
		out.OK = true
		out.Message = "reachable; model list parse incomplete: " + parseErr.Error()
		return out
	}
	out.OK = true
	out.Models = models
	out.Message = fmt.Sprintf("%d model(s)", len(models))
	return out
}

// ProbeAllBackends probes each backend sequentially (stable order as configured).
func ProbeAllBackends(ctx context.Context, cfg *config.AIConfig, client *http.Client) []BackendProbe {
	if cfg == nil {
		return nil
	}
	out := make([]BackendProbe, 0, len(cfg.Backends))
	for _, b := range cfg.Backends {
		out = append(out, ProbeBackend(ctx, b, client))
	}
	return out
}

func resolveBackendAPIKey(b config.AIBackendConfig) string {
	key, err := secrets.ResolveOrEmpty(b.APIKeyEnv, b.APIKeyFile)
	if err != nil {
		return ""
	}
	return key
}

func probeURL(b config.AIBackendConfig) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(b.BaseURL), "/")
	if base == "" {
		return "", fmt.Errorf("empty base_url")
	}
	switch b.Kind {
	case config.AIBackendOllama:
		// Ollama native tags API (not under /v1). TrimSuffix is already a
		// no-op when the suffix is absent.
		base = strings.TrimSuffix(base, "/v1")
		return base + "/api/tags", nil
	case config.AIBackendOpenAICompatible, config.AIBackendLlamaCPP, config.AIBackendMLX:
		if strings.HasSuffix(base, "/v1") {
			return base + "/models", nil
		}
		return base + "/v1/models", nil
	default:
		return "", fmt.Errorf("unsupported kind %q", b.Kind)
	}
}

func parseModelList(kind string, body []byte) ([]string, error) {
	switch kind {
	case config.AIBackendOllama:
		var payload struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(payload.Models))
		for _, m := range payload.Models {
			if m.Name != "" {
				out = append(out, m.Name)
			}
		}
		return out, nil
	default:
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(payload.Data))
		for _, m := range payload.Data {
			if m.ID != "" {
				out = append(out, m.ID)
			}
		}
		return out, nil
	}
}

// ModelListed reports whether model appears in probe models (exact or prefix match for :latest tags).
func ModelListed(models []string, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" || len(models) == 0 {
		return false
	}
	for _, m := range models {
		if m == model {
			return true
		}
		// Accept tag-less equality: "coder" matches "coder:latest".
		if strings.Split(m, ":")[0] == strings.Split(model, ":")[0] {
			return true
		}
	}
	return false
}
