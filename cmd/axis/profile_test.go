package main

import (
	"strings"
	"testing"
)

func TestProfileMatchCmdTextOutput(t *testing.T) {
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := profileCmd()
		cmd.SetArgs([]string{"match", "go build ./cmd/axis"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("profile match Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "MATCHED CLASS:") || !strings.Contains(stdout, "go-build") {
		t.Fatalf("expected go-build text output, got %q", stdout)
	}
	if !strings.Contains(stdout, "Required tools:") || !strings.Contains(stdout, "go") {
		t.Fatalf("expected required tools in output, got %q", stdout)
	}
}

func TestProfileMatchCmdJSONOutput(t *testing.T) {
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := profileCmd()
		cmd.SetArgs([]string{"match", "--format", "json", "go build ./cmd/axis"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("profile match json Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, `"match":`) || !strings.Contains(stdout, `"class": "go-build"`) {
		t.Fatalf("expected JSON match payload, got %q", stdout)
	}
	if !strings.Contains(stdout, `"required_tools": [`) {
		t.Fatalf("expected JSON requirements payload, got %q", stdout)
	}
}

func TestProfileMatchCmdYAMLOutput(t *testing.T) {
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := profileCmd()
		cmd.SetArgs([]string{"match", "--format", "yaml", "go build ./cmd/axis"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("profile match yaml Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "match:") || !strings.Contains(stdout, "class: go-build") {
		t.Fatalf("expected YAML match payload, got %q", stdout)
	}
	if !strings.Contains(stdout, "requirements:") {
		t.Fatalf("expected YAML requirements payload, got %q", stdout)
	}
}
