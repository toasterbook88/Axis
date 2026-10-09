package models

import "time"

// ResidentModel is additive truth-plane metadata describing a model that is
// currently resident in a node runtime according to a live probe.
//
// ExpiresAt and WarmthScore are populated from Ollama's /api/ps
// `expires_at` and `default_keep_alive` fields (Ollama 0.3.10+). They are
// optional: when absent (older Ollama, no `keep_alive`, or other runtimes
// such as llama-server / mlx_lm.server), both fields remain zero and
// WarmthScore is treated as cold. The fields are advisory metadata only;
// placement uses them as a bounded tiebreaker, never as a primary signal
// (see internal/placement/ranker.go modelWarmthRank).
type ResidentModel struct {
	Name         string `json:"name" yaml:"name"`
	Runtime      string `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Processor    string `json:"processor,omitempty" yaml:"processor,omitempty"`
	Source       string `json:"source,omitempty" yaml:"source,omitempty"`
	Port         int    `json:"port,omitempty" yaml:"port,omitempty"`                     // Port the runtime is listening on (if applicable)
	WeightSizeMB int64  `json:"weight_size_mb,omitempty" yaml:"weight_size_mb,omitempty"` // On-disk model weights; not a memory measurement
	SizeRAMMB    int64  `json:"size_ram_mb,omitempty" yaml:"size_ram_mb,omitempty"`       // Resident process RAM reported by the runtime or OS
	SizeVRAMMB   int64  `json:"size_vram_mb,omitempty" yaml:"size_vram_mb,omitempty"`     // Resident accelerator memory reported by the runtime
	PID          int    `json:"pid,omitempty" yaml:"pid,omitempty"`                       // Observed process ID; not sufficient alone to identify a generation
	Executable   string `json:"executable,omitempty" yaml:"executable,omitempty"`         // Observed executable path
	ProcessOwner string `json:"process_owner,omitempty" yaml:"process_owner,omitempty"`   // Observed operating-system account
	// ProcessStartToken is the platform's normalized process-start observation.
	// It is intentionally opaque; paired with PID and executable it prevents PID
	// reuse from masquerading as the same process generation.
	ProcessStartToken string `json:"process_start_token,omitempty" yaml:"process_start_token,omitempty"`

	// ExpiresAt is the wall-clock time at which the model is expected to
	// be unloaded by the runtime. Zero when unknown.
	ExpiresAt time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`

	// WarmthScore is a continuous 0.0–1.0 measure of how recently the
	// model was loaded, derived from ExpiresAt and the runtime's
	// default_keep_alive. 1.0 = freshly loaded, 0.0 = expired or unknown.
	// Always non-negative; clamped to [0, 1] at compute time.
	WarmthScore float64 `json:"warmth_score,omitempty" yaml:"warmth_score,omitempty"`

	// SupervisorType is the process supervisor managing the instance ("systemd-user",
	// "systemd-system", or "none" / empty when unmanaged).
	SupervisorType string `json:"supervisor_type,omitempty" yaml:"supervisor_type,omitempty"`
	// SupervisorUnit is the service unit name (e.g. "bonsai2-27b.service").
	SupervisorUnit string `json:"supervisor_unit,omitempty" yaml:"supervisor_unit,omitempty"`
	// GPUIndices records physical GPU device indices occupied by the model.
	GPUIndices []int `json:"gpu_indices,omitempty" yaml:"gpu_indices,omitempty"`

	// State is the load state the collector observed, from a runtime load
	// signal only (see LoadSignal). Empty means the collector did not
	// determine it; Catalog then falls back to the runtime mapping.
	State ModelCatalogState `json:"state,omitempty" yaml:"state,omitempty"`
	// LoadSignal names the evidence behind State, e.g. "ollama-ps",
	// "health-ok", "health-loading", "health-silent", "served-id-differs", "none".
	LoadSignal string `json:"load_signal,omitempty" yaml:"load_signal,omitempty"`
	// ContextWindow is the served context in tokens, only when the runtime
	// states it (llama.cpp /props n_ctx). Never training context.
	ContextWindow int `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	// Provenance maps a field name to the probe that produced it.
	Provenance map[string]string `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}
