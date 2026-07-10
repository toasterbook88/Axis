package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/lockutil"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/ui"
	"github.com/toasterbook88/axis/internal/workload"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

const llmCloudFallbackTimeout = 5 * time.Second

type llmInferenceResult struct {
	reqs models.TaskRequirements
	sig  llmrouter.IntentSignal
}

var (
	loadLLMConfig             = config.Load
	llmConfigPath             = config.DefaultConfigPath
	buildLLMRegistry          = llmrouter.NewRegistryFromConfig
	selectLLMCloudFallback    = llmrouter.SelectCloudFallback
	llmClassifyWithProvider   = llmrouter.ClassifyWithProvider
	confirmLLMCloudFallback   = defaultConfirmLLMCloudFallback
	llmSelectModelInteractive = selectModelInteractive
	llmIsTerminal             = func(fd int) bool { return term.IsTerminal(fd) }
	llmWriterIsTerminal       = func(w io.Writer) bool {
		if f, ok := w.(*os.File); ok {
			return term.IsTerminal(int(f.Fd()))
		}
		return false
	}
	llmWarmupDelay = 200 * time.Millisecond
)

var llmInferRequirementsFn = func(prompt string, engine *llmrouter.Engine) llmInferenceResult {
	class, sig, _ := engine.Classify(context.Background(), prompt, "")
	match := models.WorkloadProfileMatch{
		Class: class,
		Notes: append([]string(nil), sig.Notes...),
	}
	reqs := placement.InferRequirements(prompt, workload.InferRequirementsOptions{
		Match: &match,
	})
	if sig.Class == "" {
		sig.Class = reqs.Workload.Class
		sig.Source = llmrouter.SourceReflex
	}
	return llmInferenceResult{reqs: reqs, sig: sig}
}

// llmResult is the structured output for axis llm. Exported fields allow
// printOutput to marshal it to JSON/YAML.
type llmResult struct {
	Prompt       string          `json:"prompt"                  yaml:"prompt"`
	Class        string          `json:"class"                   yaml:"class"`
	Confidence   float64         `json:"confidence"              yaml:"confidence"`
	Source       string          `json:"source"                  yaml:"source"`
	Signals      []string        `json:"signals,omitempty"       yaml:"signals,omitempty"`
	Notes        []string        `json:"notes,omitempty"         yaml:"notes,omitempty"`
	Requirements llmRequirements `json:"requirements"            yaml:"requirements"`
}

type llmRequirements struct {
	MinFreeRAMMB      int64    `json:"min_free_ram_mb"               yaml:"min_free_ram_mb"`
	RequiredTools     []string `json:"required_tools,omitempty"      yaml:"required_tools,omitempty"`
	PreferredBackends []string `json:"preferred_backends,omitempty"  yaml:"preferred_backends,omitempty"`
	PrefersTurboQuant bool     `json:"prefers_turbo_quant,omitempty" yaml:"prefers_turbo_quant,omitempty"`
	ContextWindow     int      `json:"context_window_tokens,omitempty" yaml:"context_window_tokens,omitempty"`
}

