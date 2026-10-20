package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// consoleFleet is the shared, mutex-guarded fleet snapshot behind the footer
// gauge, @ completion, and tool evidence badges. Readers only lock and copy;
// a stale snapshot is refreshed in the background (one refresh at a time,
// bounded by consoleRuntimeLoadWait), so the UI loop never waits on the
// loader.
type consoleFleet struct {
	ctx    context.Context
	loader func(context.Context) (*runtimectx.Context, error)
	wait   time.Duration

	mu         sync.Mutex
	primed     bool
	refreshing bool
	checkedAt  time.Time
	summary    string
	nodes      []string
	pubID      string
	source     string
	assembled  time.Time
}

func newConsoleFleet(ctx context.Context, loader func(context.Context) (*runtimectx.Context, error)) *consoleFleet {
	return &consoleFleet{ctx: ctx, loader: loader, wait: consoleRuntimeLoadWait}
}

// prime fills the snapshot from an already-loaded runtime.
func (f *consoleFleet) prime(rt *runtimectx.Context, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fillLocked(rt, err)
}

// refresh loads synchronously (bounded) and fills the snapshot.
func (f *consoleFleet) refresh() {
	rt, err := loadRuntimeBounded(f.ctx, f.loader, f.wait)
	f.prime(rt, err)
}

func (f *consoleFleet) fillLocked(rt *runtimectx.Context, err error) {
	f.primed = true
	f.checkedAt = time.Now()
	if err != nil || rt == nil || rt.Snapshot == nil {
		f.summary, f.pubID, f.source, f.assembled = "unknown", "", "", time.Time{}
		return
	}
	f.pubID, f.source, f.assembled = "", "", time.Time{}
	if rt.Snapshot.Publication != nil {
		f.pubID = rt.Snapshot.Publication.ID
		f.source = rt.Snapshot.Publication.Source
		f.assembled = rt.Snapshot.Publication.AssembledAt
	}
	if f.assembled.IsZero() {
		f.assembled = rt.Snapshot.Timestamp
	}
	s := rt.Snapshot.Summary
	switch {
	case s.TotalNodes == 0:
		f.summary = "local"
	case s.ReachableNodes == s.TotalNodes:
		f.summary = fmt.Sprintf("%d/%d ok", s.ReachableNodes, s.TotalNodes)
	default:
		f.summary = fmt.Sprintf("%d/%d ok (%d unreach)", s.ReachableNodes, s.TotalNodes, s.TotalNodes-s.ReachableNodes)
	}
	nodes := make([]string, 0, len(rt.Snapshot.Nodes))
	for _, n := range rt.Snapshot.Nodes {
		if n.Name != "" {
			nodes = append(nodes, n.Name)
		}
	}
	f.nodes = nodes
}

// footerLabel is the footer's snapshot segment. Empty until primed, so the
// footer never claims a snapshot state before one was read.
func (f *consoleFleet) footerLabel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.primed {
		return ""
	}
	if !f.refreshing && time.Since(f.checkedAt) >= 5*time.Second {
		f.refreshing = true
		// Owned by the fleet; exits when the bounded load returns.
		go func() {
			rt, err := loadRuntimeBounded(f.ctx, f.loader, f.wait)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.refreshing = false
			if f.ctx.Err() == nil {
				f.fillLocked(rt, err)
			}
		}()
	}
	if f.pubID != "" {
		return "snap " + f.pubID
	}
	if !f.assembled.IsZero() {
		return "snap " + f.assembled.UTC().Format("15:04Z")
	}
	return "no snapshot"
}

func (f *consoleFleet) nodeNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.nodes...)
}

