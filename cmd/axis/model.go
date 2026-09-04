package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modelinventory"
	"github.com/toasterbook88/axis/internal/modellife"
	"github.com/toasterbook88/axis/internal/modelplan"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/transport"
)

var loadModelSnapshot = func(ctx context.Context) (*models.ClusterSnapshot, error) {
	rt, err := runtimectx.Load(ctx)
	if err != nil {
		return nil, err
	}
	if rt == nil || rt.Snapshot == nil {
		return nil, fmt.Errorf("no cluster snapshot")
	}
	return rt.Snapshot, nil
}

var loadModelConfig = func() (*config.Config, error) {
	return config.Load(config.DefaultConfigPath())
}

type modelProcessRunner interface {
	Start(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, plan modellife.StartPlan) error
	Stop(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, target modellife.StopTarget) (modelStopDisposition, error)
	Probe(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) error
	Await(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, instance models.ModelInstance, opts modellife.AwaitOptions) (models.ModelOperationReceipt, error)
	Query(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, instance models.ModelInstance, req modellife.QueryRequest) (modellife.QueryResult, error)
}

var defaultModelRunner modelProcessRunner = liveModelRunner{}

func modelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Inspect resident models or manage llama-server on a named node",
	}
	cmd.AddCommand(modelListCmd())
	cmd.AddCommand(modelInspectCmd())
	cmd.AddCommand(modelPlanCmd())
	cmd.AddCommand(modelStartCmd())
	cmd.AddCommand(modelStopCmd())
	cmd.AddCommand(modelAwaitCmd())
	cmd.AddCommand(modelQueryCmd())
	return cmd
}

func modelPlanCmd() *cobra.Command {
	var cacheAddr, format string
	var port int
	var live bool
	cmd := &cobra.Command{
		Use:          "plan <spec|weights>",
		Short:        "Dry-run evaluation of cluster nodes for model placement",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			return runModelPlan(ctx, cmd, args[0], port, live, cacheAddr, format)
		},
	}
	cmd.Flags().IntVar(&port, "port", 8080, "Target listen port to verify availability")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS daemon cache")
	cmd.Flags().BoolVar(&live, "live", false, "Bypass daemon cache and perform live fleet discovery")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	return cmd
}

