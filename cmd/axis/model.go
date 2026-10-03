package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/events"
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

var signalModelDaemonRefresh = func(ctx context.Context, cacheAddr, trigger string) error {
	return refreshDaemonCacheWithTrigger(ctx, cacheAddr, trigger)
}

type modelProcessRunner interface {
	Start(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, plan modellife.StartPlan) error
	Stop(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, target modellife.StopTarget) (modelStopDisposition, error)
	Probe(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) error
	Await(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, instance models.ModelInstance, opts modellife.AwaitOptions) (models.ModelOperationReceipt, error)
	Query(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, instance models.ModelInstance, req modellife.QueryRequest) (modellife.QueryResult, error)
	Evict(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, targets []modellife.EvictTarget, mode modellife.EvictMode) (modellife.EvictResult, error)
	Resume(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, receipt modellife.EvictionReceipt) error
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
	cmd.AddCommand(modelEvictCmd())
	cmd.AddCommand(modelResumeCmd())
	cmd.AddCommand(modelAwaitCmd())
	cmd.AddCommand(modelQueryCmd())
	return cmd
}

func modelPlanCmd() *cobra.Command {
	var cacheAddr, format, writeProfile string
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
	cmd.Flags().StringVar(&writeProfile, "write-profile", "", "Write the selected axis.model-run/v1 profile to this path")
	return cmd
}

