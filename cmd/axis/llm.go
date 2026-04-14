package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/ui"
	"github.com/toasterbook88/axis/internal/workload"
)

const llmCloudFallbackTimeout = 5 * time.Second

type llmInferenceResult struct {
	reqs models.TaskRequirements
	sig  llmrouter.IntentSignal
}

var (
	loadLLMConfig           = config.Load
	llmConfigPath           = config.DefaultConfigPath
	buildLLMRegistry        = llmrouter.NewRegistryFromConfig
	selectLLMCloudFallback  = llmrouter.SelectCloudFallback
	llmClassifyWithProvider = llmrouter.ClassifyWithProvider
	confirmLLMCloudFallback = defaultConfirmLLMCloudFallback
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
			inference = maybeLLMCloudFallback(prompt, inference, cmd.InOrStdin(), errW)

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
				return printOutput(result, format)
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

	return cmd
}

func maybeLLMCloudFallback(prompt string, current llmInferenceResult, in io.Reader, errW io.Writer) llmInferenceResult {
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

	selectCtx, cancel := context.WithTimeout(context.Background(), llmCloudFallbackTimeout)
	provider, decision, err := selectLLMCloudFallback(selectCtx, registry, prompt, prefer)
	cancel()
	if err != nil {
		return current
	}

	if maxCost > 0 && decision.EstCost > maxCost {
		return appendLLMNote(current,
			fmt.Sprintf("cloud fallback skipped: estimated cost $%.4f exceeds max $%.4f", decision.EstCost, maxCost))
	}

	if !confirmLLMCloudFallback(in, errW, decision) {
		return appendLLMNote(current,
			fmt.Sprintf("cloud fallback skipped: operator declined %s/%s", decision.Provider, decision.Model))
	}

	sendCtx, cancel := context.WithTimeout(context.Background(), llmCloudFallbackTimeout)
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