func llmCmd() *cobra.Command {
	var (
		model    string
		endpoint string
		timeout  time.Duration
		format   string
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "llm <prompt>",
		Short: "Classify a prompt and show hybrid AI router requirements",
		Long: "Classifies a task prompt using a local LLM (via Ollama) into a WorkloadClass\n" +
			"and derives hardware requirements for placement.\n\n" +
			"The classifier uses a lightweight local model (default: granite3.1-moe:1b)\n" +
			"with a hard latency budget. If the local model is unavailable or too slow,\n" +
			"AXIS can confirm a cloud fallback from configured providers before using\n" +
			"the legacy string-matcher result.\n\n" +
			"Output is advisory only — use `axis task place` for full placement decisions.\n\n" +
			"Classification sources:\n" +
			"  semantic  — a local or confirmed cloud LLM classified the prompt\n" +
			"  reflex    — legacy string-matcher used (LLM unavailable or declined)",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := args[0]
			w := cmd.OutOrStdout()
			errW := cmd.ErrOrStderr()

			// Build engine with CLI-provided overrides.
			engineOpts := []llmrouter.Option{
				llmrouter.WithTimeout(timeout),
			}
			if endpoint != "" {
				engineOpts = append(engineOpts, llmrouter.WithEndpoint(endpoint))
			}
			if model != "" {
				engineOpts = append(engineOpts, llmrouter.WithModel(model))
			}
			engine := llmrouter.NewEngine(engineOpts...)

			sp := ui.NewSpinner()
			sp.Start("Classifying locally...")
			inference := llmInferRequirementsFn(prompt, engine)
			sp.Stop("")
			localModelName := model
			if localModelName == "" {
				localModelName = "granite3.1-moe:1b"
			}
			inference = maybeLLMCloudFallback(cmd.Context(), prompt, inference, cmd.InOrStdin(), errW, localModelName)

			result := llmResult{
				Prompt:     prompt,
				Class:      string(inference.reqs.Workload.Class),
				Confidence: inference.sig.Confidence,
				Source:     string(inference.sig.Source),
				Signals:    inference.sig.Signals,
				Notes:      inference.sig.Notes,
				Requirements: llmRequirements{
					MinFreeRAMMB:      inference.reqs.MinFreeRAMMB,
					RequiredTools:     inference.reqs.RequiredTools,
					PreferredBackends: inference.reqs.PreferredBackends,
					PrefersTurboQuant: inference.reqs.PrefersTurboQuant,
					ContextWindow:     inference.reqs.ContextWindowTokens,
				},
			}

			if format == "json" || format == "yaml" {
				return printOutput(cmd.OutOrStdout(), result, format)
			}

			// Human-readable output.
			if dryRun {
				fmt.Fprintln(errW, ui.Dim("dry-run: classification only, no execution"))
			}
			printLLMResult(w, result)
			fmt.Fprintln(errW, ui.Dim("advisory: use axis task place for full placement decisions"))
			return nil
		},
	}

	cmd.Flags().StringVarP(&model, "model", "m", "", "Local classifier model (default: granite3.1-moe:1b)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Ollama endpoint (default: http://localhost:11434)")
	cmd.Flags().DurationVar(&timeout, "timeout", 150*time.Millisecond, "Classifier latency budget")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, yaml")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show routing decision without executing (classification preview only)")

	cmd.AddCommand(llmSelectCmd())
	cmd.AddCommand(llmConfigureCmd())

	return cmd
}

func maybeLLMCloudFallback(ctx context.Context, prompt string, current llmInferenceResult, in io.Reader, errW io.Writer, localModel string) llmInferenceResult {
	if current.sig.Source != llmrouter.SourceReflex {
		return current
	}

	cfg, err := loadLLMConfig(llmConfigPath())
	if err != nil {
		return current
	}

	registry, err := buildLLMRegistry(cfg)
	if err != nil || registry.Len() == 0 {
		return current
	}

	prefer := ""
	maxCost := 0.0
	if cfg.Inference != nil {
		prefer = cfg.Inference.Prefer
		maxCost = cfg.Inference.MaxCostPerRequest
	}

	selectCtx, cancel := context.WithTimeout(ctx, llmCloudFallbackTimeout)
	provider, decision, err := selectLLMCloudFallback(selectCtx, registry, prompt, prefer)
	cancel()
	if err != nil {
		return current
	}

	if maxCost > 0 && decision.EstCost > maxCost {
		return appendLLMNote(current,
			fmt.Sprintf("cloud fallback skipped: estimated cost $%.4f exceeds max $%.4f", decision.EstCost, maxCost))
	}

	nodeName := resolveLocalNodeName(ctx)
	showWarmupAndOOMAlert(errW, localModel, nodeName)

	if !confirmLLMCloudFallback(in, errW, decision) {
		return appendLLMNote(current,
			fmt.Sprintf("cloud fallback skipped: operator declined %s/%s", decision.Provider, decision.Model))
	}

	fmt.Fprintf(errW, "🔀 Fallback: Redirecting execution to %s (Cloud)...\n", decision.Model)

	sendCtx, cancel := context.WithTimeout(ctx, llmCloudFallbackTimeout)
	class, sig, err := llmClassifyWithProvider(sendCtx, provider, prompt, decision.Model)
	cancel()
	if err != nil {
		return appendLLMNote(current, fmt.Sprintf("cloud fallback failed: %v", err))
	}

	sig.Notes = append([]string(nil), current.sig.Notes...)
	sig.Notes = append(sig.Notes, fmt.Sprintf("cloud fallback via %s/%s", decision.Provider, decision.Model))

	match := models.WorkloadProfileMatch{
		Class: class,
		Notes: append([]string(nil), sig.Notes...),
	}
	reqs := placement.InferRequirements(prompt, workload.InferRequirementsOptions{
		Match: &match,
	})
	if sig.Class == "" {
		sig.Class = reqs.Workload.Class
	}
	return llmInferenceResult{
		reqs: reqs,
		sig:  sig,
	}
}

