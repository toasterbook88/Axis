package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/git"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
)

// maxParallelTools caps concurrent tool calls within a single agent turn.
const maxParallelTools = 6

type TaskRequest struct {
	Description     string `json:"description"`
	Mode            string `json:"mode,omitempty"`
	TargetNode      string `json:"target_node,omitempty"`
	MemoryRequestMB int64  `json:"memory_request_mb,omitempty"`
	MemoryMaxMB     int64  `json:"memory_max_mb,omitempty"`
	ExposePorts     string `json:"expose_ports,omitempty"`
}

// TaskRunner executes an approved cluster task.
type TaskRunner func(ctx context.Context, prepared execution.PreparedExecution) (string, error)

// Agent drives a multi-turn tool-calling loop on top of the chat client.
// It is strictly a consumer of the fact plane — its output is never cluster truth.
type Agent struct {
	client  ChatBackend
	conv    *chat.Conversation
	tools   *ToolRegistry
	confirm ConfirmFunc
	// baseConfirm is the surface-installed confirm without autonomy
	// wrapping. SetAutonomy re-wraps this instead of StdinConfirm so a
	// raw-mode surface's confirm survives mode changes.
	baseConfirm ConfirmFunc
	runShell    ShellRunner
	runOnNode   NodeShellRunner
	runTask     TaskRunner
	safety      ShellSafetyGate
	output      io.Writer
	observer    Observer
	runMu       sync.Mutex
	maxTurns    int
	maxTokens   int
	verbose     bool
	dryRun      bool
	toolContext *ToolContext
	model       string
	// usageMu guards the session usage accumulator. Responses report real
	// token counts on the returned Message (Ollama final chunk, cloud usage
	// block); accumulateUsage totals them. Stats() reads it from the tea
	// event loop and the -p summary reads it after Run returns.
	usageMu    sync.RWMutex
	usageIn    int
	usageOut   int
	usageTurns int
	// modelMu guards model for cross-goroutine readers: the console footer
	// renders Model() on the tea event loop while /model switches write
	// SetModel from a turn goroutine. (OwnerLabel and subagent construction
	// read the field inside the agent's own serialized turn loop.)
	modelMu                 sync.RWMutex
	allowRawCommandEvidence bool
	securityClass           BackendSecurityClass
	// autoApproveAll is toggled when the operator selects "always" in confirmation.
	autoApproveAll bool
	// blockAll is toggled when the operator selects "never" in confirmation.
	blockAll    bool
	mcpRegistry *mcpclient.Registry
	// dispatchMu guards autoApproveAll, blockAll, and the confirm func.
	// It must not be held across a blocking confirm: the console footer
	// calls Autonomy() on the UI goroutine, and that takes this lock.
	// Holding it while the UI paints deadlocks the approval overlay, so
	// the screen stays on "working".
	dispatchMu sync.Mutex
	// confirmGate serializes operator prompts so concurrent tool calls
	// do not interleave overlays. It is not taken by the UI goroutine.
	confirmGate sync.Mutex
	// runnerMu protects runShell/runOnNode against concurrent /model refresh
	// while tool dispatch and background launches read them.
	runnerMu sync.RWMutex
	// subAgentDepth tracks nesting of spawn_subagent calls to prevent runaway
	// recursion. The root agent is depth 0; children inherit depth+1.
	subAgentDepth int
	// backgroundTasks tracks async commands started by run_background so the
	// agent can poll them with check_task without blocking the loop.
	backgroundTasks *backgroundTaskStore
	// branchStack holds session branch snapshots for branch_session /
	// rollback_session, letting the model try risky approaches and rewind.
	branchStack []branchSnapshot
	// autonomy tracks the active autonomy policy for the session.
	autonomy AutonomyMode
}

