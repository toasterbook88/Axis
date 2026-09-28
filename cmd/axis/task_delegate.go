package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/a2a"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/ui"
)

var buildA2AClient = resolveA2AClient

var loadA2AConfig = func() (*config.Config, error) {
	return config.Load(config.DefaultConfigPath())
}

func resolveA2AClient(nodeName, addrOverride string, timeout time.Duration) (*a2a.Client, error) {
	nodeName = strings.TrimSpace(nodeName)
	addrOverride = strings.TrimSpace(addrOverride)

	token, err := auth.LoadOrGenerateToken()
	if err != nil {
		return nil, fmt.Errorf("loading api token: %w", err)
	}

	if addrOverride != "" {
		httpClient, baseURL := auth.HttpClientForAddrWithTimeout(addrOverride, timeout)
		return a2a.NewClient(baseURL, token, httpClient), nil
	}

	isLocal := nodeName == "" || nodeName == "local" || nodeName == "localhost" || nodeName == "127.0.0.1"

	cfg, err := loadA2AConfig()
	if err == nil && cfg != nil {
		if nodeCfg, ok := cfg.FindNode(nodeName); ok {
			if nodeCfg.IsLocal() {
				isLocal = true
			} else {
				host := nodeCfg.PrimaryHostname()
				if host == "" {
					host = nodeCfg.Hostname
				}
				host = strings.TrimSpace(host)
				if host == "" {
					return nil, fmt.Errorf("node %q has no resolvable hostname in %s", nodeName, config.DefaultConfigPath())
				}
				if !strings.Contains(host, ":") {
					host = fmt.Sprintf("%s:42425", host)
				}
				addr := daemon.NormalizeAddr(host)
				httpClient, baseURL := auth.HttpClientForAddrWithTimeout(addr, timeout)
				return a2a.NewClient(baseURL, token, httpClient), nil
			}
		} else if !isLocal {
			return nil, fmt.Errorf("node %q not found in %s (specify --addr to dial an unlisted endpoint)", nodeName, config.DefaultConfigPath())
		}
	} else if !isLocal {
		return nil, fmt.Errorf("loading cluster configuration: %w (specify --addr to dial an unlisted endpoint)", err)
	}

	// Local node fallback: connect via default local unix socket
	sockAddr := api.DefaultAddr()
	httpClient, baseURL := auth.HttpClientForAddrWithTimeout(sockAddr, timeout)
	return a2a.NewClient(baseURL, token, httpClient), nil
}

