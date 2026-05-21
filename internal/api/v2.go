package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/mesh"
	"github.com/toasterbook88/axis/internal/models"
)

func registerV2Routes(mux *http.ServeMux, cache snapshotCache, token string) {
	h := &v2Handlers{cache: cache}

	mux.HandleFunc("/v2/cluster", withAuth(h.handleCluster, token))
	mux.HandleFunc("/v2/nodes", withAuth(h.handleNodes, token))
	mux.HandleFunc("/v2/nodes/", withAuth(h.handleNodes, token))
	mux.HandleFunc("/v2/reservations", withAuth(h.handleReservations, token))
	mux.HandleFunc("/v2/mesh", withAuth(h.handleMesh, token))
	mux.HandleFunc("/v2/placement/dry-run", withAuth(h.handleDryRun, token))
	mux.HandleFunc("/v2/metrics", h.handleMetrics)
	mux.HandleFunc("/v2/history", withAuth(h.handleHistory, token))
	mux.HandleFunc("/v2/batch/place", withAuth(h.handleBatchPlace, token))
	mux.HandleFunc("/v2/doctor", withAuth(h.handleDoctor, token))
}

type v2Handlers struct {
	cache snapshotCache
}

func (h *v2Handlers) requireCache(w http.ResponseWriter) bool {
	if h.cache == nil {
		writeError(w, http.StatusServiceUnavailable, "snapshot cache unavailable")
		return false
	}
	return true
}

type V2ClusterResponse struct {
	Status        string   `json:"status"`
	Version       string   `json:"version"`
	NodeCount     int      `json:"node_count"`
	HealthyNodes  int      `json:"healthy_nodes"`
	DegradedNodes int      `json:"degraded_nodes"`
	TotalRAMMB    int64    `json:"total_ram_mb"`
	FreeRAMMB     int64    `json:"free_ram_mb"`
	GPUCount      int      `json:"gpu_count"`
	CacheAge      string   `json:"cache_age"`
	Warnings      []string `json:"warnings,omitempty"`
}

func (h *v2Handlers) handleCluster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireCache(w) {
		return
	}

	snap, ok := h.cache.Snapshot()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "snapshot cache not ready")
		return
	}

	meta := h.cache.Meta()
	resp := V2ClusterResponse{
		Status:    string(snap.Status),
		Version:   meta.Version,
		NodeCount: len(snap.Nodes),
		CacheAge:  fmt.Sprintf("%ds", meta.CacheAgeSec),
	}

	for _, node := range snap.Nodes {
		if node.Status == "complete" {
			resp.HealthyNodes++
		} else {
			resp.DegradedNodes++
		}
		if node.Resources != nil {
			resp.TotalRAMMB += node.Resources.RAMTotalMB
			resp.FreeRAMMB += node.Resources.RAMFreeMB
			resp.GPUCount += len(node.Resources.GPUs)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

type V2NodeResponse struct {
	Name       string                 `json:"name"`
	Status     string                 `json:"status"`
	OS         string                 `json:"os"`
	Arch       string                 `json:"arch"`
	RAMTotalMB int64                  `json:"ram_total_mb"`
	RAMFreeMB  int64                  `json:"ram_free_mb"`
	Pressure   string                 `json:"pressure"`
	GPUs       []string               `json:"gpus,omitempty"`
	Tools      []string               `json:"tools,omitempty"`
	Epistemic  *models.EpistemicState `json:"epistemic,omitempty"`
}

func (h *v2Handlers) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireCache(w) {
		return
	}

	if strings.HasPrefix(r.URL.Path, "/v2/nodes/") {
		nodeName := strings.TrimPrefix(r.URL.Path, "/v2/nodes/")
		h.handleSingleNode(w, nodeName)
		return
	}

	snap, ok := h.cache.Snapshot()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "snapshot cache not ready")
		return
	}

	nodes := make([]V2NodeResponse, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		node := V2NodeResponse{
			Name:   n.Name,
			Status: string(n.Status),
			OS:     n.OS,
			Arch:   n.Arch,
		}
		if n.Resources != nil {
			node.RAMTotalMB = n.Resources.RAMTotalMB
			node.RAMFreeMB = n.Resources.RAMFreeMB
			node.Pressure = n.Resources.Pressure
		}
		if n.Resources != nil {
			for _, g := range n.Resources.GPUs {
				node.GPUs = append(node.GPUs, g.Model)
			}
		}
		for _, t := range n.Tools {
			node.Tools = append(node.Tools, t.Name)
		}
		node.Epistemic = n.Epistemic
		nodes = append(nodes, node)
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "count": len(nodes)})
}

func (h *v2Handlers) handleSingleNode(w http.ResponseWriter, name string) {
	if name == "" {
		writeError(w, http.StatusNotFound, "node not specified")
		return
	}
	snap, ok := h.cache.Snapshot()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "snapshot cache not ready")
		return
	}
	for _, n := range snap.Nodes {
		if n.Name == name {
			writeJSON(w, http.StatusOK, n)
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Sprintf("node %q not found", name))
}

func (h *v2Handlers) handleReservations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.requireCache(w) {
			return
		}
		ledger := h.cache.Ledger()
		if ledger == nil {
			writeError(w, http.StatusServiceUnavailable, "ledger not available")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"cluster":      ledger.Summary(),
			"reservations": ledger.Entries(),
		})
	case http.MethodPost:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *v2Handlers) handleMesh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireCache(w) {
		return
	}
	m := h.cache.Mesh()
	if m == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh not available")
		return
	}
	peers := m.ActivePeers()
	if peers == nil {
		peers = []mesh.Peer{} // Ensure we output an empty array instead of null
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"peers": peers,
		"count": len(peers),
	})
}

