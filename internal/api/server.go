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
	FitScore       int                         `json:"fit_score,omitempty"` // diagnostic only; not the ranking key
	Ranking        *models.PlacementRanking    `json:"ranking,omitempty"`
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
	ObservedRSSMB  int64                       `json:"observed_rss_mb,omitempty"`
	ObservedVRAMMB int64                       `json:"observed_vram_mb,omitempty"`
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
	return ServeWithTokenSource(context.Background(), addr, cache, auth.StaticToken(token), pprof, nil)
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

// ServeWithTokenSource starts the HTTP/Unix API server and blocks until ctx is
// cancelled or a fatal listen error occurs. On cancellation it performs a
// graceful shutdown with a 10-second drain before returning nil. When
// cardFn is non-nil, the standard A2A well-known route
// (/.well-known/agent-card.json) is served alongside the API routes.
// Authenticated A2A task send/get routes are always registered in
// registerRoutesSource.
//
// src supplies the API token. The daemon passes an auth.FileToken so a
// rewritten ~/.axis/token takes effect without a restart; each request takes
// one snapshot of src and uses it for bearer auth and forwarded-origin checks.
func ServeWithTokenSource(ctx context.Context, addr string, cache snapshotCache, src auth.TokenSource, pprof bool, cardFn func() a2a.AgentCard) error {
	// F9: withAuth passes requests through when token is empty. That is only
	// acceptable on a Unix socket, whose file mode is the access control; a
	// TCP listener without a token would serve every route unauthenticated.
	if src.Current() == "" && !auth.IsUnixAddr(addr) {
		return fmt.Errorf("refusing to listen on TCP address %q without an api token", addr)
	}
	mux := http.NewServeMux()
	registerRoutesSource(mux, cache, src)
	if cardFn != nil {
		a2a.ServeCard(mux, cardFn)
	}
	if pprof {
		registerPprofRoutesSource(mux, src)
	}

	srv := newHTTPServer(withRequestID(mux))

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

type requestTokenKey struct{}

// requestToken returns the token snapshot withAuthSource took for this
// request ("" when the route is not behind withAuthSource or no token is set).
func requestToken(r *http.Request) string {
	tok, _ := r.Context().Value(requestTokenKey{}).(string)
	return tok
}

// withAuthSource enforces the shared API bearer token (same policy as /run).
// F9: when the token is "", this is a no-op and the next handler runs
// unauthenticated — identical to /run. ServeWithTokenSource only allows that on
// a Unix socket and refuses a TCP listener without a token.
//
// It reads src once per request, uses that value for the bearer compare and
// stores it in the request context so later checks in the same request
// (forwarded-origin HMAC) use the same snapshot.
//
// On a mismatch it asks src to Refresh once (rate-limited by the source) and
// compares again, so a client that already read a rotated token file is not
// refused until the next poll. The refreshed value becomes the request's
// snapshot only if it matches; an empty refreshed value never matches, so the
// retry cannot open the route.
func withAuthSource(next http.HandlerFunc, src auth.TokenSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := src.Current()
		if token == "" {
			next(w, withRequestToken(r, token))
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

		if !bearerMatches(parts[1], token) {
			token = src.Refresh()
			if token == "" || !bearerMatches(parts[1], token) {
				writeError(w, http.StatusUnauthorized, "invalid api token")
				return
			}
		}

		next(w, withRequestToken(r, token))
	}
}

func bearerMatches(presented, token string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

func withRequestToken(r *http.Request, token string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestTokenKey{}, token))
}

