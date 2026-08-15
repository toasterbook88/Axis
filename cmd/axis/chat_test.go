package main

import (
	"strings"
	"testing"
)

func TestChatCmdSurface(t *testing.T) {
	cmd := chatCmd()
	if got := cmd.Name(); got != "chat" {
		t.Fatalf("chatCmd name = %q, want chat", got)
	}
}

func TestChatCmdRegisteredOnRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "chat" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("chat command not registered on root")
	}
}

func TestChatCmdPrintsRemoval(t *testing.T) {
	cmd := chatCmd()
	cmd.SetArgs([]string{"hi"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected removal error")
	}
	if !strings.Contains(err.Error(), "was removed") || !strings.Contains(err.Error(), "axis agent") {
		t.Fatalf("removal message = %v", err)
	}
}
