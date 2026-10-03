package modellife

import (
	"fmt"
	"path"
	"reflect"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// StartPlan is the argv Axis will exec. It does not launch anything.
// Argv is the projection of Profile.
type StartPlan struct {
	Node    string
	Port    int
	Weights string
	Volume  string
	Argv    []string
	Profile models.ModelRunProfile
}

// PlanStart validates weights sit on a named local volume and that
// llama-server is an observed tool. Port must be explicit and valid.
// Optional launch fields stay unset, so the argv is the historical default.
func PlanStart(node models.NodeFacts, weights string, port int) (StartPlan, error) {
	weights = path.Clean(strings.TrimSpace(weights))
	return PlanStartProfile(node, models.ModelRunProfile{
		Schema:       models.ModelRunSchema,
		Node:         node.Name,
		Engine:       models.EngineLlamaCpp,
		ToolName:     models.ToolLlamaServer,
		ArtifactKind: models.ArtifactWeightsPath,
		WeightsPath:  weights,
		BindHost:     "127.0.0.1",
		Port:         port,
		PortSource:   models.PortSourceExplicit,
	})
}

// PlanStartProfile validates profile against the observed node and derives argv.
// A non-empty refusal list, a plan-default port, or an offload without measured
// discrete VRAM returns an error and no argv.
func PlanStartProfile(node models.NodeFacts, profile models.ModelRunProfile) (StartPlan, error) {
	profile = normalizeStartProfile(node, profile)
	if err := profile.Validate(); err != nil {
		return StartPlan{}, err
	}
	refusals := append([]string{}, profile.Refusals...)
	if profile.PortSource != models.PortSourceExplicit {
		refusals = append(refusals, fmt.Sprintf("port source %s requires an explicit port", profile.PortSource))
	}
	if !hasTool(node, models.ToolLlamaServer) {
		refusals = append(refusals, fmt.Sprintf("node %s has no observed llama-server tool", node.Name))
	}
	if vol, ok := namedLocalVolume(node, profile.WeightsPath); ok {
		profile.Volume = vol
	} else {
		refusals = append(refusals, fmt.Sprintf("weights %s are not on a named local volume", profile.WeightsPath))
	}
	if profile.Threads != nil {
		cores := 0
		if node.Resources != nil {
			cores = node.Resources.CPUCores
		}
		if cores <= 0 || *profile.Threads > cores {
			refusals = append(refusals, fmt.Sprintf("threads must be between 1 and %d observed cpu cores", cores))
		}
	}
	if len(refusals) > 0 {
		return StartPlan{}, fmt.Errorf("%s", strings.Join(refusals, "; "))
	}
	argv, err := ArgvFromProfile(profile)
	if err != nil {
		return StartPlan{}, err
	}
	return StartPlan{
		Node:    profile.Node,
		Port:    profile.Port,
		Weights: path.Clean(strings.TrimSpace(profile.WeightsPath)),
		Volume:  profile.Volume,
		Argv:    argv,
		Profile: profile,
	}, nil
}

func normalizeStartProfile(node models.NodeFacts, profile models.ModelRunProfile) models.ModelRunProfile {
	if profile.Schema == "" {
		profile.Schema = models.ModelRunSchema
	}
	if profile.Node == "" {
		profile.Node = node.Name
	}
	if profile.Engine == "" {
		profile.Engine = models.EngineLlamaCpp
	}
	if profile.ToolName == "" {
		profile.ToolName = models.ToolLlamaServer
	}
	if profile.ArtifactKind == "" {
		profile.ArtifactKind = models.ArtifactWeightsPath
	}
	if profile.BindHost == "" {
		profile.BindHost = "127.0.0.1"
	}
	profile.WeightsPath = path.Clean(strings.TrimSpace(profile.WeightsPath))
	if hasTool(node, models.ToolLlamaServer) {
		if bin := toolPath(node, models.ToolLlamaServer); bin != "" {
			profile.EngineBinary = bin
		} else {
			profile.EngineBinary = models.ToolLlamaServer
		}
	}
	dev := models.ObserveLaunchDevice(node)
	profile.DeviceKind = dev.Kind
	profile.DeviceModel = dev.Model
	profile.Accelerator = dev.Accelerator
	profile.MemoryTopology = dev.MemoryTopology
	profile.VRAMFreeMB = dev.VRAMFreeMB
	profile.VRAMFreeMeasured = dev.VRAMFreeMeasured
	profile.Refusals = append([]string{}, profile.Refusals...)
	if (profile.NGPULayers != nil || profile.NGPULayersMode != "") &&
		(dev.Kind != models.DeviceKindDiscrete || !dev.VRAMFreeMeasured) {
		profile.Refusals = append(profile.Refusals, "n-gpu-layers requires measured free VRAM on one discrete device")
	}
	return profile
}

// ArgvFromProfile projects a llama-server argv. Optional flags are appended
// after the fixed prefix, in the order ctx, n-gpu-layers, batch, ubatch, threads.
func ArgvFromProfile(profile models.ModelRunProfile) ([]string, error) {
	if profile.Engine != models.EngineLlamaCpp {
		return nil, fmt.Errorf("engine %q is not supported", profile.Engine)
	}
	if profile.BindHost != "127.0.0.1" {
		return nil, fmt.Errorf("bind host must be 127.0.0.1")
	}
	if profile.Port < 1 || profile.Port > 65535 {
		return nil, fmt.Errorf("port must be between 1 and 65535")
	}
	if profile.NGPULayers != nil && profile.NGPULayersMode != "" {
		return nil, fmt.Errorf("n-gpu-layers accepts an integer or a mode, not both")
	}
	bin := profile.EngineBinary
	if bin == "" {
		bin = models.ToolLlamaServer
	}
	weights := path.Clean(strings.TrimSpace(profile.WeightsPath))
	argv := []string{bin, "-m", weights, "--port", strconv.Itoa(profile.Port), "--host", "127.0.0.1"}
	if profile.ContextTokens != nil {
		argv = append(argv, "-c", strconv.Itoa(*profile.ContextTokens))
	}
	if profile.NGPULayers != nil {
		argv = append(argv, "-ngl", strconv.Itoa(*profile.NGPULayers))
	} else if profile.NGPULayersMode != "" {
		argv = append(argv, "-ngl", profile.NGPULayersMode)
	}
	if profile.BatchSize != nil {
		argv = append(argv, "-b", strconv.Itoa(*profile.BatchSize))
	}
	if profile.UBatchSize != nil {
		argv = append(argv, "-ub", strconv.Itoa(*profile.UBatchSize))
	}
	if profile.Threads != nil {
		argv = append(argv, "-t", strconv.Itoa(*profile.Threads))
	}
	return argv, nil
}

// ExecArgvMatchesProfile reports whether argv is exactly the profile projection.
// A hand-built argv with an empty profile does not match.
func ExecArgvMatchesProfile(plan StartPlan) error {
	want, err := ArgvFromProfile(plan.Profile)
	if err != nil {
		return fmt.Errorf("argv does not match profile: %w", err)
	}
	if !reflect.DeepEqual(plan.Argv, want) {
		return fmt.Errorf("argv does not match profile")
	}
	return nil
}

func hasTool(node models.NodeFacts, name string) bool {
	for _, t := range node.Tools {
		if strings.EqualFold(t.Name, name) {
			return true
		}
	}
	return false
}

func toolPath(node models.NodeFacts, name string) string {
	for _, t := range node.Tools {
		if strings.EqualFold(t.Name, name) {
			return t.Path
		}
	}
	return ""
}

func namedLocalVolume(node models.NodeFacts, weights string) (string, bool) {
	return models.NamedLocalVolume(node, weights)
}
