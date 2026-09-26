package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/a2a"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/events"
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

// ToolDef and ToolsResponse alias the daemon types so the JSON shape and the
// tool definitions themselves have exactly one source: daemon.ToolDefinitions.
type ToolDef = daemon.ToolDef

type ToolsResponse = daemon.ToolsResponse

type KnowledgeResponse struct {
	Knowledge *knowledge.ClusterKnowledge `json:"knowledge"`
	Skills    []skills.LearnedSkill       `json:"skills"`
	Failures  []skills.LearnedFailure     `json:"failures"`
}

type RunRequest struct {
	Description   string `json:"description"`
	Mode          string `json:"mode,omitempty"`
	Confirm       string `json:"confirm,omitempty"`
	RequestedNode string `json:"requested_node,omitempty"`
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

const (
	runtimeRefreshTimeout   = 30 * time.Second
	serverReadHeaderTimeout = 5 * time.Second
	serverReadTimeout       = 15 * time.Second
	serverIdleTimeout       = 60 * time.Second
)

var runLiveGuarded = execution.RunGuarded

func Serve(addr string, cache snapshotCache, token string, pprof bool) error {
	return ServeWithContext(context.Background(), addr, cache, token, pprof, nil)
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		IdleTimeout:       serverIdleTimeout,
		// /run streams progress for commands that may legitimately outlive a
		// fixed response deadline. Caller contexts bound that execution path.
		WriteTimeout: 0,
	}
}

// ServeWithContext starts the HTTP/Unix API server and blocks until ctx is
// cancelled or a fatal listen error occurs. On cancellation it performs a
// graceful shutdown with a 10-second drain before returning nil. When
// cardFn is non-nil, the standard A2A well-known route
// (/.well-known/agent-card.json) is served alongside the API routes.
// Authenticated A2A task send/get routes are always registered in registerRoutes.
func ServeWithContext(ctx context.Context, addr string, cache snapshotCache, token string, pprof bool, cardFn func() a2a.AgentCard) error {
	mux := http.NewServeMux()
	registerRoutes(mux, cache, token)
	if cardFn != nil {
		a2a.ServeCard(mux, cardFn)
	}
	if pprof {
		registerPprofRoutes(mux, token)
	}

	srv := newHTTPServer(mux)

	srvErr := make(chan error, 1)

	if auth.IsUnixAddr(addr) {
		ln, release, err := acquireUnixSocket(addr)
		if err != nil {
			return err
		}
		defer release()
		go func() { srvErr <- srv.Serve(ln) }()
	} else {
		// TCP addresses are already exclusive: a second listener on the same
		// host:port fails with EADDRINUSE rather than displacing the first.
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

	drainCtx, cancel := context.WithTimeout(context.Background(), daemon.ShutdownDrainTimeout)
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

// withAuth enforces the shared API bearer token (same policy as /run).
// F9: when token == "", this is a no-op and the next handler runs unauthenticated —
// identical to /run. Production TCP listeners should use a non-empty token; the
// A2A slice-2 E2E harness requires a non-empty token for that reason.
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
	// A2A slice 2v1: authenticated task send/get on the same mux as /run.
	// Public agent card is mounted separately in ServeWithContext (outside withAuth).
	// F9: task routes inherit /run token policy — withAuth no-ops when token=="";
	// E2E and any TCP expose should use a non-empty token.
	a2aQueue := a2a.NewApprovalQueue()
	a2aHandler := &a2a.Handler{
		Store:   a2a.NewStore(0),
		Scope:   a2a.ScopeObserve, // live scope; reject over-tier (F4)
		Observe: cacheObserve{cache: cache},
		Queue:   a2aQueue,
	}
	a2a.ServeTasks(mux, a2aHandler, func(next http.HandlerFunc) http.HandlerFunc {
		return withAuth(next, token)
	})
	// Slice 3: the only approve/reject routes. Approve requires confirm=YES
	// and then runs the guarded pipeline, same contract as /run.
	registerA2AApprovalRoutes(mux, a2aHandler, token, cache)
	// Operator board view: list pending approval tasks.
	mux.HandleFunc("/a2a/v1/tasks/pending", withAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, a2aTaskList{Tasks: a2aQueue.Pending()})
	}, token))

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
		writeJSON(w, http.StatusOK, ToolsResponse{Tools: daemon.ToolDefinitions()})
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
			Description:      req.Description,
			Mode:             req.Mode,
			Confirm:          req.Confirm,
			RequestedNode:    req.RequestedNode,
			OwnerSurface:     execution.OwnerSurfaceHTTPRun,
			OwnerLabel:       requestCallerLabel(r),
			Events:           events.GuardedExecutionSink{},
			BuildContextJSON: knowledge.ExecutionContextJSON,
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

