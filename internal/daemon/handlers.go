package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
)

var reportAsyncRefreshError = func(trigger string, err error) {
	fmt.Fprintf(os.Stderr, "axis daemon: async refresh (%s) failed: %v\n", trigger, err)
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

type RouteDeps struct {
	LoadRuntime          func(context.Context) (*runtimectx.Context, error)
	RunGuarded           func(context.Context, *runtimectx.Context, execution.GuardedExecutionRequest) (execution.GuardedExecutionResult, error)
	ForwardedOriginToken string
}

type triggerableSnapshotCache interface {
	RefreshWithTrigger(context.Context, string) error
}

func RegisterRoutes(mux *http.ServeMux, cache SnapshotCache) {
	RegisterRoutesWithDeps(mux, cache, RouteDeps{
		LoadRuntime: runtimectx.Load,
		RunGuarded:  execution.RunGuarded,
	})
}

func RegisterRoutesWithDeps(mux *http.ServeMux, cache SnapshotCache, deps RouteDeps) {
	if deps.LoadRuntime == nil {
		deps.LoadRuntime = runtimectx.Load
	}
	if deps.RunGuarded == nil {
		deps.RunGuarded = execution.RunGuarded
	}

	mux.HandleFunc("/health", healthHandler(cache))
	mux.HandleFunc("/healthz", permanentRedirect("/health"))

	mux.HandleFunc("/snapshot", snapshotHandler(cache))
	mux.HandleFunc("/snapshot/meta", snapshotMetaHandler(cache))
	mux.HandleFunc("/invalidate", invalidateHandler(cache))
	mux.HandleFunc("/refresh", refreshHandler(cache))

	mux.HandleFunc("/tools", toolsHandler())
	mux.HandleFunc("/mcp/tools", permanentRedirect("/tools"))

	mux.HandleFunc("/knowledge", knowledgeHandler(deps))
	mux.HandleFunc("/run", runHandler(cache, deps))
}

func HealthPayload(meta *Metadata) map[string]any {
	payload := map[string]any{
		"status":  "ok",
		"name":    "axis",
		"version": Version,
	}
	if meta == nil {
		return payload
	}

	payload["cache_ready"] = meta.Ready
	payload["cache_stale"] = meta.Stale
	payload["cache_age_sec"] = meta.CacheAgeSec
	payload["refresh_count"] = meta.RefreshCount
	if meta.LastRefreshTrigger != "" {
		payload["last_refresh_trigger"] = meta.LastRefreshTrigger
	}
	if meta.LastRefreshMs > 0 {
		payload["last_refresh_duration_ms"] = meta.LastRefreshMs
	}
	if !meta.LastConfigEventAt.IsZero() {
		payload["last_config_event_at"] = meta.LastConfigEventAt
	}
	if len(meta.StaleNodes) > 0 {
		payload["stale_nodes"] = meta.StaleNodes
	}
	if !meta.CollectedAt.IsZero() {
		payload["cache_collected_at"] = meta.CollectedAt
	}
	if meta.LastError != "" {
		payload["cache_last_error"] = meta.LastError
	}
	if meta.Freshness != nil {
		payload["discovery_freshness"] = meta.Freshness
	}
	return payload
}

func ToolDefinitions() []ToolDef {
	return []ToolDef{
		{
			Name:        "axis_execute",
			Description: "Execute a task on the live AXIS cluster using deterministic placement, reservation headroom, runtime pressure shielding, TurboQuant-aware injection, and the safety blocker. Explicit mode and explicit operator confirmation are required: use mode=script for matched scripts/skills only or mode=exec for explicit raw commands, and set confirm=YES to authorize execution.",
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
			Description: "Return live cluster state, learned skills, and recent placement context.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

func healthHandler(cache SnapshotCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var meta *Metadata
		if cache != nil {
			m := cache.Meta()
			meta = &m
		}
		writeJSON(w, http.StatusOK, HealthPayload(meta))
	}
}

func toolsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, ToolsResponse{Tools: ToolDefinitions()})
	}
}

func snapshotHandler(cache SnapshotCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
	}
}

func snapshotMetaHandler(cache SnapshotCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if cache == nil {
			writeError(w, http.StatusServiceUnavailable, "snapshot cache unavailable")
			return
		}
		writeJSON(w, http.StatusOK, cache.Meta())
	}
}

func invalidateHandler(cache SnapshotCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
	}
}

func refreshHandler(cache SnapshotCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if cache == nil {
			writeError(w, http.StatusServiceUnavailable, "snapshot cache unavailable")
			return
		}
		trigger, err := NormalizeRefreshTrigger(r.URL.Query().Get("trigger"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := refreshCache(cache, r.Context(), trigger); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func knowledgeHandler(deps RouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		rt, err := deps.LoadRuntime(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		skillStore := rt.Skills
		if skillStore == nil {
			skillStore = &skills.Store{}
		}

		payload := KnowledgeResponse{
			Knowledge: knowledge.Build(rt.Snapshot, rt.State, ""),
			Skills:    skillStore.Skills,
			Failures:  skillStore.Failures,
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func runHandler(cache SnapshotCache, deps RouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req execution.GuardedExecutionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		execution.NormalizeRequest(&req)
		if err := execution.ValidateRequest(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		runCtx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()

		forwardedOrigin, hasForwardedOrigin, err := auth.ForwardedExecutionOriginFromRequest(r, deps.ForwardedOriginToken, time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		req.OnStateChange = func(_ context.Context, trigger string, _ execution.GuardedExecutionResult) {
			scheduleCacheRefresh(cache, trigger)
		}
		req.OwnerSurface = execution.OwnerSurfaceHTTPRun
		req.OwnerLabel = requestCallerLabel(r)
		if hasForwardedOrigin {
			req.OriginOverride = forwardedOrigin
		}

		emitResult, streamed, err := WireRunStreamResponse(w, r, &req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		rt, err := deps.LoadRuntime(runCtx)
		if err != nil {
			resp := execution.GuardedExecutionResult{
				OK:          false,
				Description: req.Description,
				Mode:        req.Mode,
				Error:       err.Error(),
			}
			if streamed {
				_ = emitResult(resp)
				return
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}

		resp, runErr := deps.RunGuarded(runCtx, rt, req)
		resp = NormalizeRunResult(resp, runErr)
		if streamed {
			_ = emitResult(resp)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func refreshCache(cache SnapshotCache, ctx context.Context, trigger string) error {
	if cache == nil {
		return nil
	}
	if triggered, ok := any(cache).(triggerableSnapshotCache); ok {
		return triggered.RefreshWithTrigger(ctx, trigger)
	}
	return cache.RefreshNow(ctx)
}

func scheduleCacheRefresh(cache SnapshotCache, trigger string) {
	if cache == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runtimeRefreshTimeout)
		defer cancel()
		if err := refreshCache(cache, ctx, trigger); err != nil {
			reportAsyncRefreshError(trigger, err)
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

func permanentRedirect(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	}
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