func modelStartCmd() *cobra.Command {
	var node, weights, cacheAddr, format string
	var port int
	var live bool
	cmd := &cobra.Command{
		Use:          "start",
		Short:        "Start llama-server on a named node (explicit port and weights)",
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			return runModelStart(ctx, cmd, node, weights, port, defaultModelRunner)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Cluster node name (required)")
	cmd.Flags().StringVar(&weights, "weights", "", "GGUF path on a named local volume (required)")
	cmd.Flags().IntVar(&port, "port", 0, "Listen port (required; no default)")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS daemon cache")
	cmd.Flags().BoolVar(&live, "live", false, "Bypass daemon cache and perform live fleet discovery")
	cmd.Flags().StringVar(&format, "format", "text", "Start operation receipt format: text, json, or yaml")
	_ = cmd.MarkFlagRequired("node")
	_ = cmd.MarkFlagRequired("weights")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}

func modelStopCmd() *cobra.Command {
	var node, cacheAddr, format string
	var port int
	cmd := &cobra.Command{
		Use:          "stop [generation-id]",
		Short:        "Stop an observed llama-server generation or use legacy node/port flags",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			if len(args) == 1 {
				if strings.TrimSpace(node) != "" || port != 0 {
					return fmt.Errorf("generation ID cannot be combined with --node or --port")
				}
				return runModelStopGeneration(ctx, cmd, args[0], cacheAddr, format, defaultModelRunner)
			}
			return runModelStop(ctx, cmd, node, port, defaultModelRunner)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Legacy cluster node name")
	cmd.Flags().IntVar(&port, "port", 0, "Legacy llama-server listen port")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS daemon cache")
	cmd.Flags().StringVar(&format, "format", "text", "Generation-stop receipt format: text, json, or yaml")
	return cmd
}

func modelAwaitCmd() *cobra.Command {
	var cacheAddr, format string
	var timeout, interval time.Duration
	var live bool
	cmd := &cobra.Command{
		Use:          "await <instance-id>",
		Short:        "Wait for a resident model instance to become ready to serve",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout+5*time.Second)
			defer cancel()
			return runModelAwait(ctx, cmd, args[0], timeout, interval, live, cacheAddr, format, defaultModelRunner)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "Maximum time to wait for instance readiness")
	cmd.Flags().DurationVar(&interval, "interval", 500*time.Millisecond, "Polling interval between readiness probes")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS daemon cache")
	cmd.Flags().BoolVar(&live, "live", false, "Bypass daemon cache and perform live fleet discovery")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	return cmd
}

func modelQueryCmd() *cobra.Command {
	var cacheAddr, format, node string
	var timeout time.Duration
	var maxTokens int
	var temperature float64
	var live bool
	cmd := &cobra.Command{
		Use:          "query <instance-id|model> <prompt>",
		Short:        "Query a resident model instance with a prompt",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			return runModelQuery(ctx, cmd, args[0], args[1], node, maxTokens, temperature, live, cacheAddr, format, defaultModelRunner)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Filter candidate instances by node name")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 512, "Maximum tokens to generate")
	cmd.Flags().Float64Var(&temperature, "temperature", 0.7, "Sampling temperature")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "Request timeout")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS daemon cache")
	cmd.Flags().BoolVar(&live, "live", false, "Bypass daemon cache and perform live fleet discovery")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	return cmd
}

func runModelAwait(ctx context.Context, cmd *cobra.Command, targetID string, timeout, interval time.Duration, live bool, cacheAddr, format string, runner modelProcessRunner) error {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return fmt.Errorf("instance ID is required")
	}

	var (
		snap   *models.ClusterSnapshot
		source string
		err    error
	)
	if live {
		snap, err = loadModelSnapshot(ctx)
		source = "live"
	} else {
		snap, source, err = fetchModelInventorySnapshot(ctx, cacheAddr)
	}
	if err != nil {
		if live {
			return fmt.Errorf("collect live cluster snapshot for model await: %w", err)
		}
		return fmt.Errorf("load cluster snapshot from daemon cache for model await: %w (use --live for an explicit live collection)", err)
	}

	inventory := modelinventory.FromSnapshot(snap, source)
	var instance *models.ModelInstance
	for i := range inventory.Instances {
		inst := &inventory.Instances[i]
		if inst.ID == targetID || inst.GenerationID == targetID || strings.EqualFold(inst.Model, targetID) {
			instance = inst
			break
		}
	}
	if instance == nil {
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: fmt.Sprintf("model instance %q not found in %s inventory", targetID, sourceOrLive(source)),
		}
	}

	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, instance.Node)
	if err != nil {
		return err
	}

	opts := modellife.AwaitOptions{
		Timeout:        timeout,
		Interval:       interval,
		SnapshotSource: source,
		SnapshotAt:     snap.Timestamp,
	}
	if snap.Publication != nil {
		opts.PublicationID = snap.Publication.ID
	}

	receipt, awaitErr := runner.Await(ctx, nf, cfgNode, *instance, opts)
	if format == "json" || format == "yaml" {
		if writeErr := printOutput(cmd.OutOrStdout(), receipt, format); writeErr != nil {
			return writeErr
		}
	} else {
		if receipt.Status == models.ModelOperationCompleted {
			if _, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "ready %s:%d instance %s in %dms operation %s\n",
				receipt.Node, receipt.Port, receipt.InstanceID, receipt.DurationMS, receipt.ID); writeErr != nil {
				return writeErr
			}
		} else {
			if _, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "%s %s:%d instance %s: %s operation %s\n",
				receipt.Disposition, receipt.Node, receipt.Port, receipt.InstanceID, receipt.Error, receipt.ID); writeErr != nil {
				return writeErr
			}
		}
	}

	if awaitErr != nil || receipt.Status != models.ModelOperationCompleted {
		errMsg := receipt.Error
		if errMsg == "" && awaitErr != nil {
			errMsg = awaitErr.Error()
		}
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: fmt.Sprintf("model await on %s:%d: %s", instance.Node, instance.Port, errMsg),
		}
	}
	return nil
}