func appendLLMNote(result llmInferenceResult, note string) llmInferenceResult {
	if strings.TrimSpace(note) == "" {
		return result
	}
	result.sig.Notes = append(result.sig.Notes, note)
	result.reqs.Workload.Notes = append(result.reqs.Workload.Notes, note)
	return result
}

func defaultConfirmLLMCloudFallback(in io.Reader, errW io.Writer, decision llmrouter.RoutingDecision) bool {
	if in == nil {
		return false
	}

	estimatedCost := "estimated cost unavailable"
	if decision.EstCost > 0 {
		estimatedCost = fmt.Sprintf("estimated cost $%.4f", decision.EstCost)
	}
	fmt.Fprintf(errW,
		"cloud fallback required: %s/%s (%s, latency %s). Type YES to continue: ",
		decision.Provider, decision.Model, estimatedCost, decision.EstLatency,
	)

	line, err := bufio.NewReader(in).ReadString('\n')
	fmt.Fprintln(errW)
	if err != nil && err != io.EOF {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes")
}

// printLLMResult renders the classification result as human-readable text.
func printLLMResult(w interface{ Write([]byte) (int, error) }, r llmResult) {
	sep := strings.Repeat("─", 46)

	fmt.Fprintf(w, "\n  %s\n", ui.Bold("Workload Classification"))
	fmt.Fprintf(w, "  %s\n", sep)
	fmt.Fprintf(w, "  %-16s %s\n", "Prompt:", truncate(r.Prompt, 60))
	fmt.Fprintf(w, "  %-16s %s\n", "Class:", ui.Bold(r.Class))
	fmt.Fprintf(w, "  %-16s %.2f  [%s]\n", "Confidence:", r.Confidence, sourceLabel(r.Source))
	if len(r.Signals) > 0 {
		fmt.Fprintf(w, "  %-16s %s\n", "Signals:", strings.Join(r.Signals, ", "))
	}
	if len(r.Notes) > 0 {
		for _, note := range r.Notes {
			fmt.Fprintf(w, "  %-16s %s\n", "Note:", ui.Yellow(note))
		}
	}

	fmt.Fprintf(w, "\n  %s\n", ui.Bold("Requirements"))
	fmt.Fprintf(w, "  %s\n", sep)
	if r.Requirements.MinFreeRAMMB > 0 {
		fmt.Fprintf(w, "  %-16s %d MB\n", "Min RAM:", r.Requirements.MinFreeRAMMB)
	} else {
		fmt.Fprintf(w, "  %-16s %s\n", "Min RAM:", ui.Dim("none"))
	}
	if len(r.Requirements.RequiredTools) > 0 {
		fmt.Fprintf(w, "  %-16s %s\n", "Tools:", strings.Join(r.Requirements.RequiredTools, ", "))
	}
	if len(r.Requirements.PreferredBackends) > 0 {
		fmt.Fprintf(w, "  %-16s %s\n", "Backends:", strings.Join(r.Requirements.PreferredBackends, ", "))
	}
	if r.Requirements.PrefersTurboQuant {
		fmt.Fprintf(w, "  %-16s yes\n", "TurboQuant:")
	}
	if r.Requirements.ContextWindow > 0 {
		fmt.Fprintf(w, "  %-16s %d tokens\n", "Context:", r.Requirements.ContextWindow)
	}
	fmt.Fprintln(w)
}

func sourceLabel(source string) string {
	switch source {
	case string(llmrouter.SourceSemantic):
		return ui.Green("semantic")
	case string(llmrouter.SourceReflex):
		return ui.Yellow("reflex fallback")
	default:
		return source
	}
}

func truncate(s string, max int) string {
	if max <= 0 {
		return "…"
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

func computeFileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func rejectSymlink(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("lstat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}
	return nil
}

func writeAtomicTempYAML(dir string, rootNode *yaml.Node) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate temp name: %w", err)
	}
	tmpPath := filepath.Join(dir, ".axis-nodes-"+hex.EncodeToString(b)+".tmp")

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("create temp config: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(rootNode); err != nil {
		enc.Close()
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("marshal yaml AST: %w", err)
	}
	enc.Close()

	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write temp config: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("fsync temp config: %w", err)
	}
	return tmpPath, nil
}

func writeConfigSafely(configPath string, rootNode *yaml.Node, originalHash string) error {
	if err := rejectSymlink(configPath); err != nil {
		return err
	}

	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	lockPath := configPath + ".lock"
	lockFile, err := lockutil.OpenLock(lockPath)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if err := lockFile.LockEx(); err != nil {
		return fmt.Errorf("acquire config lock: %w", err)
	}
	defer lockFile.Unlock()

	currentHash := ""
	if _, err := os.Lstat(configPath); err == nil {
		currentHash, err = computeFileHash(configPath)
		if err != nil {
			return fmt.Errorf("hash current config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat config: %w", err)
	}
	if currentHash != originalHash {
		return fmt.Errorf("concurrent modification detected: config file changed since load")
	}

	tmpPath, err := writeAtomicTempYAML(dir, rootNode)
	if err != nil {
		return err
	}

	if _, err := loadLLMConfig(tmpPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("validate temporary config: %w", err)
	}

	if _, err := os.Stat(configPath); err == nil {
		backupPath := configPath + ".bak"
		content, readErr := os.ReadFile(configPath)
		if readErr != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("read config for backup: %w", readErr)
		}
		if writeErr := os.WriteFile(backupPath, content, 0600); writeErr != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("write backup: %w", writeErr)
		}
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename config: %w", err)
	}

	if err := syncDir(dir); err != nil {
		return fmt.Errorf("fsync config directory: %w", err)
	}

	return nil
}