// Config configures an Agent.
type Config struct {
	Endpoint                string               // Ollama endpoint (default: chat.DefaultEndpoint)
	Model                   string               // Ollama model name
	Backend                 ChatBackend          // Optional custom backend override
	MaxTurns                int                  // Maximum agent loop iterations (default: 10)
	MaxTokens               int                  // Conversation token budget (default: 4096)
	AutoApprove             bool                 // Auto-approve safe commands (score < 70)
	Autonomy                AutonomyMode         // Autonomy mode: default, edit, or full (controls auto-approval breadth)
	SystemExtra             string               // Extra text appended to system prompt
	Verbose                 bool                 // Emit trace output for tool calls and turns
	DryRun                  bool                 // Plan tool calls without executing them
	ShellTimeout            time.Duration        // Timeout for run_shell commands (default: 5m)
	AllowRawCommandEvidence bool                 // Include raw command text in local evidence
	BackendSecurityClass    BackendSecurityClass // Local or remote backend trust classification
	// Cluster is optional. If non-nil, the agent injects a cluster summary
	// into the system prompt and uses it for safety checks.
	Cluster *chat.ClusterSummaryForPrompt

	// Knowledge is optional. Used for safety gating shell commands.
	Knowledge *knowledge.ClusterKnowledge

	// Snapshot and State are used to initialize tools.
	ToolContext *ToolContext

	// Output is where the agent writes assistant text and traces.
	Output io.Writer

	// Observer, when non-nil, receives structured loop events (tool calls,
	// results, turns, shell execs) instead of having them formatted to
	// Output. Surfaces that render their own transcript set this; the plain
	// CLI leaves it nil. See observer.go.
	Observer Observer

	// Confirm is the confirmation function. If nil, StdinConfirm() is used.
	Confirm ConfirmFunc

	// RunShell executes an approved shell command. If nil, the agent falls back
	// to a direct local shell helper.
	RunShell ShellRunner

	// RunOnNode executes an approved shell command on a named cluster node.
	// If nil, run_on_node fails with a configuration error (no raw SSH bypass).
	RunOnNode NodeShellRunner

	// RunTask executes an approved remote/cluster task.
	RunTask TaskRunner

	// MCPRegistry is a connected registry of external MCP servers (optional).
	MCPRegistry *mcpclient.Registry
}

// NodeShellRunner runs an approved command on a named cluster node.
type NodeShellRunner func(ctx context.Context, node, command string) (string, error)

// New creates an Agent from the given configuration.
func New(cfg Config) *Agent {
	if cfg.Endpoint == "" {
		cfg.Endpoint = chat.DefaultEndpoint
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 25
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 32768
	}
	if cfg.Output == nil {
		cfg.Output = io.Discard
	}

	client := cfg.Backend
	if client == nil {
		client = chat.NewClient(cfg.Endpoint, cfg.Model)
	}
	conv := chat.NewConversation(cfg.MaxTokens)

	// Build system prompt.
	sysPrompt := chat.BuildSystemPrompt(cfg.Cluster, cfg.SystemExtra)

	// Inject Git context if available
	if repoState, err := git.GetRepoState("."); err == nil && repoState.IsRepo {
		sysPrompt += fmt.Sprintf("\n\nGit Repository Context:\n- Branch: %s\n- HEAD Commit: %s (Subject: %s)\n", repoState.Branch, repoState.Commit, repoState.Subject)
		if repoState.IsDirty {
			sysPrompt += fmt.Sprintf("- Status: Dirty (%d files changed)\n", repoState.DirtyCount)
		} else {
			sysPrompt += "- Status: Clean\n"
		}
	}

	// Inject nearest AGENTS.md (project rules) when present — truthful workspace
	// context for the operator's repo, subordinate to the Truth Rule.
	if path, content, ok := loadRepoInstructions("."); ok {
		sysPrompt += formatRepoInstructionsBlock(path, content)
	}

	sysPrompt += "\n\nYou have access to tools. When you need cluster data or file operations, use the tools rather than guessing. " +
		"If a tool call fails, read the error and try a corrected call. " +
		"Never fabricate tool results. Footer status is chrome, not cluster inventory.\n"
	conv.Append(chat.Message{Role: chat.RoleSystem, Content: sysPrompt})

	// Build tool registry.
	tc := cfg.ToolContext
	if tc == nil {
		tc = NewToolContext(&RuntimeView{}, nil)
	}
	tools := NewToolRegistry(tc)
	tools.SetScope(ScopeFor(cfg.Autonomy))
	if cfg.MCPRegistry != nil {
		tools.RegisterMCPTools(cfg.MCPRegistry)
	}
	conv.Append(chat.Message{Role: chat.RoleSystem, Content: visibleToolsMessage(tools)})
	// When Cortex cluster-memory tools are connected, make them first-class:
	// instruct the model to recall before non-trivial work, remember discoveries,
	// lock shared files before mutation, and publish significant events — so
	// multi-agent cluster work stays coherent across sessions and nodes.
	if tools.HasTool("mcp_cortex_recall") || tools.HasTool("mcp_cortex_remember") {
		conv.Append(chat.Message{Role: chat.RoleSystem, Content: cortexMemoryGuidance()})
	}
	// Build confirmation function.
	confirm := cfg.Confirm
	if confirm == nil {
		confirm = StdinConfirm()
	}
	if cfg.AutoApprove {
		confirm = AutoApproveConfirm(70, confirm)
	}
	// Autonomy mode takes precedence over the legacy AutoApprove flag,
	// wrapping the (possibly already-auto-approving) confirm with the
	// mode-specific policy.
	baseConfirm := confirm
	if cfg.Autonomy != "" && cfg.Autonomy != AutonomyDefault {
		confirm = autonomyConfirm(cfg.Autonomy, confirm)
	}

	// Build safety gate.
	safetyGate := DefaultSafetyGate(tc)
	if cfg.ShellTimeout <= 0 {
		cfg.ShellTimeout = 5 * time.Minute
	}
	runShell := cfg.RunShell
	if runShell == nil {
		runShell = ExecuteShellWithTimeout(cfg.ShellTimeout)
	}

	return &Agent{
		client:                  client,
		conv:                    conv,
		tools:                   tools,
		confirm:                 confirm,
		baseConfirm:             baseConfirm,
		runShell:                runShell,
		runOnNode:               cfg.RunOnNode,
		runTask:                 cfg.RunTask,
		safety:                  safetyGate,
		output:                  cfg.Output,
		maxTurns:                cfg.MaxTurns,
		maxTokens:               cfg.MaxTokens,
		verbose:                 cfg.Verbose,
		dryRun:                  cfg.DryRun,
		toolContext:             tc,
		model:                   cfg.Model,
		allowRawCommandEvidence: cfg.AllowRawCommandEvidence,
		securityClass:           cfg.BackendSecurityClass,
		mcpRegistry:             cfg.MCPRegistry,
		backgroundTasks:         newBackgroundTaskStore(),
		autonomy:                cfg.Autonomy,
		observer:                cfg.Observer,
	}
}

