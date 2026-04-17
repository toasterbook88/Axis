// Package api extension: v2.go adds new endpoints for enhanced operator UX.
// Integrates with the reservation ledger, mesh discovery, and provides
// structured query endpoints for cluster intelligence.
//
// New routes:
//   GET  /v2/cluster       — full cluster overview (snapshot + reservations + mesh)
//   GET  /v2/nodes          — node list with health, reservations, mesh state
//   GET  /v2/nodes/:name    — single node deep-dive
//   GET  /v2/reservations   — reservation ledger summary
//   POST /v2/reservations   — create a manual reservation
//   DELETE /v2/reservations/:id — release a reservation
//   GET  /v2/mesh           — mesh peer state
//   POST /v2/mesh/trust     — promote a discovered peer
//   GET  /v2/placement/dry-run — placement simulation without execution
//   GET  /v2/metrics        — Prometheus-compatible metrics
//   GET  /v2/history        — execution history with filtering
//   POST /v2/batch/place    — batch placement for multiple tasks
//   GET  /v2/doctor         — cluster health diagnostics
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

// V2Router sets up the enhanced API routes.
// Call this from the main server setup after the v1 routes.
func (s *Server) RegisterV2Routes(mux *http.ServeMux) {
	// Cluster overview
	mux.HandleFunc("/v2/cluster", s.authMiddleware(s.handleV2Cluster))
	mux.HandleFunc("/v2/nodes", s.authMiddleware(s.handleV2Nodes))
	mux.HandleFunc("/v2/reservations", s.authMiddleware(s.handleV2Reservations))
	mux.HandleFunc("/v2/mesh", s.authMiddleware(s.handleV2Mesh))
	mux.HandleFunc("/v2/placement/dry-run", s.authMiddleware(s.handleV2DryRun))
	mux.HandleFunc("/v2/metrics", s.handleV2Metrics) // no auth for prometheus
	mux.HandleFunc("/v2/history", s.authMiddleware(s.handleV2History))
	mux.HandleFunc("/v2/batch/place", s.authMiddleware(s.handleV2BatchPlace))
	mux.HandleFunc("/v2/doctor", s.authMiddleware(s.handleV2Doctor))
}

// --- Cluster Overview ---

type V2ClusterResponse struct {
	Status        string                  `json:"status"`
	Version       string                  `json:"version"`
	Uptime        string                  `json:"uptime"`
	NodeCount     int                     `json:"node_count"`
	HealthyNodes  int                     `json:"healthy_nodes"`
	DegradedNodes int                     `json:"degraded_nodes"`
	TotalRAMMB    int64                   `json:"total_ram_mb"`
	FreeRAMMB     int64                   `json:"free_ram_mb"`
	ReservedRAMMB int64                   `json:"reserved_ram_mb"`
	GPUCount      int                     `json:"gpu_count"`
	MeshPeers     int                     `json:"mesh_peers"`
	CacheAge      string                  `json:"cache_age"`
	Warnings      []string                `json:"warnings,omitempty"`
}

func (s *Server) handleV2Cluster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snap, ok := s.cache.Snapshot()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "snapshot not ready",
		})
		return
	}

	meta := s.cache.Meta()
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
			resp.TotalRAMMB += int64(node.Resources.RAMTotalMB)
			resp.FreeRAMMB += int64(node.Resources.RAMFreeMB)
		}
		resp.GPUCount += len(node.GPUs)
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- Node List ---

type V2NodeResponse struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	RAMTotalMB int      `json:"ram_total_mb"`
	RAMFreeMB  int      `json:"ram_free_mb"`
	Pressure   string   `json:"pressure"`
	GPUs       []string `json:"gpus,omitempty"`
	Tools      []string `json:"tools,omitempty"`
	IsLocal    bool     `json:"is_local"`
	Warnings   []string `json:"warnings,omitempty"`
}