func migrateASTProviders(rootNode *yaml.Node) {
	if rootNode == nil || len(rootNode.Content) == 0 {
		return
	}
	rootMap := rootNode.Content[0]
	if rootMap.Kind != yaml.MappingNode {
		return
	}
	var providersNode *yaml.Node
	for i := 0; i < len(rootMap.Content); i += 2 {
		if rootMap.Content[i].Value == "ai_providers" {
			providersNode = rootMap.Content[i+1]
			break
		}
	}
	if providersNode == nil || providersNode.Kind != yaml.MappingNode {
		return
	}

	for i := 0; i < len(providersNode.Content); i += 2 {
		providerNameNode := providersNode.Content[i]
		providerNode := providersNode.Content[i+1]
		if providerNode.Kind != yaml.MappingNode {
			continue
		}
		var typeNode, kindNode *yaml.Node
		var endpointValue string
		for j := 0; j < len(providerNode.Content); j += 2 {
			key := providerNode.Content[j].Value
			val := providerNode.Content[j+1]
			switch key {
			case "type":
				typeNode = val
			case "kind":
				kindNode = val
			case "endpoint":
				endpointValue = val.Value
			}
		}
		if typeNode == nil || !strings.EqualFold(typeNode.Value, "cloud") {
			continue
		}
		if kindNode != nil && strings.TrimSpace(kindNode.Value) != "" {
			continue
		}

		nameLower := strings.ToLower(providerNameNode.Value)
		epLower := strings.ToLower(endpointValue)
		var inferred string
		count := 0
		if strings.Contains(nameLower, "openrouter") || strings.Contains(epLower, "openrouter.ai") {
			inferred = "openrouter"
			count++
		}
		if strings.Contains(nameLower, "groq") || strings.Contains(epLower, "groq.com") {
			inferred = "groq"
			count++
		}
		if strings.Contains(nameLower, "anthropic") || strings.Contains(nameLower, "claude") || strings.Contains(epLower, "anthropic.com") {
			inferred = "anthropic"
			count++
		}
		if count != 1 {
			continue
		}

		setMappingKey(providerNode, "kind", stringNode(inferred))
	}
}

func writeKeyFileSecurely(path string, content []byte, in io.Reader, out io.Writer) (created bool, err error) {
	if err := rejectSymlink(path); err != nil {
		return false, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false, fmt.Errorf("create secrets directory: %w", err)
	}

	priorExists := false
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("%s is a symlink", path)
		}
		priorExists = true
		fmt.Fprintf(out, "Key file already exists: %s\n", path)
		choice := promptString(in, out, "Overwrite existing key file? (yes/no)", "no")
		if !strings.EqualFold(strings.TrimSpace(choice), "yes") {
			return false, fmt.Errorf("key file write aborted")
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("lstat key file: %w", err)
	}

	tmpPath, err := writeAtomicTempFile(dir, content)
	if err != nil {
		return false, err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return false, fmt.Errorf("rename key file: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return false, fmt.Errorf("fsync secrets directory: %w", err)
	}

	return !priorExists, nil
}

func writeAtomicTempFile(dir string, content []byte) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate temp name: %w", err)
	}
	tmpPath := filepath.Join(dir, ".axis-key-"+hex.EncodeToString(b)+".tmp")

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("create temp key file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write temp key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("fsync temp key file: %w", err)
	}
	return tmpPath, nil
}

func llmSelectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "select",
		Short: "Interactively select the active model for task routing",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()

			snap, _, err := collectStatusSnapshot(
				ctx,
				true, // cached
				false,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return daemon.FetchSnapshot(ctx, api.DefaultAddr())
				},
				discoverLiveSnapshot,
			)

			var options []string
			var optionValues []string

			if err == nil && snap != nil {
				for _, n := range snap.Nodes {
					for _, rm := range n.ResidentModels {
						warmth := "Cold"
						if rm.WarmthScore > 0 {
							warmth = "Warm"
						}
						sizeStr := ""
						if strings.Contains(strings.ToLower(rm.Name), "llama3") {
							sizeStr = "8B, "
						} else if strings.Contains(strings.ToLower(rm.Name), "qwen2.5") {
							sizeStr = "1.5B, "
						}
						tag := fmt.Sprintf("%s:%s", rm.Runtime, rm.Name)
						label := fmt.Sprintf("%s (%sresident on %s - %s)", tag, sizeStr, n.Name, warmth)

						duplicated := false
						for _, val := range optionValues {
							if val == tag {
								duplicated = true
								break
							}
						}
						if !duplicated {
							options = append(options, label)
							optionValues = append(optionValues, tag)
						}
					}
				}
			}

			cfg, err := loadLLMConfig(llmConfigPath())
			if err == nil && cfg != nil {
				for pName, prov := range cfg.AIProviders {
					if prov.Type == "cloud" {
						for _, m := range prov.Models {
							tag := fmt.Sprintf("%s:%s", pName, m.Name)
							label := fmt.Sprintf("%s (Cloud - Always available)", tag)

							duplicated := false
							for _, val := range optionValues {
								if val == tag {
									duplicated = true
									break
								}
							}
							if !duplicated {
								options = append(options, label)
								optionValues = append(optionValues, tag)
							}
						}
					}
				}
			}

			if len(options) == 0 {
				options = append(options, "google:gemini-2.5-pro (Cloud - Always available)")
				optionValues = append(optionValues, "google:gemini-2.5-pro")
			} else {
				hasGemini := false
				for _, val := range optionValues {
					if strings.Contains(val, "gemini-2.5-pro") {
						hasGemini = true
						break
					}
				}
				if !hasGemini {
					options = append(options, "google:gemini-2.5-pro (Cloud - Always available)")
					optionValues = append(optionValues, "google:gemini-2.5-pro")
				}
			}

			if !llmIsTerminal(int(os.Stdin.Fd())) {
				fmt.Fprintln(cmd.OutOrStdout(), "Available models for task routing:")
				for _, opt := range options {
					fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", opt)
				}
				return nil
			}

			idx, selectErr := llmSelectModelInteractive(cmd.OutOrStdout(), cmd.InOrStdin(), options)
			if selectErr != nil {
				return selectErr
			}

			selectedModelValue := optionValues[idx]

			cfgPath := llmConfigPath()
			originalHash := ""
			var rootNode yaml.Node
			data, readErr := os.ReadFile(cfgPath)
			if readErr != nil {
				if !os.IsNotExist(readErr) {
					return fmt.Errorf("read config file: %w", readErr)
				}
				rootNode = yaml.Node{
					Kind: yaml.DocumentNode,
					Content: []*yaml.Node{
						{
							Kind: yaml.MappingNode,
							Tag:  "!!map",
						},
					},
				}
			} else {
				originalHash, _ = computeFileHash(cfgPath)
				if err := yaml.Unmarshal(data, &rootNode); err != nil {
					return fmt.Errorf("parse nodes.yaml AST: %w", err)
				}
			}

			if len(rootNode.Content) == 0 {
				rootNode.Content = append(rootNode.Content, &yaml.Node{
					Kind: yaml.MappingNode,
					Tag:  "!!map",
				})
			}

			chatNode := getOrMappingNode(rootNode.Content[0], "chat")
			setMappingKey(chatNode, "default_model", stringNode(selectedModelValue))

			migrateASTProviders(&rootNode)

			if err := writeConfigSafely(cfgPath, &rootNode, originalHash); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Selected active model: %s\n", selectedModelValue)
			return nil
		},
	}
	return cmd
}