func taskDelegateCmd() *cobra.Command {
	var format string
	var skill string
	var confirm string
	var addr string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:          "delegate <node> [prompt]",
		Short:        "Delegate a task or probe agent capabilities on an Axis node via A2A protocol",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json"),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetNode := args[0]
			prompt := ""
			if len(args) > 1 {
				prompt = strings.Join(args[1:], " ")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			client, err := buildA2AClient(targetNode, addr, timeout)
			if err != nil {
				return err
			}

			// If neither prompt nor skill is provided, probe the node's agent card
			if prompt == "" && skill == "" {
				card, err := client.FetchCard(ctx)
				if err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return ctxErr
					}
					return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("fetch agent card for %s: %v", targetNode, err)}
				}

				if format == "json" {
					return printOutput(cmd.OutOrStdout(), card, "json")
				}

				var b strings.Builder
				fmt.Fprintf(&b, "%s Agent card for node %s\n", ui.Green("✓"), ui.Bold(targetNode))
				fmt.Fprintf(&b, "  Name:        %s\n", card.Name)
				if card.Description != "" {
					fmt.Fprintf(&b, "  Description: %s\n", card.Description)
				}
				if card.Version != "" {
					fmt.Fprintf(&b, "  Version:     %s\n", card.Version)
				}
				if card.URL != "" {
					fmt.Fprintf(&b, "  URL:         %s\n", card.URL)
				}
				if len(card.Skills) > 0 {
					fmt.Fprintf(&b, "  Skills (%d):\n", len(card.Skills))
					for _, s := range card.Skills {
						tagStr := ""
						if len(s.Tags) > 0 {
							tagStr = fmt.Sprintf(" [%s]", strings.Join(s.Tags, ", "))
						}
						fmt.Fprintf(&b, "    - %s%s: %s\n", ui.Bold(s.ID), ui.Dim(tagStr), s.Description)
					}
				}
				_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
				return err
			}

			// Default skill to guarded-exec if not specified
			skillID := strings.TrimSpace(skill)
			if skillID == "" {
				skillID = "guarded-exec"
			}

			sendReq := a2a.SendRequest{
				SkillID: skillID,
				Message: a2a.Message{
					Role:  "user",
					Parts: []a2a.Part{{Type: "text", Text: prompt}},
				},
				Confirm: strings.TrimSpace(confirm),
			}

			task, err := client.Send(ctx, sendReq)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task delegation to %s failed: %v", targetNode, err)}
			}

			if format == "json" {
				if writeErr := printOutput(cmd.OutOrStdout(), task, "json"); writeErr != nil {
					return writeErr
				}
				if task.Status.State == a2a.TaskStateRejected {
					reason := ""
					if task.Status.Message != nil {
						reason = a2a.TextFromMessage(*task.Status.Message)
					}
					return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s was rejected: %s", task.ID, reason)}
				}
				if task.Status.State == a2a.TaskStateFailed {
					errText := ""
					if task.Status.Message != nil {
						errText = a2a.TextFromMessage(*task.Status.Message)
					}
					return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s failed: %s", task.ID, errText)}
				}
				return nil
			}

			var b strings.Builder
			switch task.Status.State {
			case a2a.TaskStateCompleted:
				fmt.Fprintf(&b, "%s Task %s on %s completed successfully\n", ui.Green("✓"), ui.Bold(task.ID), targetNode)
				if len(task.Artifacts) > 0 {
					for _, art := range task.Artifacts {
						fmt.Fprintf(&b, "\n%s %s:\n", ui.Bold("Artifact"), art.Name)
						for _, p := range art.Parts {
							if p.Text != "" {
								fmt.Fprintf(&b, "%s\n", p.Text)
							}
						}
					}
				}
				if task.Status.Message != nil {
					msgText := a2a.TextFromMessage(*task.Status.Message)
					if msgText != "" {
						fmt.Fprintf(&b, "%s\n", msgText)
					}
				}
			case a2a.TaskStatePending:
				fmt.Fprintf(&b, "%s Task %s queued on %s (pending operator approval)\n", ui.Yellow("⏸"), ui.Bold(task.ID), targetNode)
				fmt.Fprintf(&b, "  To approve: axis task approve %s %s --confirm %s\n", targetNode, task.ID, execution.ConfirmWord)
				fmt.Fprintf(&b, "  To reject:  axis task reject %s %s --reason <reason>\n", targetNode, task.ID)
			case a2a.TaskStateRejected:
				reason := ""
				if task.Status.Message != nil {
					reason = a2a.TextFromMessage(*task.Status.Message)
				}
				fmt.Fprintf(&b, "%s Task %s on %s was rejected: %s\n", ui.Red("✗"), ui.Bold(task.ID), targetNode, reason)
				_, _ = fmt.Fprint(cmd.OutOrStdout(), b.String())
				return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s was rejected: %s", task.ID, reason)}
			case a2a.TaskStateFailed:
				errText := ""
				if task.Status.Message != nil {
					errText = a2a.TextFromMessage(*task.Status.Message)
				}
				fmt.Fprintf(&b, "%s Task %s on %s failed: %s\n", ui.Red("✗"), ui.Bold(task.ID), targetNode, errText)
				_, _ = fmt.Fprint(cmd.OutOrStdout(), b.String())
				return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s failed: %s", task.ID, errText)}
			default:
				fmt.Fprintf(&b, "%s Task %s on %s: %s\n", ui.Cyan("•"), ui.Bold(task.ID), targetNode, task.Status.State)
			}

			_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
			return err
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.Flags().StringVar(&skill, "skill", "", "Skill ID to invoke (e.g. axis-status, guarded-exec)")
	cmd.Flags().StringVar(&confirm, "confirm", "", "Confirmation token (e.g. YES)")
	cmd.Flags().StringVar(&addr, "addr", "", "Override target daemon address (Unix socket or TCP host:port)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Request timeout")
	return cmd
}