func registerRoutesSource(mux *http.ServeMux, cache snapshotCache, src auth.TokenSource) {
	// A2A slice 2v1: authenticated task send/get on the same mux as /run.
	// Public agent card is mounted separately in ServeWithTokenSource (outside withAuthSource).
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
		return withAuthSource(next, src)
	})
	// Slice 3: the only approve/reject routes. Approve requires confirm=YES
	// and then runs the guarded pipeline, same contract as /run.
	registerA2AApprovalRoutes(mux, a2aHandler, src, cache)
	// Operator board view: list pending approval tasks.
	mux.HandleFunc("/a2a/v1/tasks/pending", withAuthSource(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, a2aTaskList{Tasks: a2aQueue.Pending()})
	}, src))

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

	mux.HandleFunc("/snapshot", withAuthSource(func(w http.ResponseWriter, r *http.Request) {
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
	}, src))

	mux.HandleFunc("/snapshot/meta", withAuthSource(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if cache == nil {
			writeError(w, http.StatusServiceUnavailable, "snapshot cache unavailable")
			return
		}
		writeJSON(w, http.StatusOK, cache.Meta())
	}, src))

	mux.HandleFunc("/invalidate", withAuthSource(func(w http.ResponseWriter, r *http.Request) {
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
	}, src))

	mux.HandleFunc("/refresh", withAuthSource(func(w http.ResponseWriter, r *http.Request) {
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
	}, src))

	toolsHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, ToolsResponse{Tools: daemon.ToolDefinitions()})
	}
	mux.HandleFunc("/tools", withAuthSource(toolsHandler, src))
	mux.HandleFunc("/mcp/tools", withAuthSource(toolsHandler, src))

	mux.HandleFunc("/knowledge", withAuthSource(func(w http.ResponseWriter, r *http.Request) {
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
	}, src))

	mux.HandleFunc("/run", withAuthSource(func(w http.ResponseWriter, r *http.Request) {
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

		forwardedOrigin, hasForwardedOrigin, err := auth.ForwardedExecutionOriginFromRequest(r, requestToken(r), time.Now())
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
	}, src))

	registerV2Routes(mux, cache, src)
}

// registerPprofRoutesSource wires the profiling handlers behind the same
// bearer-token auth as every other non-health route. These endpoints expose the
// process command line (which can leak flags/tokens) and allow
// profile/trace-driven resource exhaustion, so they must never be reachable
// unauthenticated when the API is bound to a TCP address.
func registerPprofRoutesSource(mux *http.ServeMux, src auth.TokenSource) {
	mux.HandleFunc("/debug/pprof/", withRequiredAuthSource(pprof.Index, src))
	mux.HandleFunc("/debug/pprof/cmdline", withRequiredAuthSource(pprof.Cmdline, src))
	mux.HandleFunc("/debug/pprof/profile", withRequiredAuthSource(pprof.Profile, src))
	mux.HandleFunc("/debug/pprof/symbol", withRequiredAuthSource(pprof.Symbol, src))
	mux.HandleFunc("/debug/pprof/trace", withRequiredAuthSource(pprof.Trace, src))
}

// withRequiredAuthSource refuses every request when no token is configured.
// The check is per request (on the snapshot withAuthSource took), so a source
// can never fail open; ServeWithTokenSource already refuses an empty token on
// TCP at startup.
func withRequiredAuthSource(next http.HandlerFunc, src auth.TokenSource) http.HandlerFunc {
	return withAuthSource(func(w http.ResponseWriter, r *http.Request) {
		if requestToken(r) == "" {
			writeError(w, http.StatusUnauthorized, "api token is not configured")
			return
		}
		next(w, r)
	}, src)
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
func registerA2AApprovalRoutes(mux *http.ServeMux, h *a2a.Handler, src auth.TokenSource, cache snapshotCache) {
	mux.HandleFunc("/a2a/v1/tasks/{id}/approve", withAuthSource(func(w http.ResponseWriter, r *http.Request) {
		approveA2ATask(w, r, h, cache)
	}, src))
	mux.HandleFunc("/a2a/v1/tasks/{id}/reject", withAuthSource(func(w http.ResponseWriter, r *http.Request) {
		rejectA2ATask(w, r, h)
	}, src))
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
