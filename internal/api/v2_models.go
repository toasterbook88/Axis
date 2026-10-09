package api

import (
	"net/http"

	"github.com/toasterbook88/axis/internal/modelinventory"
)

// registerV2Routes mounts the v2 API: the core cluster routes plus the model
// catalog.
func registerV2Routes(mux *http.ServeMux, cache snapshotCache, token string) {
	registerV2CoreRoutes(mux, cache, token)
	h := &v2Handlers{cache: cache}
	mux.HandleFunc("/v2/models", withAuth(h.handleModels, token))
}

// handleModels serves GET /v2/models: the model catalog derived from the
// daemon's cached snapshot, the same document `axis model catalog` prints.
func (h *v2Handlers) handleModels(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, modelinventory.Catalog(snap, "daemon-cache"))
}
