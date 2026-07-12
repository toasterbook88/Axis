package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mesh"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func DefaultAddr() string {
	return persist.AxisPath("axis.sock")
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
	ExecID         string                      `json:"exec_id,omitempty"`
	OK             bool                        `json:"ok"`
	Description    string                      `json:"description"`
	Mode           string                      `json:"mode,omitempty"`
	Intent         string                      `json:"intent,omitempty"`
	Command        string                      `json:"command,omitempty"`
	Node           string                      `json:"node,omitempty"`
	Tool           string                      `json:"tool,omitempty"`
	Workload       models.WorkloadProfileMatch `json:"workload,omitempty"`
	FitScore       int                         `json:"fit_score,omitempty"`
	IsLocal        bool                        `json:"is_local,omitempty"`
	Reasoning      []string                    `json:"reasoning,omitempty"`
	Blocked        bool                        `json:"blocked,omitempty"`
	BlockReason    string                      `json:"block_reason,omitempty"`
	DumbScore      int                         `json:"dumb_score,omitempty"`
	Output         string                      `json:"output,omitempty"`
	Error          string                      `json:"error,omitempty"`
	ExitCode       int                         `json:"exit_code,omitempty"`
	SnapshotStatus models.SnapshotStatus       `json:"snapshot_status,omitempty"`
	Summary        *models.ClusterSummary      `json:"summary,omitempty"`
	PeakRAMMB      int64                       `json:"peak_ram_mb,omitempty"`
	PeakVRAMMB     int64                       `json:"peak_vram_mb,omitempty"`
	WallTimeMS     int64                       `json:"wall_time_ms,omitempty"`
}

type runnerContext struct {
	cfg        *config.Config
	snap       *models.ClusterSnapshot
	State      *state.ClusterState
	skillStore *skills.Store
	ledger     *reservation.Ledger
}

type snapshotCache interface {
	Snapshot() (*models.ClusterSnapshot, bool)
	Meta() daemon.Metadata
	Ledger() *reservation.Ledger
	Mesh() *mesh.Mesh
	Invalidate()
	RefreshNow(context.Context) error
}

type triggerableSnapshotCache interface {
	RefreshWithTrigger(context.Context, string) error
}

const runtimeRefreshTimeout = 30 * time.Second

var runLiveGuarded = execution.RunGuarded

func Serve(addr string, cache snapshotCache, token string, pprof bool) error {
	return ServeWithContext(context.Background(), addr, cache, token, pprof)
}