func selectModelInteractive(w io.Writer, in io.Reader, options []string) (int, error) {
	if len(options) == 0 {
		return -1, fmt.Errorf("no options available")
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return -1, fmt.Errorf("failed to make raw terminal: %w", err)
	}
	defer func() {
		_ = term.Restore(fd, oldState)
	}()

	currentIdx := 0
	first := true

	renderMenu := func() {
		if !first {
			fmt.Fprintf(w, "\033[%dA\r", len(options)+1)
		}
		first = false
		fmt.Fprintf(w, "? Select active model for task routing:\r\n")
		for i, opt := range options {
			if i == currentIdx {
				fmt.Fprintf(w, "  %s %s\r\n", ui.Cyan("▸"), ui.Bold(opt))
			} else {
				fmt.Fprintf(w, "    %s\r\n", opt)
			}
		}
	}

	renderMenu()

	buf := make([]byte, 3)
	for {
		n, err := in.Read(buf)
		if err != nil {
			return -1, err
		}

		if n == 1 {
			if buf[0] == '\r' || buf[0] == '\n' {
				return currentIdx, nil
			}
			if buf[0] == 3 || buf[0] == 27 {
				return -1, fmt.Errorf("selection aborted")
			}
		} else if n == 3 && buf[0] == 27 && buf[1] == '[' {
			switch buf[2] {
			case 'A': // Up arrow
				if currentIdx > 0 {
					currentIdx--
					renderMenu()
				}
			case 'B': // Down arrow
				if currentIdx < len(options)-1 {
					currentIdx++
					renderMenu()
				}
			}
		}
	}
}

func resolveLocalNodeName(ctx context.Context) string {
	snap, _, err := collectStatusSnapshot(
		ctx,
		true, // cached
		false,
		func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
			return daemon.FetchSnapshot(ctx, api.DefaultAddr())
		},
		discoverLiveSnapshot,
	)
	if err == nil && snap != nil {
		if n, ok := models.FindLocalNode(snap.Nodes); ok {
			return n.Name
		}
	}
	hostname, _ := os.Hostname()
	if hostname != "" {
		return hostname
	}
	return "localhost"
}

func showWarmupAndOOMAlert(w io.Writer, model, node string) {
	if !llmWriterIsTerminal(w) || llmWarmupDelay <= 0 {
		fmt.Fprintf(w, "⚠️  OOM Alert: %s RAM limit exceeded.\n", node)
		return
	}

	steps := []struct {
		pct int
		eta int
	}{
		{20, 8},
		{40, 6},
		{60, 4},
		{80, 2},
	}
	for _, step := range steps {
		bars := strings.Repeat("|", step.pct/10)
		dots := strings.Repeat(".", 10-step.pct/10)
		fmt.Fprintf(w, "\r\033[K🔄 Warm-up: Loading %s on %s [%s%s] %d%% (ETA %ds)", model, node, bars, dots, step.pct, step.eta)
		time.Sleep(llmWarmupDelay)
	}
	fmt.Fprintf(w, "\r\033[K⚠️  OOM Alert: %s RAM limit exceeded.\n", node)
}

func llmConfigureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "configure [provider]",
		Short: "Interactively configure an AI provider in nodes.yaml",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := cmd.InOrStdin()
			out := cmd.OutOrStdout()
			errW := cmd.ErrOrStderr()

			var providerName string
			if len(args) > 0 {
				providerName = strings.TrimSpace(args[0])
			} else {
				providerName = promptString(in, out, "Enter provider name (e.g. openai, anthropic, google)", "")
			}
			if providerName == "" {
				return fmt.Errorf("provider name is required")
			}

			// Load nodes.yaml
			configPath := config.DefaultConfigPath()
			originalHash := ""
			data, err := os.ReadFile(configPath)
			if err != nil {
				if os.IsNotExist(err) {
					data = []byte("")
				} else {
					return fmt.Errorf("read config file: %w", err)
				}
			} else {
				originalHash, _ = computeFileHash(configPath)
			}
			var rootNode yaml.Node
			if len(data) > 0 {
				if err := yaml.Unmarshal(data, &rootNode); err != nil {
					return fmt.Errorf("parse nodes.yaml AST: %w", err)
				}
			} else {
				rootNode = yaml.Node{
					Kind: yaml.DocumentNode,
					Content: []*yaml.Node{
						{
							Kind: yaml.MappingNode,
							Tag:  "!!map",
						},
					},
				}
			}

			if len(rootNode.Content) == 0 {
				rootNode.Content = append(rootNode.Content, &yaml.Node{
					Kind: yaml.MappingNode,
					Tag:  "!!map",
				})
			}

			// Prompt provider details
			providerType := promptString(in, out, "Enter provider type (local or cloud)", "cloud")
			enabled := promptBool(in, out, "Enable this provider?", true)

			var priority int
			priorityStr := promptString(in, out, "Enter provider priority (0-100)", "50")
			if _, err := fmt.Sscanf(priorityStr, "%d", &priority); err != nil {
				fmt.Fprintf(errW, "%s invalid priority %q, defaulting to 50\n", ui.Yellow("⚠"), priorityStr)
				priority = 50
			}

			var endpoint string
			if strings.EqualFold(providerType, "local") {
				endpoint = promptString(in, out, "Enter local provider endpoint", "http://localhost:11434")
			} else {
				endpoint = promptString(in, out, "Enter cloud provider endpoint (optional)", "")
			}

			// API key credentials if provider is cloud
			var apiKey string
			var apiKeyEnv string
			var apiKeyFile string
			var createdKeyThisTransaction bool

			if strings.EqualFold(providerType, "cloud") {
				apiKey = promptString(in, out, "Enter API key value (leave blank to skip)", "")
				if apiKey != "" {
					fmt.Fprintln(out, "\nChoose credential storage option:")
					fmt.Fprintln(out, "  1) Store key value in a separate file (e.g. ~/.axis/secrets/[provider].key)")
					fmt.Fprintln(out, "  2) Reference an environment variable containing the key")
					optionStr := promptString(in, out, "Enter option (1 or 2)", "1")

					homeDir, _ := os.UserHomeDir()
					secretsDir := filepath.Join(homeDir, ".axis", "secrets")

					if optionStr == "2" {
						envDefault := strings.ToUpper(providerName) + "_API_KEY"
						apiKeyEnv = promptString(in, out, "Enter environment variable name", envDefault)
					} else {
						apiKeyFile = filepath.Join(secretsDir, providerName+".key")
						created, keyErr := writeKeyFileSecurely(apiKeyFile, []byte(apiKey), in, out)
						if keyErr != nil {
							return fmt.Errorf("store api key: %w", keyErr)
						}
						createdKeyThisTransaction = created
						fmt.Fprintf(out, "Stored API key in %s\n", apiKeyFile)
					}
				} else {
					// Ask for env or file reference directly
					choice := promptString(in, out, "Store using env variable (1) or file path (2)?", "1")
					if choice == "2" {
						apiKeyFile = promptString(in, out, "Enter API key file path", "")
					} else {
						apiKeyEnv = promptString(in, out, "Enter API key environment variable name", strings.ToUpper(providerName)+"_API_KEY")
					}
				}
			}

			// Modify nodes.yaml AST
			aiProvidersNode := getOrMappingNode(rootNode.Content[0], "ai_providers")
			providerNode := getOrMappingNode(aiProvidersNode, providerName)

			setMappingKey(providerNode, "type", stringNode(providerType))
			if endpoint != "" {
				setMappingKey(providerNode, "endpoint", stringNode(endpoint))
			}
			setMappingKey(providerNode, "enabled", boolNode(enabled))
			setMappingKey(providerNode, "priority", intNode(priority))
			if apiKeyEnv != "" {
				setMappingKey(providerNode, "api_key_env", stringNode(apiKeyEnv))
				// remove file reference if present
				removeMappingKey(providerNode, "api_key_file")
			}
			if apiKeyFile != "" {
				setMappingKey(providerNode, "api_key_file", stringNode(apiKeyFile))
				// remove env reference if present
				removeMappingKey(providerNode, "api_key_env")
			}

			// Add standard models if not configured
			var defaultModels = map[string][]config.AIModelConfig{
				"openai": {
					{Name: "gpt-4o", CostPer1K: 0.005},
					{Name: "gpt-4o-mini", CostPer1K: 0.00015},
				},
				"anthropic": {
					{Name: "claude-3-5-sonnet", CostPer1K: 0.015},
					{Name: "claude-3-5-haiku", CostPer1K: 0.003},
				},
				"google": {
					{Name: "gemini-2.5-pro", CostPer1K: 0.007},
					{Name: "gemini-2.5-flash", CostPer1K: 0.000075},
				},
			}

			hasModels := false
			for i := 0; i < len(providerNode.Content); i += 2 {
				if providerNode.Content[i].Value == "models" {
					hasModels = true
					break
				}
			}
			if !hasModels {
				if modelsSlice, ok := defaultModels[strings.ToLower(providerName)]; ok {
					var modelsNode yaml.Node
					mBytes, _ := yaml.Marshal(modelsSlice)
					_ = yaml.Unmarshal(mBytes, &modelsNode)
					if len(modelsNode.Content) > 0 {
						setMappingKey(providerNode, "models", modelsNode.Content[0])
					}
				}
			}

			migrateASTProviders(&rootNode)

			if err := writeConfigSafely(configPath, &rootNode, originalHash); err != nil {
				if createdKeyThisTransaction && apiKeyFile != "" {
					os.Remove(apiKeyFile)
				}
				return fmt.Errorf("save config: %w", err)
			}

			// Validate
			if enabled && strings.EqualFold(providerType, "cloud") && apiKey != "" {
				provCfg := config.AIProviderConfig{
					Type:       providerType,
					Endpoint:   endpoint,
					APIKeyEnv:  apiKeyEnv,
					APIKeyFile: apiKeyFile,
					Priority:   priority,
				}
				if modelsSlice, ok := defaultModels[strings.ToLower(providerName)]; ok {
					provCfg.Models = modelsSlice
				}

				tempCfg := &config.Config{
					AIProviders: map[string]config.AIProviderConfig{
						providerName: provCfg,
					},
				}

				if apiKeyEnv != "" {
					os.Setenv(apiKeyEnv, apiKey)
					defer os.Unsetenv(apiKeyEnv)
				}

				reg, regErr := buildLLMRegistry(tempCfg)
				if regErr != nil {
					fmt.Fprintf(errW, "%s Failed to construct validation registry: %v\n", ui.Yellow("⚠"), regErr)
				} else {
					fmt.Fprintln(out, "Validating provider credentials via Health check...")
					healthCtx, healthCancel := context.WithTimeout(cmd.Context(), 10*time.Second)
					statuses := reg.CheckHealth(healthCtx)
					healthCancel()

					status, ok := statuses[providerName]
					if !ok || !status.OK {
						msg := "unknown error"
						if ok {
							msg = status.Message
						}
						fmt.Fprintf(errW, "%s Validation failed: %s. Proceeding with configuration anyway.\n", ui.Yellow("⚠"), msg)
					} else {
						fmt.Fprintf(out, "%s Validation succeeded (latency: %v).\n", ui.Green("✓"), status.Latency)
					}
				}
			}

			// Display non-secret configuration
			fmt.Fprintln(out, "\nConfiguration saved successfully.")
			fmt.Fprintf(out, "  Provider: %s\n", providerName)
			fmt.Fprintf(out, "  Type:     %s\n", providerType)
			fmt.Fprintf(out, "  Enabled:  %v\n", enabled)
			fmt.Fprintf(out, "  Priority: %d\n", priority)
			if endpoint != "" {
				fmt.Fprintf(out, "  Endpoint: %s\n", endpoint)
			}
			if apiKeyEnv != "" {
				fmt.Fprintf(out, "  API Key Env: %s\n", apiKeyEnv)
			}
			if apiKeyFile != "" {
				fmt.Fprintf(out, "  API Key File: %s\n", apiKeyFile)
			}
			return nil
		},
	}
	return cmd
}