func runModelQuery(ctx context.Context, cmd *cobra.Command, target, prompt, nodeFilter string, maxTokens int, temperature float64, live bool, cacheAddr, format string, runner modelProcessRunner) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("target instance ID or model name is required")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("prompt cannot be empty")
	}

	startedAt := time.Now().UTC()
	var (
		snap   *models.ClusterSnapshot
		source string
		err    error
	)
	if live {
		snap, err = loadModelSnapshot(ctx)
		source = "live"
	} else {
		snap, source, err = fetchModelInventorySnapshot(ctx, cacheAddr)
	}
	if err != nil {
		if live {
			return fmt.Errorf("collect live cluster snapshot for model query: %w", err)
		}
		return fmt.Errorf("load cluster snapshot from daemon cache for model query: %w (use --live for an explicit live collection)", err)
	}

	inventory := modelinventory.FromSnapshot(snap, source)
	var instance *models.ModelInstance
	for i := range inventory.Instances {
		inst := &inventory.Instances[i]
		if nodeFilter != "" && !strings.EqualFold(inst.Node, nodeFilter) {
			continue
		}
		if inst.ID == target || inst.GenerationID == target || strings.EqualFold(inst.Model, target) {
			instance = inst
			break
		}
	}
	if instance == nil {
		msg := fmt.Sprintf("model instance or name %q not found in %s inventory", target, sourceOrLive(source))
		if nodeFilter != "" {
			msg = fmt.Sprintf("model instance or name %q on node %q not found in %s inventory", target, nodeFilter, sourceOrLive(source))
		}
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: msg,
		}
	}

	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, instance.Node)
	if err != nil {
		return err
	}

	var tempPtr *float64
	if cmd.Flags().Changed("temperature") || temperature != 0 {
		tempPtr = &temperature
	}

	req := modellife.QueryRequest{
		Model:       instance.Model,
		Prompt:      prompt,
		MaxTokens:   maxTokens,
		Temperature: tempPtr,
	}

	result, queryErr := runner.Query(ctx, nf, cfgNode, *instance, req)

	receipt := models.ModelOperationReceipt{
		Schema:           "axis.model-operation/v1",
		ID:               models.GenerateID("mo"),
		Action:           models.ModelOperationQuery,
		Status:           models.ModelOperationCompleted,
		Disposition:      "answered",
		InstanceID:       instance.ID,
		GenerationID:     instance.GenerationID,
		Node:             instance.Node,
		Engine:           instance.Engine,
		Port:             instance.Port,
		PID:              instance.PID,
		Model:            instance.Model,
		SnapshotSource:   source,
		SnapshotAt:       snap.Timestamp,
		StartedAt:        startedAt,
		CompletedAt:      time.Now().UTC(),
		DurationMS:       result.DurationMS,
		PromptTokens:     result.PromptTokens,
		CompletionTokens: result.CompletionTokens,
		TotalTokens:      result.TotalTokens,
		EndpointURL:      result.Endpoint,
		ResponseText:     result.Content,
	}
	if snap.Publication != nil {
		receipt.PublicationID = snap.Publication.ID
	}
	if queryErr != nil {
		receipt.Status = models.ModelOperationFailed
		receipt.Disposition = "failed"
		receipt.Error = queryErr.Error()
	}

	if format == "json" || format == "yaml" {
		if writeErr := printOutput(cmd.OutOrStdout(), receipt, format); writeErr != nil {
			return writeErr
		}
	} else {
		if queryErr == nil {
			if _, writeErr := fmt.Fprintln(cmd.OutOrStdout(), result.Content); writeErr != nil {
				return writeErr
			}
		} else {
			if _, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "query failed on %s:%d: %s operation %s\n",
				instance.Node, instance.Port, receipt.Error, receipt.ID); writeErr != nil {
				return writeErr
			}
		}
	}

	if queryErr != nil {
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: fmt.Sprintf("model query on %s:%d failed: %v", instance.Node, instance.Port, queryErr),
		}
	}
	return nil
}

func runModelPlan(ctx context.Context, cmd *cobra.Command, specOrWeights string, port int, live bool, cacheAddr, format string) error {
	var (
		snap   *models.ClusterSnapshot
		source string
		err    error
	)
	if live {
		snap, err = loadModelSnapshot(ctx)
		source = "live"
	} else {
		snap, source, err = fetchModelInventorySnapshot(ctx, cacheAddr)
	}
	if err != nil {
		if live {
			return fmt.Errorf("collect live cluster snapshot for model plan: %w", err)
		}
		return fmt.Errorf("load cluster snapshot from daemon cache for model plan: %w (use --live for an explicit live collection)", err)
	}

	spec, err := resolveModelSpec(specOrWeights, snap)
	if err != nil {
		return err
	}

	plan, err := modelplan.PlanSingleNode(snap, spec, port)
	if err != nil {
		return err
	}
	plan.SnapshotSource = source

	if format == "json" || format == "yaml" {
		if writeErr := printOutput(cmd.OutOrStdout(), plan, format); writeErr != nil {
			return writeErr
		}
	} else {
		if _, writeErr := fmt.Fprint(cmd.OutOrStdout(), modelplan.FormatModelPlacementPlanText(plan)); writeErr != nil {
			return writeErr
		}
	}

	if len(plan.Candidates) == 0 {
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: "model plan: no eligible placement candidates found",
		}
	}
	return nil
}

