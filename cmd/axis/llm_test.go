package main

import (
	"strings"
	"testing"
)

func TestLLMCmdSurface(t *testing.T) {
	cmd := llmCmd()
	if got := cmd.Name(); got != "llm" {
		t.Fatalf("llmCmd name = %q, want llm", got)
	}
}

func TestLLMCmdRegisteredOnRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "llm" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("llm command not registered on root")
	}
}

func TestLLMCmdPrintsRemoval(t *testing.T) {
	cmd := llmCmd()
	cmd.SetArgs([]string{"go build the binary"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected removal error")
	}
	if !strings.Contains(err.Error(), "was removed") || !strings.Contains(err.Error(), "axis ai route") {
		t.Fatalf("removal message = %v", err)
	}
}