func promptString(in io.Reader, out io.Writer, prompt string, defaultValue string) string {
	if defaultValue != "" {
		fmt.Fprintf(out, "%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Fprintf(out, "%s: ", prompt)
	}
	scanner := bufio.NewScanner(in)
	if scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			return defaultValue
		}
		return text
	}
	return defaultValue
}

func promptBool(in io.Reader, out io.Writer, prompt string, defaultValue bool) bool {
	defStr := "y/N"
	if defaultValue {
		defStr = "Y/n"
	}
	fmt.Fprintf(out, "%s (%s): ", prompt, defStr)
	scanner := bufio.NewScanner(in)
	if scanner.Scan() {
		text := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if text == "" {
			return defaultValue
		}
		if text == "y" || text == "yes" {
			return true
		}
		if text == "n" || text == "no" {
			return false
		}
	}
	return defaultValue
}

func setMappingKey(mapNode *yaml.Node, key string, valNode *yaml.Node) {
	for i := 0; i < len(mapNode.Content); i += 2 {
		if mapNode.Content[i].Value == key {
			mapNode.Content[i+1] = valNode
			return
		}
	}
	keyNode := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: key,
	}
	mapNode.Content = append(mapNode.Content, keyNode, valNode)
}

func removeMappingKey(mapNode *yaml.Node, key string) {
	for i := 0; i < len(mapNode.Content); i += 2 {
		if mapNode.Content[i].Value == key {
			mapNode.Content = append(mapNode.Content[:i], mapNode.Content[i+2:]...)
			return
		}
	}
}

func getOrMappingNode(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			if parent.Content[i+1].Kind == yaml.MappingNode {
				return parent.Content[i+1]
			}
		}
	}
	newNode := &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
	}
	setMappingKey(parent, key, newNode)
	return newNode
}

func stringNode(val string) *yaml.Node {
	return &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: val,
	}
}

func boolNode(val bool) *yaml.Node {
	valStr := "false"
	if val {
		valStr = "true"
	}
	return &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!bool",
		Value: valStr,
	}
}

func intNode(val int) *yaml.Node {
	return &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!int",
		Value: fmt.Sprintf("%d", val),
	}
}