func resolveModelSpec(input string, snap *models.ClusterSnapshot) (models.ModelSpec, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return models.ModelSpec{}, fmt.Errorf("model spec or weights path is required")
	}

	// 1. Check if input is a local file
	if fi, err := os.Stat(input); err == nil && !fi.IsDir() {
		content, readErr := os.ReadFile(input)
		if readErr == nil {
			var spec models.ModelSpec
			if jsonErr := json.Unmarshal(content, &spec); jsonErr == nil && spec.ID != "" && spec.Name != "" {
				if err := spec.Validate(); err == nil {
					return spec, nil
				}
			}
			var yamlSpec models.ModelSpec
			if yamlErr := yaml.Unmarshal(content, &yamlSpec); yamlErr == nil && yamlSpec.ID != "" && yamlSpec.Name != "" {
				if err := yamlSpec.Validate(); err == nil {
					return yamlSpec, nil
				}
			}
		}
		spec := models.ModelSpecFromPath(input, fi.Size())
		return spec, nil
	}

	// 2. Check cluster snapshot DiskWeights
	if snap != nil {
		for _, n := range snap.Nodes {
			for _, dw := range n.DiskWeights {
				if strings.EqualFold(dw.Name, input) || strings.EqualFold(dw.Path, input) || strings.EqualFold(filepath.Base(dw.Path), filepath.Base(input)) {
					spec := models.ModelSpecFromDiskWeight(dw)
					return spec, nil
				}
			}
		}
		// Check resident models
		for _, n := range snap.Nodes {
			for _, res := range n.ResidentModels {
				if strings.EqualFold(res.Name, input) {
					weightMB := res.WeightSizeMB
					if weightMB <= 0 {
						weightMB = 2048
					}
					spec := models.ModelSpec{
						Schema:           "axis.model-spec/v1",
						ID:               models.ModelSpecID(res.Name, models.ModelFormatGGUF, weightMB),
						Name:             res.Name,
						Format:           models.ModelFormatGGUF,
						Source:           "resident-model",
						ObservedAt:       time.Now().UTC(),
						Accelerators:     []models.AcceleratorType{models.AcceleratorCUDA, models.AcceleratorMetal, models.AcceleratorROCm, models.AcceleratorCPU},
						SupportedEngines: []string{"llama.cpp"},
						ParallelismModes: []string{"single-node"},
						Memory: models.ModelMemoryRequirements{
							WeightSizeMB:      weightMB,
							ContextOverheadMB: 512,
							RuntimeOverheadMB: 256,
							MinVRAMMB:         0,
							RecommendedVRAMMB: weightMB + 512,
						},
					}
					return spec, nil
				}
			}
		}
	}

	// 3. If input ends with .gguf or has path separators, construct an unobserved spec with default estimate
	base := filepath.Base(input)
	ext := strings.ToLower(filepath.Ext(base))
	if ext == ".gguf" || ext == ".safetensors" || strings.Contains(input, "/") {
		name := strings.TrimSuffix(base, ext)
		spec := models.ModelSpecFromPath(input, 2048*1024*1024)
		spec.Name = name
		return spec, nil
	}

	return models.ModelSpec{}, fmt.Errorf("unable to resolve model spec or weights for %q (not found in local filesystem or cluster disk weights)", input)
}

func runModelStart(ctx context.Context, cmd *cobra.Command, nodeName, weights string, port int, runner modelProcessRunner) error {
	startedAt := time.Now().UTC()
	live, _ := cmd.Flags().GetBool("live")
	cacheAddr, _ := cmd.Flags().GetString("cache-addr")
	if cacheAddr == "" {
		cacheAddr = api.DefaultAddr()
	}
	format, _ := cmd.Flags().GetString("format")
	if format == "" {
		format = "text"
	}
	var (
		snap   *models.ClusterSnapshot
		source string
		err    error
	)
	if live {
		snap, err = loadModelSnapshot(ctx)
		source = "live"
	} else {
		snap, source, err = fetchModelInventorySnapshot(ctx, cacheAddr)
	}
	if err != nil {
		if live {
			return fmt.Errorf("collect live cluster snapshot for model start: %w", err)
		}
		return fmt.Errorf("load cluster snapshot from daemon cache for model start: %w (use --live for an explicit live collection)", err)
	}

	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, nodeName)
	if err != nil {
		return err
	}

	for _, res := range nf.ResidentModels {
		if res.Port == port {
			receipt := models.ModelOperationReceipt{
				Schema:         "axis.model-operation/v1",
				ID:             models.GenerateID("mo"),
				Action:         models.ModelOperationStart,
				Status:         models.ModelOperationRejected,
				Disposition:    "port_occupied",
				Node:           nf.Name,
				Engine:         "llama.cpp",
				Port:           port,
				SnapshotSource: source,
				SnapshotAt:     snap.Timestamp,
				StartedAt:      startedAt,
				CompletedAt:    time.Now().UTC(),
				Error:          fmt.Sprintf("port %d already occupied by resident model %q (%s)", port, res.Name, res.Runtime),
			}
			if snap.Publication != nil {
				receipt.PublicationID = snap.Publication.ID
			}
			_ = writeModelStartReceipt(cmd, receipt, format)
			return ExitCodeError{
				Code:    ExitErrCommandFail,
				Message: fmt.Sprintf("refusing to start model on %s:%d: %s", nf.Name, port, receipt.Error),
			}
		}
	}

	plan, err := modellife.PlanStart(nf, weights, port)
	if err != nil {
		return err
	}

	startErr := runner.Start(ctx, nf, cfgNode, plan)
	if startErr == nil {
		startErr = runner.Probe(ctx, nf, cfgNode, plan.Port)
	}

	executable := "llama-server"
	if len(plan.Argv) > 0 {
		executable = plan.Argv[0]
	}

	receipt := models.ModelOperationReceipt{
		Schema:         "axis.model-operation/v1",
		ID:             models.GenerateID("mo"),
		Action:         models.ModelOperationStart,
		Status:         models.ModelOperationCompleted,
		Disposition:    "started",
		Node:           plan.Node,
		Engine:         "llama.cpp",
		Port:           plan.Port,
		Model:          path.Base(plan.Weights),
		Weights:        plan.Weights,
		Volume:         plan.Volume,
		Executable:     executable,
		SnapshotSource: source,
		SnapshotAt:     snap.Timestamp,
		StartedAt:      startedAt,
		CompletedAt:    time.Now().UTC(),
	}
	if snap.Publication != nil {
		receipt.PublicationID = snap.Publication.ID
	}
	if startErr != nil {
		receipt.Status = models.ModelOperationFailed
		receipt.Disposition = "failed"
		receipt.Error = startErr.Error()
	}

	if writeErr := writeModelStartReceipt(cmd, receipt, format); writeErr != nil {
		return writeErr
	}
	if startErr != nil {
		return fmt.Errorf("started but probe failed: %w", startErr)
	}
	return nil
}

