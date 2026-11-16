package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

type recordingChatRound struct {
	sysPrefix string
	defsJSON  string
}

type kvCacheProbeBackend struct {
	mu        sync.Mutex
	responses []chat.Message
	idx       int
	rounds    []recordingChatRound
}

func (b *kvCacheProbeBackend) ChatStream(_ context.Context, msgs []chat.Message, tools []chat.ToolDef, _ io.Writer) (chat.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sysPrefix := ""
	if len(msgs) > 0 && msgs[0].Role == chat.RoleSystem {
		sysPrefix = msgs[0].Content
	}
	defsBytes, _ := json.Marshal(tools)
	b.rounds = append(b.rounds, recordingChatRound{
		sysPrefix: sysPrefix,
		defsJSON:  string(defsBytes),
	})
	if b.idx >= len(b.responses) {
		return chat.Message{Role: chat.RoleAssistant, Content: "done"}, nil
	}
	resp := b.responses[b.idx]
	b.idx++
	return resp, nil
}

func buildLargeMCPRegistry(serverCount, toolsPerServer int) *mcpclient.Registry {
	reg := mcpclient.NewRegistry()
	for s := 0; s < serverCount; s++ {
		var tools []mcp.Tool
		for i := 0; i < toolsPerServer; i++ {
			tools = append(tools, mcp.Tool{
				Name:        fmt.Sprintf("op_%02d", i),
				Description: fmt.Sprintf("Perform operation %d cleanly with detailed structured diagnostics output across targets", i),
				InputSchema: mcp.ToolInputSchema{Type: "object"},
			})
		}
		reg.Add(&mcpclient.ServerConnection{
			Name:       fmt.Sprintf("srv_%02d", s),
			InitResult: &mcp.InitializeResult{},
			Tools:      tools,
		})
	}
	return reg
}

func TestDeferredTools_DefaultDefersMCPAndKeepsClusterUnchanged(t *testing.T) {
	// Without MCP servers in default mode, bridge tools stay absent.
	rPlain := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	for _, d := range rPlain.Defs() {
		if isBridgeTool(d.Function.Name) {
			t.Fatalf("unexpected bridge tool %q when zero deferred tools exist", d.Function.Name)
		}
	}
	if rPlain.Visible(toolSearchName) || rPlain.Visible(toolCallName) {
		t.Fatal("bridge tools must not be Visible when no deferred tools exist")
	}

	// With MCP tools connected, observe/edit/exec defer mcp_* behind the bridge.
	srv := &recordingMCPClient{}
	reg := fakeApprovalRegistry(srv)
	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	r.RegisterMCPTools(reg)

	for _, sc := range []ToolScope{ScopeObserve, ScopeEdit, ScopeExec} {
		r.SetScope(sc)
		names := defNames(r)
		if !containsName(names, toolSearchName) || !containsName(names, toolCallName) {
			t.Fatalf("scope %s missing bridge tools in Defs: %v", sc, names)
		}
		for _, n := range names {
			if strings.HasPrefix(n, "mcp_") {
				t.Fatalf("scope %s eagerly exposed MCP tool %q in Defs", sc, n)
			}
		}
		if !r.Visible("mcp_gadget_forget_memory") || !r.IsDeferred("mcp_gadget_forget_memory") {
			t.Fatalf("scope %s: expected MCP tool to be Visible+IsDeferred", sc)
		}
	}

	// Cluster scope stays locked to the 5 built-in cluster reads with no bridge or MCP.
	r.SetScope(ScopeCluster)
	clusterNames := defNames(r)
	if len(clusterNames) != 5 || containsName(clusterNames, toolSearchName) || containsName(clusterNames, toolCallName) {
		t.Fatalf("cluster scope defs altered: %v", clusterNames)
	}
	if r.Visible(toolSearchName) || r.Visible(toolCallName) || r.Visible("mcp_gadget_forget_memory") {
		t.Fatal("cluster scope leaked bridge or MCP visibility")
	}

	// visibleToolsMessage includes compact sorted deferred catalog, and falls back
	// to name-only entries above deferredCatalogSummaryLimit (e.g. 100+ MCP tools).
	r.SetScope(ScopeObserve)
	smallMsg := visibleToolsMessage(r)
	if !strings.Contains(smallMsg, "Deferred tools (use tool_search") || !strings.Contains(smallMsg, "- mcp_gadget_collab_search") {
		t.Fatalf("visibleToolsMessage missing deferred catalog:\n%s", smallMsg)
	}

	rBig := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	rBig.RegisterMCPTools(buildLargeMCPRegistry(5, 22))
	bigMsg := visibleToolsMessage(rBig)
	if !strings.Contains(bigMsg, "- mcp_srv_00_op_00\n") {
		t.Fatalf("100+ MCP tools should fall back to compact name-only catalog:\n%s", bigMsg)
	}
	if strings.Contains(bigMsg, "Perform operation 0 cleanly") {
		t.Fatalf("compact catalog unexpectedly retained verbose descriptions")
	}
}