// Run executes one full agent turn: the user prompt goes in, the agent loops
// through tool calls until the model produces a text response or hits the
// turn limit.
// SetConfirm replaces the confirmation function. A surface that owns the
// terminal installs its own here: the default reads os.Stdin directly, which
// would corrupt a raw-mode input loop.
//
// It takes both the run lock (so it cannot land midway through a turn) and
// dispatchMu, which guards confirm/baseConfirm against SetAutonomy and
// dispatchToolCall.
func (a *Agent) SetConfirm(fn ConfirmFunc) {
	if fn == nil {
		return
	}
	a.runMu.Lock()
	defer a.runMu.Unlock()
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.baseConfirm = fn
	a.confirm = a.wrapConfirm(fn)
}

// askConfirm runs the operator prompt without holding dispatchMu.
// confirmGate keeps concurrent prompts from drawing on top of each other.
func (a *Agent) askConfirm(tool, desc string, score int) ConfirmResult {
	a.dispatchMu.Lock()
	fn := a.confirm
	a.dispatchMu.Unlock()
	if fn == nil {
		return ConfirmNo
	}
	a.confirmGate.Lock()
	defer a.confirmGate.Unlock()
	return fn(tool, desc, score)
}

// wrapConfirm applies the active autonomy policy to a base confirm.
// Callers must hold a lock covering confirm/autonomy.
func (a *Agent) wrapConfirm(fn ConfirmFunc) ConfirmFunc {
	if a.autonomy != "" && a.autonomy != AutonomyDefault {
		return autonomyConfirm(a.autonomy, fn)
	}
	return fn
}

// Run executes one full agent turn. Calls are serialized: the conversation is
// not safe for concurrent mutation, so a caller that abandons a turn (a
// cancellation the backend never acknowledged) blocks here until that turn
// actually exits rather than racing it.
func (a *Agent) Run(ctx context.Context, userPrompt string) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	return a.runLocked(ctx, userPrompt)
}

// SetAutonomy updates the autonomy mode and its confirmation policy,
// re-wrapping the surface-installed base confirm rather than StdinConfirm.
func (a *Agent) SetAutonomy(mode AutonomyMode) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.autonomy = mode
	if a.tools != nil {
		a.tools.SetScope(ScopeFor(mode))
		a.refreshVisibleTools()
	}
	base := a.baseConfirm
	if base == nil {
		base = StdinConfirm()
	}
	a.confirm = autonomyConfirm(mode, base)
}

