package main

import (
	"context"
	"fmt"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
)

var doctorProbeAIBackends = func(ctx context.Context, nodes *config.Config) ([]llmrouter.BackendProbe, error) {
	aiCfg, err := config.LoadAIOrEmpty(config.DefaultAIConfigPath())
	if err != nil {
		return nil, err
	}
	if aiCfg == nil || len(aiCfg.Backends) == 0 {
		return nil, nil
	}
	return llmrouter.ProbeAllBackendsForNodes(ctx, aiCfg, nil, nodes), nil
}

// doctorAIConfigChecks probes configured ~/.axis/ai.yaml HTTP backends within
// budget and maps each result to a DoctorCheck. Skipped probes (overall budget
// exhausted before that backend started) warn without a Fix, so operators are
// not told to repair a service that was never contacted.
func doctorAIConfigChecks(ctx context.Context, nodes *config.Config) []DoctorCheck {
	aiCtx, aiCancel := context.WithTimeout(ctx, 5*time.Second)
	aiProbes, aiErr := doctorProbeAIBackends(aiCtx, nodes)
	aiCancel()
	if aiErr != nil {
		return []DoctorCheck{{
			Name:    "AI Config",
			Status:  "warn",
			Message: fmt.Sprintf("Failed to load ai.yaml: %v", aiErr),
		}}
	}
	checks := make([]DoctorCheck, 0, len(aiProbes))
	for _, p := range aiProbes {
		targetURL := p.ProbedURL
		if targetURL == "" {
			targetURL = p.BaseURL
		}
		switch {
		case !p.Enabled:
			checks = append(checks, DoctorCheck{
				Name:    fmt.Sprintf("AI Config: %s", p.Backend),
				Status:  "pass",
				Message: "disabled",
			})
		case p.Skipped:
			checks = append(checks, DoctorCheck{
				Name:    fmt.Sprintf("AI Config: %s", p.Backend),
				Status:  "warn",
				Message: p.Message,
			})
		case p.OK:
			checks = append(checks, DoctorCheck{
				Name:    fmt.Sprintf("AI Config: %s", p.Backend),
				Status:  "pass",
				Message: fmt.Sprintf("%s (%s)", targetURL, p.Message),
			})
		default:
			checks = append(checks, DoctorCheck{
				Name:    fmt.Sprintf("AI Config: %s", p.Backend),
				Status:  "warn",
				Message: fmt.Sprintf("%s (%s)", targetURL, p.Message),
				Fix:     "verify backend service, DNS/Tailscale reachability, and ~/.axis/ai.yaml",
			})
		}
	}
	return checks
}