func writeModelStartReceipt(cmd *cobra.Command, receipt models.ModelOperationReceipt, format string) error {
	if format == "json" || format == "yaml" {
		return printOutput(cmd.OutOrStdout(), receipt, format)
	}
	if receipt.Status == models.ModelOperationCompleted {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "started %s on %s:%d volume %s operation %s\n",
			receipt.Executable, receipt.Node, receipt.Port, receipt.Volume, receipt.ID)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s:%d: %s operation %s\n",
		receipt.Disposition, receipt.Node, receipt.Port, receipt.Error, receipt.ID)
	return err
}

func runModelStop(ctx context.Context, cmd *cobra.Command, nodeName string, port int, runner modelProcessRunner) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	cacheAddr, _ := cmd.Flags().GetString("cache-addr")
	if cacheAddr == "" {
		cacheAddr = api.DefaultAddr()
	}
	snap, _, err := fetchModelInventorySnapshot(ctx, cacheAddr)
	if err != nil {
		snap, err = loadModelSnapshot(ctx)
		if err != nil {
			return err
		}
	}
	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, nodeName)
	if err != nil {
		return err
	}
	disposition, err := runner.Stop(ctx, nf, cfgNode, modellife.StopTarget{Port: port})
	if err != nil {
		return err
	}
	// Report what was actually observed. Only "stopped" is a successful
	// lifecycle transition; the rest mean nothing was stopped, so they must
	// not print success, and must fail for shell automation.
	if _, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "%s %s:%d\n", disposition, nf.Name, port); writeErr != nil {
		return writeErr
	}
	if disposition == modelStopStopped {
		return nil
	}
	return ExitCodeError{
		Code:    ExitErrCommandFail,
		Message: fmt.Sprintf("model stop on %s:%d: %s", nf.Name, port, modelStopExplanation(disposition)),
	}
}

