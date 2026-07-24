package llmrouter

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/toasterbook88/axis/internal/config"
)

// RoleRouteDecision is the advisory output of role resolution.
// It is dry-run: AXIS does not send the user prompt here.
type RoleRouteDecision struct {
	// Role is the requested role name, or empty when resolving a bare model id.
	Role string `json:"role,omitempty" yaml:"role,omitempty"`

	// Backend is the selected backend name.
	Backend string `json:"backend" yaml:"backend"`

	// Model is the model id to request from the backend.
	Model string `json:"model" yaml:"model"`

	// Endpoint is the backend base URL.
	Endpoint string `json:"endpoint" yaml:"endpoint"`

	// Kind is the backend kind (ollama, openai-compatible, ...).
	Kind string `json:"kind" yaml:"kind"`

	// Node is an optional nodes.yaml binding from the backend config.
	Node string `json:"node,omitempty" yaml:"node,omitempty"`

	// Healthy is true when the chosen backend probe succeeded.
	Healthy bool `json:"healthy" yaml:"healthy"`

	// ModelPresent is true when the probe listed the model (when list available).
	ModelPresent bool `json:"model_present" yaml:"model_present"`

	// Confidence is [0,1] based on health + model listing.
	Confidence float64 `json:"confidence" yaml:"confidence"`

	// Reasoning lists evaluation steps in order.
	Reasoning []string `json:"reasoning" yaml:"reasoning"`

	// Fallbacks lists remaining prefer backends after the selection.
	Fallbacks []string `json:"fallbacks,omitempty" yaml:"fallbacks,omitempty"`

	// Probes holds per-backend probe snapshots used for this decision.
	Probes []BackendProbe `json:"probes,omitempty" yaml:"probes,omitempty"`
}

// ResolveRoleOptions controls route resolution.
type ResolveRoleOptions struct {
	// Role is a configured role name. Mutually exclusive with Model-only path when set.
	Role string

	// Model overrides the role's model when non-empty.
	Model string

	// SkipProbe skips network health checks (config-only preference order).
	SkipProbe bool

	// HTTPClient is optional; used for probes.
	HTTPClient *http.Client
}

