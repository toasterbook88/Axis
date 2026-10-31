package llmrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

// upstreamTimeout bounds each request this file makes (the hub's /model/info
// and each upstream's model list), so a dead upstream costs at most this long.
const upstreamTimeout = 2 * time.Second

// UpstreamCheck is one check of a server behind a hub's listed model.
type UpstreamCheck struct {
	Backend string `json:"backend" yaml:"backend"`
	APIBase string `json:"api_base" yaml:"api_base"`
	OK      bool   `json:"ok" yaml:"ok"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// checkModelUpstreams asks a hub which servers serve model, then asks each
// one for its model list. A hub that lists a model only proves the hub is up;
// the server behind the name can be gone.
//
// It needs a LiteLLM-style GET /model/info (model_name and
// litellm_params.api_base per entry). It runs only when the hub is on this
// machine, because those addresses are relative to the hub's machine. No
// prompt is sent: each upstream gets a model-list GET only, at
// <api_base>/models when api_base ends in /v1, else <api_base>/v1/models and,
// on a 404, <api_base>/models.
//
// checked is false when no verdict exists; note then says why. When checked
// is true, the model is served if any returned check is OK.
func checkModelUpstreams(ctx context.Context, b config.AIBackendConfig, client *http.Client, nodes *config.Config, model string) (checks []UpstreamCheck, checked bool, note string) {
	if b.Kind != config.AIBackendOpenAICompatible {
		return nil, false, ""
	}
	if !hubOnThisMachine(b, nodes) {
		return nil, false, "upstream not checked: hub is on another node"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = &http.Client{Timeout: upstreamTimeout}
	}

	base := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(b.BaseURL), "/"), "/v1")
	body, err := upstreamGet(ctx, client, base+"/model/info", resolveBackendAPIKey(b))
	if err != nil {
		return nil, false, "upstream not checked: hub /model/info unavailable (" + err.Error() + ")"
	}
	apiBases, unset, err := modelAPIBases(body, model)
	if err != nil {
		return nil, false, "upstream not checked: hub /model/info unreadable (" + err.Error() + ")"
	}
	if len(apiBases) == 0 {
		return nil, false, fmt.Sprintf("upstream not checked: hub /model/info has no api_base for %q", model)
	}

	for _, apiBase := range apiBases {
		c := UpstreamCheck{Backend: b.Name, APIBase: apiBase}
		listBase := strings.TrimRight(apiBase, "/")
		// The hub's own key is not sent: it belongs to the hub, not to the upstream.
		var err error
		if strings.HasSuffix(listBase, "/v1") {
			_, err = upstreamGet(ctx, client, listBase+"/models", "")
		} else {
			// A server root (Ollama's form) lists at /v1/models; a base that
			// already carries another API prefix lists at /models.
			_, err = upstreamGet(ctx, client, listBase+"/v1/models", "")
			var se statusError
			if errors.As(err, &se) && se.code == http.StatusNotFound {
				_, err = upstreamGet(ctx, client, listBase+"/models", "")
			}
		}
		if err != nil && !isAuthRefusal(err) {
			c.Message = err.Error()
		} else {
			c.OK = true
			c.Message = "answered"
		}
		checks = append(checks, c)
	}
	if unset > 0 && !anyUpstreamOK(checks) {
		return checks, false, fmt.Sprintf("upstream not checked: %d deployment(s) of %q use the provider's default endpoint", unset, model)
	}
	return checks, true, ""
}

// hubOnThisMachine reports whether b's hub runs here. A bound backend uses its
// nodes.yaml binding; an unbound one needs a BaseURL host that names this
// machine, since a private address can be another machine on the LAN.
func hubOnThisMachine(b config.AIBackendConfig, nodes *config.Config) bool {
	if strings.TrimSpace(b.Node) != "" {
		return BackendIsLocal(b, nodes)
	}
	u, err := url.Parse(strings.TrimSpace(b.BaseURL))
	if err != nil || u.Hostname() == "" {
		return false
	}
	return models.IsLocalConfig("", u.Hostname(), "")
}

// statusError is a non-2xx HTTP answer.
type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// isAuthRefusal reports an upstream that answered but wants credentials:
// the server is up, so it counts as serving.
func isAuthRefusal(err error) bool {
	se, ok := err.(statusError)
	return ok && (se.code == http.StatusUnauthorized || se.code == http.StatusForbidden)
}

func upstreamGet(ctx context.Context, client *http.Client, url, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError{resp.StatusCode}
	}
	return body, nil
}

// modelAPIBases returns the distinct api_base values for model in a
// /model/info body, in order of first appearance, and how many of model's
// entries have no api_base.
func modelAPIBases(body []byte, model string) (bases []string, unset int, err error) {
	var payload struct {
		Data []struct {
			ModelName     string `json:"model_name"`
			LiteLLMParams struct {
				APIBase string `json:"api_base"`
			} `json:"litellm_params"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, err
	}
	seen := map[string]bool{}
	for _, d := range payload.Data {
		if !ModelListed([]string{d.ModelName}, model) {
			continue
		}
		apiBase := strings.TrimSpace(d.LiteLLMParams.APIBase)
		if apiBase == "" {
			unset++
			continue
		}
		if seen[apiBase] {
			continue
		}
		seen[apiBase] = true
		bases = append(bases, apiBase)
	}
	return bases, unset, nil
}

func anyUpstreamOK(checks []UpstreamCheck) bool {
	for _, c := range checks {
		if c.OK {
			return true
		}
	}
	return false
}
