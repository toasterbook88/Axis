package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/toasterbook88/axis/internal/models"
)

func requireDailyModelStart(cmd *cobra.Command) error {
	var used []string
	for _, name := range []string{
		"node", "weights", "port", "from-plan", "n-gpu-layers", "ctx-size",
		"batch-size", "ubatch-size", "threads", "main-gpu", "ollama-model",
		"ollama-keep-alive", "ollama-num-ctx", "mlx-model", "prefill-step-size",
		"prompt-cache-bytes", "kv-bits",
	} {
		if cmd.Flags().Changed(name) {
			used = append(used, "--"+name)
		}
	}
	if len(used) == 0 {
		return nil
	}
	return fmt.Errorf("axis model start <model> picks the node; do not combine it with %s", strings.Join(used, ", "))
}

func requireModelStartIdentity(cmd *cobra.Command) error {
	if cmd.Flags().Changed("from-plan") {
		return nil
	}
	ollamaSet := cmd.Flags().Changed("ollama-model")
	mlxSet := cmd.Flags().Changed("mlx-model")
	weightsSet := cmd.Flags().Changed("weights")
	if ollamaSet && weightsSet {
		return fmt.Errorf("--ollama-model and --weights are mutually exclusive")
	}
	if mlxSet && weightsSet {
		return fmt.Errorf("--mlx-model and --weights are mutually exclusive")
	}
	if ollamaSet && mlxSet {
		return fmt.Errorf("--mlx-model and --ollama-model are mutually exclusive")
	}
	if ollamaSet {
		if !cmd.Flags().Changed("node") {
			return fmt.Errorf(`required flag(s) "node" not set`)
		}
		name, err := cmd.Flags().GetString("ollama-model")
		if err != nil {
			return err
		}
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("ollama model is required")
		}
		return nil
	}
	if mlxSet {
		var missing []string
		for _, name := range []string{"node", "port"} {
			if !cmd.Flags().Changed(name) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf(`required flag(s) "%s" not set`, strings.Join(missing, `", "`))
		}
		name, err := cmd.Flags().GetString("mlx-model")
		if err != nil {
			return err
		}
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("mlx model is required")
		}
		return nil
	}
	var missing []string
	for _, name := range []string{"node", "weights", "port"} {
		if !cmd.Flags().Changed(name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(`required flag(s) "%s" not set`, strings.Join(missing, `", "`))
}

func profileForModelStart(cmd *cobra.Command, nodeName, weights string, port int) (models.ModelRunProfile, error) {
	fromPlan, err := cmd.Flags().GetString("from-plan")
	if err != nil {
		fromPlan = ""
	}
	var profile models.ModelRunProfile
	if strings.TrimSpace(fromPlan) != "" {
		data, readErr := os.ReadFile(fromPlan)
		if readErr != nil {
			return models.ModelRunProfile{}, readErr
		}
		profile, err = models.LoadModelRunProfile(data)
		if err != nil {
			return models.ModelRunProfile{}, err
		}
		if cmd.Flags().Changed("node") {
			profile.Node = nodeName
		}
		if cmd.Flags().Changed("weights") {
			profile.WeightsPath = weights
			profile.Volume = ""
		}
		if cmd.Flags().Changed("port") {
			profile.Port = port
			profile.PortSource = models.PortSourceExplicit
		}
	} else {
		profile = models.ModelRunProfile{
			Schema:       models.ModelRunSchema,
			Node:         nodeName,
			Engine:       models.EngineLlamaCpp,
			ToolName:     models.ToolLlamaServer,
			ArtifactKind: models.ArtifactWeightsPath,
			WeightsPath:  weights,
			BindHost:     "127.0.0.1",
			Port:         port,
			PortSource:   models.PortSourceExplicit,
		}
	}
	if err := applyChangedStartFlags(cmd, &profile); err != nil {
		return models.ModelRunProfile{}, err
	}
	return profile, nil
}

func applyChangedStartFlags(cmd *cobra.Command, profile *models.ModelRunProfile) error {
	if cmd.Flags().Changed("n-gpu-layers") {
		raw, err := cmd.Flags().GetString("n-gpu-layers")
		if err != nil {
			return err
		}
		n, mode, err := models.ParseNGPULayers(raw)
		if err != nil {
			return err
		}
		profile.NGPULayers = n
		profile.NGPULayersMode = mode
	}
	if cmd.Flags().Changed("ctx-size") {
		value, err := cmd.Flags().GetInt("ctx-size")
		if err != nil {
			return err
		}
		profile.ContextTokens = &value
	}
	if cmd.Flags().Changed("batch-size") {
		value, err := cmd.Flags().GetInt("batch-size")
		if err != nil {
			return err
		}
		profile.BatchSize = &value
	}
	if cmd.Flags().Changed("ubatch-size") {
		value, err := cmd.Flags().GetInt("ubatch-size")
		if err != nil {
			return err
		}
		profile.UBatchSize = &value
	}
	if cmd.Flags().Changed("threads") {
		value, err := cmd.Flags().GetInt("threads")
		if err != nil {
			return err
		}
		profile.Threads = &value
	}
	if cmd.Flags().Changed("main-gpu") {
		value, err := cmd.Flags().GetInt("main-gpu")
		if err != nil {
			return err
		}
		profile.DeviceIndex = &value
		profile.IndexSource = models.IndexSourceNvidiaSMI
	}
	if cmd.Flags().Changed("ollama-model") {
		value, err := cmd.Flags().GetString("ollama-model")
		if err != nil {
			return err
		}
		profile.OllamaModel = strings.TrimSpace(value)
		profile.Engine = models.EngineOllama
		profile.ArtifactKind = models.ArtifactOllamaModelName
		profile.WeightsPath = ""
		profile.Volume = ""
		profile.ToolName = ""
		if profile.BindHost == "" {
			profile.BindHost = "127.0.0.1"
		}
	}
	if cmd.Flags().Changed("ollama-num-ctx") {
		value, err := cmd.Flags().GetInt("ollama-num-ctx")
		if err != nil {
			return err
		}
		profile.OllamaNumCtx = &value
	}
	if cmd.Flags().Changed("ollama-keep-alive") {
		value, err := cmd.Flags().GetString("ollama-keep-alive")
		if err != nil {
			return err
		}
		profile.OllamaKeepAlive = value
	}
	if cmd.Flags().Changed("mlx-model") {
		value, err := cmd.Flags().GetString("mlx-model")
		if err != nil {
			return err
		}
		profile.MLXModel = strings.TrimSpace(value)
		profile.Engine = models.EngineMLX
		profile.ArtifactKind = models.ArtifactMLXModelDir
		profile.ToolName = models.ToolMLXServer
		profile.WeightsPath = ""
		profile.Volume = ""
		if profile.BindHost == "" {
			profile.BindHost = "127.0.0.1"
		}
	}
	if cmd.Flags().Changed("prefill-step-size") {
		value, err := cmd.Flags().GetInt("prefill-step-size")
		if err != nil {
			return err
		}
		profile.PrefillStepSize = &value
	}
	if cmd.Flags().Changed("prompt-cache-bytes") {
		value, err := cmd.Flags().GetInt64("prompt-cache-bytes")
		if err != nil {
			return err
		}
		profile.PromptCacheBytes = &value
	}
	if cmd.Flags().Changed("kv-bits") {
		value, err := cmd.Flags().GetInt("kv-bits")
		if err != nil {
			return err
		}
		profile.KVBits = &value
	}
	return nil
}

func writeSelectedRunProfile(cmd *cobra.Command, selected *models.ModelRunProfile) error {
	path, err := cmd.Flags().GetString("write-profile")
	if err != nil || strings.TrimSpace(path) == "" || selected == nil {
		return nil
	}
	data, err := json.MarshalIndent(selected, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0644)
}
