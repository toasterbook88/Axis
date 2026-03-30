package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/safety"
	"github.com/toasterbook88/axis/internal/scripts"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/transport"
)

func DefaultAddr() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", "axis.sock")
}

type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type ToolsResponse struct {
	Tools []ToolDef `json:"tools"`
}

type KnowledgeResponse struct {
	Knowledge *knowledge.ClusterKnowledge `json:"knowledge"`
	Skills    []skills.LearnedSkill       `json:"skills"`
	Failures  []skills.LearnedFailure     `json:"failures"`
}

type RunRequest struct {
	Description string `json:"description"`
	Mode        string `json:"mode,omitempty"`
	Confirm     string `json:"confirm,omitempty"`
}

type RunResponse struct {
	OK             bool                   `json:"ok"`
	Description    string                 `json:"description"`
	Mode           string                 `json:"mode,omitempty"`
	Intent         string                 `json:"intent,omitempty"`
	Command        string                 `json:"command,omitempty"`
	Node           string                 `json:"node,omitempty"`
	FitScore       int                    `json:"fit_score,omitempty"`
	IsLocal        bool                   `json:"is_local,omitempty"`
	Reasoning      []string               `json:"reasoning,omitempty"`
	Blocked        bool                   `json:"blocked,omitempty"`
	BlockReason    string                 `json:"block_reason,omitempty"`
	DumbScore      int                    `json:"dumb_score,omitempty"`
	Output         string                 `json:"output,omitempty"`
	Error          string                 `json:"error,omitempty"`
	ExitCode       int                    `json:"exit_code,omitempty"`
	SnapshotStatus models.SnapshotStatus  `json:"snapshot_status,omitempty"`
	Summary        *models.ClusterSummary `json:"summary,omitempty"`
}

type runIntent struct {
	command       string
	label         string
	matchedScript *scripts.Script
	matchedSkill  *skills.LearnedSkill
}

type runnerContext struct {
	cfg        *config.Config
	snap       *models.ClusterSnapshot
	st         *state.ClusterState
	skillStore *skills.Store
}

type snapshotCache interface {
	Snapshot() (*models.ClusterSnapshot, bool)
	Meta() daemon.Metadata
	Invalidate()
	RefreshNow(context.Context) error
}

func Serve(addr string, cache snapshotCache, token string) error {
	mux := http.NewServeMux()
	registerRoutes(mux, cache, token)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if auth.IsUnixAddr(addr) {
		if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing existing socket: %w", err)
		}
		ln, err := net.Listen("unix", addr)
		if err != nil {
			return fmt.Errorf("listen unix %s: %w", addr, err)
		}
		if err := os.Chmod(addr, 0600); err != nil {
			return fmt.Errorf("chmod unix socket: %w", err)
		}
		return srv.Serve(ln)
	}

	srv.Addr = addr
	return srv.ListenAndServe()
}


func withAuth(next http.HandlerFunc, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			next(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeError(w, http.StatusUnauthorized, "missing authorization header")
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			writeError(w, http.StatusUnauthorized, "invalid authorization header format")
			return
		}

		if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(token)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid api token")
			return
		}

		next(w, r)
	}
}

