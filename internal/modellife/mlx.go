package modellife

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// MLXArgv projects an mlx_lm.server command. importOK is the observed
// `python3 -c "import mlx_lm"` result and is used only when mlx_lm.server
// itself is not an observed tool. The mlx_lm console tool is not a server.
func MLXArgv(node models.NodeFacts, profile models.ModelRunProfile, importOK bool) ([]string, error) {
	if profile.Engine != models.EngineMLX {
		return nil, fmt.Errorf("engine %q is not mlx", profile.Engine)
	}
	if profile.BindHost != "127.0.0.1" {
		return nil, fmt.Errorf("bind host must be 127.0.0.1")
	}
	if profile.Port < 1 || profile.Port > 65535 {
		return nil, fmt.Errorf("port must be between 1 and 65535")
	}
	if node.Resources == nil || node.Resources.MemoryTopology != models.MemoryTopologyUnified {
		return nil, fmt.Errorf("mlx requires unified memory")
	}
	model, err := mlxModelDir(node, profile.MLXModel)
	if err != nil {
		return nil, err
	}
	if profile.PrefillStepSize != nil && *profile.PrefillStepSize < 1 {
		return nil, fmt.Errorf("prefill-step-size must be >= 1")
	}
	if profile.PromptCacheBytes != nil && *profile.PromptCacheBytes < 1 {
		return nil, fmt.Errorf("prompt-cache-bytes must be >= 1")
	}
	if profile.KVBits != nil && *profile.KVBits < 1 {
		return nil, fmt.Errorf("kv-bits must be >= 1")
	}
	argv, err := mlxArgvPrefix(node, importOK)
	if err != nil {
		return nil, err
	}
	argv = append(argv, "--model", model, "--port", strconv.Itoa(profile.Port), "--host", "127.0.0.1")
	if profile.PrefillStepSize != nil {
		argv = append(argv, "--prefill-step-size", strconv.Itoa(*profile.PrefillStepSize))
	}
	if profile.PromptCacheBytes != nil {
		argv = append(argv, "--prompt-cache-bytes", strconv.FormatInt(*profile.PromptCacheBytes, 10))
	}
	if profile.KVBits != nil {
		argv = append(argv, "--kv-bits", strconv.Itoa(*profile.KVBits))
	}
	return argv, nil
}

func mlxArgvPrefix(node models.NodeFacts, importOK bool) ([]string, error) {
	if bin := toolPath(node, models.ToolMLXServer); bin != "" {
		return []string{bin}, nil
	}
	if hasTool(node, models.ToolMLXServer) {
		return []string{models.ToolMLXServer}, nil
	}
	python := toolPath(node, "python3")
	if importOK && python != "" {
		return []string{python, "-m", "mlx_lm.server"}, nil
	}
	return nil, fmt.Errorf("node %s has no observed mlx_lm.server tool", node.Name)
}

func mlxModelDir(node models.NodeFacts, model string) (string, error) {
	model = path.Clean(strings.TrimSpace(model))
	if model == "" || model == "." {
		return "", fmt.Errorf("mlx model is required")
	}
	if !path.IsAbs(model) {
		return "", fmt.Errorf("mlx hub repo id %q is not a local directory", model)
	}
	switch strings.ToLower(path.Ext(model)) {
	case ".gguf", ".safetensors", ".bin", ".pt":
		return "", fmt.Errorf("mlx model %s must be a local directory", model)
	}
	if _, ok := models.NamedLocalVolume(node, model); !ok {
		return "", fmt.Errorf("mlx model %s is not on a named local volume", model)
	}
	return model, nil
}