type V2DryRunRequest struct {
	Description string `json:"description"`
}

func (h *v2Handlers) handleDryRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	desc := r.URL.Query().Get("description")
	if desc == "" && r.Method == http.MethodPost {
		var req V2DryRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			desc = req.Description
		}
	}
	desc = strings.TrimSpace(desc)
	if desc == "" {
		writeError(w, http.StatusBadRequest, "description required")
		return
	}
	writeError(w, http.StatusNotImplemented, "dry-run placement wiring pending")
}

type V2BatchPlaceRequest struct {
	Tasks []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	} `json:"tasks"`
}

type V2BatchPlaceResult struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

func (h *v2Handlers) handleBatchPlace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeError(w, http.StatusNotImplemented, "batch placement is not implemented")
}

func (h *v2Handlers) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireCache(w) {
		return
	}

	meta := h.cache.Meta()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "# HELP axis_cache_age_seconds Age of the cached snapshot in seconds\n")
	fmt.Fprintf(w, "# TYPE axis_cache_age_seconds gauge\n")
	fmt.Fprintf(w, "axis_cache_age_seconds %d\n", meta.CacheAgeSec)
	fmt.Fprintf(w, "# HELP axis_cache_refresh_total Total number of cache refreshes\n")
	fmt.Fprintf(w, "# TYPE axis_cache_refresh_total counter\n")
	fmt.Fprintf(w, "axis_cache_refresh_total %d\n", meta.RefreshCount)
	fmt.Fprintf(w, "# HELP axis_cache_refresh_duration_ms Duration of last cache refresh\n")
	fmt.Fprintf(w, "# TYPE axis_cache_refresh_duration_ms gauge\n")
	fmt.Fprintf(w, "axis_cache_refresh_duration_ms %d\n", meta.LastRefreshMs)
	fmt.Fprintf(w, "# HELP axis_cache_max_refresh_latency_ms Maximum queue/execution starvation latency of cache refresh\n")
	fmt.Fprintf(w, "# TYPE axis_cache_max_refresh_latency_ms gauge\n")
	fmt.Fprintf(w, "axis_cache_max_refresh_latency_ms %d\n", meta.MaxRefreshLatencyMs)
	fmt.Fprintf(w, "# HELP axis_cache_stale Whether the cache is stale\n")
	fmt.Fprintf(w, "# TYPE axis_cache_stale gauge\n")
	if meta.Stale {
		fmt.Fprintln(w, "axis_cache_stale 1")
	} else {
		fmt.Fprintln(w, "axis_cache_stale 0")
	}
	fmt.Fprintf(w, "# HELP axis_cache_ready Whether the cache has a snapshot\n")
	fmt.Fprintf(w, "# TYPE axis_cache_ready gauge\n")
	if meta.Ready {
		fmt.Fprintln(w, "axis_cache_ready 1")
	} else {
		fmt.Fprintln(w, "axis_cache_ready 0")
	}

	snap, ok := h.cache.Snapshot()
	if ok {
		fmt.Fprintf(w, "# HELP axis_nodes_total Total nodes in cluster\n")
		fmt.Fprintf(w, "# TYPE axis_nodes_total gauge\n")
		fmt.Fprintf(w, "axis_nodes_total %d\n", len(snap.Nodes))

		healthy := 0
		for _, n := range snap.Nodes {
			if n.Status == "complete" {
				healthy++
			}
		}
		fmt.Fprintf(w, "# HELP axis_nodes_healthy Healthy nodes in cluster\n")
		fmt.Fprintf(w, "# TYPE axis_nodes_healthy gauge\n")
		fmt.Fprintf(w, "axis_nodes_healthy %d\n", healthy)
	}
}

func (h *v2Handlers) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeError(w, http.StatusNotImplemented, "execution history wiring pending")
}

type V2DiagnosticCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (h *v2Handlers) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireCache(w) {
		return
	}

	var checks []V2DiagnosticCheck
	meta := h.cache.Meta()
	if !meta.Ready {
		checks = append(checks, V2DiagnosticCheck{Name: "cache_ready", Status: "fail", Message: "snapshot cache is not ready"})
	} else if meta.Stale {
		checks = append(checks, V2DiagnosticCheck{
			Name:    "cache_freshness",
			Status:  "warn",
			Message: fmt.Sprintf("cache is stale (age: %ds, threshold: %ds)", meta.CacheAgeSec, meta.StaleThresholdSec),
		})
	} else {
		checks = append(checks, V2DiagnosticCheck{
			Name:    "cache_freshness",
			Status:  "pass",
			Message: fmt.Sprintf("cache is fresh (age: %ds)", meta.CacheAgeSec),
		})
	}

	snap, ok := h.cache.Snapshot()
	if ok {
		degraded := 0
		for _, n := range snap.Nodes {
			if n.Status != "complete" {
				degraded++
			}
		}
		if degraded > 0 {
			checks = append(checks, V2DiagnosticCheck{
				Name:    "node_health",
				Status:  "warn",
				Message: fmt.Sprintf("%d of %d nodes are degraded", degraded, len(snap.Nodes)),
			})
		} else {
			checks = append(checks, V2DiagnosticCheck{
				Name:    "node_health",
				Status:  "pass",
				Message: fmt.Sprintf("all %d nodes healthy", len(snap.Nodes)),
			})
		}
	}

	overall := "healthy"
	for _, c := range checks {
		if c.Status == "fail" {
			overall = "unhealthy"
			break
		}
		if c.Status == "warn" {
			overall = "degraded"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"overall":    overall,
		"checks":     checks,
		"checked_at": time.Now().UTC().Format(time.RFC3339),
	})
}
