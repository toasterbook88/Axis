// Package models defines core AXIS data types.
// All types are internal — there is no stable public API surface.
package models

import "time"

// --- Enums ---

// NodeStatus classifies the result of fact collection for a node.
//   - complete:    all facts collected successfully
//   - partial:     node reachable but some facts failed to collect
//   - unreachable: SSH/connect failure — no facts collected
//   - error:       internal parsing or collector failure
type NodeStatus string

const (
	StatusComplete    NodeStatus = "complete"
	StatusPartial     NodeStatus = "partial"
	StatusUnreachable NodeStatus = "unreachable"
	StatusError       NodeStatus = "error"
)

// SnapshotStatus classifies overall cluster health.
// Any node with status != complete causes degraded.
type SnapshotStatus string

const (
	SnapshotHealthy  SnapshotStatus = "healthy"
	SnapshotDegraded SnapshotStatus = "degraded"
)

// ToolClass constrains tool classification to known categories.
type ToolClass string

const (
	ToolClassAICLI     ToolClass = "ai-cli"
	ToolClassBuild     ToolClass = "build"
	ToolClassVCS       ToolClass = "vcs"
	ToolClassContainer ToolClass = "container"
	ToolClassRuntime   ToolClass = "runtime"
)

// MemoryTopology classifies the relationship between CPU/GPU accessible memory.
type MemoryTopology string

const (
	MemoryTopologyStandard MemoryTopology = "standard"
	MemoryTopologyUnified  MemoryTopology = "unified"
)

// --- Observed State ---

// Resources holds observed hardware resource metrics.
type Resources struct {
	CPUCores         int            `json:"cpu_cores" yaml:"cpu_cores"`
	CPUModel         string         `json:"cpu_model" yaml:"cpu_model"`
	RAMTotalMB       int64          `json:"ram_total_mb" yaml:"ram_total_mb"`
	RAMFreeMB        int64          `json:"ram_free_mb" yaml:"ram_free_mb"`
	MemoryTopology   MemoryTopology `json:"memory_topology,omitempty" yaml:"memory_topology,omitempty"`
	MemoryClass      int            `json:"memory_class,omitempty" yaml:"memory_class,omitempty"`
	Load1M           float64        `json:"load_1m" yaml:"load_1m"`
	Load5M           float64        `json:"load_5m" yaml:"load_5m"`
	Load15M          float64        `json:"load_15m" yaml:"load_15m"`
	RAMReservedMB    int64          `json:"ram_reserved_mb,omitempty" yaml:"ram_reserved_mb,omitempty"`
	RAMAllocatableMB int64          `json:"ram_allocatable_mb,omitempty" yaml:"ram_allocatable_mb,omitempty"`
	DiskTotalGB      int64          `json:"disk_total_gb" yaml:"disk_total_gb"`
	DiskFreeGB       int64          `json:"disk_free_gb" yaml:"disk_free_gb"`
	GPUs             []string       `json:"gpus,omitempty" yaml:"gpus,omitempty"`
	Pressure         string         `json:"pressure" yaml:"pressure"` // none, low, medium, high
	PressureStall10  float64        `json:"pressure_stall_10,omitempty" yaml:"pressure_stall_10,omitempty"`
	PressureSource   string         `json:"pressure_source,omitempty" yaml:"pressure_source,omitempty"`
}

// NetworkAddress represents a single network address.
// Kind is one of: ipv4, ipv6, hostname.
// No transport-specific labels (LAN/Thunderbolt/Tailscale) in core schema.
type NetworkAddress struct {
	Kind    string `json:"kind" yaml:"kind"`
	Address string `json:"address" yaml:"address"`
}

// ToolInfo describes a discovered tool on a node.
type ToolInfo struct {
	Name    string    `json:"name" yaml:"name"`
	Path    string    `json:"path" yaml:"path"`
	Version string    `json:"version,omitempty" yaml:"version,omitempty"`
	Class   ToolClass `json:"class" yaml:"class"`
}

// OllamaInfo is collected in addition to the normal ToolInfo for "ollama".
// This is what makes discovery actually useful for placement and task run.
type OllamaInfo struct {
	Installed  bool     `json:"installed" yaml:"installed"`
	Path       string   `json:"path,omitempty" yaml:"path,omitempty"`
	Version    string   `json:"version,omitempty" yaml:"version,omitempty"`
	Running    bool     `json:"running" yaml:"running"`
	Listening  bool     `json:"listening" yaml:"listening"`
	Port       int      `json:"port,omitempty" yaml:"port,omitempty"`
	Models     []string `json:"models,omitempty" yaml:"models,omitempty"`
	GPUOffload string   `json:"gpu_offload,omitempty" yaml:"gpu_offload,omitempty"`
	Error      string   `json:"error,omitempty" yaml:"error,omitempty"`
}