func registerRoutes(mux *http.ServeMux, cache snapshotCache, token string) {
	healthHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		payload := map[string]any{
			"status": "ok",
			"name":   "axis",
		}
		if cache != nil {
			meta := cache.Meta()
			payload["cache_ready"] = meta.Ready
		}
		writeJSON(w, http.StatusOK, payload)
	}
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/healthz", healthHandler)

	mux.HandleFunc("/snapshot", withAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if cache == nil {
			writeError(w, http.StatusServiceUnavailable, "snapshot cache unavailable")
			return
		}
		snap, ok := cache.Snapshot()
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "snapshot cache not ready")
			return
		}
		writeJSON(w, http.StatusOK, snap)
	}, token))

	mux.HandleFunc("/snapshot/meta", withAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if cache == nil {
			writeError(w, http.StatusServiceUnavailable, "snapshot cache unavailable")
			return
		}
		writeJSON(w, http.StatusOK, cache.Meta())
	}, token))

	mux.HandleFunc("/invalidate", withAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if cache == nil {
			writeError(w, http.StatusServiceUnavailable, "snapshot cache unavailable")
			return
		}
		cache.Invalidate()
		w.WriteHeader(http.StatusNoContent)
	}, token))

	mux.HandleFunc("/refresh", withAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if cache == nil {
			writeError(w, http.StatusServiceUnavailable, "snapshot cache unavailable")
			return
		}
		if err := cache.RefreshNow(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}, token))

	toolsHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tools := []ToolDef{
			{
				Name:        "axis_execute",
				Description: "Execute a task on the live AXIS cluster using placement, learned skills/scripts, live RAM pressure, and the safety blocker. Explicit mode and explicit operator confirmation are required: use mode=script for matched scripts/skills only or mode=exec for explicit raw commands, and set confirm=YES to authorize execution.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"description": map[string]any{"type": "string", "description": "Natural language task description or raw command"},
						"mode":        map[string]any{"type": "string", "description": "Execution mode: script or exec"},
						"confirm":     map[string]any{"type": "string", "description": "Must be YES to authorize execution"},
					},
					"required": []string{"description", "mode", "confirm"},
				},
			},
			{
				Name:        "axis_knowledge",
				Description: "Return live cluster state, Ollama status, learned skills, and recent placement state.",
				InputSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		}
		writeJSON(w, http.StatusOK, ToolsResponse{Tools: tools})
	}
	mux.HandleFunc("/tools", withAuth(toolsHandler, token))
	mux.HandleFunc("/mcp/tools", withAuth(toolsHandler, token))

	mux.HandleFunc("/knowledge", withAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		rc, err := loadRunnerContext(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		payload := KnowledgeResponse{
			Knowledge: knowledge.Build(rc.snap, rc.st, ""),
			Skills:    rc.skillStore.Skills,
			Failures:  rc.skillStore.Failures,
		}
		writeJSON(w, http.StatusOK, payload)
	}, token))

	mux.HandleFunc("/run", withAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		req.Description = strings.TrimSpace(req.Description)
		req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
		req.Confirm = strings.TrimSpace(req.Confirm)
		if req.Description == "" {
			writeError(w, http.StatusBadRequest, "description is required")
			return
		}
		if req.Mode == "" {
			writeError(w, http.StatusBadRequest, "mode is required (use script or exec)")
			return
		}
		if req.Mode != "script" && req.Mode != "exec" {
			writeError(w, http.StatusBadRequest, "mode must be script or exec")
			return
		}
		if req.Confirm != "YES" {
			writeError(w, http.StatusBadRequest, "confirm must be YES to authorize execution")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()

		resp, err := runTask(ctx, req)
		if err != nil {
			resp.OK = false
			if resp.Error == "" {
				resp.Error = err.Error()
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}

		writeJSON(w, http.StatusOK, resp)
	}, token))
}

var loadLiveRuntime = runtimectx.Load

var runLocalShell = func(ctx context.Context, command string, env []string) ([]byte, error) {
	// codeql[go/command-injection] - command is protected by UDS-only socket, bearer token auth, confirm=YES, and safety.Check()
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Env = env
	return cmd.CombinedOutput()
}

func loadRunnerContext(ctx context.Context) (*runnerContext, error) {
	rt, err := loadLiveRuntime(ctx)
	if err != nil {
		return nil, err
	}
	return &runnerContext{
		cfg:        rt.Config,
		snap:       rt.Snapshot,
		st:         rt.State,
		skillStore: rt.Skills,
	}, nil
}