func taskStatusCmd() *cobra.Command {
	var format string
	var addr string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:          "status <node> <task-id>",
		Short:        "Get status and artifacts of a delegated A2A task",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json"),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetNode := args[0]
			taskID := strings.TrimSpace(args[1])

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			client, err := buildA2AClient(targetNode, addr, timeout)
			if err != nil {
				return err
			}

			task, err := client.Get(ctx, taskID)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("get task %s from %s: %v", taskID, targetNode, err)}
			}

			if format == "json" {
				if writeErr := printOutput(cmd.OutOrStdout(), task, "json"); writeErr != nil {
					return writeErr
				}
				if task.Status.State == a2a.TaskStateFailed || task.Status.State == a2a.TaskStateRejected {
					return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s is %s", task.ID, task.Status.State)}
				}
				return nil
			}

			var b strings.Builder
			stateColor := ui.Cyan(string(task.Status.State))
			switch task.Status.State {
			case a2a.TaskStateCompleted:
				stateColor = ui.Green(string(task.Status.State))
			case a2a.TaskStatePending:
				stateColor = ui.Yellow(string(task.Status.State))
			case a2a.TaskStateFailed, a2a.TaskStateRejected:
				stateColor = ui.Red(string(task.Status.State))
			}

			fmt.Fprintf(&b, "Task ID:    %s\n", ui.Bold(task.ID))
			fmt.Fprintf(&b, "Node:       %s\n", targetNode)
			fmt.Fprintf(&b, "State:      %s\n", stateColor)
			if task.SkillID != "" {
				fmt.Fprintf(&b, "Skill:      %s\n", task.SkillID)
			}
			if !task.Status.Timestamp.IsZero() {
				fmt.Fprintf(&b, "Timestamp:  %s\n", task.Status.Timestamp.Local().Format("2006-01-02 15:04:05"))
			}
			if task.Status.Message != nil {
				msgText := a2a.TextFromMessage(*task.Status.Message)
				if msgText != "" {
					fmt.Fprintf(&b, "Message:    %s\n", msgText)
				}
			}
			if len(task.Artifacts) > 0 {
				fmt.Fprintf(&b, "Artifacts (%d):\n", len(task.Artifacts))
				for _, art := range task.Artifacts {
					fmt.Fprintf(&b, "  - %s:\n", ui.Bold(art.Name))
					for _, p := range art.Parts {
						if p.Text != "" {
							fmt.Fprintf(&b, "    %s\n", p.Text)
						}
					}
				}
			}

			_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
			if err != nil {
				return err
			}

			if task.Status.State == a2a.TaskStateFailed || task.Status.State == a2a.TaskStateRejected {
				return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s is %s", task.ID, task.Status.State)}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.Flags().StringVar(&addr, "addr", "", "Override target daemon address (Unix socket or TCP host:port)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Request timeout")
	return cmd
}