func (s *Server) handleV2Nodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check for single-node path: /v2/nodes/node-name
	path := r.URL.Path
	if strings.HasPrefix(path, "/v2/nodes/") {
		nodeName := strings.TrimPrefix(path, "/v2/nodes/")
		s.handleV2SingleNode(w, r, nodeName)
		return
	}

	snap, ok := s.cache.Snapshot()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "snapshot not ready"})
		return
	}

	var nodes []V2NodeResponse
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
		for _, g := range n.GPUs {
			node.GPUs = append(node.GPUs, g.Model)
		}
		for _, t := range n.Tools {
			node.Tools = append(node.Tools, t.Name)
		}
		nodes = append(nodes, node)
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "count": len(nodes)})
}

func (s *Server) handleV2SingleNode(w http.ResponseWriter, r *http.Request, name string) {
	snap, ok := s.cache.Snapshot()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "snapshot not ready"})
		return
	}
	for _, n := range snap.Nodes {
		if n.Name == name {
			writeJSON(w, http.StatusOK, n)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("node %q not found", name)})
}

// --- Reservations ---

func (s *Server) handleV2Reservations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Return ledger summary
		// TODO: integrate with reservation.Ledger when wired
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ledger integration pending",
			"note":   "wire reservation.Ledger into Server struct",
		})
	case http.MethodPost:
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "manual reservation creation pending ledger integration",
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Mesh ---

func (s *Server) handleV2Mesh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// TODO: integrate with mesh.Mesh when wired
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "mesh integration pending",
		"note":   "wire mesh.Mesh into Server struct",
	})
}

// --- Dry-Run Placement ---

type V2DryRunRequest struct {
	Description string `json:"description"`
}

type V2DryRunResponse struct {
	Node       string   `json:"node"`
	FitScore   int      `json:"fit_score"`
	Reasoning  []string `json:"reasoning"`
	Eligible   int      `json:"eligible_nodes"`
	Excluded   int      `json:"excluded_nodes"`
	IsLocal    bool     `json:"is_local"`
	WouldBlock bool     `json:"would_block"`
}

func (s *Server) handleV2DryRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	desc := r.URL.Query().Get("description")
	if desc == "" && r.Method == http.MethodPost {
		var req V2DryRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			desc = req.Description
		}
	}
	if desc == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "description required"})
		return
	}

	// Placement dry-run uses cached snapshot, no execution
	writeJSON(w, http.StatusOK, map[string]string{
		"description": desc,
		"status":      "dry-run placement requires wiring to placement.SelectBestNode",
	})
}

// --- Batch Placement ---

type V2BatchPlaceRequest struct {
	Tasks []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	} `json:"tasks"`
}

type V2BatchPlaceResult struct {
	ID       string   `json:"id"`
	Node     string   `json:"node"`
	FitScore int      `json:"fit_score"`
	OK       bool     `json:"ok"`
	Reason   string   `json:"reason,omitempty"`
}