func runModelStopGeneration(ctx context.Context, cmd *cobra.Command, generationID, cacheAddr, format string, runner modelProcessRunner) error {
	generationID = strings.TrimSpace(generationID)
	if strings.HasPrefix(generationID, "mi-") {
		return fmt.Errorf("%s is a stable slot ID and cannot authorize a stop; use the instance generation_id", generationID)
	}
	if !strings.HasPrefix(generationID, "mg-") {
		return fmt.Errorf("model generation ID must start with mg-")
	}
	startedAt := time.Now().UTC()
	snap, source, err := fetchModelInventorySnapshot(ctx, cacheAddr)
	if err != nil {
		return fmt.Errorf("load model generation from daemon cache: %w", err)
	}
	inventory := modelinventory.FromSnapshot(snap, source)
	if inventory.PublicationID == "" {
		return fmt.Errorf("daemon cache has no bound publication ID; refusing lifecycle mutation")
	}
	var instance *models.ModelInstance
	for i := range inventory.Instances {
		if inventory.Instances[i].GenerationID == generationID {
			instance = &inventory.Instances[i]
			break
		}
	}
	if instance == nil {
		return fmt.Errorf("model generation %q not found in %s inventory", generationID, sourceOrLive(inventory.Source))
	}
	if instance.NodeStatus != models.StatusComplete {
		return fmt.Errorf("model generation %s is on node %s with status %s; refusing lifecycle mutation", generationID, instance.Node, instance.NodeStatus)
	}
	if instance.Engine != "llama.cpp" {
		return fmt.Errorf("model generation %s uses unsupported stop engine %q", generationID, instance.Engine)
	}
	target := modellife.StopTarget{
		Port:              instance.Port,
		PID:               instance.PID,
		Executable:        instance.Executable,
		ProcessOwner:      instance.ProcessOwner,
		ProcessStartToken: instance.ProcessStartToken,
		GenerationID:      instance.GenerationID,
	}
	if err := target.Validate(); err != nil {
		return fmt.Errorf("model generation %s has incomplete stop evidence: %w", generationID, err)
	}
	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, instance.Node)
	if err != nil {
		return err
	}
	disposition, stopErr := runner.Stop(ctx, nf, cfgNode, target)
	receipt := models.ModelOperationReceipt{
		Schema:         "axis.model-operation/v1",
		ID:             models.GenerateID("mo"),
		Action:         models.ModelOperationStop,
		Status:         modelStopOperationStatus(disposition, stopErr),
		Disposition:    string(disposition),
		InstanceID:     instance.ID,
		GenerationID:   instance.GenerationID,
		Node:           instance.Node,
		Engine:         instance.Engine,
		Port:           instance.Port,
		PID:            instance.PID,
		SnapshotSource: inventory.Source,
		PublicationID:  inventory.PublicationID,
		SnapshotAt:     inventory.ObservedAt,
		StartedAt:      startedAt,
		CompletedAt:    time.Now().UTC(),
	}
	if stopErr != nil {
		receipt.Disposition = "execution_failed"
		receipt.Error = stopErr.Error()
	}
	if writeErr := writeModelOperationReceipt(cmd, receipt, format); writeErr != nil {
		return writeErr
	}
	if stopErr != nil {
		return stopErr
	}
	if disposition == modelStopStopped {
		return nil
	}
	return ExitCodeError{
		Code:    ExitErrCommandFail,
		Message: fmt.Sprintf("model generation stop on %s:%d: %s", instance.Node, instance.Port, modelStopExplanation(disposition)),
	}
}

func modelStopOperationStatus(disposition modelStopDisposition, err error) models.ModelOperationStatus {
	if err != nil {
		return models.ModelOperationFailed
	}
	switch disposition {
	case modelStopStopped:
		return models.ModelOperationCompleted
	case modelStopNotRunning:
		return models.ModelOperationNoOp
	default:
		return models.ModelOperationRejected
	}
}

func writeModelOperationReceipt(cmd *cobra.Command, receipt models.ModelOperationReceipt, format string) error {
	if format == "json" || format == "yaml" {
		return printOutput(cmd.OutOrStdout(), receipt, format)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s:%d generation %s operation %s\n",
		receipt.Disposition, receipt.Node, receipt.Port, receipt.GenerationID, receipt.ID)
	return err
}

func modelStopExplanation(d modelStopDisposition) string {
	switch d {
	case modelStopNotRunning:
		return "no listener was running on that port"
	case modelStopWrongOwner:
		return "the listener is not an axis-managed llama-server; refusing to kill it"
	case modelStopInspectionUnavailable:
		return "port ownership could not be inspected (fuser, lsof, or ps unavailable)"
	case modelStopGenerationMismatch:
		return "the process generation changed or was replaced; refusing to kill it"
	default:
		return string(d)
	}
}

func resolveModelNode(ctx context.Context, name string) (models.NodeFacts, *config.NodeConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return models.NodeFacts{}, nil, fmt.Errorf("node is required")
	}
	snap, err := loadModelSnapshot(ctx)
	if err != nil {
		return models.NodeFacts{}, nil, err
	}
	return resolveModelNodeFromSnapshot(snap, name)
}

func resolveModelNodeFromSnapshot(snap *models.ClusterSnapshot, name string) (models.NodeFacts, *config.NodeConfig, error) {
	if snap == nil {
		return models.NodeFacts{}, nil, fmt.Errorf("no cluster snapshot")
	}
	var nf *models.NodeFacts
	for i := range snap.Nodes {
		if snap.Nodes[i].Name == name {
			nf = &snap.Nodes[i]
			break
		}
	}
	if nf == nil {
		return models.NodeFacts{}, nil, fmt.Errorf("node %s not in snapshot", name)
	}
	cfg, err := loadModelConfig()
	if err != nil {
		return *nf, nil, err
	}
	for i := range cfg.Nodes {
		if cfg.Nodes[i].Name == name {
			return *nf, &cfg.Nodes[i], nil
		}
	}
	if models.IsLocalNode(*nf) {
		return *nf, nil, nil
	}
	return *nf, nil, fmt.Errorf("node %s has no configuration entry", name)
}

type liveModelRunner struct{}

func (liveModelRunner) Start(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, plan modellife.StartPlan) error {
	if len(plan.Argv) == 0 {
		return fmt.Errorf("empty argv")
	}
	script := shellStart(plan.Argv, plan.Port)
	return runOnNode(ctx, node, cfgNode, script)
}