func modelStartCmd() *cobra.Command {
	var node, weights, cacheAddr, format, fromPlan, nGPULayers string
	var port, ctxSize, batchSize, ubatchSize, threads int
	var live bool
	cmd := &cobra.Command{
		Use:          "start",
		Short:        "Start llama-server on a named node (explicit port and weights)",
		SilenceUsage: true,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(&format, "text", "json", "yaml")(cmd, args); err != nil {
				return err
			}
			return requireModelStartIdentity(cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			return runModelStart(ctx, cmd, node, weights, port, defaultModelRunner)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Cluster node name (required unless --from-plan supplies it)")
	cmd.Flags().StringVar(&weights, "weights", "", "GGUF path on a named local volume (required unless --from-plan supplies it)")
	cmd.Flags().IntVar(&port, "port", 0, "Listen port (required unless --from-plan has an explicit port)")
	cmd.Flags().StringVar(&fromPlan, "from-plan", "", "Read an axis.model-run/v1 profile JSON file")
	cmd.Flags().StringVar(&nGPULayers, "n-gpu-layers", "", "llama-server -ngl value: an integer >= 1, auto, or all")
	cmd.Flags().IntVar(&ctxSize, "ctx-size", 0, "llama-server context length (-c); omitted when unset")
	cmd.Flags().IntVar(&batchSize, "batch-size", 0, "llama-server logical batch size (-b); omitted when unset")
	cmd.Flags().IntVar(&ubatchSize, "ubatch-size", 0, "llama-server physical batch size (-ub); omitted when unset")
	cmd.Flags().IntVar(&threads, "threads", 0, "llama-server threads (-t); must be within observed CPU cores")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS daemon cache")
	cmd.Flags().BoolVar(&live, "live", false, "Bypass daemon cache and perform live fleet discovery")
	cmd.Flags().StringVar(&format, "format", "text", "Start operation receipt format: text, json, or yaml")
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

func modelEvictCmd() *cobra.Command {
	var node, cacheAddr, format, mode string
	var gpuIndex int
	var all, live bool
	cmd := &cobra.Command{
		Use:          "evict [target-spec]",
		Short:        "Preempt resident models from GPU and neutralize supervisor restart loops",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			targetSpec := ""
			if len(args) == 1 {
				targetSpec = args[0]
			}
			return runModelEvict(ctx, cmd, targetSpec, node, gpuIndex, all, mode, live, cacheAddr, format, defaultModelRunner)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Target cluster node (defaults to local node)")
	cmd.Flags().IntVar(&gpuIndex, "gpu", -1, "Target models occupying a specific physical GPU index")
	cmd.Flags().BoolVar(&all, "all", false, "Evict all resident models across all GPUs on the target node")
	cmd.Flags().StringVar(&mode, "mode", "stop", "Eviction strategy: stop (supervisor stop), freeze (cgroup freeze), force")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS daemon cache")
	cmd.Flags().BoolVar(&live, "live", true, "Select targets from a fresh snapshot. Set --live=false to use the daemon cache")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	return cmd
}

func modelResumeCmd() *cobra.Command {
	var receiptID, node, cacheAddr, format string
	var timeout time.Duration
	var live bool
	cmd := &cobra.Command{
		Use:          "resume [target-spec]",
		Short:        "Restore previously evicted models from receipt or unit name",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json", "yaml"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout+10*time.Second)
			defer cancel()
			targetSpec := ""
			if len(args) == 1 {
				targetSpec = args[0]
			}
			return runModelResume(ctx, cmd, targetSpec, receiptID, node, timeout, live, cacheAddr, format, defaultModelRunner)
		},
	}
	cmd.Flags().StringVar(&receiptID, "receipt", "", "Restore models using an eviction receipt ID")
	cmd.Flags().StringVar(&node, "node", "", "Target cluster node")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "Maximum time to wait for model readiness probe")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS daemon cache")
	cmd.Flags().BoolVar(&live, "live", false, "Bypass daemon cache and perform live fleet discovery")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
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

	snap, source, err := loadModelCommandSnapshot(ctx, live, cacheAddr, "await", false)
	if err != nil {
		return err
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
	snap, source, err := loadModelCommandSnapshot(ctx, live, cacheAddr, "query", false)
	if err != nil {
		return err
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

// loadModelCommandSnapshot is the single snapshot-acquisition seam for the
// model subcommands: cache-first unless --live, with an explicit opt-in
// fallback to live collection for advisory planning (plan). Mutating or
// target-executing commands (start/stop/await/query) never fall back silently —
// they must run against the snapshot the daemon published, or fail loudly.
// model evict passes live=true by default because it kills the selected PIDs.
// --live=false still fails closed when the cache is missing.
func loadModelCommandSnapshot(ctx context.Context, live bool, cacheAddr, command string, allowLiveFallback bool) (*models.ClusterSnapshot, string, error) {
	if live {
		snap, err := loadModelSnapshot(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("collect live cluster snapshot for model %s: %w", command, err)
		}
		return snap, "live", nil
	}
	snap, source, err := fetchModelInventorySnapshot(ctx, cacheAddr)
	if err == nil {
		return snap, source, nil
	}
	if !allowLiveFallback {
		return nil, "", fmt.Errorf("load cluster snapshot from daemon cache for model %s: %w (use --live for an explicit live collection)", command, err)
	}
	// Auto-degrade: an advisory dry-run should not hard-fail just because
	// this node runs no daemon (status already falls back to live). The
	// snapshot source is printed with the output, so the operator always
	// sees which path produced it.
	fallback, liveErr := loadModelSnapshot(ctx)
	if liveErr != nil {
		return nil, "", fmt.Errorf("load cluster snapshot from daemon cache for model %s: %w (live collection also failed: %v; use --live for an explicit live collection)", command, err, liveErr)
	}
	return fallback, "live-fallback", nil
}

func runModelPlan(ctx context.Context, cmd *cobra.Command, specOrWeights string, port int, live bool, cacheAddr, format string) error {
	snap, source, err := loadModelCommandSnapshot(ctx, live, cacheAddr, "plan", true)
	if err != nil {
		return err
	}

	spec, err := resolveModelSpec(specOrWeights, snap)
	if err != nil {
		// An unresolvable spec is a "no placement target exists" condition,
		// not a generic crash: reuse the taxonomy's dedicated code.
		return ExitCodeError{Code: ExitErrNoNodesFit, Message: err.Error()}
	}

	plan, err := modelplan.PlanSingleNode(snap, spec, port)
	if err != nil {
		return err
	}
	plan.SnapshotSource = source
	if cmd.Flags().Changed("port") && plan.Selected != nil {
		plan.Selected.PortSource = models.PortSourceExplicit
	}
	if err := writeSelectedRunProfile(cmd, plan.Selected); err != nil {
		return err
	}

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
	profile, err := profileForModelStart(cmd, nodeName, weights, port)
	if err != nil {
		return err
	}
	snap, source, err := loadModelCommandSnapshot(ctx, live, cacheAddr, "start", false)
	if err != nil {
		return err
	}
	if profile.SnapshotPublicationID == "" && snap.Publication != nil {
		profile.SnapshotPublicationID = snap.Publication.ID
	}

	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, profile.Node)
	if err != nil {
		return err
	}

	for _, res := range nf.ResidentModels {
		if res.Port == profile.Port {
			receipt := models.ModelOperationReceipt{
				Schema:         "axis.model-operation/v1",
				ID:             models.GenerateID("mo"),
				Action:         models.ModelOperationStart,
				Status:         models.ModelOperationRejected,
				Disposition:    "port_occupied",
				Node:           nf.Name,
				Engine:         profile.Engine,
				Port:           profile.Port,
				SnapshotSource: source,
				SnapshotAt:     snap.Timestamp,
				StartedAt:      startedAt,
				CompletedAt:    time.Now().UTC(),
				Error:          fmt.Sprintf("port %d already occupied by resident model %q (%s)", profile.Port, res.Name, res.Runtime),
			}
			if snap.Publication != nil {
				receipt.PublicationID = snap.Publication.ID
			}
			_ = writeModelStartReceipt(cmd, receipt, format)
			return ExitCodeError{
				Code:    ExitErrCommandFail,
				Message: fmt.Sprintf("refusing to start model on %s:%d: %s", nf.Name, profile.Port, receipt.Error),
			}
		}
	}

	plan, err := modellife.PlanStartProfile(nf, profile)
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
		Schema:           "axis.model-operation/v1",
		ID:               models.GenerateID("mo"),
		Action:           models.ModelOperationStart,
		Status:           models.ModelOperationCompleted,
		Disposition:      "started",
		Node:             plan.Node,
		Engine:           plan.Profile.Engine,
		Port:             plan.Port,
		Model:            path.Base(plan.Weights),
		Weights:          plan.Weights,
		Volume:           plan.Volume,
		Executable:       executable,
		SnapshotSource:   source,
		SnapshotAt:       snap.Timestamp,
		StartedAt:        startedAt,
		CompletedAt:      time.Now().UTC(),
		SpecSource:       plan.Profile.SpecSource,
		DeviceKind:       plan.Profile.DeviceKind,
		DeviceIndex:      plan.Profile.DeviceIndex,
		VRAMFreeMeasured: plan.Profile.VRAMFreeMeasured,
		PortSource:       plan.Profile.PortSource,
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
	warnModelDaemonRefresh(cmd, cacheAddr, "manual")
	return nil
}

func warnModelDaemonRefresh(cmd *cobra.Command, cacheAddr, trigger string) {
	if err := signalModelDaemonRefresh(context.Background(), cacheAddr, trigger); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: daemon cache refresh failed: %v\n", err)
	}
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
	nf, cfgNode, err := resolveModelStopTargetNode(ctx, nodeName, cacheAddr)
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
		warnModelDaemonRefresh(cmd, cacheAddr, "manual")
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
		SupervisorType:    instance.SupervisorType,
		SupervisorUnit:    instance.SupervisorUnit,
		GPUIndices:        append([]int(nil), instance.GPUIndices...),
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
		warnModelDaemonRefresh(cmd, cacheAddr, "manual")
		return nil
	}
	return ExitCodeError{
		Code:    ExitErrCommandFail,
		Message: fmt.Sprintf("model generation stop on %s:%d: %s", instance.Node, instance.Port, modelStopExplanation(disposition)),
	}
}

func runModelEvict(ctx context.Context, cmd *cobra.Command, targetSpec, nodeName string, gpuIndex int, all bool, modeStr string, live bool, cacheAddr, format string, runner modelProcessRunner) error {
	startedAt := time.Now().UTC()
	mode := modellife.EvictMode(strings.TrimSpace(modeStr))
	if mode == "" {
		mode = modellife.EvictModeStop
	}

	snap, source, err := loadModelCommandSnapshot(ctx, live, cacheAddr, "evict", false)
	if err != nil {
		return err
	}

	inventory := modelinventory.FromSnapshot(snap, source)
	if len(inventory.Instances) == 0 {
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: "no resident model instances found",
		}
	}

	var candidates []models.ModelInstance
	for _, inst := range inventory.Instances {
		if nodeName != "" && !strings.EqualFold(inst.Node, nodeName) {
			continue
		}
		matchesTarget := targetSpec != "" && (strings.EqualFold(inst.ID, targetSpec) ||
			strings.EqualFold(inst.GenerationID, targetSpec) ||
			strings.EqualFold(inst.Model, targetSpec) ||
			strings.EqualFold(inst.SupervisorUnit, targetSpec) ||
			(inst.Port > 0 && targetSpec == strconv.Itoa(inst.Port)))
		// --gpu is a filter on whatever target or --all selected. It also
		// selects on its own. An index match is required whenever it is set,
		// including when --all would otherwise keep every resident.
		if !matchesTarget && !all && gpuIndex < 0 {
			continue
		}
		if gpuIndex >= 0 {
			matchesGPU := false
			for _, g := range inst.GPUIndices {
				if g == gpuIndex {
					matchesGPU = true
					break
				}
			}
			if !matchesGPU {
				continue
			}
		}
		candidates = append(candidates, inst)
	}

	if len(candidates) == 0 {
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: fmt.Sprintf("no resident model instances matched evict criteria (target: %q, node: %q, gpu: %d, all: %v)", targetSpec, nodeName, gpuIndex, all),
		}
	}

	targetNodeName := candidates[0].Node
	for _, c := range candidates {
		if c.Node != targetNodeName {
			return ExitCodeError{
				Code:    ExitErrCommandFail,
				Message: "eviction across multiple nodes in a single command is not supported; target nodes individually with --node",
			}
		}
	}

	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, targetNodeName)
	if err != nil {
		return err
	}

	var targets []modellife.EvictTarget
	var evictedInsts []modellife.EvictedInstanceReceipt
	for _, inst := range candidates {
		targets = append(targets, modellife.EvictTarget{
			InstanceID:        inst.ID,
			GenerationID:      inst.GenerationID,
			Model:             inst.Model,
			Port:              inst.Port,
			PID:               inst.PID,
			Executable:        inst.Executable,
			ProcessOwner:      inst.ProcessOwner,
			ProcessStartToken: inst.ProcessStartToken,
			SupervisorType:    inst.SupervisorType,
			SupervisorUnit:    inst.SupervisorUnit,
			GPUIndices:        append([]int(nil), inst.GPUIndices...),
			WeightSizeMB:      inst.WeightSizeMB,
			SizeVRAMMB:        inst.SizeVRAMMB,
		})

		evictedInsts = append(evictedInsts, modellife.EvictedInstanceReceipt{
			InstanceID:     inst.ID,
			Model:          inst.Model,
			Port:           inst.Port,
			PID:            inst.PID,
			GPUIndices:     append([]int(nil), inst.GPUIndices...),
			SupervisorType: inst.SupervisorType,
			SupervisorUnit: inst.SupervisorUnit,
		})
	}

	result, evictErr := runner.Evict(ctx, nf, cfgNode, targets, mode)
	reclaimedMB := int64(0)
	vramObserved := false
	// Freeze keeps the allocation. A node-wide delta is not split across
	// instances, and an unmeasured stop must not reuse the snapshot size.
	if evictErr == nil && mode != modellife.EvictModeFreeze && result.VRAMMeasured {
		reclaimedMB = result.ReclaimedVRAMMB
		vramObserved = true
		if len(evictedInsts) == 1 {
			evictedInsts[0].VRAMFreedMB = reclaimedMB
		}
	}

	receipt := modellife.EvictionReceipt{
		Schema:           "axis.eviction-receipt/v1",
		ID:               models.GenerateID("mo"),
		Node:             nf.Name,
		Action:           "evict",
		Mode:             string(mode),
		Status:           models.ModelOperationCompleted,
		Disposition:      "evicted",
		ReclaimedVRAMMB:  reclaimedMB,
		VRAMObserved:     vramObserved,
		DurationMS:       time.Since(startedAt).Milliseconds(),
		EvictedInstances: evictedInsts,
		SnapshotSource:   source,
		StartedAt:        startedAt,
		CompletedAt:      time.Now().UTC(),
	}
	if snap.Publication != nil {
		receipt.PublicationID = snap.Publication.ID
	}
	if evictErr != nil {
		receipt.Status = models.ModelOperationFailed
		receipt.Disposition = "failed"
		receipt.Error = evictErr.Error()
	} else {
		receipt.ResumeCommand = fmt.Sprintf("axis model resume --receipt %s", receipt.ID)
		if _, saveErr := modellife.SaveEvictionReceipt(receipt); saveErr != nil {
			receipt.Status = models.ModelOperationFailed
			receipt.Disposition = "failed"
			receipt.Error = saveErr.Error()
			evictErr = saveErr
		} else {
			events.EmitToBuffer(nil, events.EventModelEvicted, map[string]any{
				"receipt_id":        receipt.ID,
				"node":              receipt.Node,
				"reclaimed_vram_mb": receipt.ReclaimedVRAMMB,
				"evicted_instances": len(receipt.EvictedInstances),
			})
		}
	}

	if writeErr := writeModelEvictReceipt(cmd, receipt, format); writeErr != nil {
		return writeErr
	}
	if evictErr != nil {
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: fmt.Sprintf("eviction failed on %s: %v", nf.Name, evictErr),
		}
	}
	warnModelDaemonRefresh(cmd, cacheAddr, "manual")
	return nil
}