func visibleToolsMessage(r *ToolRegistry) string {
	if r == nil {
		return VisibleToolPrompt(nil)
	}
	defs := r.Defs()
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Function.Name)
	}
	return VisibleToolPrompt(names)
}

func (a *Agent) refreshVisibleTools() {
	if a == nil || a.conv == nil {
		return
	}
	msg := chat.Message{Role: chat.RoleSystem, Content: visibleToolsMessage(a.tools)}
	msgs := a.conv.Messages()
	for i, m := range msgs {
		if m.Role == chat.RoleSystem && strings.HasPrefix(m.Content, "Tools you may call right now:") {
			a.conv.ReplaceRange(i, i+1, []chat.Message{msg})
			return
		}
	}
	a.conv.Append(msg)
}

// RunWithSinks executes one turn against turn-scoped sinks, restoring the
// previous ones when it returns. A surface that stamps events with the turn
// that produced them constructs a fresh observer and writer per turn and
// passes them here; because the swap happens under the same lock as the run,
// an abandoned turn keeps writing to its own sinks and can never be
// misattributed to the turn that replaced it.
//
// Either sink may be nil to leave that one unchanged.
func (a *Agent) RunWithSinks(ctx context.Context, userPrompt string, obs Observer, out io.Writer) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()

	prevObs, prevOut := a.observer, a.output
	if obs != nil {
		a.observer = obs
	}
	if out != nil {
		a.output = out
	}
	defer func() { a.observer, a.output = prevObs, prevOut }()

	return a.runLocked(ctx, userPrompt)
}

