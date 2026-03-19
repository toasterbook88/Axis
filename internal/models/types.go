// Package models defines core AXIS data types.
// All types are internal — no public API surface in Phase 1.
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

// --- Observed State ---

// Resources holds observed hardware resource metrics.
type Resources struct {
	CPUCores    int      `json:"cpu_cores" yaml:"cpu_cores"`
	CPUModel    string   `json:"cpu_model" yaml:"cpu_model"`
	RAMTotalMB  int64    `json:"ram_total_mb" yaml:"ram_total_mb"`
	RAMFreeMB   int64    `json:"ram_free_mb" yaml:"ram_free_mb"`
	DiskTotalGB int64    `json:"disk_total_gb" yaml:"disk_total_gb"`
	DiskFreeGB  int64    `json:"disk_free_gb" yaml:"disk_free_gb"`
	GPUs        []string `json:"gpus,omitempty" yaml:"gpus,omitempty"`
	Pressure    string   `json:"pressure" yaml:"pressure"` // none, low, medium, high
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

// --- Node Result ---

// NodeFacts holds combined observed and assigned state for a node.
// Assigned: Name, Role (from config). Observed: everything else.
// Does NOT include ssh_user or any transport-specific fields.
type NodeFacts struct {
	// Assigned state (from config)
	Name string `json:"name" yaml:"name"`
	Role string `json:"role,omitempty" yaml:"role,omitempty"`

	// Observed state
	Hostname  string           `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	OS        string           `json:"os,omitempty" yaml:"os,omitempty"`                 // darwin, linux
	OSVersion string           `json:"os_version,omitempty" yaml:"os_version,omitempty"` // e.g. 26.4, 6.1.0
	Arch      string           `json:"arch,omitempty" yaml:"arch,omitempty"`
	Resources *Resources       `json:"resources,omitempty" yaml:"resources,omitempty"`
	Addresses []NetworkAddress `json:"addresses,omitempty" yaml:"addresses,omitempty"`
	Tools     []ToolInfo       `json:"tools,omitempty" yaml:"tools,omitempty"`

	// Result metadata
	Status      NodeStatus `json:"status" yaml:"status"`
	Error       string     `json:"error,omitempty" yaml:"error,omitempty"`
	CollectedAt time.Time  `json:"collected_at" yaml:"collected_at"`
}

// --- Derived State (Snapshot) ---

// ClusterSummary holds cluster-level aggregates derived from node facts.
type ClusterSummary struct {
	TotalNodes     int   `json:"total_nodes" yaml:"total_nodes"`
	ReachableNodes int   `json:"reachable_nodes" yaml:"reachable_nodes"`
	TotalRAMMB     int64 `json:"total_ram_mb" yaml:"total_ram_mb"`
	TotalFreeRAMMB int64 `json:"total_free_ram_mb" yaml:"total_free_ram_mb"`
}

// Warning represents a specific issue detected during snapshot assembly.
type Warning struct {
	Node    string `json:"node" yaml:"node"`
	Kind    string `json:"kind" yaml:"kind"`       // unreachable, partial, ram_pressure, error
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
