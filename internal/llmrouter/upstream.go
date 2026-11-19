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
	// Model is the name the hub forwards to this server, when it can be
	// derived from the deployment (see forwardedModel).
	Model string `json:"model,omitempty" yaml:"model,omitempty"`
	// Unlisted is true when the server answered with a model list that does
	// not name Model: it is up, but does not serve the model.
	Unlisted bool `json:"unlisted,omitempty" yaml:"unlisted,omitempty"`
}

// checkModelUpstreams asks a hub which servers serve model, then asks each
// one for its model list. A hub that lists a model only proves the hub is up;
// the server behind the name can be gone, or can be up without the model.
//
// It needs a LiteLLM-style GET /model/info (model_name and
// litellm_params.api_base per entry). It runs only when the hub is on this
// machine, because those addresses are relative to the hub's machine. No
// prompt is sent: each upstream gets a model-list GET only, at
// <api_base>/models when api_base ends in /v1, else <api_base>/v1/models and,
// on a 404, <api_base>/models.
//
// Each distinct api_base is fetched once. A server whose list does not name
// the model the hub forwards to it is up but does not serve it: that check is
// Unlisted (see upstreamOmits for the lists that cannot say).
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
	deployments, unset, err := modelDeployments(body, model)
	if err != nil {
		return nil, false, "upstream not checked: hub /model/info unreadable (" + err.Error() + ")"
	}
	if len(deployments) == 0 {
		return nil, false, fmt.Sprintf("upstream not checked: hub /model/info has no api_base for %q", model)
	}

	type listing struct {
		body []byte
		err  error
	}
	fetched := map[string]listing{}
	for _, d := range deployments {
		l, ok := fetched[d.apiBase]
		if !ok {
			l.body, l.err = upstreamModelList(ctx, client, d.apiBase)
			fetched[d.apiBase] = l
		}
		c := UpstreamCheck{Backend: b.Name, APIBase: d.apiBase, Model: d.model}
		switch {
		case l.err != nil && !isAuthRefusal(l.err):
			c.Message = l.err.Error()
		case l.err == nil && d.model != "" && upstreamOmits(l.body, d.model):
			c.Unlisted = true
			c.Message = fmt.Sprintf("answered, but does not list %q", d.model)
		default:
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

// upstreamModelList fetches a server's model list. The hub's own key is not
// sent: it belongs to the hub, not to the upstream.
func upstreamModelList(ctx context.Context, client *http.Client, apiBase string) ([]byte, error) {
	listBase := strings.TrimRight(apiBase, "/")
	if strings.HasSuffix(listBase, "/v1") {
		return upstreamGet(ctx, client, listBase+"/models", "")
	}
	// A server root (Ollama's form) lists at /v1/models; a base that already
	// carries another API prefix lists at /models.
	body, err := upstreamGet(ctx, client, listBase+"/v1/models", "")
	var se statusError
	if errors.As(err, &se) && se.code == http.StatusNotFound {
		return upstreamGet(ctx, client, listBase+"/models", "")
	}
	return body, err
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
	return models.IsLocalHost(u.Hostname())
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

// upstreamDeployment is one server behind a hub's model name and the name the
// hub forwards to it ("" when that cannot be derived).
type upstreamDeployment struct {
	apiBase string
	model   string
}

// modelDeployments returns the distinct (api_base, forwarded model) pairs for
// model in a /model/info body, in order of first appearance, and how many of
// model's entries have no api_base. LiteLLM routes on the exact model_name, so
// exact entries are used when any exist; the tag-insensitive ModelListed match
// is only a fallback for a hub whose names differ from the request by a tag.
func modelDeployments(body []byte, model string) (deployments []upstreamDeployment, unset int, err error) {
	var payload struct {
		Data []struct {
			ModelName     string `json:"model_name"`
			LiteLLMParams struct {
				APIBase           string `json:"api_base"`
				Model             string `json:"model"`
				CustomLLMProvider string `json:"custom_llm_provider"`
			} `json:"litellm_params"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, err
	}
	exact := false
	for _, d := range payload.Data {
		if d.ModelName == model {
			exact = true
			break
		}
	}
	seen := map[upstreamDeployment]bool{}
	for _, d := range payload.Data {
		if exact && d.ModelName != model {
			continue
		}
		if !exact && !ModelListed([]string{d.ModelName}, model) {
			continue
		}
		apiBase := strings.TrimSpace(d.LiteLLMParams.APIBase)
		if apiBase == "" {
			unset++
			continue
		}
		dep := upstreamDeployment{apiBase: apiBase, model: forwardedModel(d.LiteLLMParams.Model, d.LiteLLMParams.CustomLLMProvider)}
		if seen[dep] {
			continue
		}
		seen[dep] = true
		deployments = append(deployments, dep)
	}
	return deployments, unset, nil
}

// upstreamProviders are the LiteLLM providers that send a request to the
// server named by api_base under the deployment's model name.
var upstreamProviders = map[string]bool{
	"openai": true, "ollama": true, "ollama_chat": true,
	"hosted_vllm": true, "lm_studio": true, "llamafile": true,
}

// forwardedModel is the model name LiteLLM sends to the server, or "" when
// the provider is not one of upstreamProviders. A custom_llm_provider decides
// when set, and LiteLLM prepends it to a model that does not already start
// with it; otherwise the model's first path segment names the provider.
func forwardedModel(model, customProvider string) string {
	model = strings.TrimSpace(model)
	if cp := strings.TrimSpace(customProvider); cp != "" {
		if !upstreamProviders[cp] {
			return ""
		}
		return strings.TrimPrefix(model, cp+"/")
	}
	if i := strings.Index(model, "/"); i > 0 && upstreamProviders[model[:i]] {
		return model[i+1:]
	}
	return ""
}

// upstreamOmits reports whether a server's model list shows that it does not
// serve model. It is false whenever the list cannot say: the body does not
// parse or lists nothing, or the server answers names it does not list.
// llama-server marks its entries owned_by "llamacpp" and ignores the request's
// name in single-model mode; TGI sets owned_by to its model ID and serves the
// name "tgi".
func upstreamOmits(body []byte, model string) bool {
	if strings.EqualFold(model, "tgi") {
		return false
	}
	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	listed := false
	for _, d := range payload.Data {
		if d.ID == "" {
			continue
		}
		if d.OwnedBy == "llamacpp" || d.OwnedBy == d.ID || sameServedName(d.ID, model) {
			return false
		}
		listed = true
	}
	return listed
}

// sameServedName matches a listed ID to a forwarded name ignoring case, with
// an untagged name equal to its ":latest" form, as Ollama resolves it.
func sameServedName(id, model string) bool {
	return strings.EqualFold(id, model) || strings.EqualFold(id, model+":latest") || strings.EqualFold(id+":latest", model)
}

// firstUnlisted returns the first check whose server answered without the model.
func firstUnlisted(checks []UpstreamCheck) (UpstreamCheck, bool) {
	for _, c := range checks {
		if c.Unlisted {
			return c, true
		}
	}
	return UpstreamCheck{}, false
}

func anyUpstreamOK(checks []UpstreamCheck) bool {
	for _, c := range checks {
		if c.OK {
			return true
		}
	}
	return false
}