func (a *Agent) runLocked(ctx context.Context, userPrompt string) error {
	userMsgIdx := a.conv.Len()
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: userPrompt})

	for turn := 0; turn < a.maxTurns; turn++ {
		if a.verbose {
			a.emitTurnStarted(turn+1, a.maxTurns)
		}
		// Proactively compress older conversation turns before sending context
		// to the model, so long sessions stay within the token budget.
		if err := a.compactContext(ctx); err != nil && a.verbose {
			a.emitCompactionSkipped(err)
		}
		msgs := a.conv.Messages()
		toolDefs := a.tools.Defs()

		// Clone msgs list and dynamically merge evidence into the first system message.
		clonedMsgs := make([]chat.Message, len(msgs))
		copy(clonedMsgs, msgs)

		evidence := a.retrieveEvidence(userPrompt)
		if evidence != "" {
			systemMsgIdx := -1
			for i, m := range clonedMsgs {
				if m.Role == chat.RoleSystem {
					systemMsgIdx = i
					break
				}
			}
			if systemMsgIdx >= 0 {
				if clonedMsgs[systemMsgIdx].Content != "" {
					clonedMsgs[systemMsgIdx].Content += "\n\n" + evidence
				} else {
					clonedMsgs[systemMsgIdx].Content = evidence
				}
			} else {
				clonedMsgs = append([]chat.Message{{Role: chat.RoleSystem, Content: evidence}}, clonedMsgs...)
			}
		}
		// Inject a live cluster snapshot so the agent continuously knows node
		// health, free memory, and resident models — and can adapt placement
		// mid-task (e.g. reroute when a node hits memory pressure).
		if clusterCtx := a.clusterContextSnippet(); clusterCtx != "" {
			systemMsgIdx := -1
			for i, m := range clonedMsgs {
				if m.Role == chat.RoleSystem {
					systemMsgIdx = i
					break
				}
			}
			if systemMsgIdx >= 0 {
				if clonedMsgs[systemMsgIdx].Content != "" {
					clonedMsgs[systemMsgIdx].Content += "\n\n" + clusterCtx
				} else {
					clonedMsgs[systemMsgIdx].Content = clusterCtx
				}
			} else {
				clonedMsgs = append([]chat.Message{{Role: chat.RoleSystem, Content: clusterCtx}}, clonedMsgs...)
			}
		}

		clonedMsgs = chat.ConsolidateMessages(clonedMsgs)

		// Wire invariant: model chat templates behind tool calling (notably
		// Qwen Jinja templates via LiteLLM) reject payloads with no user-role
		// message. Compaction can delete every original user turn, so if none
		// survived, re-inject this turn's prompt after the leading system
		// messages rather than sending a user-less payload into a template
		// guard.
		if !hasUserRole(clonedMsgs) {
			insert := firstNonSystemIndexIn(clonedMsgs)
			if insert < 0 {
				insert = len(clonedMsgs)
			}
			clonedMsgs = append(clonedMsgs[:insert], append([]chat.Message{{Role: chat.RoleUser, Content: userPrompt}}, clonedMsgs[insert:]...)...)
		}

		// Stream the model response with code block highlighting.
		cw := NewColorWriter(a.output)
		resp, err := a.client.ChatStream(ctx, clonedMsgs, toolDefs, cw)
		cw.Close()
		if err != nil {
			if turn == 0 {
				a.conv.ReplaceRange(userMsgIdx, userMsgIdx+1, nil)
			}
			return fmt.Errorf("chat stream (turn %d): %w", turn, err)
		}

		a.accumulateUsage(resp.UsageTokensIn, resp.UsageTokensOut)
		a.conv.Append(resp)

		// If no tool calls, the model produced a final text answer — done.
		if len(resp.ToolCalls) == 0 {
			return nil
		}

		// Process tool calls. Dry-run calls are handled inline (instant); live
		// calls dispatch concurrently with a bounded worker pool so independent
		// reads/runs proceed in parallel. Results are appended to the conversation
		// in the original tool-call order so tool_call_id alignment is preserved.
		var liveCalls []chat.ToolCall
		for _, tc := range resp.ToolCalls {
			a.emitToolCalled(tc.ID, tc.Function.Name, string(tc.Function.Arguments))
			if a.dryRun {
				a.emitToolSkipped(tc.ID, tc.Function.Name, "dry-run")
				a.conv.Append(chat.Message{
					Role:       chat.RoleTool,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("[dry-run] %s execution skipped", tc.Function.Name),
				})
				continue
			}
			liveCalls = append(liveCalls, tc)
		}

		if len(liveCalls) > 0 {
			type toolResult struct {
				result string
				err    error
			}
			results := make([]toolResult, len(liveCalls))
			var outMu sync.Mutex
			var wg sync.WaitGroup
			concurrency := len(liveCalls)
			if concurrency > maxParallelTools {
				concurrency = maxParallelTools
			}
			sem := make(chan struct{}, concurrency)
			for i, tc := range liveCalls {
				wg.Add(1)
				go func(i int, tc chat.ToolCall) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					start := time.Now()
					result, err := a.dispatchToolCall(ctx, tc)
					elapsed := time.Since(start)
					outMu.Lock()
					if err != nil {
						a.emitToolFailed(tc.ID, tc.Function.Name, err, elapsed)
					} else {
						a.emitToolSucceeded(tc.ID, tc.Function.Name, formatToolResultSummary(tc.Function.Name, result), len(result), elapsed)
					}
					outMu.Unlock()
					results[i] = toolResult{result: result, err: err}
				}(i, tc)
			}
			wg.Wait()

			// Append to conversation in original order (tool_call_id alignment).
			for i, tc := range liveCalls {
				if results[i].err != nil {
					errMsg := fmt.Sprintf("Error executing tool %q: %s", tc.Function.Name, results[i].err.Error())
					a.conv.Append(chat.Message{
						Role:       chat.RoleTool,
						ToolCallID: tc.ID,
						Content:    errMsg,
					})
					continue
				}
				a.conv.Append(chat.Message{
					Role:       chat.RoleTool,
					ToolCallID: tc.ID,
					Content:    results[i].result,
				})
			}
		}
	}

	a.emitMaxTurnsReached(a.maxTurns)
	return nil
}

// formatToolResultSummary produces a human-readable one-line summary of a
// tool result for operator feedback.
func (a *Agent) ToolNames() string {
	if a.tools == nil {
		return ""
	}
	return a.tools.visibleNames()
}

func (a *Agent) ToolDefs() []chat.ToolDef {
	if a.tools == nil {
		return nil
	}
	return a.tools.Defs()
}

// Conversation returns the underlying conversation for inspection/testing.
func (a *Agent) Conversation() *chat.Conversation {
	return a.conv
}

// ContextTokens returns the current estimated conversation tokens.
func (a *Agent) ContextTokens() int {
	if a == nil || a.conv == nil {
		return 0
	}
	return a.conv.EstimateTokens()
}

// MaxTokens returns the session token limit.
func (a *Agent) MaxTokens() int {
	return a.maxTokens
}

