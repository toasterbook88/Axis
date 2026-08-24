package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func TestSaveAgentConversationSurfacesPersistenceFailure(t *testing.T) {
	conversation := chat.NewConversation(0)
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("occupied"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	var errOut bytes.Buffer
	err := saveAgentConversation(conversation, filepath.Join(parentFile, "history.json"), &errOut)
	if err == nil {
		t.Fatal("expected history persistence error")
	}
	if got := errOut.String(); !strings.Contains(got, "warning: could not save conversation:") {
		t.Fatalf("warning = %q", got)
	}
}

func TestSaveAgentConversationSucceedsWithoutWarning(t *testing.T) {
	conversation := chat.NewConversation(0)
	var errOut bytes.Buffer
	if err := saveAgentConversation(conversation, filepath.Join(t.TempDir(), "history.json"), &errOut); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := errOut.String(); got != "" {
		t.Fatalf("unexpected warning = %q", got)
	}
}
