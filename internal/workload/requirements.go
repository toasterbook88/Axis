package workload

import (
	"context"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// InferRequirements derives TaskRequirements from a task description string.
// It uses structured workload profiles to determine hardware and tool needs.
//
// An optional InferRequirementsOptions may be provided to inject a semantic
// Classifier. When a Classifier is present it determines the primary
// WorkloadClass; the legacy string-matcher is used only when the Classifier
// is nil or returns an error. All existing call-sites that pass no options
// continue to use the legacy path unchanged.
func InferRequirements(desc string, opts ...InferRequirementsOptions) models.TaskRequirements {
	reqs := models.TaskRequirements{
		Description: desc,
	}

	match := resolveWorkloadMatch(desc, opts)
	Apply(match, &reqs)

	// Context window inference (additive to profile)
	reqs.ContextWindowTokens = InferContextWindowTokens(desc)
	if reqs.ContextWindowTokens > 0 {
		reqs.PrefersTurboQuant = true
		if minForContext := LongContextMinRAM(reqs.ContextWindowTokens); minForContext > reqs.MinFreeRAMMB {
			reqs.MinFreeRAMMB = minForContext
		}
	}

	// Backend inference (preferences based on description keywords)
	InferBackends(desc, &reqs)

	return reqs
}

// Apply populates TaskRequirements based on the matched workload class.
// It applies class defaults and a small set of explicit runtime/tool modifiers.
func Apply(match models.WorkloadProfileMatch, reqs *models.TaskRequirements) {
	reqs.Workload = match
	signals := analyzeDescription(reqs.Description)
	view := newDescriptionView(reqs.Description)

	if profile, ok := profileForClass(match.Class); ok {
		applyProfile(reqs, profile)
	}

	switch match.Class {
	case models.ClassAppleIntelligence:
		reqs.MinFreeRAMMB = 0
	case models.ClassLocalLLMInference, models.ClassLongContextInference:
		if floor := inferLocalLLMMinRAMMB(view); floor > reqs.MinFreeRAMMB {
			reqs.MinFreeRAMMB = floor
		}
		if shouldRequireOllama(view, match.Class) {
			addTools(reqs, "ollama")
		}
	}

	// Preserve strong explicit secondary requirements without reintroducing
	// broad substring aggregation.
	if signals.repo && match.Class != models.ClassRepoAnalysis {
		addTools(reqs, "git")
	}
	if signals.goBuild && match.Class != models.ClassGoBuild {
		addTools(reqs, "go", "git")
	}
	if signals.dockerBuild && match.Class != models.ClassDockerBuild {
		addTools(reqs, "docker")
	}
	if signals.explicitOllama && !contains(reqs.RequiredTools, "ollama") && !hasNonOllamaExplicitBackend(view) {
		addTools(reqs, "ollama")
	}
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

// InferBackends adds backend preferences based on keywords in the description.
func InferBackends(desc string, reqs *models.TaskRequirements) {
	d := newDescriptionView(desc)
	if d.hasAny("mlx", "mlx lm", "apple silicon", "mac studio", "macbook pro", "mac mini") {
		if !contains(reqs.PreferredBackends, "mlx") {
			reqs.PreferredBackends = append(reqs.PreferredBackends, "mlx")
		}
	}
	if d.hasAny("apple intelligence", "apple foundation models", "language model session") {
		if !contains(reqs.PreferredBackends, "apple-foundation-models") {
			reqs.PreferredBackends = append(reqs.PreferredBackends, "apple-foundation-models")
		}
	}
	if d.hasAny("llama cpp", "llama cli", "llama server") {
		if !contains(reqs.PreferredBackends, "llama.cpp") {
			reqs.PreferredBackends = append(reqs.PreferredBackends, "llama.cpp")
		}
	}
}

// InferContextWindowTokens derives a heuristic token count from description keywords.
func InferContextWindowTokens(desc string) int {
	d := newDescriptionView(desc)
	switch {
	case d.hasAny("million token", "million tokens", "1m context", "1m tokens"):
		return 1000000
	case d.has("512k"):
		return 512000
	case d.hasAny("256k", "200k"):
		return 256000
	case d.hasAny("128k", "long context", "book length", "needle in a haystack"):
		return 128000
	default:
		return 0
	}
}

// LongContextMinRAM returns the minimum RAM floor for a given token count.
func LongContextMinRAM(tokens int) int64 {
	switch {
	case tokens >= 1000000:
		return 12288
	case tokens >= 512000:
		return 8192
	case tokens >= 256000:
		return 6144
	case tokens >= 128000:
		return 6144 // Unified with profile floor
	default:
		return 0
	}
}

func applyProfile(reqs *models.TaskRequirements, profile Profile) {
	if profile.MinFreeRAMMB > reqs.MinFreeRAMMB {
		reqs.MinFreeRAMMB = profile.MinFreeRAMMB
	}
	if profile.PrefersTurboQuant {
		reqs.PrefersTurboQuant = true
	}
	addTools(reqs, profile.RequiredTools...)
	addBackends(reqs, profile.PreferredBackends...)
}

func addTools(reqs *models.TaskRequirements, tools ...string) {
	for _, tool := range tools {
		if !contains(reqs.RequiredTools, tool) {
			reqs.RequiredTools = append(reqs.RequiredTools, tool)
		}
	}
}

func addBackends(reqs *models.TaskRequirements, backends ...string) {
	for _, backend := range backends {
		if !contains(reqs.PreferredBackends, backend) {
			reqs.PreferredBackends = append(reqs.PreferredBackends, backend)
		}
	}
}

func inferLocalLLMMinRAMMB(d descriptionView) int64 {
	switch {
	case d.has("70b"):
		return 12288
	case d.hasAny("13b", "14b", "32b", "34b", "heavy"):
		return 8192
	case d.hasAny("7b", "8b"):
		return 4096
	case d.hasAny("inference", "llm", "ollama"):
		return 6144
	default:
		return 0
	}
}

func shouldRequireOllama(d descriptionView, class models.WorkloadClass) bool {
	if class != models.ClassLocalLLMInference && class != models.ClassLongContextInference {
		return false
	}
	if d.has("ollama") {
		return true
	}
	if hasNonOllamaExplicitBackend(d) {
		return false
	}
	switch {
	case d.has("llm"):
		return true
	case d.hasAny("13b", "14b", "32b", "34b", "70b"):
		return true
	case d.has("inference"):
		return true
	default:
		return false
	}
}

func hasNonOllamaExplicitBackend(d descriptionView) bool {
	return d.hasAny(
		"mlx",
		"llama cpp",
		"llama server",
		"llama cli",
		"apple intelligence",
		"apple foundation models",
	)
}

// resolveWorkloadMatch selects the primary WorkloadClass for a description.
//
// When opts contains a non-nil Classifier it delegates to that classifier and
// uses the result if no error is returned. On error it falls through to the
// legacy string-matcher so the caller is never left without a classification.
//
// The legacy path (matchFromSignals + analyzeDescription) is always used when
// no opts are provided — preserving the behaviour of all existing call-sites.
func resolveWorkloadMatch(desc string, opts []InferRequirementsOptions) models.WorkloadProfileMatch {
	if len(opts) > 0 && opts[0].Classifier != nil {
		match, err := opts[0].Classifier.ClassifyWorkload(
			context.Background(), desc, opts[0].ExtraContext,
		)
		if err == nil {
			return match
		}
		// Classifier failed — fall through to legacy.
	}
	return matchFromSignals(analyzeDescription(desc))
}