// MCPRegistry returns the connected MCP client registry.
func (a *Agent) MCPRegistry() *mcpclient.Registry {
	return a.mcpRegistry
}

// SetBackend changes the active LLM backend and its trust classification.
func (a *Agent) SetBackend(client ChatBackend, securityClass BackendSecurityClass) {
	a.client = client
	a.securityClass = securityClass
}

// Backend returns the active LLM backend.
func (a *Agent) Backend() ChatBackend {
	return a.client
}

// Model returns the current active model name.
func (a *Agent) Model() string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.model
}

// Autonomy returns the current autonomy mode.
func (a *Agent) Autonomy() AutonomyMode {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.autonomy == "" {
		return AutonomyDefault
	}
	return a.autonomy
}

// ExecuteToolDirect allows direct tool execution (e.g. from slash commands or harness scripts).
func (a *Agent) ExecuteToolDirect(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if a.tools == nil {
		return "", fmt.Errorf("tool registry unavailable")
	}
	return a.tools.Execute(ctx, name, args)
}

// SetModel updates the current active model name.
func (a *Agent) SetModel(model string) {
	a.modelMu.Lock()
	a.model = model
	a.modelMu.Unlock()
}

// SafetyGate returns the shell safety gate used to check commands.
func (a *Agent) SafetyGate() ShellSafetyGate {
	if a == nil || a.safety == nil {
		return DefaultSafetyGate(nil)
	}
	return a.safety
}

// SetRunShell replaces the local shell runner (e.g. after /model so OwnerLabel
// provenance matches the live model on the guarded path).
func (a *Agent) SetRunShell(r ShellRunner) {
	if r == nil {
		return
	}
	a.runnerMu.Lock()
	a.runShell = r
	a.runnerMu.Unlock()
}

// SetRunOnNode replaces the node shell runner (paired with SetRunShell after /model).
func (a *Agent) SetRunOnNode(r NodeShellRunner) {
	a.runnerMu.Lock()
	a.runOnNode = r
	a.runnerMu.Unlock()
}

// shellRunner snapshots the current local shell runner under lock.
func (a *Agent) shellRunner() ShellRunner {
	a.runnerMu.RLock()
	r := a.runShell
	a.runnerMu.RUnlock()
	return r
}

// nodeRunner snapshots the current node shell runner under lock.
func (a *Agent) nodeRunner() NodeShellRunner {
	a.runnerMu.RLock()
	r := a.runOnNode
	a.runnerMu.RUnlock()
	return r
}

// AgentStats holds session statistics.
type AgentStats struct {
	TokensIn  int
	TokensOut int
	Cost      float64
}

// accumulateUsage totals one response turn's reported usage. Zero counts
// from backends that do not report usage leave the accumulator untouched,
// so mixed real/estimated sessions never fabricate numbers.
func (a *Agent) accumulateUsage(in, out int) {
	if in <= 0 && out <= 0 {
		return
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	a.usageIn += in
	a.usageOut += out
	a.usageTurns++
}

// UsageStats returns the session's accumulated real token usage: totals
// plus how many turns reported usage. Turns is zero when the backend
// never reported usage, which distinguishes "no data" from "zero tokens".
func (a *Agent) UsageStats() (in, out, turns int) {
	a.usageMu.RLock()
	defer a.usageMu.RUnlock()
	return a.usageIn, a.usageOut, a.usageTurns
}

// Stats returns the accumulated statistics from the underlying client, if supported.
func (a *Agent) Stats() AgentStats {
	if sp, ok := a.client.(interface {
		Stats() (int, int, float64)
	}); ok {
		in, out, cost := sp.Stats()
		return AgentStats{
			TokensIn:  in,
			TokensOut: out,
			Cost:      cost,
		}
	}
	return AgentStats{}
}

type BackendSecurityClass int

const (
	BackendRemote BackendSecurityClass = iota
	BackendLocal
)

var (
	bearerRegex        = regexp.MustCompile(`(?i)(bearer\s+)[a-zA-Z0-9\-\._~\+\/]+=*`)
	apiKeyHeaderRegex  = regexp.MustCompile(`(?i)(x-api-key:\s*)\S+`)
	authHeaderRegex    = regexp.MustCompile(`(?i)(authorization:\s*)\S+`)
	genericSecretRegex = regexp.MustCompile(`(?i)(key|password|secret|token|passwd|credential)(["':=\s]+)([a-zA-Z0-9\-_]{8,})`)
)