func (liveModelRunner) Stop(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, target modellife.StopTarget) (modelStopDisposition, error) {
	script := shellStopTarget(target)
	out, err := runOnNodeCapturing(ctx, node, cfgNode, script)
	return classifyModelStop(out, err)
}

func (liveModelRunner) Probe(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) error {
	script := shellProbe(port)
	var last error
	for i := 0; i < 10; i++ {
		if err := runOnNode(ctx, node, cfgNode, script); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if last == nil {
		last = fmt.Errorf("probe failed")
	}
	return last
}

func (r liveModelRunner) Await(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, instance models.ModelInstance, opts modellife.AwaitOptions) (models.ModelOperationReceipt, error) {
	opts.ProbeFn = func(probeCtx context.Context) error {
		script := shellProbe(instance.Port)
		return runOnNode(probeCtx, node, cfgNode, script)
	}
	return modellife.AwaitInstance(ctx, instance, opts)
}

func (r liveModelRunner) Query(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, instance models.ModelInstance, req modellife.QueryRequest) (modellife.QueryResult, error) {
	start := time.Now()
	if models.IsLocalNode(node) {
		endpoint := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", instance.Port)
		return modellife.QueryHTTP(ctx, endpoint, req, nil)
	}
	script, err := shellQuery(instance.Port, req)
	if err != nil {
		return modellife.QueryResult{}, err
	}
	out, err := runOnNodeCapturing(ctx, node, cfgNode, script)
	if err != nil {
		return modellife.QueryResult{}, fmt.Errorf("remote query on %s:%d failed: %w (output: %s)", node.Name, instance.Port, err, strings.TrimSpace(out))
	}
	const maxResponseBytes = 10 * 1024 * 1024
	raw := []byte(out)
	if len(raw) > maxResponseBytes {
		raw = raw[:maxResponseBytes]
	}
	return modellife.ParseQueryResponse(raw, time.Since(start), fmt.Sprintf("%s:%d", node.Name, instance.Port))
}

func shellQuery(port int, req modellife.QueryRequest) (string, error) {
	var messages []map[string]string
	if strings.TrimSpace(req.SystemPrompt) != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": req.SystemPrompt,
		})
	}
	messages = append(messages, map[string]string{
		"role":    "user",
		"content": req.Prompt,
	})
	payload := map[string]interface{}{
		"model":    req.Model,
		"messages": messages,
		"stream":   false,
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"curl -fsS -X POST http://127.0.0.1:%d/v1/chat/completions -H 'Content-Type: application/json' -d %s",
		port, shellQuote(string(body)),
	), nil
}

func shellStart(argv []string, port int) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return shellListenerLookup(port) + fmt.Sprintf(
		"if test -n \"$_axis_pids\"; then "+
			"echo \"refusing to start llama-server: port %d already has listener pid(s) $_axis_pids\" >&2; exit 1; fi; "+
			"nohup %s >/dev/null 2>&1 &",
		port, strings.Join(quoted, " "),
	)
}

func shellStop(port int) string {
	return shellStopTarget(modellife.StopTarget{Port: port})
}

func shellStopTarget(target modellife.StopTarget) string {
	port := target.Port
	killCmd := "for _axis_pid in $_axis_pids; do kill -KILL \"$_axis_pid\" || exit $?; done; "
	if target.IsGenerationBound() {
		killCmd = fmt.Sprintf("kill -KILL \"%d\" || exit $?; ", target.PID)
	}
	return shellListenerLookup(port) +
		"if test -z \"$_axis_pids\"; then echo '" + modelStopMarker + "not_running'; exit 0; fi; " +
		shellLlamaServerOwnerGuard(port) +
		shellGenerationGuard(target) +
		killCmd +
		"echo '" + modelStopMarker + "stopped'"
}

func shellGenerationGuard(target modellife.StopTarget) string {
	if !target.IsGenerationBound() {
		return ""
	}
	return fmt.Sprintf(
		"_axis_matched=false; "+
			"for _axis_pid in $_axis_pids; do "+
			"if test \"$_axis_pid\" = \"%d\"; then "+
			"_axis_start=$(ps -p \"$_axis_pid\" -o lstart= 2>/dev/null | awk '{$1=$1; print}' || echo \"\"); "+
			"if test \"$_axis_start\" = %s; then _axis_matched=true; break; fi; "+
			"fi; "+
			"done; "+
			"if test \"$_axis_matched\" != true; then "+
			"echo 'axis model generation mismatch: target pid or start time changed' >&2; "+
			"echo '"+modelStopMarker+"generation_mismatch' >&2; exit 1; fi; ",
		target.PID,
		shellQuote(target.ProcessStartToken),
	)
}