func runModelResume(ctx context.Context, cmd *cobra.Command, targetSpec, receiptID, nodeName string, timeout time.Duration, live bool, cacheAddr, format string, runner modelProcessRunner) error {
	startedAt := time.Now().UTC()
	if strings.TrimSpace(receiptID) == "" && strings.TrimSpace(targetSpec) == "" {
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: "must specify a target-spec or --receipt <id> to resume",
		}
	}

	snap, source, err := loadModelCommandSnapshot(ctx, live, cacheAddr, "resume", false)
	if err != nil {
		return err
	}

	var receipt *modellife.EvictionReceipt
	if strings.TrimSpace(receiptID) != "" {
		loaded, loadErr := modellife.LoadEvictionReceipt(receiptID)
		if loadErr != nil {
			return ExitCodeError{
				Code:    ExitErrCommandFail,
				Message: fmt.Sprintf("unable to load receipt %q: %v", receiptID, loadErr),
			}
		}
		receipt = loaded
	} else {
		unit := targetSpec
		if !strings.HasSuffix(unit, ".service") {
			unit += ".service"
		}
		inventory := modelinventory.FromSnapshot(snap, source)
		var found *models.ModelInstance
		for i := range inventory.Instances {
			inst := &inventory.Instances[i]
			if nodeName != "" && !strings.EqualFold(inst.Node, nodeName) {
				continue
			}
			if strings.EqualFold(inst.SupervisorUnit, unit) || strings.EqualFold(inst.SupervisorUnit, targetSpec) {
				found = inst
				break
			}
		}
		if found == nil || strings.TrimSpace(found.SupervisorType) == "" {
			return ExitCodeError{
				Code:    ExitErrCommandFail,
				Message: "supervisor type unknown",
			}
		}
		receipt = &modellife.EvictionReceipt{
			Schema: "axis.eviction-receipt/v1",
			ID:     models.GenerateID("mo"),
			Node:   found.Node,
			Action: "resume",
			EvictedInstances: []modellife.EvictedInstanceReceipt{
				{
					InstanceID:     found.ID,
					Model:          found.Model,
					Port:           found.Port,
					PID:            found.PID,
					SupervisorType: found.SupervisorType,
					SupervisorUnit: found.SupervisorUnit,
				},
			},
		}
	}

	targetNode := receipt.Node
	if targetNode == "" {
		targetNode = nodeName
	}

	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, targetNode)
	if err != nil {
		return err
	}

	resumeErr := runner.Resume(ctx, nf, cfgNode, *receipt)
	if resumeErr == nil && ctx.Err() != nil {
		resumeErr = ctx.Err()
	}
	if resumeErr == nil {
		for _, inst := range receipt.EvictedInstances {
			if inst.Port < 1 || inst.Port > 65535 {
				continue
			}
			opts := modellife.AwaitOptions{
				Timeout:        timeout,
				SnapshotSource: source,
				SnapshotAt:     snap.Timestamp,
			}
			if snap.Publication != nil {
				opts.PublicationID = snap.Publication.ID
			}
			_, awaitErr := runner.Await(ctx, nf, cfgNode, models.ModelInstance{
				ID:             inst.InstanceID,
				Model:          inst.Model,
				Node:           nf.Name,
				Port:           inst.Port,
				PID:            inst.PID,
				SupervisorType: inst.SupervisorType,
				SupervisorUnit: inst.SupervisorUnit,
			}, opts)
			if awaitErr != nil {
				resumeErr = awaitErr
				break
			}
			if ctx.Err() != nil {
				resumeErr = ctx.Err()
				break
			}
		}
	}

	resumedReceipt := modellife.EvictionReceipt{
		Schema:           "axis.eviction-receipt/v1",
		ID:               models.GenerateID("mo"),
		Node:             nf.Name,
		Action:           "resume",
		Status:           models.ModelOperationCompleted,
		Disposition:      "resumed",
		DurationMS:       time.Since(startedAt).Milliseconds(),
		EvictedInstances: receipt.EvictedInstances,
		SnapshotSource:   source,
		StartedAt:        startedAt,
		CompletedAt:      time.Now().UTC(),
	}
	if snap.Publication != nil {
		resumedReceipt.PublicationID = snap.Publication.ID
	}
	if resumeErr != nil {
		resumedReceipt.Status = models.ModelOperationFailed
		resumedReceipt.Disposition = "failed"
		resumedReceipt.Error = resumeErr.Error()
	} else {
		events.EmitToBuffer(nil, events.EventModelResumed, map[string]any{
			"receipt_id": receipt.ID,
			"node":       nf.Name,
		})
	}

	if writeErr := writeModelEvictReceipt(cmd, resumedReceipt, format); writeErr != nil {
		return writeErr
	}
	if resumeErr != nil {
		return ExitCodeError{
			Code:    ExitErrCommandFail,
			Message: fmt.Sprintf("resume failed on %s: %v", nf.Name, resumeErr),
		}
	}
	warnModelDaemonRefresh(cmd, cacheAddr, "manual")
	return nil
}

