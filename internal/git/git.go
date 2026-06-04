package git

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// RepoState captures key details of a local git repository.
type RepoState struct {
	IsRepo      bool     `json:"is_repo"`
	Branch      string   `json:"branch,omitempty"`
	Commit      string   `json:"commit,omitempty"`
	Subject     string   `json:"subject,omitempty"`
	IsDirty     bool     `json:"is_dirty"`
	DirtyFiles  []string `json:"dirty_files,omitempty"`
	AheadCount  int      `json:"ahead_count,omitempty"`
	BehindCount int      `json:"behind_count,omitempty"`
}

func runGitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// GetRepoState queries the git command line tool at the specified directory
// to collect repository metadata. If git is not installed or the directory
// is not inside a git repository, it returns a RepoState with IsRepo: false.
func GetRepoState(dir string) (RepoState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := exec.LookPath("git"); err != nil {
		return RepoState{IsRepo: false}, nil
	}

	out, err := runGitCmd(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil || out != "true" {
		return RepoState{IsRepo: false}, nil
	}

	state := RepoState{IsRepo: true}

	if br, err := runGitCmd(ctx, dir, "branch", "--show-current"); err == nil && br != "" {
		state.Branch = br
	} else if brRef, err := runGitCmd(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && brRef != "" {
		state.Branch = brRef
	}

	if commit, err := runGitCmd(ctx, dir, "rev-parse", "HEAD"); err == nil && commit != "" {
		state.Commit = commit
	}

	if subject, err := runGitCmd(ctx, dir, "log", "-1", "--format=%s"); err == nil && subject != "" {
		state.Subject = subject
	}

	if statusLines, err := runGitCmd(ctx, dir, "status", "--porcelain"); err == nil {
		if statusLines != "" {
			state.IsDirty = true
			lines := strings.Split(statusLines, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				// Format: " M path/to/file" or "?? path/to/file"
				parts := strings.SplitN(line, " ", 2)
				if len(parts) == 2 {
					state.DirtyFiles = append(state.DirtyFiles, strings.TrimSpace(parts[1]))
				} else {
					state.DirtyFiles = append(state.DirtyFiles, line)
				}
			}
		}
	}

	if aheadStr, err := runGitCmd(ctx, dir, "rev-list", "--count", "@{u}..HEAD"); err == nil && aheadStr != "" {
		if val, err := strconv.Atoi(aheadStr); err == nil {
			state.AheadCount = val
		}
	}
	if behindStr, err := runGitCmd(ctx, dir, "rev-list", "--count", "HEAD..@{u}"); err == nil && behindStr != "" {
		if val, err := strconv.Atoi(behindStr); err == nil {
			state.BehindCount = val
		}
	}

	return state, nil
}
