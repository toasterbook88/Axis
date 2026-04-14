package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/ui"
	"github.com/toasterbook88/axis/internal/workload"
)

// llmClassifyFn is the classify call, injected in tests to avoid a real Ollama.
var llmClassifyFn = func(ctx context.Context, e *llmrouter.Engine, prompt, extra string) (models.WorkloadClass, llmrouter.IntentSignal, error) {
	return e.Classify(ctx, prompt, extra)
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
		Short: "Classify a prompt and show workload routing (Phase 2: semantic reflex)",
		Long: "Classifies a task prompt using a local LLM (via Ollama) into a WorkloadClass\n" +
			"and derives hardware requirements for placement.\n\n" +
			"The classifier uses a lightweight local model (default: granite3.1-moe:1b)\n" +
			"with a hard latency budget. If the local model is unavailable or too slow,\n" +
			"it transparently falls back to the legacy string-matcher.\n\n" +
			"Output is advisory only — use `axis task place` for full placement decisions.\n\n" +
			"Classification sources:\n" +
			"  semantic  — local LLM classified the prompt (Ollama required)\n" +
			"  reflex    — legacy string-matcher used (Ollama unavailable or timed out)",
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

			// Classify with latency budget.
			ctx, cancel := context.WithTimeout(context.Background(), timeout+50*time.Millisecond)
			defer cancel()

			sp := ui.NewSpinner()
			sp.Start("Classifying...")
			class, sig, _ := llmClassifyFn(ctx, engine, prompt, "")
			sp.Stop("")

			// Derive full TaskRequirements via the seam.
			reqs := placement.InferRequirements(prompt, workload.InferRequirementsOptions{
				Classifier: engine,
			})

			result := llmResult{
				Prompt:     prompt,
				Class:      string(class),
				Confidence: sig.Confidence,
				Source:     string(sig.Source),
				Signals:    sig.Signals,
				Notes:      sig.Notes,
				Requirements: llmRequirements{
					MinFreeRAMMB:      reqs.MinFreeRAMMB,
					RequiredTools:     reqs.RequiredTools,
					PreferredBackends: reqs.PreferredBackends,
					PrefersTurboQuant: reqs.PrefersTurboQuant,
					ContextWindow:     reqs.ContextWindowTokens,
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
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show routing decision without executing (always true in Phase 2)")

	return cmd
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
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