func writeModelEvictReceipt(cmd *cobra.Command, receipt modellife.EvictionReceipt, format string) error {
	if format == "json" || format == "yaml" {
		return printOutput(cmd.OutOrStdout(), receipt, format)
	}
	if receipt.Status == models.ModelOperationCompleted {
		if receipt.Action == "resume" {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "resumed on %s in %dms operation %s\n",
				receipt.Node, receipt.DurationMS, receipt.ID)
			return err
		}
		targetDesc := fmt.Sprintf("%d instance(s)", len(receipt.EvictedInstances))
		if len(receipt.EvictedInstances) == 1 {
			targetDesc = receipt.EvictedInstances[0].Model
		}
		switch {
		case receipt.Mode == string(modellife.EvictModeFreeze):
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "evicted %s on %s (reclaimed 0 MiB in %dms) receipt %s\n",
				targetDesc, receipt.Node, receipt.DurationMS, receipt.ID)
			return err
		case receipt.VRAMObserved:
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "evicted %s on %s (reclaimed %d MiB observed in %dms) receipt %s\n",
				targetDesc, receipt.Node, receipt.ReclaimedVRAMMB, receipt.DurationMS, receipt.ID)
			return err
		default:
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "evicted %s on %s (VRAM unmeasured in %dms) receipt %s\n",
				targetDesc, receipt.Node, receipt.DurationMS, receipt.ID)
			return err
		}
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s operation %s\n",
		receipt.Disposition, receipt.Node, receipt.Error, receipt.ID)
	return err
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