func shellProbe(port int) string {
	return shellListenerLookup(port) + fmt.Sprintf(
		"if test -z \"$_axis_pids\"; then echo \"no listener on port %d\" >&2; exit 1; fi; ",
		port,
	) + shellLlamaServerOwnerGuard(port) + fmt.Sprintf(
		"curl -fsS --max-time 5 http://127.0.0.1:%d/v1/models >/dev/null",
		port,
	)
}

func shellLlamaServerOwnerGuard(port int) string {
	return fmt.Sprintf(
		"if ! command -v ps >/dev/null 2>&1; then echo 'axis model requires ps to verify process ownership' >&2; echo '"+modelStopMarker+"inspection_unavailable' >&2; exit 127; fi; "+
			"for _axis_pid in $_axis_pids; do "+
			"case \"$_axis_pid\" in ''|*[!0-9]*) echo \"refusing invalid listener pid $_axis_pid\" >&2; exit 1;; esac; "+
			"_axis_cmd=$(ps -p \"$_axis_pid\" -o comm=) || exit $?; _axis_cmd=${_axis_cmd##*/}; "+
			"if test \"$_axis_cmd\" != llama-server; then "+
			"echo \"refusing port %d: pid $_axis_pid is $_axis_cmd, not llama-server\" >&2; echo '"+modelStopMarker+"wrong_owner' >&2; exit 1; fi; "+
			"done; ",
		port,
	)
}

func shellListenerLookup(port int) string {
	return fmt.Sprintf(
		"if command -v fuser >/dev/null 2>&1; then "+
			"_axis_pids=$(fuser %d/tcp 2>/dev/null); _axis_rc=$?; "+
			"elif command -v lsof >/dev/null 2>&1; then "+
			"_axis_pids=$(lsof -nP -tiTCP:%d -sTCP:LISTEN); _axis_rc=$?; "+
			"else echo 'axis model requires fuser or lsof to inspect port ownership' >&2; echo '"+modelStopMarker+"inspection_unavailable' >&2; exit 127; fi; "+
			"if test \"$_axis_rc\" -gt 1; then exit \"$_axis_rc\"; fi; "+
			"true; ",
		port, port,
	)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func runOnNode(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, script string) error {
	_, err := runOnNodeCapturing(ctx, node, cfgNode, script)
	return err
}

// runOnNodeCapturing is runOnNode that also returns the command output, so
// callers can read a result marker the script emitted. Both transports return
// combined output, including on failure.
func runOnNodeCapturing(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, script string) (string, error) {
	if models.IsLocalNode(node) {
		ex := transport.NewLocalExecutor()
		return ex.Run(ctx, script)
	}
	if cfgNode == nil {
		return "", fmt.Errorf("node %s has no configuration entry", node.Name)
	}
	spec := cfgNode.SSHDialSpec()
	ex := transport.NewSSHExecutorFromDial(spec.Host, spec.Port, spec.User, spec.DialTimeoutSec, spec.Fallbacks)
	defer ex.Close()
	if err := ex.Connect(ctx); err != nil {
		return "", err
	}
	return ex.Run(ctx, script)
}

// modelStopDisposition is the observed outcome of a stop request. Only
// modelStopStopped is a successful lifecycle transition; the others describe
// states where nothing was stopped, and must not be reported as success.
type modelStopDisposition string

const (
	modelStopStopped               modelStopDisposition = "stopped"
	modelStopNotRunning            modelStopDisposition = "not_running"
	modelStopWrongOwner            modelStopDisposition = "wrong_owner"
	modelStopInspectionUnavailable modelStopDisposition = "inspection_unavailable"
	modelStopGenerationMismatch    modelStopDisposition = "generation_mismatch"
)

// modelStopMarker is emitted by the stop script so the outcome survives both
// the local and SSH transports, neither of which exposes a portable exit
// status to the caller.
const modelStopMarker = "axis-stop-result:"

// classifyModelStop maps script output and error into a typed disposition.
// A missing marker with no error is treated as stopped only when the script
// said so; an unrecognized success is an error, never an assumed success.
func classifyModelStop(out string, err error) (modelStopDisposition, error) {
	switch {
	case strings.Contains(out, modelStopMarker+string(modelStopInspectionUnavailable)):
		return modelStopInspectionUnavailable, nil
	case strings.Contains(out, modelStopMarker+string(modelStopWrongOwner)):
		return modelStopWrongOwner, nil
	case strings.Contains(out, modelStopMarker+string(modelStopGenerationMismatch)):
		return modelStopGenerationMismatch, nil
	case strings.Contains(out, modelStopMarker+string(modelStopNotRunning)):
		return modelStopNotRunning, nil
	case strings.Contains(out, modelStopMarker+string(modelStopStopped)):
		return modelStopStopped, nil
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("model stop produced no recognizable result marker")
}