// ServeWithContext starts the HTTP/Unix API server and blocks until ctx is
// cancelled or a fatal listen error occurs. On cancellation it performs a
// graceful shutdown with a 10-second drain before returning nil.
func ServeWithContext(ctx context.Context, addr string, cache snapshotCache, token string, pprof bool) error {
	mux := http.NewServeMux()
	registerRoutes(mux, cache, token)
	if pprof {
		registerPprofRoutes(mux, token)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	srvErr := make(chan error, 1)

	if auth.IsUnixAddr(addr) {
		if err := os.MkdirAll(filepath.Dir(addr), 0700); err != nil {
			return fmt.Errorf("creating unix socket directory: %w", err)
		}
		if fi, err := os.Lstat(addr); err == nil {
			if fi.Mode()&os.ModeSocket == 0 {
				return fmt.Errorf("refusing to remove non-socket file at %s", addr)
			}
			if err := os.Remove(addr); err != nil {
				return fmt.Errorf("removing existing socket: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat socket path: %w", err)
		}
		ln, err := net.Listen("unix", addr)
		if err != nil {
			return fmt.Errorf("listen unix %s: %w", addr, err)
		}
		if err := os.Chmod(addr, 0600); err != nil {
			return fmt.Errorf("chmod unix socket: %w", err)
		}
		go func() { srvErr <- srv.Serve(ln) }()
	} else {
		srv.Addr = addr
		go func() { srvErr <- srv.ListenAndServe() }()
	}

	select {
	case err := <-srvErr:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(drainCtx) //nolint:contextcheck

	select {
	case err := <-srvErr:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	default:
		return nil
	}
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

		parts := strings.Fields(authHeader)
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
		payload := daemon.HealthPayload(nil)
		if cache != nil {
			meta := cache.Meta()
			payload = daemon.HealthPayload(&meta)
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
		trigger, err := daemon.NormalizeRefreshTrigger(r.URL.Query().Get("trigger"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := refreshCache(cache, r.Context(), trigger); err != nil {
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
			Knowledge: knowledge.Build(rc.snap, rc.State, ""),
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

		resp := RunResponse{
			Description: req.Description,
			Mode:        req.Mode,
		}

		forwardedOrigin, hasForwardedOrigin, err := auth.ForwardedExecutionOriginFromRequest(r, token, time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		guardedReq := execution.GuardedExecutionRequest{
			Description:  req.Description,
			Mode:         req.Mode,
			Confirm:      req.Confirm,
			OwnerSurface: execution.OwnerSurfaceHTTPRun,
			OwnerLabel:   requestCallerLabel(r),
			OnStateChange: func(_ context.Context, trigger string, _ execution.GuardedExecutionResult) {
				scheduleCacheRefresh(cache, trigger)
			},
		}
		if hasForwardedOrigin {
			guardedReq.OriginOverride = forwardedOrigin
		}

		emitResult, streamed, err := daemon.WireRunStreamResponse(w, r, &guardedReq)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		rc, err := loadRunnerContext(ctx)
		if err != nil {
			if streamed {
				_ = emitResult(execution.GuardedExecutionResult{
					OK:          false,
					Description: req.Description,
					Mode:        req.Mode,
					Error:       err.Error(),
				})
				return
			}
			resp.Error = err.Error()
			writeJSON(w, http.StatusOK, resp)
			return
		}

		rtCtx := &runtimectx.Context{
			Config:   rc.cfg,
			Snapshot: rc.snap,
			State:    rc.State,
			Skills:   rc.skillStore,
			Ledger:   rc.ledger,
		}

		res, runErr := runLiveGuarded(ctx, rtCtx, guardedReq)
		res = daemon.NormalizeRunResult(res, runErr)

		if streamed {
			_ = emitResult(res)
			return
		}

		resp = RunResponse(res)

		writeJSON(w, http.StatusOK, resp)
	}, token))

	registerV2Routes(mux, cache, token)
}

// registerPprofRoutes wires the profiling handlers behind the same bearer-token
// auth as every other non-health route. These endpoints expose the process
// command line (which can leak flags/tokens) and allow profile/trace-driven
// resource exhaustion, so they must never be reachable unauthenticated when the
// API is bound to a TCP address.
func registerPprofRoutes(mux *http.ServeMux, token string) {
	mux.HandleFunc("/debug/pprof/", withRequiredAuth(pprof.Index, token))
	mux.HandleFunc("/debug/pprof/cmdline", withRequiredAuth(pprof.Cmdline, token))
	mux.HandleFunc("/debug/pprof/profile", withRequiredAuth(pprof.Profile, token))
	mux.HandleFunc("/debug/pprof/symbol", withRequiredAuth(pprof.Symbol, token))
	mux.HandleFunc("/debug/pprof/trace", withRequiredAuth(pprof.Trace, token))
}

func withRequiredAuth(next http.HandlerFunc, token string) http.HandlerFunc {
	if token == "" {
		return func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusUnauthorized, "api token is not configured")
		}
	}
	return withAuth(next, token)
}

var loadLiveRuntime = runtimectx.Load

func loadRunnerContext(ctx context.Context) (*runnerContext, error) {
	rt, err := loadLiveRuntime(ctx)
	if err != nil {
		return nil, err
	}
	return &runnerContext{
		cfg:        rt.Config,
		snap:       rt.Snapshot,
		State:      rt.State,
		skillStore: rt.Skills,
		ledger:     rt.Ledger,
	}, nil
}

func refreshCache(cache snapshotCache, ctx context.Context, trigger string) error {
	if cache == nil {
		return nil
	}
	if triggered, ok := any(cache).(triggerableSnapshotCache); ok {
		return triggered.RefreshWithTrigger(ctx, trigger)
	}
	return cache.RefreshNow(ctx)
}

func scheduleCacheRefresh(cache snapshotCache, trigger string) {
	if cache == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runtimeRefreshTimeout)
		defer cancel()
		if err := refreshCache(cache, ctx, trigger); err != nil {
			slog.Error("api: async refresh failed", "trigger", trigger, "error", err)
		}
	}()
}

func requestCallerLabel(r *http.Request) string {
	if r == nil {
		return ""
	}
	remoteAddr := strings.TrimSpace(r.RemoteAddr)
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return strings.TrimSpace(host)
	}
	return remoteAddr
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
