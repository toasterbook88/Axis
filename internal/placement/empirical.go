package placement

import (
	"fmt"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func observationBackend(reqs models.TaskRequirements, tool string) string {
	if len(reqs.PreferredBackends) > 0 {
		return strings.TrimSpace(reqs.PreferredBackends[0])
	}
	switch {
	case strings.EqualFold(tool, "apple-foundation-models"):
		return "apple-foundation-models"
	case strings.EqualFold(tool, "ollama"):
		return "ollama"
	case strings.EqualFold(tool, "llama-server"):
		return "llama.cpp"
	default:
		return ""
	}
}

func inferredToolForObservation(reqs models.TaskRequirements, selectedTool string) string {
	if strings.TrimSpace(selectedTool) != "" {
		return strings.TrimSpace(selectedTool)
	}
	if len(reqs.RequiredTools) == 1 {
		return strings.TrimSpace(reqs.RequiredTools[0])
	}
	for _, tool := range reqs.RequiredTools {
		switch {
		case strings.EqualFold(tool, "apple-foundation-models"):
			return tool
		case strings.EqualFold(tool, "ollama"):
			return tool
		case strings.EqualFold(tool, "llama-server"):
			return tool
		}
	}
	return ""
}

// ObservationScopeForRequirements normalizes the exact empirical scope used by
// guarded execution recording and placement lookup.
//
// ModelName is populated by extracting a model name from reqs.Description only
// for inference-related workload classes. Non-inference workloads leave ModelName
// empty — preserving backward compatibility and avoiding false positives from
// flags like -m in git commit messages.
func ObservationScopeForRequirements(node string, reqs models.TaskRequirements, selectedTool string) models.ObservationScope {
	tool := inferredToolForObservation(reqs, selectedTool)
	return models.ObservationScope{
		Node:      strings.TrimSpace(node),
		Workload:  reqs.Workload.Class,
		Backend:   observationBackend(reqs, tool),
		Tool:      tool,
		ModelName: inferenceModelName(reqs),
	}
}

// inferenceModelName extracts a model name from the task description when the
// workload is inference-related. Returns "" for all other workload classes to
// avoid false positives from generic -m flags (e.g. git commit -m "...").
func inferenceModelName(reqs models.TaskRequirements) string {
	switch reqs.Workload.Class {
	case models.ClassLocalLLMInference, models.ClassLongContextInference,
		models.ClassAppleIntelligence, models.ClassLlamaServer:
		return ExtractModelName(reqs.Description)
	default:
		return ""
	}
}

func freshObservation(n models.NodeFacts, reqs models.TaskRequirements, st *state.ClusterState) (*models.ExecutionObservation, bool) {
	if st == nil {
		return nil, false
	}
	obs, ok := st.Observation(ObservationScopeForRequirements(n.Name, reqs, ""))
	if !ok || obs == nil || !state.ObservationIsFresh(*obs, time.Now().UTC()) {
		return nil, false
	}
	return obs, true
}

func empiricalObservation(n models.NodeFacts, reqs models.TaskRequirements, st *state.ClusterState) *models.ExecutionObservation {
	obs, ok := freshObservation(n, reqs, st)
	if !ok {
		return nil
	}
	return obs
}

func compareOptionalLower(a, b int64) int {
	switch {
	case a > 0 && b > 0 && a != b:
		if a < b {
			return 1
		}
		return -1
	default:
		return 0
	}
}

func compareObservationPreference(a, b *models.ExecutionObservation) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a != nil && b == nil:
		return 1
	case a == nil && b != nil:
		return -1
	}
	if a.LastSuccess != b.LastSuccess {
		if a.LastSuccess {
			return 1
		}
		return -1
	}
	if cmp := compareOptionalLower(a.PeakRAMMB, b.PeakRAMMB); cmp != 0 {
		return cmp
	}
	if cmp := compareOptionalLower(a.PeakVRAMMB, b.PeakVRAMMB); cmp != 0 {
		return cmp
	}
	if a.WallTimeMS != b.WallTimeMS {
		if a.WallTimeMS < b.WallTimeMS {
			return 1
		}
		return -1
	}
	if a.SampleCount != b.SampleCount {
		if a.SampleCount > b.SampleCount {
			return 1
		}
		return -1
	}
	return 0
}

func residentRuntimePreference(reqs models.TaskRequirements) string {
	switch {
	case requiresAppleFoundationModels(reqs):
		return "apple-foundation-models"
	case requiresTool(reqs.RequiredTools, "ollama"):
		return "ollama"
	case requiresTool(reqs.RequiredTools, "llama-server"):
		return "llama.cpp"
	}
	for _, backend := range reqs.PreferredBackends {
		switch strings.ToLower(strings.TrimSpace(backend)) {
		case "mlx":
			return "mlx"
		case "ollama":
			return "ollama"
		case "llama.cpp", "llama-server":
			return "llama.cpp"
		}
	}
	return ""
}

func relevantResidentModels(n models.NodeFacts, reqs models.TaskRequirements) []models.ResidentModel {
	if len(n.ResidentModels) == 0 {
		return nil
	}
	runtime := residentRuntimePreference(reqs)
	if runtime == "" {
		return nil
	}
	var relevant []models.ResidentModel
	for _, model := range n.ResidentModels {
		if strings.EqualFold(model.Runtime, runtime) {
			relevant = append(relevant, model)
		}
	}
	return relevant
}

func residentModelRank(n models.NodeFacts, reqs models.TaskRequirements) int {
	return len(relevantResidentModels(n, reqs))
}

func residentModelReason(n models.NodeFacts, reqs models.TaskRequirements) string {
	modelsForReq := relevantResidentModels(n, reqs)
	if len(modelsForReq) == 0 {
		return ""
	}
	names := make([]string, 0, minInt(len(modelsForReq), 3))
	for _, model := range modelsForReq {
		names = append(names, model.Name)
		if len(names) == 3 {
			break
		}
	}
	runtime := firstNonEmpty(modelsForReq[0].Runtime, residentRuntimePreference(reqs))
	return fmt.Sprintf("resident model locality: %s via %s already loaded", strings.Join(names, ", "), runtime)
}

func empiricalReason(obs *models.ExecutionObservation) string {
	if obs == nil {
		return ""
	}
	parts := []string{
		fmt.Sprintf("empirical history: %d run(s), avg %dms", obs.SampleCount, obs.WallTimeMS),
	}
	if obs.PeakRAMMB > 0 {
		parts = append(parts, fmt.Sprintf("peak RAM %dMB", obs.PeakRAMMB))
	}
	if obs.PeakVRAMMB > 0 {
		parts = append(parts, fmt.Sprintf("peak VRAM %dMB", obs.PeakVRAMMB))
	}
	if obs.ModelName != "" {
		parts = append(parts, fmt.Sprintf("model %s", obs.ModelName))
	}
	if !obs.LastSuccess {
		parts = append(parts, "last run unsuccessful")
	}
	return strings.Join(parts, ", ")
}