func taskApproveCmd() *cobra.Command {
	var format string
	var confirm string
	var mode string
	var addr string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:          "approve <node> <task-id>",
		Short:        "Approve and execute a pending exec-shaped A2A task",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json"),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetNode := args[0]
			taskID := strings.TrimSpace(args[1])

			confirm = strings.TrimSpace(confirm)
			if confirm != execution.ConfirmWord {
				return ExitCodeError{Code: ExitErrGeneric, Message: "--confirm YES is required to authorize task execution"}
			}

			mode = strings.ToLower(strings.TrimSpace(mode))
			if mode == "" {
				mode = execution.ModeScript
			}
			if mode != execution.ModeScript && mode != execution.ModeExec {
				return ExitCodeError{Code: ExitErrGeneric, Message: "mode must be script or exec"}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			client, err := buildA2AClient(targetNode, addr, timeout)
			if err != nil {
				return err
			}

			task, err := client.Approve(ctx, taskID, confirm, mode)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("approve task %s on %s: %v", taskID, targetNode, err)}
			}

			if format == "json" {
				if writeErr := printOutput(cmd.OutOrStdout(), task, "json"); writeErr != nil {
					return writeErr
				}
				if task.Status.State == a2a.TaskStateFailed {
					errText := ""
					if task.Status.Message != nil {
						errText = a2a.TextFromMessage(*task.Status.Message)
					}
					return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s execution failed: %s", taskID, errText)}
				}
				if task.Status.State == a2a.TaskStateRejected {
					reason := ""
					if task.Status.Message != nil {
						reason = a2a.TextFromMessage(*task.Status.Message)
					}
					return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s was rejected: %s", taskID, reason)}
				}
				return nil
			}

			var b strings.Builder
			if task.Status.State == a2a.TaskStateCompleted {
				fmt.Fprintf(&b, "%s Task %s approved and completed successfully\n", ui.Green("✓"), ui.Bold(taskID))
				if task.Status.Message != nil {
					msgText := a2a.TextFromMessage(*task.Status.Message)
					if msgText != "" {
						fmt.Fprintf(&b, "%s\n", msgText)
					}
				}
			} else if task.Status.State == a2a.TaskStateFailed {
				errText := ""
				if task.Status.Message != nil {
					errText = a2a.TextFromMessage(*task.Status.Message)
				}
				fmt.Fprintf(&b, "%s Task %s execution failed: %s\n", ui.Red("✗"), ui.Bold(taskID), errText)
				_, _ = fmt.Fprint(cmd.OutOrStdout(), b.String())
				return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s execution failed: %s", taskID, errText)}
			} else if task.Status.State == a2a.TaskStateRejected {
				reason := ""
				if task.Status.Message != nil {
					reason = a2a.TextFromMessage(*task.Status.Message)
				}
				fmt.Fprintf(&b, "%s Task %s was rejected: %s\n", ui.Red("✗"), ui.Bold(taskID), reason)
				_, _ = fmt.Fprint(cmd.OutOrStdout(), b.String())
				return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("task %s was rejected: %s", taskID, reason)}
			} else {
				fmt.Fprintf(&b, "%s Task %s approved: %s\n", ui.Cyan("•"), ui.Bold(taskID), task.Status.State)
			}

			_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
			return err
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.Flags().StringVar(&confirm, "confirm", "", "Confirmation token (required: YES)")
	cmd.Flags().StringVar(&mode, "mode", "script", "Execution mode: script or exec")
	cmd.Flags().StringVar(&addr, "addr", "", "Override target daemon address (Unix socket or TCP host:port)")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "Request timeout")
	return cmd
}

func taskRejectCmd() *cobra.Command {
	var format string
	var reason string
	var addr string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:          "reject <node> <task-id>",
		Short:        "Reject a pending exec-shaped A2A task",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		PreRunE:      validateOutputFormat(&format, "text", "json"),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetNode := args[0]
			taskID := strings.TrimSpace(args[1])

			reason = strings.TrimSpace(reason)
			if reason == "" {
				return ExitCodeError{Code: ExitErrGeneric, Message: "--reason is required to reject a task"}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			client, err := buildA2AClient(targetNode, addr, timeout)
			if err != nil {
				return err
			}

			task, err := client.Reject(ctx, taskID, reason)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("reject task %s on %s: %v", taskID, targetNode, err)}
			}

			if format == "json" {
				return printOutput(cmd.OutOrStdout(), task, "json")
			}

			var b strings.Builder
			fmt.Fprintf(&b, "%s Task %s rejected: %s\n", ui.Green("✓"), ui.Bold(taskID), reason)
			_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
			return err
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason for rejection (required)")
	cmd.Flags().StringVar(&addr, "addr", "", "Override target daemon address (Unix socket or TCP host:port)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Request timeout")
	return cmd
}
