package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestNodeFactsWiredAndClusterDoesNotOwnFacts(t *testing.T) {
	if _, _, err := nodeCmd().Find([]string{"facts"}); err != nil {
		t.Fatalf("axis node facts: %v", err)
	}
	if _, _, err := clusterCmd().Find([]string{"status"}); err != nil {
		t.Fatalf("axis cluster status: %v", err)
	}
	if _, _, err := clusterCmd().Find([]string{"facts"}); err == nil {
		t.Fatal("axis cluster facts must not exist")
	}
	if _, _, err := clusterCmd().Find([]string{"doctor"}); err == nil {
		t.Fatal("axis cluster doctor must not exist")
	}
}

func TestChatAndLLMPrintRemoval(t *testing.T) {
	for _, name := range []string{"chat", "llm"} {
		cmd := newRootCmd()
		cmd.SetArgs([]string{name, "hello"})
		runErr := cmd.Execute()
		if runErr == nil {
			t.Fatalf("%s: expected non-nil error", name)
		}
		if !strings.Contains(runErr.Error(), "was removed") {
			t.Fatalf("%s removal message missing: %v", name, runErr)
		}
	}
}

func TestNodesSlashPrintsRetirement(t *testing.T) {
	session := &agentREPLSession{
		Agent: agent.New(agent.Config{Endpoint: "http://127.0.0.1:9", Model: "x", MaxTokens: 1}),
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return nil, nil
		},
		Out:    &bytes.Buffer{},
		ErrOut: &bytes.Buffer{},
	}
	var w, errW bytes.Buffer
	session.Out = &w
	session.ErrOut = &errW
	handled, shouldExit, err := handleREPLSlashCommand(session, "/nodes")
	if err != nil {
		t.Fatalf("/nodes: %v", err)
	}
	if !handled || shouldExit {
		t.Fatalf("/nodes handled=%v exit=%v", handled, shouldExit)
	}
	text := w.String() + errW.String()
	if !strings.Contains(text, "/cluster") || !strings.Contains(text, "axis cluster status") {
		t.Fatalf("/nodes must point at /cluster and axis cluster status, got %q", text)
	}
}

func TestClusterSlashPrintsSessionSnapshotAge(t *testing.T) {
	collected := time.Now().UTC().Add(-12 * time.Minute)
	var w, errW bytes.Buffer
	session := &agentREPLSession{
		Agent: agent.New(agent.Config{Endpoint: "http://127.0.0.1:9", Model: "x", MaxTokens: 1}),
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return &runtimectx.Context{
				Snapshot: &models.ClusterSnapshot{
					Timestamp: collected,
					Nodes: []models.NodeFacts{
						{Name: "node-a", Hostname: "node-a", Status: models.StatusComplete, OS: "linux", Arch: "amd64"},
						{Name: "node-b", Hostname: "node-b", Status: models.StatusComplete, OS: "linux", Arch: "amd64"},
					},
				},
				Config: &config.Config{},
			}, nil
		},
		Out:    &w,
		ErrOut: &errW,
	}
	handled, shouldExit, err := handleREPLSlashCommand(session, "/cluster")
	if err != nil {
		t.Fatalf("/cluster: %v", err)
	}
	if !handled || shouldExit {
		t.Fatalf("/cluster handled=%v exit=%v", handled, shouldExit)
	}
	text := w.String() + errW.String()
	if !strings.Contains(text, "session-snapshot") {
		t.Fatalf("/cluster must name session-snapshot, got %q", text)
	}
	if !strings.Contains(text, "12m") && !strings.Contains(text, "11m") && !strings.Contains(text, "13m") {
		t.Fatalf("/cluster must print age from Timestamp, got %q", text)
	}
	if !strings.Contains(text, "node-a") || !strings.Contains(text, "node-b") {
		t.Fatalf("/cluster must list session nodes, got %q", text)
	}
}