// TurboQuantInfo records whether a node appears able to run a TurboQuant-like
// long-context backend. This is additive advisory metadata only.
type TurboQuantInfo struct {
	Supported    bool     `json:"supported" yaml:"supported"`
	Verified     bool     `json:"verified,omitempty" yaml:"verified,omitempty"`
	Backends     []string `json:"backends,omitempty" yaml:"backends,omitempty"`
	Capabilities []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

// --- Node Result ---

// NodeFacts holds combined observed and assigned state for a node.
// Assigned: Name, Role (from config). Observed: everything else.
// Does NOT include ssh_user or any transport-specific fields.
type NodeFacts struct {
	// Assigned state (from config)
	Name string `json:"name" yaml:"name"`
	Role string `json:"role,omitempty" yaml:"role,omitempty"`

	// Observed state
	Hostname   string           `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	OS         string           `json:"os,omitempty" yaml:"os,omitempty"`                 // darwin, linux
	OSVersion  string           `json:"os_version,omitempty" yaml:"os_version,omitempty"` // e.g. 26.4, 6.1.0
	Arch       string           `json:"arch,omitempty" yaml:"arch,omitempty"`
	Resources  *Resources       `json:"resources,omitempty" yaml:"resources,omitempty"`
	Addresses  []NetworkAddress `json:"addresses,omitempty" yaml:"addresses,omitempty"`
	Tools      []ToolInfo       `json:"tools,omitempty" yaml:"tools,omitempty"`
	Ollama     *OllamaInfo      `json:"ollama,omitempty" yaml:"ollama,omitempty"`
	TurboQuant *TurboQuantInfo  `json:"turboquant,omitempty" yaml:"turboquant,omitempty"`

	// Result metadata
	Status      NodeStatus `json:"status" yaml:"status"`
	Error       string     `json:"error,omitempty" yaml:"error,omitempty"`
	CollectedAt time.Time  `json:"collected_at" yaml:"collected_at"`
}

// --- Derived State (Snapshot) ---

// ClusterSummary holds cluster-level aggregates derived from node facts.
type ClusterSummary struct {
	TotalNodes         int   `json:"total_nodes" yaml:"total_nodes"`
	ReachableNodes     int   `json:"reachable_nodes" yaml:"reachable_nodes"`
	TotalRAMMB         int64 `json:"total_ram_mb" yaml:"total_ram_mb"`
	TotalFreeRAMMB     int64 `json:"total_free_ram_mb" yaml:"total_free_ram_mb"`
	TotalAllocatableMB int64 `json:"total_allocatable_mb,omitempty" yaml:"total_allocatable_mb,omitempty"`
	TotalReservedMB    int64 `json:"total_reserved_mb,omitempty" yaml:"total_reserved_mb,omitempty"`
}

// Warning represents a specific issue detected during snapshot assembly.
type Warning struct {
	Node    string `json:"node" yaml:"node"`
	Kind    string `json:"kind" yaml:"kind"` // unreachable, partial, ram_pressure, error
	Message string `json:"message" yaml:"message"`
}

// ClusterSnapshot is the principal output of AXIS: a compact structured
// summary of cluster state for consumption by frontier models and operators.
type ClusterSnapshot struct {
	Timestamp time.Time      `json:"timestamp" yaml:"timestamp"`
	Status    SnapshotStatus `json:"status" yaml:"status"`
	Nodes     []NodeFacts    `json:"nodes" yaml:"nodes"`
	Summary   ClusterSummary `json:"summary" yaml:"summary"`
	Warnings  []Warning      `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// --- Phase 2: Task Placement ---

// TaskRequirements describes what a task needs to run.
// Inferred from task description by keyword matching in the CLI layer.
type TaskRequirements struct {
	Description         string   `json:"description" yaml:"description"`
	RequiredTools       []string `json:"required_tools,omitempty" yaml:"required_tools,omitempty"`
	MinFreeRAMMB        int64    `json:"min_free_ram_mb,omitempty" yaml:"min_free_ram_mb,omitempty"`
	ContextWindowTokens int      `json:"context_window_tokens,omitempty" yaml:"context_window_tokens,omitempty"`
	PrefersTurboQuant   bool     `json:"prefers_turboquant,omitempty" yaml:"prefers_turboquant,omitempty"`
	PreferredBackends   []string `json:"preferred_backends,omitempty" yaml:"preferred_backends,omitempty"`
}

// PlacementDecision is the output of the placement engine.
// OK is false when no node qualifies.
type PlacementDecision struct {
	Node      string   `json:"node" yaml:"node"`
	Tool      string   `json:"tool,omitempty" yaml:"tool,omitempty"`
	FitScore  int      `json:"fit_score" yaml:"fit_score"`
	IsLocal   bool     `json:"is_local" yaml:"is_local"`
	Reasoning []string `json:"reasoning" yaml:"reasoning"`
	OK        bool     `json:"ok" yaml:"ok"`
}

// PlacementError describes why placement failed.
type PlacementError struct {
	Message string `json:"message" yaml:"message"`
}

func (e *PlacementError) Error() string {
	return e.Message
}
