package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"gopkg.in/yaml.v3"
)

// Test seams for AI config path resolution.
var (
	aiConfigPathFn = config.DefaultAIConfigPath
	aiLoadFn       = config.LoadAIOrEmpty
)

func aiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "Inference backends, roles, and dry-run routing (advisory)",
		Long: `Configure backends and roles in ~/.axis/ai.yaml (see ai.example.yaml).

AXIS resolves roles against live health probes and prints an advisory decision.
It does not manage tunnels, GPU processes, or operator topology — those stay
outside the public substrate in operator-local config.

Examples:
  axis ai backends
  axis ai roles
  axis ai route default
  axis ai route --model coder:latest
  axis ai route fast --format json`,
	}
	cmd.AddCommand(aiBackendsCmd())
	cmd.AddCommand(aiRolesCmd())
	cmd.AddCommand(aiRouteCmd())
	return cmd
}

func aiBackendsCmd() *cobra.Command {
	var (
		format    string
		aiPath    string
		skipProbe bool
		timeout   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "backends",
		Short: "List configured inference backends (optional live probe)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadAIConfig(aiPath)
			if err != nil {
				return err
			}
			if len(cfg.Backends) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No backends configured. Copy ai.example.yaml to ~/.axis/ai.yaml")
				return nil
			}

			var probes []llmrouter.BackendProbe
			if !skipProbe {
				ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
				defer cancel()
				probes = llmrouter.ProbeAllBackends(ctx, cfg, nil)
			} else {
				for _, b := range cfg.Backends {
					probes = append(probes, llmrouter.BackendProbe{
						Backend: b.Name,
						Kind:    b.Kind,
						BaseURL: b.BaseURL,
						Node:    b.Node,
						Enabled: b.IsEnabled(),
						Message: "probe skipped",
					})
				}
			}

			switch strings.ToLower(format) {
			case "json":
				return writeJSON(cmd, probes)
			case "yaml":
				return writeYAML(cmd, probes)
			default:
				for _, p := range probes {
					status := "down"
					if !p.Enabled {
						status = "disabled"
					} else if p.OK {
						status = "ok"
					} else if skipProbe {
						status = "unprobed"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%-16s %-18s %-8s %s",
						p.Backend, p.Kind, status, p.BaseURL)
					if p.Node != "" {
						fmt.Fprintf(cmd.OutOrStdout(), " node=%s", p.Node)
					}
					if p.Message != "" && status != "ok" {
						fmt.Fprintf(cmd.OutOrStdout(), " (%s)", p.Message)
					}
					if p.OK && len(p.Models) > 0 {
						fmt.Fprintf(cmd.OutOrStdout(), " models=%d", len(p.Models))
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	cmd.Flags().StringVar(&aiPath, "ai-config", "", "Path to ai.yaml (default: ~/.axis/ai.yaml)")
	cmd.Flags().BoolVar(&skipProbe, "skip-probe", false, "Do not contact backends")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "Probe budget for all backends")
	return cmd
}

func aiRolesCmd() *cobra.Command {
	var (
		format string
		aiPath string
	)
	cmd := &cobra.Command{
		Use:   "roles",
		Short: "List configured inference roles",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadAIConfig(aiPath)
			if err != nil {
				return err
			}
			if len(cfg.Roles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No roles configured. Copy ai.example.yaml to ~/.axis/ai.yaml")
				return nil
			}
			switch strings.ToLower(format) {
			case "json":
				return writeJSON(cmd, cfg.Roles)
			case "yaml":
				return writeYAML(cmd, cfg.Roles)
			default:
				names := make([]string, 0, len(cfg.Roles))
				for name := range cfg.Roles {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					role := cfg.Roles[name]
					fmt.Fprintf(cmd.OutOrStdout(), "%-12s model=%-20s prefer=%v",
						name, role.Model, role.Prefer)
					if role.RequireArch != "" {
						fmt.Fprintf(cmd.OutOrStdout(), " arch=%s", role.RequireArch)
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	cmd.Flags().StringVar(&aiPath, "ai-config", "", "Path to ai.yaml (default: ~/.axis/ai.yaml)")
	return cmd
}

func aiRouteCmd() *cobra.Command {
	var (
		format    string
		aiPath    string
		model     string
		skipProbe bool
		timeout   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "route [role]",
		Short: "Dry-run: resolve a role (or model) to a backend",
		Long: `Resolve which backend and model AXIS would use for a role.

This is advisory only — no completion request is sent.

  axis ai route default
  axis ai route --model fast-chat
  axis ai route long --format json --skip-probe`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadAIConfig(aiPath)
			if err != nil {
				return err
			}
			if len(cfg.Backends) == 0 {
				return fmt.Errorf("no backends configured; copy ai.example.yaml to %s", config.DefaultAIConfigPath())
			}

			role := ""
			if len(args) == 1 {
				role = args[0]
			}
			if role == "" && strings.TrimSpace(model) == "" {
				return fmt.Errorf("provide a role argument or --model")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			dec, err := llmrouter.ResolveRole(ctx, cfg, llmrouter.ResolveRoleOptions{
				Role:      role,
				Model:     model,
				SkipProbe: skipProbe,
			})
			if err != nil {
				return err
			}

			switch strings.ToLower(format) {
			case "json":
				return writeJSON(cmd, dec)
			case "yaml":
				return writeYAML(cmd, dec)
			default:
				printRouteText(cmd, dec)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	cmd.Flags().StringVar(&aiPath, "ai-config", "", "Path to ai.yaml (default: ~/.axis/ai.yaml)")
	cmd.Flags().StringVar(&model, "model", "", "Override role model, or resolve model without a role")
	cmd.Flags().BoolVar(&skipProbe, "skip-probe", false, "Config-only preference order (no health checks)")
	cmd.Flags().DurationVar(&timeout, "timeout", 8*time.Second, "Probe budget")
	return cmd
}

func loadAIConfig(aiPath string) (*config.AIConfig, error) {
	path := strings.TrimSpace(aiPath)
	if path == "" {
		path = aiConfigPathFn()
	}
	cfg, err := aiLoadFn(path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func printRouteText(cmd *cobra.Command, dec llmrouter.RoleRouteDecision) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "AXIS inference route (advisory / dry-run)")
	if dec.Role != "" {
		fmt.Fprintf(out, "  role:       %s\n", dec.Role)
	}
	fmt.Fprintf(out, "  backend:    %s\n", dec.Backend)
	fmt.Fprintf(out, "  kind:       %s\n", dec.Kind)
	fmt.Fprintf(out, "  model:      %s\n", dec.Model)
	fmt.Fprintf(out, "  endpoint:   %s\n", dec.Endpoint)
	if dec.Node != "" {
		fmt.Fprintf(out, "  node:       %s\n", dec.Node)
	}
	fmt.Fprintf(out, "  healthy:    %v\n", dec.Healthy)
	fmt.Fprintf(out, "  model_seen: %v\n", dec.ModelPresent)
	fmt.Fprintf(out, "  confidence: %.2f\n", dec.Confidence)
	if len(dec.Fallbacks) > 0 {
		fmt.Fprintf(out, "  fallbacks:  %s\n", strings.Join(dec.Fallbacks, ", "))
	}
	if len(dec.Reasoning) > 0 {
		fmt.Fprintln(out, "  reasoning:")
		for _, line := range dec.Reasoning {
			fmt.Fprintf(out, "    - %s\n", line)
		}
	}
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeYAML(cmd *cobra.Command, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(data)
	return err
}
