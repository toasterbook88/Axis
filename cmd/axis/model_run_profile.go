package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/toasterbook88/axis/internal/models"
)

func requireModelStartIdentity(cmd *cobra.Command) error {
	if cmd.Flags().Changed("from-plan") {
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
