// Package models defines core AXIS data types.
// All types are internal — there is no stable public API surface.
package models

import (
	"strings"
	"time"
)

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

// GPUInfo describes a single GPU with vendor, model, VRAM, and capabilities.
type GPUInfo struct {
	Vendor       string   `json:"vendor" yaml:"vendor"`                                 // apple, nvidia, amd, intel, unknown
	Model        string   `json:"model" yaml:"model"`                                   // e.g. "Apple M3 Pro", "NVIDIA GeForce RTX 4090"
	VRAMMB       int      `json:"vram_mb,omitempty" yaml:"vram_mb,omitempty"`           // 0 means unknown or unified
	Capabilities []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"` // metal, cuda, rocm, vulkan
}

// GPUName returns the model string for display purposes.
func (g GPUInfo) GPUName() string { return g.Model }

// HasCapability reports whether the GPU supports the named capability.
func (g GPUInfo) HasCapability(cap string) bool {
	for _, c := range g.Capabilities {
		if strings.EqualFold(c, cap) {
			return true
		}
	}
	return false
}

// Resources holds observed hardware resource metrics.
type Resources struct {
	CPUCores        int            `json:"cpu_cores" yaml:"cpu_cores"`
	CPUModel        string         `json:"cpu_model" yaml:"cpu_model"`
	RAMTotalMB      int64          `json:"ram_total_mb" yaml:"ram_total_mb"`
	RAMFreeMB       int64          `json:"ram_free_mb" yaml:"ram_free_mb"`
	MemoryTopology  MemoryTopology `json:"memory_topology,omitempty" yaml:"memory_topology,omitempty"`
	MemoryClass     int            `json:"memory_class,omitempty" yaml:"memory_class,omitempty"`
	Load1M          float64        `json:"load_1m" yaml:"load_1m"`
	Load5M          float64        `json:"load_5m" yaml:"load_5m"`
	Load15M         float64        `json:"load_15m" yaml:"load_15m"`
	DiskTotalGB     int64          `json:"disk_total_gb" yaml:"disk_total_gb"`
	DiskFreeGB      int64          `json:"disk_free_gb" yaml:"disk_free_gb"`
	GPUs            []GPUInfo      `json:"gpus,omitempty" yaml:"gpus,omitempty"`
	GPUUtilPercent  *float64       `json:"gpu_util_percent,omitempty" yaml:"gpu_util_percent,omitempty"`
	StorageClass    string         `json:"storage_class,omitempty" yaml:"storage_class,omitempty"` // nvme, ssd, hdd, unknown
	BatteryPercent  *int           `json:"battery_percent,omitempty" yaml:"battery_percent,omitempty"`
	PowerSource     string         `json:"power_source,omitempty" yaml:"power_source,omitempty"`   // ac, battery, unknown
	ThermalState    string         `json:"thermal_state,omitempty" yaml:"thermal_state,omitempty"` // nominal, fair, serious, critical
	ThermalZones    []ThermalZone  `json:"thermal_zones,omitempty" yaml:"thermal_zones,omitempty"`
	Pressure        string         `json:"pressure" yaml:"pressure"` // none, low, medium, high
	PressureStall10 float64        `json:"pressure_stall_10,omitempty" yaml:"pressure_stall_10,omitempty"`
	PressureSource  string         `json:"pressure_source,omitempty" yaml:"pressure_source,omitempty"`
}

// ThermalZone holds a single thermal sensor reading.
type ThermalZone struct {
	Type  string  `json:"type" yaml:"type"`                       // e.g. "cpu_0", "gpu", "soc", "battery"
	TempC float64 `json:"temp_c" yaml:"temp_c"`                   // temperature in Celsius
	State string  `json:"state,omitempty" yaml:"state,omitempty"` // nominal, fair, serious, critical
}

// ThermalStateFromZones derives the worst-case ThermalState from ThermalZones.
// Returns "" if no zones have an explicit state.
func ThermalStateFromZones(zones []ThermalZone) string {
	worst := ""
	for _, z := range zones {
		if thermalWorse(z.State, worst) {
			worst = z.State
		}
	}
	return worst
}

func thermalWorse(candidate, current string) bool {
	order := map[string]int{"": 0, "nominal": 1, "fair": 2, "serious": 3, "critical": 4}
	return order[candidate] > order[current]
}

