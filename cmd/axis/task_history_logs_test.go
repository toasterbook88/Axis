package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/state"
)

func TestTaskHistoryCmd(t *testing.T) {
	st := &state.ClusterState{
		TaskHistory: []state.TaskExecutionRecord{
			{
				ExecID:      "exec-1234567890-alpha",
				Description: "test task 1",
				Node:        "alpha",
				ExitCode:    0,
				PeakRAMMB:   512,
				WallTimeMS:  1200,
				Timestamp:   time.Now().Add(-10 * time.Minute),
			},
			{
				ExecID:      "exec-0987654321-beta",
				Description: "test task 2 failing",
				Node:        "beta",
				ExitCode:    1,
				PeakRAMMB:   0,
				WallTimeMS:  350,
				Timestamp:   time.Now().Add(-5 * time.Minute),
			},
		},
	}
	restore := stubPlacementState(t, st, nil)
	defer restore()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := taskHistoryCmd()
		cmd.SetArgs([]string{})
		return cmd.Execute()
	})

	if err != nil {
		t.Fatalf("task history Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}

	if !strings.Contains(stdout, "AXIS Task Execution History") {
		t.Fatalf("expected header, got %q", stdout)
	}
	if !strings.Contains(stdout, "exec-123") || !strings.Contains(stdout, "alpha") || !strings.Contains(stdout, "1.20s") {
		t.Errorf("missing record 1 info, got %q", stdout)
	}
	if !strings.Contains(stdout, "exec-098") || !strings.Contains(stdout, "beta") || !strings.Contains(stdout, "350ms") {
		t.Errorf("missing record 2 info, got %q", stdout)
	}
}

func TestTaskLogsCmd(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	logDir := filepath.Join(tempHome, ".axis", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("failed to create log dir: %v", err)
	}

	logID := "abcdef123456"
	logContent := "line 1: starting task\nline 2: process completed successfully\n"
	logPath := filepath.Join(logDir, fmt.Sprintf("task-%s.log", logID))
	if err := os.WriteFile(logPath, []byte(logContent), 0644); err != nil {
		t.Fatalf("failed to write mock log: %v", err)
	}

	// Test exact match
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := taskLogsCmd()
		cmd.SetArgs([]string{logID})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("task logs Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if stdout != logContent {
		t.Fatalf("expected log content %q, got %q", logContent, stdout)
	}

	// Test fuzzy prefix match
	stdout, stderr, err = captureProcessOutput(t, func() error {
		cmd := taskLogsCmd()
		cmd.SetArgs([]string{"abc"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("task logs fuzzy Execute: %v", err)
	}
	if stdout != logContent {
		t.Fatalf("expected fuzzy log content %q, got %q", logContent, stdout)
	}

	// Test non-existent match
	_, _, err = captureProcessOutput(t, func() error {
		cmd := taskLogsCmd()
		cmd.SetArgs([]string{"xyz"})
		return cmd.Execute()
	})
	if err == nil {
		t.Fatal("expected error for non-existent log ID")
	}
	if !strings.Contains(err.Error(), "no logs found matching execution ID") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Test path traversal rejection
	_, _, err = captureProcessOutput(t, func() error {
		cmd := taskLogsCmd()
		cmd.SetArgs([]string{"../../etc/passwd"})
		return cmd.Execute()
	})
	if err == nil {
		t.Fatal("expected error for path traversal log ID")
	}
	if !strings.Contains(err.Error(), "invalid execution ID") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