// registerA2AApprovalRoutes mounts the only operator approve and reject
// routes. Both use the same bearer policy as /run. Approve requires the
// caller to send confirm=YES and an explicit mode, then dispatches through
// the guarded runner. Neither route is the /tasks/approve/{id} shape.
func registerA2AApprovalRoutes(mux *http.ServeMux, h *a2a.Handler, token string, cache snapshotCache) {
	mux.HandleFunc("/a2a/v1/tasks/{id}/approve", withAuth(func(w http.ResponseWriter, r *http.Request) {
		approveA2ATask(w, r, h, cache)
	}, token))
	mux.HandleFunc("/a2a/v1/tasks/{id}/reject", withAuth(func(w http.ResponseWriter, r *http.Request) {
		rejectA2ATask(w, r, h)
	}, token))
}

func approveA2ATask(w http.ResponseWriter, r *http.Request, h *a2a.Handler, cache snapshotCache) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h == nil || h.Queue == nil || h.Store == nil {
		writeError(w, http.StatusNotFound, "task not pending")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusNotFound, "unknown task")
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
		Mode    string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	confirm := strings.TrimSpace(body.Confirm)
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if confirm != execution.ConfirmWord {
		writeError(w, http.StatusBadRequest, "confirm must be YES to authorize execution")
		return
	}
	if mode == "" {
		writeError(w, http.StatusBadRequest, "mode is required (use script or exec)")
		return
	}
	if mode != execution.ModeScript && mode != execution.ModeExec {
		writeError(w, http.StatusBadRequest, "mode must be script or exec")
		return
	}

	task, ok := h.Queue.Approve(id)
	if !ok {
		writeError(w, http.StatusNotFound, "task not pending")
		return
	}
	text := ""
	if len(task.History) > 0 {
		text = strings.TrimSpace(a2a.TextFromMessage(task.History[0]))
	}
	if text == "" {
		persistA2ARun(w, h, task, execution.GuardedExecutionResult{OK: false, Error: "description is required"}, nil, false)
		return
	}

	guardedReq := execution.GuardedExecutionRequest{
		Description:      text,
		Mode:             mode,
		Confirm:          confirm,
		OwnerSurface:     execution.OwnerSurfaceA2ATask,
		OwnerLabel:       requestCallerLabel(r),
		Events:           events.GuardedExecutionSink{},
		BuildContextJSON: knowledge.ExecutionContextJSON,
		OnStateChange: func(_ context.Context, trigger string, _ execution.GuardedExecutionResult) {
			scheduleCacheRefresh(cache, trigger)
		},
	}
	emitResult, streamed, err := daemon.WireRunStreamResponse(w, r, &guardedReq)
	if err != nil {
		persistA2ARun(w, h, task, execution.GuardedExecutionResult{OK: false, Error: err.Error()}, nil, false)
		return
	}

	rc, rcErr := loadRunnerContext(r.Context())
	if rcErr != nil {
		res := execution.GuardedExecutionResult{OK: false, Description: text, Mode: mode, Error: rcErr.Error()}
		persistA2ARun(w, h, task, res, emitResult, streamed)
		return
	}
	rtCtx := &runtimectx.Context{
		Config:   rc.cfg,
		Snapshot: rc.snap,
		State:    rc.State,
		Skills:   rc.skillStore,
		Ledger:   rc.ledger,
	}
	res, runErr := runLiveGuarded(r.Context(), rtCtx, guardedReq)
	res = daemon.NormalizeRunResult(res, runErr)
	persistA2ARun(w, h, task, res, emitResult, streamed)
}

func rejectA2ATask(w http.ResponseWriter, r *http.Request, h *a2a.Handler) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h == nil || h.Queue == nil || h.Store == nil {
		writeError(w, http.StatusNotFound, "task not pending")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusNotFound, "unknown task")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	task, ok := h.Queue.Reject(id, reason)
	if !ok {
		writeError(w, http.StatusNotFound, "task not pending")
		return
	}
	h.Store.Put(task)
	writePublicA2ATask(w, http.StatusOK, task)
}

func persistA2ARun(w http.ResponseWriter, h *a2a.Handler, task *a2a.Task, res execution.GuardedExecutionResult, emitResult func(execution.GuardedExecutionResult) error, streamed bool) {
	cp := *task
	now := time.Now()
	if res.OK && res.Error == "" {
		cp.Status = a2a.TaskStatus{State: a2a.TaskStateCompleted, Timestamp: now}
	} else {
		msg := res.Error
		if msg == "" {
			msg = res.BlockReason
		}
		if msg == "" {
			msg = "guarded execution failed"
		}
		cp.Status = a2a.TaskStatus{
			State:     a2a.TaskStateFailed,
			Timestamp: now,
			Message:   &a2a.Message{Role: "agent", Parts: []a2a.Part{{Type: "text", Text: msg}}},
		}
	}
	h.Store.Put(&cp)
	if streamed && emitResult != nil {
		_ = emitResult(res)
		return
	}
	writePublicA2ATask(w, http.StatusOK, &cp)
}

func writePublicA2ATask(w http.ResponseWriter, status int, task *a2a.Task) {
	pub := *task
	pub.PrincipalHash = ""
	a2a.WriteTaskJSON(w, status, &pub)
}

type a2aTaskList struct {
	Tasks []a2a.Task `json:"tasks"`
}