func resolveFromDaemonCache(ctx context.Context, cacheAddr, nodeName string, targetCfg *config.NodeConfig) (models.NodeFacts, *config.NodeConfig, bool) {
	if cacheAddr == "" {
		cacheAddr = api.DefaultAddr()
	}
	snap, _, err := fetchModelInventorySnapshot(ctx, cacheAddr)
	if err != nil || snap == nil {
		return models.NodeFacts{}, nil, false
	}
	if nodeName == "" {
		localNF, ok := models.FindLocalNode(snap.Nodes)
		if !ok {
			return models.NodeFacts{}, nil, false
		}
		return localNF, targetCfg, true
	}
	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, nodeName)
	if err != nil {
		return models.NodeFacts{}, nil, false
	}
	return nf, cfgNode, true
}

func makeLocalNodeFacts(nodeName, role string) models.NodeFacts {
	localHostname, _ := os.Hostname()
	if nodeName == "" {
		if localHostname != "" {
			nodeName = localHostname
		} else {
			nodeName = "local"
		}
	}
	return models.NodeFacts{
		Name:     nodeName,
		Role:     role,
		Hostname: localHostname,
		Identity: &models.NodeIdentity{
			StableID: models.CurrentLocalStableID(),
		},
		Addresses: []models.NetworkAddress{
			{Address: "127.0.0.1", Scope: "loopback"},
		},
	}
}