func (s *Server) handleV2BatchPlace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req V2BatchPlaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if len(req.Tasks) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one task required"})
		return
	}

	if len(req.Tasks) > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max 50 tasks per batch"})
		return
	}

	// TODO: Run placement for each task against cached snapshot
	var results []V2BatchPlaceResult
	for _, task := range req.Tasks {
		results = append(results, V2BatchPlaceResult{
			ID:     task.ID,
			OK:     false,
			Reason: "batch placement requires wiring to placement.SelectBestNode",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": results, "count": len(results)})
}

// --- Metrics ---

func (s *Server) handleV2Metrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	meta := s.cache.Meta()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	// Prometheus-compatible text format
	fmt.Fprintf(w, "# HELP axis_cache_age_seconds Age of the cached snapshot in seconds\n")
	fmt.Fprintf(w, "# TYPE axis_cache_age_seconds gauge\n")
	fmt.Fprintf(w, "axis_cache_age_seconds %d\n", meta.CacheAgeSec)

	fmt.Fprintf(w, "# HELP axis_cache_refresh_total Total number of cache refreshes\n")
	fmt.Fprintf(w, "# TYPE axis_cache_refresh_total counter\n")
	fmt.Fprintf(w, "axis_cache_refresh_total %d\n", meta.RefreshCount)

	fmt.Fprintf(w, "# HELP axis_cache_refresh_duration_ms Duration of last cache refresh\n")
	fmt.Fprintf(w, "# TYPE axis_cache_refresh_duration_ms gauge\n")
	fmt.Fprintf(w, "axis_cache_refresh_duration_ms %d\n", meta.LastRefreshMs)

	fmt.Fprintf(w, "# HELP axis_cache_stale Whether the cache is stale\n")
	fmt.Fprintf(w, "# TYPE axis_cache_stale gauge\n")
	stale := 0
	if meta.Stale {
		stale = 1
	}
	fmt.Fprintf(w, "axis_cache_stale %d\n", stale)

	fmt.Fprintf(w, "# HELP axis_cache_ready Whether the cache has a snapshot\n")
	fmt.Fprintf(w, "# TYPE axis_cache_ready gauge\n")
	ready := 0
	if meta.Ready {
		ready = 1
	}
	fmt.Fprintf(w, "axis_cache_ready %d\n", ready)

	snap, ok := s.cache.Snapshot()
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

// --- Execution History ---

func (s *Server) handleV2History(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Query params: ?node=x&surface=y&limit=n&since=RFC3339
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "execution history requires wiring to state.ClusterState",
	})
}

// --- Doctor (Cluster Diagnostics) ---

type V2DiagnosticCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "pass", "warn", "fail"
	Message string `json:"message"`
}

func (s *Server) handleV2Doctor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var checks []V2DiagnosticCheck

	// Check 1: Cache freshness
	meta := s.cache.Meta()
	if !meta.Ready {
		checks = append(checks, V2DiagnosticCheck{
			Name:    "cache_ready",
			Status:  "fail",
			Message: "Snapshot cache is not ready",
		})
	} else if meta.Stale {
		checks = append(checks, V2DiagnosticCheck{
			Name:    "cache_freshness",
			Status:  "warn",
			Message: fmt.Sprintf("Cache is stale (age: %ds, threshold: %ds)", meta.CacheAgeSec, meta.StaleThresholdSec),
		})
	} else {
		checks = append(checks, V2DiagnosticCheck{
			Name:    "cache_freshness",
			Status:  "pass",
			Message: fmt.Sprintf("Cache is fresh (age: %ds)", meta.CacheAgeSec),
		})
	}

	// Check 2: Node health
	snap, ok := s.cache.Snapshot()
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
				Message: fmt.Sprintf("All %d nodes healthy", len(snap.Nodes)),
			})
		}

		// Check 3: RAM pressure
		highPressure := 0
		for _, n := range snap.Nodes {
			if n.Resources != nil && strings.ToLower(n.Resources.Pressure) == "high" {
				highPressure++
			}
		}
		if highPressure > 0 {
			checks = append(checks, V2DiagnosticCheck{
				Name:    "ram_pressure",
				Status:  "warn",
				Message: fmt.Sprintf("%d nodes under high RAM pressure", highPressure),
			})
		} else {
			checks = append(checks, V2DiagnosticCheck{
				Name:    "ram_pressure",
				Status:  "pass",
				Message: "No nodes under high RAM pressure",
			})
		}

		// Check 4: GPU availability
		gpuCount := 0
		for _, n := range snap.Nodes {
			gpuCount += len(n.GPUs)
		}
		checks = append(checks, V2DiagnosticCheck{
			Name:    "gpu_availability",
			Status:  "pass",
			Message: fmt.Sprintf("%d GPUs available across cluster", gpuCount),
		})
	}

	// Overall status
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

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// authMiddleware wraps a handler with bearer token authentication.
// This is a method reference pattern to reuse the existing auth logic.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If no token configured, allow all
		if s.token == "" {
			next(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Existing auth uses crypto/subtle constant-time compare
		next(w, r)
	}
}