// ResolveRole picks a backend for a role using prefer order and optional probes.
// When role is empty and Model is set, every enabled backend is considered.
func ResolveRole(ctx context.Context, cfg *config.AIConfig, opts ResolveRoleOptions) (RoleRouteDecision, error) {
	if cfg == nil {
		return RoleRouteDecision{}, fmt.Errorf("AI config is nil")
	}

	roleName := strings.TrimSpace(opts.Role)
	modelOverride := strings.TrimSpace(opts.Model)

	var prefer []string
	model := modelOverride
	reasoning := []string{}

	if roleName != "" {
		role, ok := cfg.Roles[roleName]
		if !ok {
			// case-insensitive role lookup
			for k, v := range cfg.Roles {
				if strings.EqualFold(k, roleName) {
					role = v
					roleName = k
					ok = true
					break
				}
			}
		}
		if !ok {
			return RoleRouteDecision{}, fmt.Errorf("unknown role %q (configure roles in AI config)", roleName)
		}
		prefer = append([]string(nil), role.Prefer...)
		if model == "" {
			model = role.Model
		}
		reasoning = append(reasoning, fmt.Sprintf("role %q prefers %v model %q", roleName, prefer, model))
		if role.RequireArch != "" {
			reasoning = append(reasoning, fmt.Sprintf("role require_arch=%s (hint only; not enforced in this release)", role.RequireArch))
		}
	} else if model != "" {
		// Bare model: try all enabled backends in config order.
		for _, b := range cfg.Backends {
			if b.IsEnabled() {
				prefer = append(prefer, b.Name)
			}
		}
		reasoning = append(reasoning, fmt.Sprintf("no role; scanning enabled backends for model %q", model))
	} else {
		return RoleRouteDecision{}, fmt.Errorf("role or model is required")
	}

	if len(prefer) == 0 {
		return RoleRouteDecision{}, fmt.Errorf("no backends to evaluate")
	}

	var probes []BackendProbe
	probeByName := map[string]BackendProbe{}
	if !opts.SkipProbe {
		for _, name := range prefer {
			b, ok := cfg.FindBackend(name)
			if !ok {
				reasoning = append(reasoning, fmt.Sprintf("skip %q: not in config", name))
				continue
			}
			p := ProbeBackend(ctx, b, opts.HTTPClient)
			probes = append(probes, p)
			probeByName[strings.ToLower(name)] = p
		}
	} else {
		reasoning = append(reasoning, "skip_probe: selecting first enabled preferred backend without health checks")
	}

	var fallbacks []string
	for i, name := range prefer {
		b, ok := cfg.FindBackend(name)
		if !ok {
			continue
		}
		if !b.IsEnabled() {
			reasoning = append(reasoning, fmt.Sprintf("skip %q: disabled", name))
			continue
		}

		if opts.SkipProbe {
			rest := remaining(prefer, i+1)
			return RoleRouteDecision{
				Role:       roleName,
				Backend:    b.Name,
				Model:      model,
				Endpoint:   b.BaseURL,
				Kind:       b.Kind,
				Node:       b.Node,
				Healthy:    false,
				Confidence: 0.4,
				Reasoning:  append(reasoning, fmt.Sprintf("selected %q without probe", b.Name)),
				Fallbacks:  rest,
			}, nil
		}

		p, ok := probeByName[strings.ToLower(name)]
		if !ok {
			continue
		}
		if !p.OK {
			reasoning = append(reasoning, fmt.Sprintf("skip %q: unhealthy (%s)", name, p.Message))
			continue
		}

		modelPresent := ModelListed(p.Models, model)
		conf := 0.75
		if modelPresent {
			conf = 0.95
			reasoning = append(reasoning, fmt.Sprintf("backend %q healthy; model %q listed", name, model))
		} else if len(p.Models) == 0 {
			conf = 0.7
			reasoning = append(reasoning, fmt.Sprintf("backend %q healthy; model list empty or unparsed — accepting", name))
		} else {
			conf = 0.55
			reasoning = append(reasoning, fmt.Sprintf("backend %q healthy; model %q not in list — still selecting (hub may lazy-load)", name, model))
		}

		rest := remaining(prefer, i+1)
		// Only list remaining that were preferred after this one.
		for _, f := range rest {
			fallbacks = append(fallbacks, f)
		}

		return RoleRouteDecision{
			Role:         roleName,
			Backend:      b.Name,
			Model:        model,
			Endpoint:     b.BaseURL,
			Kind:         b.Kind,
			Node:         b.Node,
			Healthy:      true,
			ModelPresent: modelPresent,
			Confidence:   conf,
			Reasoning:    reasoning,
			Fallbacks:    fallbacks,
			Probes:       probes,
		}, nil
	}

	// Nothing healthy: return first enabled prefer as degraded advisory.
	for _, name := range prefer {
		b, ok := cfg.FindBackend(name)
		if !ok || !b.IsEnabled() {
			continue
		}
		reasoning = append(reasoning, fmt.Sprintf("degraded: no healthy backend; advising first enabled prefer %q", b.Name))
		return RoleRouteDecision{
			Role:       roleName,
			Backend:    b.Name,
			Model:      model,
			Endpoint:   b.BaseURL,
			Kind:       b.Kind,
			Node:       b.Node,
			Healthy:    false,
			Confidence: 0.2,
			Reasoning:  reasoning,
			Fallbacks:  remainingNames(prefer, b.Name),
			Probes:     probes,
		}, nil
	}

	return RoleRouteDecision{}, fmt.Errorf("no usable backend for role %q model %q", roleName, model)
}

func remaining(prefer []string, from int) []string {
	if from >= len(prefer) {
		return nil
	}
	return append([]string(nil), prefer[from:]...)
}

func remainingNames(prefer []string, selected string) []string {
	var out []string
	for _, p := range prefer {
		if !strings.EqualFold(p, selected) {
			out = append(out, p)
		}
	}
	return out
}