func resolveModelStopTargetNode(ctx context.Context, nodeName, cacheAddr string) (models.NodeFacts, *config.NodeConfig, error) {
	nodeName = strings.TrimSpace(nodeName)
	cfg, _ := loadModelConfig()

	var targetCfg *config.NodeConfig
	if cfg != nil {
		for i := range cfg.Nodes {
			if (nodeName == "" && cfg.Nodes[i].IsLocal()) || (nodeName != "" && cfg.Nodes[i].Name == nodeName) {
				targetCfg = &cfg.Nodes[i]
				break
			}
		}
	}
	if nodeName == "" && targetCfg != nil {
		nodeName = targetCfg.Name
	}

	if nf, cfgNode, ok := resolveFromDaemonCache(ctx, cacheAddr, nodeName, targetCfg); ok {
		return nf, cfgNode, nil
	}

	isLocal := (targetCfg != nil && targetCfg.IsLocal()) ||
		nodeName == "" ||
		models.IsLocalTarget(nodeName, "") ||
		nodeName == "localhost" ||
		nodeName == "127.0.0.1"

	if isLocal {
		role := ""
		if targetCfg != nil {
			role = targetCfg.Role
		}
		return makeLocalNodeFacts(nodeName, role), targetCfg, nil
	}

	if nodeName == "" {
		return models.NodeFacts{}, nil, fmt.Errorf("node is required")
	}
	snap, err := loadModelSnapshot(ctx)
	if err != nil {
		return models.NodeFacts{}, nil, err
	}
	return resolveModelNodeFromSnapshot(snap, nodeName)
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
	if err := modellife.ExecArgvMatchesProfile(plan); err != nil {
		return err
	}
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

func (liveModelRunner) Evict(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, targets []modellife.EvictTarget, mode modellife.EvictMode) (modellife.EvictResult, error) {
	script := modellife.BuildEvictShellScript(targets, mode)
	out, err := runOnNodeCapturing(ctx, node, cfgNode, script)
	if err != nil {
		return modellife.EvictResult{}, fmt.Errorf("evict on %s failed: %w (output: %s)", node.Name, err, strings.TrimSpace(out))
	}
	result, err := modellife.ParseEvictOutput(out)
	if err != nil {
		return modellife.EvictResult{}, fmt.Errorf("evict on %s: %w (output: %s)", node.Name, err, strings.TrimSpace(out))
	}
	return result, nil
}

func (liveModelRunner) Resume(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, receipt modellife.EvictionReceipt) error {
	script := modellife.BuildResumeShellScript(receipt)
	out, err := runOnNodeCapturing(ctx, node, cfgNode, script)
	if err != nil {
		return fmt.Errorf("resume on %s failed: %w (output: %s)", node.Name, err, strings.TrimSpace(out))
	}
	if !strings.Contains(out, modellife.EvictMarkerOk) {
		return fmt.Errorf("resume on %s did not emit confirmation marker (output: %s)", node.Name, strings.TrimSpace(out))
	}
	return nil
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

func shellStopTarget(target modellife.StopTarget) string {
	port := target.Port
	killCmd := "for _axis_pid in $_axis_pids; do kill -KILL \"$_axis_pid\" || exit $?; done; "
	if target.IsGenerationBound() {
		killCmd = fmt.Sprintf("kill -KILL \"%d\" || exit $?; ", target.PID)
	}
	supervisorCmd := ""
	if strings.TrimSpace(target.SupervisorUnit) != "" {
		unit := strings.TrimSpace(target.SupervisorUnit)
		if target.SupervisorType == "systemd-user" {
			supervisorCmd = fmt.Sprintf("systemctl --user stop %s 2>/dev/null || true; ", shellQuote(unit))
		} else if target.SupervisorType == "systemd-system" {
			supervisorCmd = fmt.Sprintf("systemctl stop %s 2>/dev/null || true; ", shellQuote(unit))
		}
	}
	return shellListenerLookup(port) +
		"if test -z \"$_axis_pids\"; then echo '" + modelStopMarker + "not_running'; exit 0; fi; " +
		shellLlamaServerOwnerGuard(port) +
		shellGenerationGuard(target) +
		supervisorCmd +
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