// ReservableRAM returns the amount of RAM available for tracking cluster reservations.
// It prioritizes RAMReservableMB if explicitly reported by the node probe,
// otherwise falls back to calculating the reservable budget via ReservableRAMMB.
func (r *Resources) ReservableRAM() int64 {
	if r == nil {
		return 0
	}
	return ReservableRAMMB(r.RAMTotalMB, r.RAMFreeMB)
}

func (n NodeFacts) ReservableRAM() int64 {
	if n.Resources == nil {
		return 0
	}
	return n.Resources.ReservableRAM()
}

// NetworkAddress represents a single network address with interface metadata.
// Kind is one of: ipv4, ipv6, hostname.
// Scope is one of: global, link-local (empty means global for backward compat).
type NetworkAddress struct {
	Kind       string `json:"kind" yaml:"kind"`
	Address    string `json:"address" yaml:"address"`
	Interface  string `json:"interface,omitempty" yaml:"interface,omitempty"`     // e.g. en0, eth0, wg0
	Subnet     string `json:"subnet,omitempty" yaml:"subnet,omitempty"`           // CIDR e.g. 192.168.1.0/24
	SpeedClass string `json:"speed_class,omitempty" yaml:"speed_class,omitempty"` // thunderbolt, 10gbe, gigabit, wifi, tailscale, wireguard, unknown
	Scope      string `json:"scope,omitempty" yaml:"scope,omitempty"`             // global, link-local
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

// ResidentModel is additive truth-plane metadata describing a model that is
// currently resident in a node runtime according to a live probe.
type ResidentModel struct {
	Name       string `json:"name" yaml:"name"`
	Runtime    string `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Processor  string `json:"processor,omitempty" yaml:"processor,omitempty"`
	Source     string `json:"source,omitempty" yaml:"source,omitempty"`
	SizeVRAMMB int64  `json:"size_vram_mb,omitempty" yaml:"size_vram_mb,omitempty"` // 0 = unknown/not reported by the runtime; currently populated only by the Ollama probe
}

// TurboQuantInfo records whether a node appears able to run a TurboQuant-like
// long-context backend. This is additive advisory metadata only.
type TurboQuantInfo struct {
	Supported    bool     `json:"supported" yaml:"supported"`
	Verified     bool     `json:"verified,omitempty" yaml:"verified,omitempty"`
	Backends     []string `json:"backends,omitempty" yaml:"backends,omitempty"`
	Capabilities []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

// AppleFoundationModelsInfo records whether the local Apple on-device model
// path is available and runtime-verified through the FoundationModels framework.
type AppleFoundationModelsInfo struct {
	Available bool   `json:"available" yaml:"available"`
	Verified  bool   `json:"verified,omitempty" yaml:"verified,omitempty"`
	Version   string `json:"version,omitempty" yaml:"version,omitempty"`
	Error     string `json:"error,omitempty" yaml:"error,omitempty"`
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
	Hostname         string                     `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	Identity         *NodeIdentity              `json:"identity,omitempty" yaml:"identity,omitempty"`
	OS               string                     `json:"os,omitempty" yaml:"os,omitempty"`                 // darwin, linux
	OSVersion        string                     `json:"os_version,omitempty" yaml:"os_version,omitempty"` // e.g. 26.4, 6.1.0
	Arch             string                     `json:"arch,omitempty" yaml:"arch,omitempty"`
	Resources        *Resources                 `json:"resources,omitempty" yaml:"resources,omitempty"`
	Addresses        []NetworkAddress           `json:"addresses,omitempty" yaml:"addresses,omitempty"`
	Tools            []ToolInfo                 `json:"tools,omitempty" yaml:"tools,omitempty"`
	Ollama           *OllamaInfo                `json:"ollama,omitempty" yaml:"ollama,omitempty"`
	ResidentModels   []ResidentModel            `json:"resident_models,omitempty" yaml:"resident_models,omitempty"`
	TurboQuant       *TurboQuantInfo            `json:"turboquant,omitempty" yaml:"turboquant,omitempty"`
	AppleFM          *AppleFoundationModelsInfo `json:"apple_foundation_models,omitempty" yaml:"apple_foundation_models,omitempty"`
	RAMReservedMB    int64                      `json:"ram_reserved_mb,omitempty" yaml:"ram_reserved_mb,omitempty"`
	RAMAllocatableMB int64                      `json:"ram_allocatable_mb,omitempty" yaml:"ram_allocatable_mb,omitempty"`

	// Epistemic state (Truth Classification)
	Epistemic *EpistemicState `json:"epistemic,omitempty" yaml:"epistemic,omitempty"`

	// Result metadata
	Status      NodeStatus `json:"status" yaml:"status"`
	Error       string     `json:"error,omitempty" yaml:"error,omitempty"`
	CollectedAt time.Time  `json:"collected_at" yaml:"collected_at"`
}

// --- Epistemic State (Truth Classification) ---

type EpistemicSource string

const (
	SourceDaemonCache EpistemicSource = "daemon-cache"
	SourceLiveProbe   EpistemicSource = "live-probe"
)

type VerificationMethod string

const (
	VerifiedByMesh   VerificationMethod = "mesh-hmac"
	VerifiedByConfig VerificationMethod = "config-trust"
	VerifiedByLocal  VerificationMethod = "local-trust"
)

type EpistemicState struct {
	Source      EpistemicSource    `json:"source" yaml:"source"`
	VerifiedBy  VerificationMethod `json:"verified_by" yaml:"verified_by"`
	FreshnessMS int64              `json:"freshness_ms" yaml:"freshness_ms"`
	Degraded    bool               `json:"degraded" yaml:"degraded"`
	Warnings    []string           `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// --- Derived State (Snapshot) ---

// ClusterSummary holds cluster-level aggregates derived from node facts.
type ClusterSummary struct {
	TotalNodes         int   `json:"total_nodes" yaml:"total_nodes"`
	ReachableNodes     int   `json:"reachable_nodes" yaml:"reachable_nodes"`
	TotalRAMMB         int64 `json:"total_ram_mb" yaml:"total_ram_mb"`
	TotalFreeRAMMB     int64 `json:"total_free_ram_mb" yaml:"total_free_ram_mb"`
	TotalReservableMB  int64 `json:"total_reservable_mb,omitempty" yaml:"total_reservable_mb,omitempty"`
	TotalAllocatableMB int64 `json:"total_allocatable_mb,omitempty" yaml:"total_allocatable_mb,omitempty"`
	TotalReservedMB    int64 `json:"total_reserved_mb,omitempty" yaml:"total_reserved_mb,omitempty"`
}

// Warning represents a specific issue detected during snapshot assembly.
type Warning struct {
	Node    string `json:"node" yaml:"node"`
	Kind    string `json:"kind" yaml:"kind"` // unreachable, partial, ram_pressure, error
	Message string `json:"message" yaml:"message"`
}

// DiscoveryFreshness describes how complete the current beacon-derived node
// picture is relative to the expected accumulation window.
type DiscoveryFreshness struct {
	Source           string `json:"source" yaml:"source"`
	ExpectedWindowMS int64  `json:"expected_window_ms,omitempty" yaml:"expected_window_ms,omitempty"`
	ObservedWindowMS int64  `json:"observed_window_ms,omitempty" yaml:"observed_window_ms,omitempty"`
	SeededNodeCount  int    `json:"seeded_node_count,omitempty" yaml:"seeded_node_count,omitempty"`
	BeaconNodeCount  int    `json:"beacon_node_count,omitempty" yaml:"beacon_node_count,omitempty"`
	CompletedWindow  bool   `json:"completed_window" yaml:"completed_window"`
	Warning          string `json:"warning,omitempty" yaml:"warning,omitempty"`
}

// ClusterSnapshot is the principal output of AXIS: a compact structured
// summary of cluster state for consumption by frontier models and operators.
type ClusterSnapshot struct {
	Timestamp time.Time           `json:"timestamp" yaml:"timestamp"`
	Status    SnapshotStatus      `json:"status" yaml:"status"`
	Nodes     []NodeFacts         `json:"nodes" yaml:"nodes"`
	Summary   ClusterSummary      `json:"summary" yaml:"summary"`
	Warnings  []Warning           `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Freshness *DiscoveryFreshness `json:"freshness,omitempty" yaml:"freshness,omitempty"`
}

// --- Phase 2: Task Placement ---

// WorkloadClass categorizes a task based on its resource and behavioral needs.
type WorkloadClass string

const (
	ClassRepoAnalysis         WorkloadClass = "repo-analysis"
	ClassGoBuild              WorkloadClass = "go-build"
	ClassDockerBuild          WorkloadClass = "docker-build"
	ClassLocalLLMInference    WorkloadClass = "local-llm-inference"
	ClassLongContextInference WorkloadClass = "long-context-inference"
	ClassAppleIntelligence    WorkloadClass = "apple-intelligence"
	ClassLlamaServer          WorkloadClass = "llama-server"
	ClassIndexingIO           WorkloadClass = "indexing-io"
	ClassBatchScript          WorkloadClass = "batch-script"
	ClassUnknown              WorkloadClass = "unknown"
)

// WorkloadProfileMatch contains the result of matching a task to a profile.
type WorkloadProfileMatch struct {
	Class WorkloadClass `json:"class" yaml:"class"`
	Notes []string      `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// TaskRequirements describes what a task needs to run.
// Inferred from task description by workload profile matching.
type TaskRequirements struct {
	Description         string               `json:"description" yaml:"description"`
	Workload            WorkloadProfileMatch `json:"workload,omitempty" yaml:"workload,omitempty"`
	RequiredTools       []string             `json:"required_tools,omitempty" yaml:"required_tools,omitempty"`
	MinFreeRAMMB        int64                `json:"min_free_ram_mb,omitempty" yaml:"min_free_ram_mb,omitempty"`
	ContextWindowTokens int                  `json:"context_window_tokens,omitempty" yaml:"context_window_tokens,omitempty"`
	PrefersTurboQuant   bool                 `json:"prefers_turboquant,omitempty" yaml:"prefers_turboquant,omitempty"`
	PreferredBackends   []string             `json:"preferred_backends,omitempty" yaml:"preferred_backends,omitempty"`
}

// PlacementDecision is the output of the placement engine.
// OK is false when no node qualifies.
type PlacementDecision struct {
	Node      string               `json:"node" yaml:"node"`
	Tool      string               `json:"tool,omitempty" yaml:"tool,omitempty"`
	FitScore  int                  `json:"fit_score" yaml:"fit_score"`
	IsLocal   bool                 `json:"is_local" yaml:"is_local"`
	Workload  WorkloadProfileMatch `json:"workload,omitempty" yaml:"workload,omitempty"`
	Reasoning []string             `json:"reasoning" yaml:"reasoning"`
	OK        bool                 `json:"ok" yaml:"ok"`
}

type PlacementCandidateExplanation struct {
	Node      string   `json:"node" yaml:"node"`
	FitScore  int      `json:"fit_score" yaml:"fit_score"`
	IsLocal   bool     `json:"is_local" yaml:"is_local"`
	Reasoning []string `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
}

type PlacementExclusion struct {
	Node    string   `json:"node" yaml:"node"`
	Reasons []string `json:"reasons,omitempty" yaml:"reasons,omitempty"`
}

type PlacementExplanation struct {
	Decision PlacementDecision               `json:"decision" yaml:"decision"`
	Eligible []PlacementCandidateExplanation `json:"eligible,omitempty" yaml:"eligible,omitempty"`
	Excluded []PlacementExclusion            `json:"excluded,omitempty" yaml:"excluded,omitempty"`
}

// --- Phase 3: Failure Memory ---

// FailureClass categorizes an operational failure.
type FailureClass string

const (
	FailureExecCrash          FailureClass = "exec-crash"
	FailureToolMissing        FailureClass = "tool-missing"
	FailureResourceExhaustion FailureClass = "resource-exhaustion"
	FailureThermal            FailureClass = "thermal-failure"
	FailureBattery            FailureClass = "battery-failure"
	FailureNetwork            FailureClass = "network-failure"
	FailureTimeout            FailureClass = "timeout"
	FailureBackendMisfit      FailureClass = "backend-misfit"
	FailureOperatorAbort      FailureClass = "operator-abort"
	FailureUnknown            FailureClass = "unknown"
)

// FailureScope defines what a failure applies to.
type FailureScope struct {
	Node     string        `json:"node,omitempty" yaml:"node,omitempty"`
	Workload WorkloadClass `json:"workload,omitempty" yaml:"workload,omitempty"`
	Tool     string        `json:"tool,omitempty" yaml:"tool,omitempty"`
	Backend  string        `json:"backend,omitempty" yaml:"backend,omitempty"`
	Surface  string        `json:"surface,omitempty" yaml:"surface,omitempty"`
}

// ObservationScope identifies the exact execution shape tracked by empirical
// runtime observations.
//
// ModelName is optional: when set, the observation is scoped to a specific
// inference model (e.g. "llama3.2:latest"). When empty, the observation
// covers the workload class without model specificity, preserving backward
// compatibility with existing observation entries.
type ObservationScope struct {
	Node      string        `json:"node,omitempty" yaml:"node,omitempty"`
	Workload  WorkloadClass `json:"workload,omitempty" yaml:"workload,omitempty"`
	Backend   string        `json:"backend,omitempty" yaml:"backend,omitempty"`
	Tool      string        `json:"tool,omitempty" yaml:"tool,omitempty"`
	ModelName string        `json:"model_name,omitempty" yaml:"model_name,omitempty"`
}

// ExecutionObservation records the latest empirical execution profile for an
// exact scope. Unknown resource peaks remain unset.
type ExecutionObservation struct {
	Scope       ObservationScope `json:"scope" yaml:"scope"`
	ObservedAt  time.Time        `json:"observed_at" yaml:"observed_at"`
	SampleCount int              `json:"sample_count" yaml:"sample_count"`
	LastSuccess bool             `json:"last_success" yaml:"last_success"`
	WallTimeMS  int64            `json:"wall_time_ms" yaml:"wall_time_ms"`
	PeakRAMMB   int64            `json:"peak_ram_mb,omitempty" yaml:"peak_ram_mb,omitempty"`
	PeakVRAMMB  int64            `json:"peak_vram_mb,omitempty" yaml:"peak_vram_mb,omitempty"`
	// ModelName is the inference model name observed during execution
	// (e.g. "llama3.2:latest", "qwen2.5-coder:7b"). Populated when a model
	// name is extractable from the task command or description. Used by
	// empirical placement to prefer nodes that already have the named model
	// loaded in the target runtime.
	ModelName string `json:"model_name,omitempty" yaml:"model_name,omitempty"`
}

// FailureRecord represents a tracked failure pattern for the immune system.
type FailureRecord struct {
	ID               string       `json:"id" yaml:"id"`
	Class            FailureClass `json:"class" yaml:"class"`
	Scope            FailureScope `json:"scope" yaml:"scope"`
	OccurredAt       time.Time    `json:"occurred_at" yaml:"occurred_at"`
	ExpiresAt        time.Time    `json:"expires_at" yaml:"expires_at"`
	Count            int          `json:"count" yaml:"count"`
	Confidence       float64      `json:"confidence" yaml:"confidence"`
	Reason           string       `json:"reason" yaml:"reason"`
	Evidence         []string     `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	OperatorOverride bool         `json:"operator_override" yaml:"operator_override"`
	OperatorNote     string       `json:"operator_note,omitempty" yaml:"operator_note,omitempty"`
}

// PlacementError describes why placement failed.
type PlacementError struct {
	Message string `json:"message" yaml:"message"`
}

func (e *PlacementError) Error() string {
	return e.Message
}

// GPUNames returns model strings for all GPUs (display helper).
func GPUNames(gpus []GPUInfo) []string {
	names := make([]string, len(gpus))
	for i, g := range gpus {
		names[i] = g.Model
	}
	return names
}

// GPUFromString creates a GPUInfo from a plain model string (backward compat).
func GPUFromString(model string) GPUInfo {
	g := GPUInfo{Model: model, Vendor: classifyGPUVendor(model)}
	g.Capabilities = inferGPUCapabilities(g.Vendor)
	return g
}

func classifyGPUVendor(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "apple") || strings.HasPrefix(m, "m1") || strings.HasPrefix(m, "m2") || strings.HasPrefix(m, "m3") || strings.HasPrefix(m, "m4"):
		return "apple"
	case strings.Contains(m, "nvidia") || strings.Contains(m, "geforce") || strings.Contains(m, "quadro") || strings.Contains(m, "tesla") || strings.Contains(m, "rtx") || strings.Contains(m, "gtx"):
		return "nvidia"
	case strings.Contains(m, "amd") || strings.Contains(m, "radeon"):
		return "amd"
	case strings.Contains(m, "intel"):
		return "intel"
	default:
		return "unknown"
	}
}

func inferGPUCapabilities(vendor string) []string {
	switch vendor {
	case "apple":
		return []string{"metal"}
	case "nvidia":
		return []string{"cuda"}
	case "amd":
		return []string{"rocm"}
	default:
		return nil
	}
}
