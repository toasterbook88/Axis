package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

type modelReadiness int

const (
	// modelUnchecked was never asked: cloud and cloud-proxy targets (a check
	// would be billed) and results built without a check. It is the zero
	// value so nothing reads as ready unless a probe said so.
	modelUnchecked modelReadiness = iota
	// modelReady answered a one-token request.
	modelReady
	// modelCold did not answer in time; it may still be loading.
	modelCold
	// modelDown failed fast: an error status, a refused connection, no route.
	modelDown
)

// maxStartupFallbacks bounds how many dead implicit defaults are skipped.
const maxStartupFallbacks = 2

var (
	startupReadyTimeout = 8 * time.Second
	startupReadyFn      = checkModelReadiness
	startupRouteFn      = resolveModelRoute
)

// resolveReadyStartupModelTarget picks the startup model the way
// resolveStartupModelTarget does, then routes it and asks it for one token.
// An implicit default that fails fast is ruled out and the next candidate is
// tried, with a notice saying why. A model the operator pinned (--model, or
// an interactive pick) is kept and only warned about. Cloud targets and
// cloud-proxy models are not checked, since a check would be billed. The
// verdict on the returned target is reported so callers never call an
// unproven model ready. Cancelling parent stops the checks, and a tunnel a
// check opened after cancellation is closed rather than installed.
func resolveReadyStartupModelTarget(
	parent context.Context,
	requestedModel, providerFlag, cloudModelFlag string,
	explicit *ModelChoice,
	rt *runtimectx.Context,
	choices []ModelChoice,
	pinned bool,
	notes io.Writer,
) (ModelChoice, agent.CloudBackendOptions, modelReadiness, error) {
	if notes == nil {
		notes = io.Discard
	}
	if parent == nil {
		parent = context.Background()
	}
	choices = slices.Clone(choices)
	var skipped []string
	originalRequest := strings.TrimSpace(requestedModel)
	for attempt := 0; ; attempt++ {
		target, opts, err := resolveStartupModelTarget(requestedModel, providerFlag, cloudModelFlag, explicit, rt, choices)
		if err == nil && pinned && explicit == nil && requestedModel != "" && target.ProviderKind != "cloud" &&
			!strings.EqualFold(target.Model, requestedModel) {
			return ModelChoice{}, agent.CloudBackendOptions{}, modelUnchecked, pinnedModelUnavailable(requestedModel, choices)
		}
		if err != nil || target.Protocol == agent.ProtocolCloud || target.CloudProxy {
			return target, opts, modelUnchecked, err
		}
		if err := parent.Err(); err != nil {
			return target, opts, modelUnchecked, err
		}

		ctx, cancel := context.WithTimeout(parent, startupReadyTimeout+5*time.Second)
		verdict, reason := modelDown, ""
		routed, tunnel, routeErr := startupRouteFn(ctx, rt, target)
		if routeErr != nil {
			reason = routeErr.Error()
		} else {
			target = routed
			verdict, reason = startupReadyFn(ctx, target, opts)
		}
		cancel()
		if err := parent.Err(); err != nil {
			// Startup was cancelled while the check ran (the operator quit):
			// nothing will use this route, so its tunnel must not outlive us.
			if tunnel != nil {
				tunnel()
			}
			return target, opts, modelUnchecked, err
		}

		keep := verdict == modelReady || verdict == modelCold ||
			pinned || explicit != nil || attempt >= maxStartupFallbacks || !disableChoice(choices, target)
		if !keep {
			if tunnel != nil {
				tunnel() // this attempt is being skipped
			}
			fmt.Fprintf(notes, "Default model %q is not answering (%s); choosing another.\n", target.Model, reason)
			skipped = append(skipped, target.Model)
			requestedModel = "" // the dead default must not be re-picked by name
			continue
		}

		startupTunnelInstaller(parent)(tunnel)
		// A borrow of another node's Ollama is not a casual substitution.
		// Name the startup model that missed and the model that was borrowed.
		if foreignOllamaBorrow(target, originalRequest) {
			if originalRequest == "" {
				fmt.Fprintf(notes, "Warning: no startup model was a usable catalog choice; borrowed %q from another node's Ollama. No enabled hub role answered.\n", target.Model)
			} else {
				fmt.Fprintf(notes, "Warning: startup model %q is not a usable catalog choice; borrowed %q from another node's Ollama. No enabled hub role answered.\n", originalRequest, target.Model)
			}
			if verdict == modelCold {
				fmt.Fprintf(notes, "Note: model %q did not answer within %s; it may still be loading.\n", target.Model, startupReadyTimeout)
			} else if verdict != modelReady {
				fmt.Fprintf(notes, "Warning: model %q is not answering (%s).\n", target.Model, reason)
			}
			return target, opts, verdict, nil
		}
		switch verdict {
		case modelReady, modelCold:
			if len(skipped) > 0 {
				fmt.Fprintf(notes, "Using model %q instead.\n", target.Model)
			}
			if verdict == modelCold {
				fmt.Fprintf(notes, "Note: model %q did not answer within %s; it may still be loading.\n", target.Model, startupReadyTimeout)
			}
		default:
			fmt.Fprintf(notes, "Warning: model %q is not answering (%s).\n", target.Model, reason)
		}
		return target, opts, verdict, nil
	}
}

