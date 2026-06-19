// Package modelplan builds deterministic, snapshot-backed distributed model
// placement plans. It is advisory only: it never launches runtimes, mutates
// reservations, or treats unmeasured topology as cluster truth.
package modelplan

import "time"

// PlanStatus reports the confidence level of a completed plan.
type PlanStatus string

const (
	// PlanComplete means the plan was built from a healthy snapshot and fresh,
	// measured topology for every inter-node edge.
	PlanComplete PlanStatus = "complete"
	// PlanPartial means a usable plan was built from a degraded snapshot. Only
	// complete, placement-eligible nodes are included in the plan.
	PlanPartial PlanStatus = "partial"
)

// Strategy identifies the runtime execution shape described by a plan.
type Strategy string

const (
	StrategySingleNode Strategy = "single-node"
	StrategyPipeline   Strategy = "pipeline"
)

// LinkObservation is a directional, measured route between two nodes.
// Bandwidth and latency are route properties, not node properties.
type LinkObservation struct {
	SourceNode      string    `json:"source_node" yaml:"source_node"`
	DestinationNode string    `json:"destination_node" yaml:"destination_node"`
	Interconnect    string    `json:"interconnect,omitempty" yaml:"interconnect,omitempty"`
	BandwidthMBps   float64   `json:"bandwidth_mbps" yaml:"bandwidth_mbps"`
	LatencyP50MS    float64   `json:"latency_p50_ms,omitempty" yaml:"latency_p50_ms,omitempty"`
	LatencyP95MS    float64   `json:"latency_p95_ms" yaml:"latency_p95_ms"`
	MTU             int       `json:"mtu,omitempty" yaml:"mtu,omitempty"`
	RDMA            bool      `json:"rdma,omitempty" yaml:"rdma,omitempty"`
	Confidence      float64   `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	Source          string    `json:"source" yaml:"source"`
	MeasuredAt      time.Time `json:"measured_at" yaml:"measured_at"`
	ExpiresAt       time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
}

// ModelManifest describes the memory shape and runtime requirements used by
// the planner. LayerMemoryMB is preferred because transformer layers are not
// always uniform; DefaultLayerMemoryMB is an explicit fallback for manifests
// that only know a uniform per-layer estimate.
type ModelManifest struct {
	Name                         string   `json:"name" yaml:"name"`
	Runtime                      string   `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Quantization                 string   `json:"quantization,omitempty" yaml:"quantization,omitempty"`
	RequiredTools                []string `json:"required_tools,omitempty" yaml:"required_tools,omitempty"`
	TotalLayers                  int      `json:"total_layers" yaml:"total_layers"`
	LayerMemoryMB                []int64  `json:"layer_memory_mb,omitempty" yaml:"layer_memory_mb,omitempty"`
	DefaultLayerMemoryMB         int64    `json:"default_layer_memory_mb,omitempty" yaml:"default_layer_memory_mb,omitempty"`
	PerNodeOverheadMB            int64    `json:"per_node_overhead_mb,omitempty" yaml:"per_node_overhead_mb,omitempty"`
	KVCacheBytesPerTokenPerLayer int64    `json:"kv_cache_bytes_per_token_per_layer,omitempty" yaml:"kv_cache_bytes_per_token_per_layer,omitempty"`
	ContextWindowTokens          int      `json:"context_window_tokens,omitempty" yaml:"context_window_tokens,omitempty"`
	Concurrency                  int      `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
}

// PlanOptions controls hard planning constraints. Zero values select safe
// defaults except for the bandwidth and latency thresholds, where zero means
// no additional operator constraint beyond requiring a fresh measurement.
type PlanOptions struct {
	MaxNodes         int     `json:"max_nodes,omitempty" yaml:"max_nodes,omitempty"`
	MinBandwidthMBps float64 `json:"min_bandwidth_mbps,omitempty" yaml:"min_bandwidth_mbps,omitempty"`
	MaxLatencyP95MS  float64 `json:"max_latency_p95_ms,omitempty" yaml:"max_latency_p95_ms,omitempty"`
	CoordinatorNode  string  `json:"coordinator_node,omitempty" yaml:"coordinator_node,omitempty"`
}

// ModelShard is a contiguous half-open layer interval [StartLayer,
// EndLayerExclusive) assigned to one node.
type ModelShard struct {
	Node              string  `json:"node" yaml:"node"`
	StartLayer        int     `json:"start_layer" yaml:"start_layer"`
	EndLayerExclusive int     `json:"end_layer_exclusive" yaml:"end_layer_exclusive"`
	LayerMemoryMB     int64   `json:"layer_memory_mb" yaml:"layer_memory_mb"`
	OverheadMB        int64   `json:"overhead_mb" yaml:"overhead_mb"`
	RequiredMemoryMB  int64   `json:"required_memory_mb" yaml:"required_memory_mb"`
	AllocatableMB     int64   `json:"allocatable_mb" yaml:"allocatable_mb"`
	Utilization       float64 `json:"utilization" yaml:"utilization"`
}

// PlanLink records the fresh topology observation selected for one adjacent
// pipeline hop.
type PlanLink struct {
	SourceNode      string    `json:"source_node" yaml:"source_node"`
	DestinationNode string    `json:"destination_node" yaml:"destination_node"`
	Interconnect    string    `json:"interconnect,omitempty" yaml:"interconnect,omitempty"`
	BandwidthMBps   float64   `json:"bandwidth_mbps" yaml:"bandwidth_mbps"`
	LatencyP95MS    float64   `json:"latency_p95_ms" yaml:"latency_p95_ms"`
	RDMA            bool      `json:"rdma,omitempty" yaml:"rdma,omitempty"`
	Source          string    `json:"source" yaml:"source"`
	MeasuredAt      time.Time `json:"measured_at" yaml:"measured_at"`
}

// NodeExclusion preserves placement-admission evidence for nodes omitted from
// the distributed plan.
type NodeExclusion struct {
	Node    string   `json:"node" yaml:"node"`
	Reasons []string `json:"reasons" yaml:"reasons"`
}

// DistributedPlacementPlan is a read-only execution proposal backed by one
// observed snapshot and, for multi-node plans, fresh pairwise measurements.
type DistributedPlacementPlan struct {
	Status                 PlanStatus      `json:"status" yaml:"status"`
	Strategy               Strategy        `json:"strategy" yaml:"strategy"`
	Model                  string          `json:"model" yaml:"model"`
	Runtime                string          `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Quantization           string          `json:"quantization,omitempty" yaml:"quantization,omitempty"`
	SnapshotTimestamp      time.Time       `json:"snapshot_timestamp" yaml:"snapshot_timestamp"`
	PlannedAt              time.Time       `json:"planned_at" yaml:"planned_at"`
	CoordinatorNode        string          `json:"coordinator_node" yaml:"coordinator_node"`
	TotalLayers            int             `json:"total_layers" yaml:"total_layers"`
	EstimatedLayerMemoryMB int64           `json:"estimated_layer_memory_mb" yaml:"estimated_layer_memory_mb"`
	EstimatedTotalMemoryMB int64           `json:"estimated_total_memory_mb" yaml:"estimated_total_memory_mb"`
	Shards                 []ModelShard    `json:"shards" yaml:"shards"`
	Links                  []PlanLink      `json:"links,omitempty" yaml:"links,omitempty"`
	Excluded               []NodeExclusion `json:"excluded,omitempty" yaml:"excluded,omitempty"`
	Warnings               []string        `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Reasoning              []string        `json:"reasoning" yaml:"reasoning"`
}
