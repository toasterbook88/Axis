package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
)

func TestHandleREPLSlashCommand(t *testing.T) {
	a := agent.New(agent.Config{
		Endpoint:  "http://localhost:11434",
		Model:     "granite3.1-moe:1b",
		MaxTokens: 4096,
	})

	var w, errW bytes.Buffer

	// Test /help
	handled, shouldExit, err := handleREPLSlashCommand("/help", a, &w, &errW, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected /help to be handled")
	}
	if shouldExit {
		t.Error("expected /help not to cause exit")
	}
	if !strings.Contains(errW.String(), "Available commands:") {
		t.Errorf("expected help output, got %q", errW.String())
	}

	// Test /context
	errW.Reset()
	handled, shouldExit, err = handleREPLSlashCommand("/context", a, &w, &errW, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected /context to be handled")
	}
	if !strings.Contains(errW.String(), "Tokens used:") {
		t.Errorf("expected context output, got %q", errW.String())
	}

	// Test /clear
	errW.Reset()
	a.Conversation().Append(chat.Message{Role: chat.RoleUser, Content: "hello"})
	if a.Conversation().Len() <= 1 {
		t.Fatal("expected conversation to have messages")
	}
	handled, shouldExit, err = handleREPLSlashCommand("/clear", a, &w, &errW, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected /clear to be handled")
	}
	// Check that non-system messages are cleared
	for _, msg := range a.Conversation().Messages() {
		if msg.Role != chat.RoleSystem {
			t.Errorf("expected conversation to be cleared of non-system messages, found role %q", msg.Role)
		}
	}

	// Test /exit
	handled, shouldExit, err = handleREPLSlashCommand("/exit", a, &w, &errW, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled || !shouldExit {
		t.Error("expected /exit to handle and cause exit")
	}
}