func resolveIntent(description, mode string, skillStore *skills.Store) (runIntent, error) {
	var intent runIntent
	if skillStore != nil {
		if skill, ok := skillStore.BestMatch(description); ok {
			skillCopy := skill
			intent.matchedSkill = &skillCopy
		}
	}
	if script, ok := scripts.GetBestScript(description); ok {
		scriptCopy := script
		intent.matchedScript = &scriptCopy
	}

	switch mode {
	case "script":
		if intent.matchedScript != nil {
			intent.command = intent.matchedScript.Command
			intent.label = fmt.Sprintf("fallback script %q", intent.matchedScript.Name)
			return intent, nil
		}
		if intent.matchedSkill != nil {
			intent.command = intent.matchedSkill.Command
			intent.label = fmt.Sprintf("learned skill %q", intent.matchedSkill.ID)
			return intent, nil
		}
		return runIntent{}, fmt.Errorf("no known script or learned skill matches %q", description)
	case "exec":
		intent.command = description
		intent.label = "raw command"
		return intent, nil
	default:
		return runIntent{}, fmt.Errorf("unsupported mode %q", mode)
	}
}

func runTask(ctx context.Context, req RunRequest) (RunResponse, error) {
	resp := RunResponse{
		Description: req.Description,
		Mode:        req.Mode,
	}

	rc, err := loadRunnerContext(ctx)
	if err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	resp.SnapshotStatus = rc.snap.Status
	resp.Summary = &rc.snap.Summary

	intent, err := resolveIntent(req.Description, req.Mode, rc.skillStore)
	if err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	resp.Intent = intent.label
	resp.Command = intent.command

	reqs := placement.InferRequirements(req.Description)
	if intent.matchedScript != nil {
		reqs.RequiredTools = append([]string(nil), intent.matchedScript.RequiredTools...)
		if intent.matchedScript.EstRAMMB > reqs.MinFreeRAMMB {
			reqs.MinFreeRAMMB = intent.matchedScript.EstRAMMB
		}
	}
	if req.Mode == "exec" && len(reqs.RequiredTools) > 0 {
		filtered := reqs.RequiredTools[:0]
		for _, tool := range reqs.RequiredTools {
			if !strings.EqualFold(tool, "ollama") {
				filtered = append(filtered, tool)
			}
		}
		reqs.RequiredTools = append([]string(nil), filtered...)
	}

	decision := placement.SelectBestNode(reqs, rc.snap.Nodes, rc.st)
	resp.Reasoning = decision.Reasoning
	resp.Node = decision.Node
	resp.FitScore = decision.FitScore
	resp.IsLocal = decision.IsLocal

	if !decision.OK {
		resp.Error = "no suitable node found"
		return resp, fmt.Errorf("no suitable node found")
	}

	reservationMB := reqs.MinFreeRAMMB + 1024
	if !daemon.CanReserve(rc.snap, rc.st, decision.Node, reservationMB) {
		resp.Error = fmt.Sprintf("node %s cannot reserve %d MB (current reservations exceed cap)", decision.Node, reservationMB)
		return resp, fmt.Errorf("reservation cap exceeded")
	}

	var targetNode models.NodeFacts
	for _, n := range rc.snap.Nodes {
		if n.Name == decision.Node {
			targetNode = n
			break
		}
	}

	k := knowledge.Build(rc.snap, rc.st, decision.Node)
	if block := safety.Check(k, intent.command, rc.skillStore.IsKnownBad); block.Blocked {
		resp.Blocked = true
		resp.BlockReason = block.Reason
		resp.DumbScore = block.Score
		resp.Error = block.Reason
		return resp, nil
	}

	contextJSON, err := knowledge.ExecutionContextJSON(rc.snap, rc.st, decision, req.Description, intent.matchedScript, intent.matchedSkill)
	if err != nil {
		resp.Error = err.Error()
		return resp, err
	}

	if models.IsLocalNode(targetNode) {
		contextFile, err := os.CreateTemp("", "axis-knows-*.json")
		if err != nil {
			resp.Error = err.Error()
			return resp, err
		}
		defer os.Remove(contextFile.Name())
		if _, err := contextFile.Write(contextJSON); err != nil {
			_ = contextFile.Close()
			resp.Error = err.Error()
			return resp, err
		}
		if err := contextFile.Close(); err != nil {
			resp.Error = err.Error()
			return resp, err
		}

		env := append(os.Environ(),
			"AXIS_CONTEXT_FILE="+contextFile.Name(),
			"BEST_NODE="+decision.Node,
		)
		if rc.st != nil {
			execID, err := rc.st.AcquireTask(decision.Node, req.Description, reservationMB)
			if err != nil {
				resp.Error = err.Error()
				return resp, err
			}
			defer func() {
				if err := rc.st.ReleaseTask(decision.Node, execID, reservationMB); err != nil && resp.Error == "" {
					resp.Error = err.Error()
				}
			}()
		}
		out, err := runLocalShell(ctx, intent.command, env)
		resp.Output = string(out)
		resp.ExitCode = exitCode(err)
		if err != nil {
			resp.Error = err.Error()
			rc.skillStore.RecordFailure(req.Description, fmt.Sprintf("failed with code %d", resp.ExitCode))
			_ = rc.skillStore.Save()
			return resp, err
		}
		recordSuccess(rc, req.Description, intent.command, decision.Node)
		resp.OK = true
		return resp, nil
	}

	targetConfig, ok := findNodeConfig(rc.cfg, decision.Node)
	if !ok {
		resp.Error = fmt.Sprintf("node %q not found in config", decision.Node)
		return resp, fmt.Errorf("%s", resp.Error)
	}

	executor := transport.NewSSHExecutor(targetConfig.Hostname, targetConfig.EffectiveSSHPort(), targetConfig.SSHUser, targetConfig.EffectiveTimeout())
	defer executor.Close()

	remoteContextPath := fmt.Sprintf("/tmp/axis-knows-%d.json", time.Now().UTC().UnixNano())
	writeJSONCmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF\n", shellescape.Quote(remoteContextPath), string(contextJSON))
	if _, err := executor.Run(ctx, writeJSONCmd); err != nil {
		resp.Error = err.Error()
		resp.ExitCode = 1
		return resp, err
	}

	if rc.st != nil {
		execID, err := rc.st.AcquireTask(decision.Node, req.Description, reservationMB)
		if err != nil {
			resp.Error = err.Error()
			return resp, err
		}
		defer func() {
			if err := rc.st.ReleaseTask(decision.Node, execID, reservationMB); err != nil && resp.Error == "" {
				resp.Error = err.Error()
			}
		}()
	}

	quotedCmd := fmt.Sprintf(
		"export BEST_NODE=%s AXIS_CONTEXT_FILE=%s; trap 'rm -f %s' EXIT; bash -lc %s",
		shellescape.Quote(decision.Node),
		shellescape.Quote(remoteContextPath),
		shellescape.Quote(remoteContextPath),
		shellescape.Quote(intent.command),
	)
	out, err := executor.Run(ctx, quotedCmd)
	resp.Output = out
	resp.ExitCode = exitCode(err)
	if err != nil {
		resp.Error = err.Error()
		rc.skillStore.RecordFailure(req.Description, fmt.Sprintf("failed with code %d", resp.ExitCode))
		_ = rc.skillStore.Save()
		return resp, err
	}

	recordSuccess(rc, req.Description, intent.command, decision.Node)
	resp.OK = true
	return resp, nil
}

func recordSuccess(rc *runnerContext, description, command, node string) {
	rc.skillStore.RecordSuccess(description, command, node)
	_ = rc.skillStore.Save()
}

func findNodeConfig(cfg *config.Config, name string) (config.NodeConfig, bool) {
	for _, n := range cfg.Nodes {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return config.NodeConfig{}, false
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"ok":    false,
		"error": message,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
