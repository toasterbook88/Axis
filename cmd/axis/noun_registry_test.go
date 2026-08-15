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

func TestNounRegistryTiers(t *testing.T) {
	reg := nounRegistry()
	operate := map[string]bool{}
	inspect := map[string]bool{}
	for _, n := range reg {
		switch n.Tier {
		case nounOperate:
			operate[n.Name] = true
		case nounInspect:
			inspect[n.Name] = true
		default:
			t.Fatalf("unknown tier %q on %s", n.Tier, n.Name)
		}
	}
	for _, name := range []string{"cluster", "node", "model", "task", "daemon", "agent"} {
		if !operate[name] {
			t.Fatalf("missing operate noun %q", name)
		}
	}
	for _, name := range []string{"doctor", "mesh", "ai", "init", "serve", "update", "version"} {
		if !inspect[name] {
			t.Fatalf("missing inspect noun %q", name)
		}
	}
	cluster := nounByName(reg, "cluster")
	if cluster.CLIFreshness != freshnessLive || cluster.SlashFreshness != freshnessSession {
		t.Fatalf("cluster freshness cli=%s slash=%s", cluster.CLIFreshness, cluster.SlashFreshness)
	}
}

func TestSlashSubsetOfRegistryOrSession(t *testing.T) {
	allowed := map[string]bool{}
	for _, n := range nounRegistry() {
		allowed["/"+n.Name] = true
	}
	// /facts is the node-scope slash, not a second noun.
	allowed["/facts"] = true
	for _, s := range sessionOnlySlashes() {
		allowed[s] = true
	}
	for _, s := range liveAgentSlashes() {
		if !allowed[s] {
			t.Fatalf("slash %q is not in registry or session-only", s)
		}
	}
}

func TestOperateMinusSlashEqualsGaps(t *testing.T) {
	sessionOnly := map[string]bool{}
	for _, s := range sessionOnlySlashes() {
		sessionOnly[strings.TrimPrefix(s, "/")] = true
	}
	slashes := map[string]bool{}
	for _, s := range liveAgentSlashes() {
		name := strings.TrimPrefix(s, "/")
		if sessionOnly[name] {
			continue
		}
		slashes[name] = true
	}
	var missing []string
	for _, n := range nounRegistry() {
		if n.Tier != nounOperate {
			continue
		}
		covered := slashes[n.Name]
		if n.Name == "node" {
			covered = slashes["facts"] || slashes["node"]
		}
		if n.Name == "agent" {
			// The REPL is the agent noun; slashes live inside it.
			covered = true
		}
		if !covered {
			missing = append(missing, n.Name)
		}

	}
	got := strings.Join(missing, ",")
	want := strings.Join(slashGaps(), ",")
	if got != want {
		t.Fatalf("operate−slash = [%s], slashGaps = [%s]", got, want)
	}
}

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

func liveAgentSlashes() []string {
	a := agent.New(agent.Config{Endpoint: "http://127.0.0.1:9", Model: "x", MaxTokens: 1})
	var errW bytes.Buffer
	session := &agentREPLSession{
		Agent:  a,
		Out:    &bytes.Buffer{},
		ErrOut: &errW,
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return nil, nil
		},
	}
	_, _, _ = handleREPLSlashCommand(session, "/help")
	var out []string
	for _, line := range strings.Split(errW.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "/") {
			continue
		}
		tok := strings.Fields(line)[0]
		tok = strings.TrimSuffix(tok, ",")
		if tok == "/quit" {
			continue
		}
		out = append(out, tok)
	}
	return out
}
