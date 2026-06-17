package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/git"
)

type gitDiffArgs struct {
	Staged bool `json:"staged,omitempty"`
}

type gitLogArgs struct {
	Count int `json:"count,omitempty"`
}

func (r *ToolRegistry) registerGitTools() {
	// 1. git_status
	r.add("git_status",
		"Get the current status of the git repository (branch, commit, dirty files, ahead/behind counts).",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			state, err := git.GetRepoState(".")
			if err != nil {
				return "", fmt.Errorf("git status error: %w", err)
			}
			if !state.IsRepo {
				return "Not a git repository", nil
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "Branch: %s\nCommit: %s\nSubject: %s\n", state.Branch, state.Commit, state.Subject)
			if state.IsDirty {
				fmt.Fprintf(&sb, "Status: Dirty (%d files changed)\n", state.DirtyCount)
				if len(state.DirtyFiles) > 0 {
					sb.WriteString("Modified files:\n")
					for _, f := range state.DirtyFiles {
						fmt.Fprintf(&sb, "  - %s\n", f)
					}
				}
			} else {
				sb.WriteString("Status: Clean\n")
			}
			if state.AheadCount > 0 {
				fmt.Fprintf(&sb, "Ahead of upstream by %d commit(s)\n", state.AheadCount)
			}
			if state.BehindCount > 0 {
				fmt.Fprintf(&sb, "Behind upstream by %d commit(s)\n", state.BehindCount)
			}
			return sb.String(), nil
		},
	)

	// 2. git_diff
	r.add("git_diff",
		"Show differences in the git repository (unstaged or staged changes).",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"staged":{"type":"boolean","description":"If true, show staged changes (equivalent to git diff --cached)"}
			}
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a gitDiffArgs
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}

			gitArgs := []string{"diff"}
			if a.Staged {
				gitArgs = append(gitArgs, "--cached")
			}

			cmd := exec.CommandContext(ctx, "git", gitArgs...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()
			output := stdout.String()
			if stderr.Len() > 0 {
				output += "\n[stderr] " + stderr.String()
			}

			if err != nil {
				return output, fmt.Errorf("git diff failed: %w", err)
			}

			if output == "" {
				return "No changes found.", nil
			}

			// Cap output
			const maxOutput = 16000
			if len([]rune(output)) > maxOutput {
				output = truncateRune(output, maxOutput) + fmt.Sprintf("\n... [truncated to %d chars]", maxOutput)
			}
			return output, nil
		},
	)

	// 3. git_log
	r.add("git_log",
		"Show recent git commits.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"count":{"type":"integer","description":"Number of commits to return (default 10)"}
			}
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a gitLogArgs
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			count := a.Count
			if count <= 0 {
				count = 10
			}

			cmd := exec.CommandContext(ctx, "git", "log", "--oneline", "-n", strconv.Itoa(count))
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()
			output := stdout.String()
			if stderr.Len() > 0 {
				output += "\n[stderr] " + stderr.String()
			}

			if err != nil {
				return output, fmt.Errorf("git log failed: %w", err)
			}

			return output, nil
		},
	)
}