// evidence labels a finished tool cell from the latest snapshot probe.
func (f *consoleFleet) evidence(name string) (string, string) {
	intent := map[string]string{
		"axis_status": "cluster status",
		"axis_facts":  "local facts",
		"axis_place":  "placement",
	}[name]
	if intent == "" {
		return "", ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return toolEvidenceBadge(f.source, f.pubID), intent
}

// consoleInitialHistory seeds the prompt-recall ring: the conversation's user
// prompts, then the persisted prompt history, consecutive duplicates
// collapsed (a resumed conversation and the file overlap).
func consoleInitialHistory(a *agent.Agent) []string {
	var seeded []string
	if a != nil && a.Conversation() != nil {
		for _, msg := range a.Conversation().Messages() {
			if msg.Role == chat.RoleUser && strings.TrimSpace(msg.Content) != "" {
				seeded = append(seeded, msg.Content)
			}
		}
	}
	seeded = append(seeded, loadConsoleHistory(consoleHistoryPath(), 100)...)
	var history []string
	for _, h := range seeded {
		if len(history) > 0 && history[len(history)-1] == h {
			continue
		}
		history = append(history, h)
	}
	trimConsoleHistory(consoleHistoryPath(), 500)
	return history
}

// consoleBaseOptions builds the console options both console paths share.
// Every agent read goes through the launcher, so the same options work before
// and after the agent is bound.
func consoleBaseOptions(ctx context.Context, l *consoleLauncher, fleet *consoleFleet, history []string) console.Options {
	l.evidence = fleet.evidence
	mode := func() string {
		if a := l.boundAgent(); a != nil {
			return agent.DisplayScope(a.Autonomy())
		}
		return "default"
	}
	tokens := func() int {
		if a := l.boundAgent(); a != nil {
			return a.ContextTokens()
		}
		return 0
	}
	usage := func() (int, int, int) { return consoleFooterUsage(l.boundAgent()) }
	footer := console.NewStatusFooter(console.StatusFooterConfig{
		Model: func() string {
			b, _ := l.bound()
			return consoleFooterModel(b.agent, b.target)
		},
		UsedTokens: tokens,
		MaxTokens: func() int {
			if a := l.boundAgent(); a != nil {
				return a.MaxTokens()
			}
			return 32768
		},
		UsageStats: usage,
		Mode:       mode,
		Snapshot:   fleet.footerLabel,
	})
	promptHistoryPath := consoleHistoryPath()
	return console.Options{
		Submit:        l.submit(ctx),
		Cancel:        l.cancel,
		Footer:        footer,
		Mode:          mode,
		History:       history,
		TokenEstimate: tokens,
		UsageStats:    usage,
		HistorySink: func(text string) tea.Cmd {
			return appendConsoleHistory(promptHistoryPath, text)
		},
		AtCandidates: fleet.nodeNames,
	}
}

// agentToolRuntime derives the prompt summary, cluster knowledge, and tool
// context an agent session reads from a loaded runtime. rt may be nil.
func agentToolRuntime(rt *runtimectx.Context, loader func(context.Context) (*runtimectx.Context, error)) (*chat.ClusterSummaryForPrompt, *knowledge.ClusterKnowledge, *agent.ToolContext) {
	var cluster *chat.ClusterSummaryForPrompt
	var k *knowledge.ClusterKnowledge
	var initialView *agent.RuntimeView
	if rt != nil {
		if rt.Snapshot != nil {
			cluster = chat.BuildClusterSummary(rt.Snapshot)
			bestNode := ""
			if len(rt.Snapshot.Nodes) > 0 {
				bestNode = rt.Snapshot.Nodes[0].Name
			}
			k = knowledge.Build(rt.Snapshot, rt.State, bestNode)
		}
		initialView = &agent.RuntimeView{
			Config:    rt.Config,
			Snapshot:  rt.Snapshot,
			State:     rt.State,
			Ledger:    rt.Ledger,
			Skills:    rt.Skills,
			Knowledge: k,
		}
	}
	tc := agent.NewToolContext(initialView, func(ctx context.Context) (*agent.RuntimeView, error) {
		newRt, err := loader(ctx)
		if err != nil {
			return nil, err
		}
		if newRt == nil {
			return nil, fmt.Errorf("loaded runtime context is nil")
		}
		bestNode := ""
		if newRt.Snapshot != nil && len(newRt.Snapshot.Nodes) > 0 {
			bestNode = newRt.Snapshot.Nodes[0].Name
		}
		return &agent.RuntimeView{
			Config:    newRt.Config,
			Snapshot:  newRt.Snapshot,
			State:     newRt.State,
			Ledger:    newRt.Ledger,
			Skills:    newRt.Skills,
			Knowledge: knowledge.Build(newRt.Snapshot, newRt.State, bestNode),
		}, nil
	})
	return cluster, k, tc
}

// loadRuntimeBounded runs loader under a deadline. The loader runs in its own
// goroutine so a loader that ignores its context still cannot hold the caller
// past wait; that goroutine exits when the loader returns (the result channel
// is buffered), and a loader panic becomes an error.
func loadRuntimeBounded(ctx context.Context, loader func(context.Context) (*runtimectx.Context, error), wait time.Duration) (*runtimectx.Context, error) {
	if loader == nil {
		return nil, errors.New("no runtime loader")
	}
	lctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	type result struct {
		rt  *runtimectx.Context
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("runtime loader panicked: %v", r)}
			}
		}()
		rt, err := loader(lctx)
		ch <- result{rt: rt, err: err}
	}()
	select {
	case r := <-ch:
		return r.rt, r.err
	case <-lctx.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("timed out after %s", wait)
	}
}