// startupTunnelKey carries a tunnel installer to the startup resolver.
type startupTunnelKey struct{}

// withStartupTunnelInstaller lets the console's background resolution decide
// whether a tunnel it found is still wanted (the operator may have switched
// models meanwhile). It travels in the context because the resolver is shared
// with the synchronous path, which keeps installModelTunnel.
func withStartupTunnelInstaller(ctx context.Context, install func(func())) context.Context {
	return context.WithValue(ctx, startupTunnelKey{}, install)
}

// startupTunnelInstaller returns the installer carried by ctx, or
// installModelTunnel.
func startupTunnelInstaller(ctx context.Context) func(func()) {
	if install, ok := ctx.Value(startupTunnelKey{}).(func(func())); ok && install != nil {
		return install
	}
	return installModelTunnel
}

// disableChoice marks the catalog entry for target as unusable. It reports
// false when target is not in the catalog, so a fallback cannot loop.
func disableChoice(choices []ModelChoice, target ModelChoice) bool {
	for i := range choices {
		if choices[i].Disabled {
			continue
		}
		if (target.ID != "" && choices[i].ID == target.ID) || (target.ID == "" && choices[i].Model == target.Model) {
			choices[i].Disabled, choices[i].DisabledReason = true, "not answering"
			return true
		}
	}
	return false
}

// checkModelReadiness asks the model for a single token.
func checkModelReadiness(parent context.Context, c ModelChoice, opts agent.CloudBackendOptions) (modelReadiness, string) {
	base := strings.TrimRight(c.Endpoint, "/")
	messages := []map[string]string{{"role": "user", "content": "hi"}}
	var url string
	var body any
	if c.Protocol == agent.ProtocolOllama {
		url = base + "/api/chat"
		body = map[string]any{"model": c.Model, "messages": messages, "stream": false, "options": map[string]int{"num_predict": 1}}
	} else {
		url = base + "/v1/chat/completions"
		if strings.HasSuffix(base, "/v1") {
			url = base + "/chat/completions"
		}
		body = map[string]any{"model": c.Model, "messages": messages, "max_tokens": 1}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return modelDown, err.Error()
	}

	ctx, cancel := context.WithTimeout(parent, startupReadyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return modelDown, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+opts.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return modelCold, "timeout"
		}
		return modelDown, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return modelReady, ""
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 160)) // best effort: the status alone is the verdict
	return modelDown, strings.TrimSpace(fmt.Sprintf("HTTP %d %s", resp.StatusCode, bytes.TrimSpace(snippet)))
}

// pinnedModelUnavailable explains why a --model name cannot start: the reason
// its catalog entry is disabled, or that nothing offers it.
func pinnedModelUnavailable(name string, choices []ModelChoice) error {
	for _, c := range choices {
		if c.Disabled && strings.EqualFold(c.Model, name) {
			return fmt.Errorf("model %q is not usable: %s; /models lists the choices", name, c.DisabledReason)
		}
	}
	return fmt.Errorf("model %q is not in the model catalog or on local Ollama; /models lists the choices", name)
}