func TestDeferredTools_EagerAllowlistAndDisableDeferral(t *testing.T) {
	srv := &recordingMCPClient{}
	reg := fakeApprovalRegistry(srv)

	// Blank / whitespace-only allowlist keeps default MCP-only deferral mode.
	aBlank := New(Config{
		Backend:     &kvCacheProbeBackend{},
		MCPRegistry: reg,
		EagerTools:  []string{"", "   "},
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	if !containsName(defNames(aBlank.tools), "read_file") || containsName(defNames(aBlank.tools), "mcp_gadget_collab_search") {
		t.Fatalf("blank EagerTools should stay in default mode, defs=%v", defNames(aBlank.tools))
	}

	// Explicit EagerTools allowlist demotes non-allowlisted built-ins, allows pinned MCP tools,
	// and fails closed on scope-prohibited or never-set tools.
	a := New(Config{
		Backend:     &kvCacheProbeBackend{},
		MCPRegistry: reg,
		EagerTools:  []string{" read_file ", "mcp_gadget_collab_search", "run_shell", "spawn_subagent"},
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	names := defNames(a.tools)
	if !containsName(names, "read_file") || !containsName(names, "mcp_gadget_collab_search") {
		t.Fatalf("missing allowlisted eager tools in %v", names)
	}
	if !containsName(names, toolSearchName) || !containsName(names, toolCallName) {
		t.Fatalf("missing bridge tools in %v", names)
	}
	for _, demotedOrBlocked := range []string{"todo", "axis_status", "run_shell", "spawn_subagent"} {
		if containsName(names, demotedOrBlocked) {
			t.Fatalf("unexpected %q in Defs: %v", demotedOrBlocked, names)
		}
	}
	if !a.tools.IsDeferred("axis_status") || a.tools.IsDeferred("run_shell") || a.tools.IsDeferred("spawn_subagent") {
		t.Fatalf("unexpected deferral states: axis_status=%v run_shell=%v spawn_subagent=%v",
			a.tools.IsDeferred("axis_status"), a.tools.IsDeferred("run_shell"), a.tools.IsDeferred("spawn_subagent"))
	}

	// DisableToolDeferral takes precedence over EagerTools and omits bridge tools.
	aNoDefer := New(Config{
		Backend:             &kvCacheProbeBackend{},
		MCPRegistry:         reg,
		EagerTools:          []string{"read_file"},
		DisableToolDeferral: true,
		Output:              io.Discard,
		ToolContext:         NewToolContext(&RuntimeView{}, nil),
	})
	noDeferNames := defNames(aNoDefer.tools)
	if containsName(noDeferNames, toolSearchName) || containsName(noDeferNames, toolCallName) {
		t.Fatalf("DisableToolDeferral exposed bridge tools: %v", noDeferNames)
	}
	if !containsName(noDeferNames, "mcp_gadget_forget_memory") || !containsName(noDeferNames, "axis_status") {
		t.Fatalf("DisableToolDeferral missing eager tools: %v", noDeferNames)
	}

	// /autonomy transitions refresh Defs() and a.conv via refreshVisibleTools().
	hasDeferredMsg := func(ag *Agent) bool {
		for _, m := range ag.conv.Messages() {
			if m.Role == chat.RoleSystem && strings.HasPrefix(m.Content, "Tools you may call right now:") {
				return strings.Contains(m.Content, "Deferred tools") && strings.Contains(m.Content, toolSearchName)
			}
		}
		return false
	}
	if !hasDeferredMsg(a) {
		t.Fatal("initial observe mode should announce Deferred tools in system prompt")
	}
	a.SetAutonomy(AutonomyCluster)
	if len(defNames(a.tools)) != 5 || hasDeferredMsg(a) {
		t.Fatalf("SetAutonomy(cluster) did not strip deferred tools: defs=%v hasDeferred=%v", defNames(a.tools), hasDeferredMsg(a))
	}
	a.SetAutonomy(AutonomyFull)
	if !containsName(defNames(a.tools), "run_shell") || !hasDeferredMsg(a) {
		t.Fatalf("SetAutonomy(full) did not reveal allowlisted run_shell + deferred catalog: defs=%v", defNames(a.tools))
	}
	a.SetAutonomy(AutonomyDefault)
	if containsName(defNames(a.tools), "run_shell") || !hasDeferredMsg(a) {
		t.Fatalf("SetAutonomy(default) did not restore observe scope + deferred catalog: defs=%v", defNames(a.tools))
	}
}

func TestDeferredTools_KVCacheDefsAndSystemPrefixByteStable(t *testing.T) {
	backend := &kvCacheProbeBackend{
		responses: []chat.Message{
			{
				Role: chat.RoleAssistant,
				ToolCalls: []chat.ToolCall{
					toolCall("c1", toolSearchName, `{"query":"select:mcp_cortex_recall"}`),
				},
			},
			{
				Role: chat.RoleAssistant,
				ToolCalls: []chat.ToolCall{
					toolCall("c2", toolCallName, `{"name":"mcp_cortex_recall","arguments":{}}`),
				},
			},
			{
				Role:    chat.RoleAssistant,
				Content: "final answer from cortex",
			},
		},
	}

	// Add fake Client so mcpclient.CallTool succeeds on fakeCortexRegistry.
	reg := mcpclient.NewRegistry()
	srv := &recordingMCPClient{}
	reg.Add(&mcpclient.ServerConnection{
		Name:       "cortex",
		InitResult: &mcp.InitializeResult{},
		Client:     srv,
		Config:     config.MCPServerConfig{AutoApproveTools: []string{"recall"}},
		Tools: []mcp.Tool{
			{Name: "recall", Description: "semantic search of cluster memories", InputSchema: mcp.ToolInputSchema{Type: "object"}},
			{Name: "remember", Description: "store a cluster memory", InputSchema: mcp.ToolInputSchema{Type: "object"}},
		},
	})

	a := New(Config{
		Backend:     backend,
		MCPRegistry: reg,
		MaxTurns:    5,
		Output:      io.Discard,
		Confirm:     neverConfirm(),
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})

	if err := a.Run(context.Background(), "check memory"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(backend.rounds) != 3 {
		t.Fatalf("rounds = %d, want 3", len(backend.rounds))
	}
	for i := 1; i < len(backend.rounds); i++ {
		if backend.rounds[i].defsJSON != backend.rounds[0].defsJSON {
			t.Fatalf("round %d defs drifted (prompt cache invalidated):\nr0=%s\nr%d=%s",
				i, backend.rounds[0].defsJSON, i, backend.rounds[i].defsJSON)
		}
		if backend.rounds[i].sysPrefix != backend.rounds[0].sysPrefix {
			t.Fatalf("round %d consolidated system message msgs[0] drifted", i)
		}
	}
	if !strings.Contains(backend.rounds[0].sysPrefix, "Cluster-shared memory (Cortex) is connected") {
		t.Fatalf("expected Cortex guidance inside consolidated msgs[0]")
	}
	if len(srv.called) != 1 || srv.called[0] != "recall" {
		t.Fatalf("expected deferred recall execution, called=%v", srv.called)
	}
}

func TestToolSearch_SelectAndKeywordRanking(t *testing.T) {
	reg := mcpclient.NewRegistry()
	srv := &recordingMCPClient{}
	reg.Add(&mcpclient.ServerConnection{
		Name:       "srv_one",
		InitResult: &mcp.InitializeResult{},
		Client:     srv,
		Tools: []mcp.Tool{
			{
				Name:        "delete_record",
				Description: "Delete an obsolete cluster record",
				InputSchema: mcp.ToolInputSchema{Type: "object", Properties: map[string]any{"id": map[string]any{"type": "string"}}},
			},
			{
				Name:        "CaseTool",
				Description: "Mixed case tool",
				InputSchema: mcp.ToolInputSchema{Type: "object"},
			},
			{
				Name:        "casetool",
				Description: "Lower case duplicate for ambiguity test",
				InputSchema: mcp.ToolInputSchema{Type: "object"},
			},
		},
	})
	for i := 0; i < 6; i++ {
		reg.Add(&mcpclient.ServerConnection{
			Name:       "batch",
			InitResult: &mcp.InitializeResult{},
			Client:     srv,
			Tools:      []mcp.Tool{{Name: fmt.Sprintf("item_%02d", i), Description: "Batch helper item", InputSchema: mcp.ToolInputSchema{Type: "object"}}},
		})
	}
	// Put 6 batch tools cleanly on one connection (duplicate server name Add replaces previous).
	batchTools := make([]mcp.Tool, 6)
	for i := 0; i < 6; i++ {
		batchTools[i] = mcp.Tool{
			Name:        fmt.Sprintf("item_%02d", i),
			Description: "Batch helper item for rank tiebreak",
			InputSchema: mcp.ToolInputSchema{Type: "object"},
		}
	}
	reg.Add(&mcpclient.ServerConnection{
		Name:       "batch",
		InitResult: &mcp.InitializeResult{},
		Client:     srv,
		Tools:      batchTools,
	})

	r := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	r.RegisterMCPTools(reg)
	r.SetEagerTools([]string{"read_file"})

	// 1. select: exact underscored server tool + already-eager tool + neverTool + out-of-scope run_shell + truncation.
	out, err := r.Execute(context.Background(), toolSearchName, json.RawMessage(`{
		"query":"select:'mcp_srv_one_delete_record', mcp_srv_one_delete_record, read_file, run_shell, spawn_subagent, run_background, mcp_batch_item_00, mcp_batch_item_01",
		"max_results":2
	}`))
	if err != nil {
		t.Fatalf("tool_search select: %v", err)
	}
	if !strings.Contains(out, `"name":"mcp_srv_one_delete_record"`) || !strings.Contains(out, `"server":"srv_one"`) {
		t.Fatalf("expected underscored server provenance for mcp_srv_one_delete_record, got:\n%s", out)
	}
	if !strings.Contains(out, "Already directly callable (call without tool_call): read_file") {
		t.Fatalf("expected eager guidance for read_file, got:\n%s", out)
	}
	if !strings.Contains(out, "Not found or unavailable in current scope: run_shell, spawn_subagent, run_background") {
		t.Fatalf("expected missing/out-of-scope refusal, got:\n%s", out)
	}
	if !strings.Contains(out, "Truncated by max_results: mcp_batch_item_01") {
		t.Fatalf("expected max_results=2 truncation note, got:\n%s", out)
	}

	// 2. Ambiguous case-insensitive select report.
	ambOut, err := r.Execute(context.Background(), toolSearchName, json.RawMessage(`{"query":"select:MCP_SRV_ONE_CASETOOL"}`))
	if err != nil {
		t.Fatalf("tool_search ambiguous: %v", err)
	}
	if !strings.Contains(ambOut, "Ambiguous:") || !strings.Contains(ambOut, "mcp_srv_one_CaseTool") {
		t.Fatalf("expected ambiguity notice, got:\n%s", ambOut)
	}

	// 3. Keyword + synonym ("remove" matches "delete") + required "+record" + stop-words ignored.
	kwOut, err := r.Execute(context.Background(), toolSearchName, json.RawMessage(`{"query":"please remove the +record"}`))
	if err != nil {
		t.Fatalf("tool_search keyword: %v", err)
	}
	if !strings.Contains(kwOut, `"name":"mcp_srv_one_delete_record"`) {
		t.Fatalf("expected synonym + required keyword match for delete_record, got:\n%s", kwOut)
	}

	// 4. Deterministic tie-breaking & clamping max_results <= 20.
	tieOut, err := r.Execute(context.Background(), toolSearchName, json.RawMessage(`{"query":"batch item","max_results":99}`))
	if err != nil {
		t.Fatalf("tool_search tiebreak: %v", err)
	}
	i0 := strings.Index(tieOut, "mcp_batch_item_00")
	i1 := strings.Index(tieOut, "mcp_batch_item_01")
	if i0 < 0 || i1 < 0 || i0 > i1 {
		t.Fatalf("expected deterministic lexicographic order for item_00 before item_01:\n%s", tieOut)
	}
}

func TestToolCall_SafetyApprovalIsolationAndRefusals(t *testing.T) {
	srv := &recordingMCPClient{}
	reg := fakeApprovalRegistry(srv)

	// 1. Direct ToolRegistry.Execute("tool_call", ...) refuses to bypass safety gate.
	rDirect := NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
	if _, err := rDirect.Execute(context.Background(), toolCallName, json.RawMessage(`{"name":"axis_status"}`)); err == nil || !strings.Contains(err.Error(), "agent safety gate") {
		t.Fatalf("direct Execute(tool_call) must fail closed, got %v", err)
	}

	// 2. Per-tool session approval, unratedMCPWhy, approvalWait exclusion, and DailySessionLog target logging.
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("OpenDailySessionLog: %v", err)
	}
	day := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	log.now = func() time.Time { return day }

	var askedTools, askedDescs []string
	var confirmDelay time.Duration
	a := New(Config{
		Backend:     &kvCacheProbeBackend{},
		MCPRegistry: reg,
		Autonomy:    AutonomyFull,
		SessionLog:  log,
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
		Confirm: func(tool, desc string, _ int) ConfirmResult {
			askedTools = append(askedTools, tool)
			askedDescs = append(askedDescs, desc)
			if confirmDelay > 0 {
				time.Sleep(confirmDelay)
			}
			return ConfirmAlways
		},
	})

	// Allowlisted mcp_gadget_collab_search skips confirm cleanly via tool_call.
	if _, err := a.dispatchToolCall(context.Background(), toolCall("c-allow", toolCallName, `{"name":"mcp_gadget_collab_search"}`)); err != nil {
		t.Fatalf("allowlisted tool_call: %v", err)
	}
	if len(askedTools) != 0 {
		t.Fatalf("allowlisted MCP tool unexpectedly prompted: %v", askedTools)
	}

	// Unlisted MCP tool triggers unratedMCPWhy prompt for exact target, caches sessionApproved[target] only,
	// excludes confirm sleep from timedDispatch elapsed time, and logs target tool approval.
	confirmDelay = 35 * time.Millisecond
	_, elapsed, err := a.timedDispatch(context.Background(), toolCall("c-mcp-1", toolCallName, `{"name":"mcp_gadget_forget_memory","arguments":{}}`))
	if err != nil {
		t.Fatalf("timedDispatch unlisted MCP: %v", err)
	}
	if elapsed >= 25*time.Millisecond {
		t.Fatalf("approvalWait not excluded from timedDispatch elapsed: %s", elapsed)
	}
	if len(askedTools) != 1 || askedTools[0] != "mcp_gadget_forget_memory" {
		t.Fatalf("Confirm received tool %v, want [mcp_gadget_forget_memory]", askedTools)
	}
	if !strings.HasPrefix(askedDescs[0], "safety why: "+unratedMCPWhy) {
		t.Fatalf("missing unratedMCPWhy on deferred MCP confirm: %q", askedDescs[0])
	}
	confirmDelay = 0

	// Second call to mcp_gadget_forget_memory reuses session approval; different unlisted tool still prompts!
	if _, err := a.dispatchToolCall(context.Background(), toolCall("c-mcp-2", toolCallName, `{"name":"mcp_gadget_forget_memory"}`)); err != nil {
		t.Fatalf("second forget_memory: %v", err)
	}
	if len(askedTools) != 1 {
		t.Fatalf("expected cached session approval for forget_memory, prompted %v", askedTools)
	}
	if _, err := a.dispatchToolCall(context.Background(), toolCall("c-mcp-3", toolCallName, `{"name":"mcp_gadget_search_and_delete"}`)); err != nil {
		t.Fatalf("different unlisted MCP: %v", err)
	}
	if len(askedTools) != 2 || askedTools[1] != "mcp_gadget_search_and_delete" {
		t.Fatalf("different unlisted MCP failed to prompt independently: %v", askedTools)
	}
	if a.sessionApproved[toolCallName] {
		t.Fatal("tool_call itself must never be recorded in sessionApproved")
	}

	rawLog, err := os.ReadFile(filepath.Join(dir, "agent-2026-11-16.jsonl"))
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	if !strings.Contains(string(rawLog), `"name":"mcp_gadget_forget_memory"`) || strings.Contains(string(rawLog), `"name":"tool_call","event":"approval"`) {
		t.Fatalf("session log must record unwrapped approval target name, got:\n%s", rawLog)
	}

	// 3. Refusals: eager tool, bridge tool, neverTool, out-of-scope tool, non-object args.
	refuseCases := []struct {
		name string
		args string
		want string
	}{
		{"eager read_file", `{"name":"read_file","arguments":{"path":"go.mod"}}`, "already directly callable"},
		{"bridge self", `{"name":"tool_call","arguments":{"name":"mcp_gadget_forget_memory"}}`, "cannot invoke bridge"},
		{"bridge search", `{"name":"tool_search","arguments":{"query":"memory"}}`, "cannot invoke bridge"},
		{"never tool", `{"name":"spawn_subagent","arguments":{"prompt":"hi"}}`, "not available in this mode"},
		{"non-object args", `{"name":"mcp_gadget_forget_memory","arguments":[1,2]}`, "JSON object"},
	}
	for _, rc := range refuseCases {
		_, err := a.dispatchToolCall(context.Background(), toolCall("c-ref", toolCallName, rc.args))
		if err == nil || !strings.Contains(err.Error(), rc.want) {
			t.Errorf("%s: err = %v, want %q", rc.name, err, rc.want)
		}
	}

	// 4. Demoted built-ins via EagerTools keep write_file/edit_file diff preview,
	// dispatchShell safety scoring, dispatchRunTask repeated confirm, and ConfirmNever/blockAll.
	workDir := chdirToTempDir(t)
	targetFile := filepath.Join(workDir, "note.txt")
	if err := os.WriteFile(targetFile, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	var builtPrompts []string
	var shellRan []string
	var taskCount int
	var nextDecision = ConfirmAlways
	aDemo := New(Config{
		Backend:     &kvCacheProbeBackend{},
		Autonomy:    AutonomyDefault,
		EagerTools:  []string{"read_file"},
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
		RunShell: func(_ context.Context, cmd string) (string, error) {
			shellRan = append(shellRan, cmd)
			return "ok", nil
		},
		RunTask: func(_ context.Context, _ execution.PreparedExecution) (string, error) {
			taskCount++
			return "ran task", nil
		},
		Confirm: func(tool, desc string, _ int) ConfirmResult {
			builtPrompts = append(builtPrompts, tool+":"+desc)
			return nextDecision
		},
	})

	// Outside ScopeExec, run_shell via tool_call is still refused.
	if _, err := aDemo.dispatchToolCall(context.Background(), toolCall("c-obs-sh", toolCallName, `{"name":"run_shell","arguments":{"command":"echo hi"}}`)); err == nil || !strings.Contains(err.Error(), "not available in this mode") {
		t.Fatalf("observe scope allowed deferred run_shell: %v", err)
	}

	// Permit ScopeEdit while keeping default confirm policy so write_file/edit_file show diff prompts.
	aDemo.tools.SetScope(ScopeEdit)
	if _, err := aDemo.dispatchToolCall(context.Background(), toolCall("c-w", toolCallName, fmt.Sprintf(`{"name":"write_file","arguments":{"path":%q,"content":"new\n"}}`, "note.txt"))); err != nil {
		t.Fatalf("deferred write_file: %v", err)
	}
	if _, err := aDemo.dispatchToolCall(context.Background(), toolCall("c-e", toolCallName, fmt.Sprintf(`{"name":"Edit_File","arguments":{"path":%q,"target_content":"new\n","replacement_content":"newer\n"}}`, "note.txt"))); err != nil {
		t.Fatalf("deferred Edit_File: %v", err)
	}
	if len(builtPrompts) < 2 || !strings.Contains(builtPrompts[0], "Overwrite file") || !strings.Contains(builtPrompts[1], "Edit file") {
		t.Fatalf("missing diff previews for deferred write_file/edit_file: %v", builtPrompts)
	}

	aDemo.SetAutonomy(AutonomyFull)
	// Risky shell command under AutonomyFull triggers safety scoring even through tool_call.
	builtPrompts = nil
	if _, err := aDemo.dispatchToolCall(context.Background(), toolCall("c-sh", toolCallName, `{"name":"run_shell","arguments":{"command":"git push origin main"}}`)); err != nil {
		t.Fatalf("deferred run_shell: %v", err)
	}
	if len(shellRan) != 1 || len(builtPrompts) != 1 || !strings.HasPrefix(builtPrompts[0], "run_shell:") {
		t.Fatalf("deferred run_shell bypassed safety gate: ran=%v prompts=%v", shellRan, builtPrompts)
	}

	// axis_run_task prompts every time even when previous decision was ConfirmAlways.
	aTask := New(Config{
		Backend:     &kvCacheProbeBackend{},
		AutoApprove: true,
		Autonomy:    AutonomyDefault,
		EagerTools:  []string{"read_file"},
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{
			Config: &config.Config{Nodes: []config.NodeConfig{{Name: "node-a", Hostname: "localhost", SSHUser: "me"}}},
			Snapshot: &models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
				Nodes: []models.NodeFacts{{
					Name:     "node-a",
					Hostname: "localhost",
					Status:   models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  4096,
						Pressure:   "low",
						CPUCores:   8,
					},
				}},
				Summary: models.ClusterSummary{TotalNodes: 1, ReachableNodes: 1},
			},
			State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
			Skills: &skills.Store{},
		}, nil),
		RunTask: func(_ context.Context, _ execution.PreparedExecution) (string, error) {
			taskCount++
			return "ran task", nil
		},
		Confirm: func(tool, desc string, _ int) ConfirmResult {
			builtPrompts = append(builtPrompts, tool+":"+desc)
			return nextDecision
		},
	})
	aTask.tools.SetScope(ScopeExec)
	builtPrompts = nil
	if _, err := aTask.dispatchToolCall(context.Background(), toolCall("c-t1", toolCallName, `{"name":"axis_run_task","arguments":{"description":"echo hello"}}`)); err != nil {
		t.Fatalf("deferred axis_run_task 1: %v", err)
	}
	if _, err := aTask.dispatchToolCall(context.Background(), toolCall("c-t2", toolCallName, `{"name":"axis_run_task","arguments":{"description":"echo hello again"}}`)); err != nil {
		t.Fatalf("deferred axis_run_task 2: %v", err)
	}
	if taskCount != 2 || len(builtPrompts) != 2 {
		t.Fatalf("axis_run_task must prompt every call via tool_call too: taskCount=%d prompts=%d", taskCount, len(builtPrompts))
	}

	// ConfirmNever sets blockAll and blocks later deferred calls cleanly.
	nextDecision = ConfirmNever
	if _, err := aTask.dispatchToolCall(context.Background(), toolCall("c-blk1", toolCallName, `{"name":"axis_run_task","arguments":{"description":"echo blocked"}}`)); err == nil || !strings.Contains(err.Error(), "blocked all") {
		t.Fatalf("expected ConfirmNever block, got %v", err)
	}
	if _, err := aTask.dispatchToolCall(context.Background(), toolCall("c-blk2", toolCallName, `{"name":"edit_file","arguments":{"path":"x","target_content":"a","replacement_content":"b"}}`)); err == nil || !strings.Contains(err.Error(), "blocked all") {
		t.Fatalf("expected subsequent deferred call blocked by blockAll, got %v", err)
	}
}
