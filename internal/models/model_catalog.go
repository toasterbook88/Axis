package models

import "time"

// ModelCatalogState is how far a catalog entry is from serving.
type ModelCatalogState string

const (
	// ModelCatalogLoaded requires a load signal: Ollama /api/ps, or a
	// llama.cpp process whose -m names the model.
	ModelCatalogLoaded ModelCatalogState = "loaded"
	// ModelCatalogListed means a server named the model but gave no load
	// signal (e.g. mlx_lm.server /v1/models). Never treated as loaded.
	ModelCatalogListed ModelCatalogState = "listed"
	// ModelCatalogInstalled means the weights (or a cloud-proxy stub) are
	// present and the load signal says not loaded.
	ModelCatalogInstalled ModelCatalogState = "installed"
	// ModelCatalogLoading means the runtime reports it is still loading
	// (llama.cpp /health 503).
	ModelCatalogLoading ModelCatalogState = "loading"
	// ModelCatalogDown means the process was seen but its endpoint did not
	// answer.
	ModelCatalogDown ModelCatalogState = "down"
)

// ModelLocality says where inference for an entry actually runs.
type ModelLocality string

const (
	// ModelLocalityOnNode means the weights are on the reporting node.
	ModelLocalityOnNode ModelLocality = "on-node"
	// ModelLocalityCloudProxy means the node forwards requests to RemoteHost;
	// inference leaves the cluster.
	ModelLocalityCloudProxy ModelLocality = "cloud-proxy"
)

// ModelCatalogEntry is one model on one node, as that node reported it.
// The same model on two nodes is two entries.
type ModelCatalogEntry struct {
	Model      string            `json:"model" yaml:"model"`
	Node       string            `json:"node" yaml:"node"`
	NodeStatus NodeStatus        `json:"node_status" yaml:"node_status"`
	Engine     string            `json:"engine" yaml:"engine"`
	State      ModelCatalogState `json:"state" yaml:"state"`
	Locality   ModelLocality     `json:"locality" yaml:"locality"`
	RemoteHost string            `json:"remote_host,omitempty" yaml:"remote_host,omitempty"`
	// Capabilities as the engine reported them; empty means unknown.
	Capabilities  []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Family        string   `json:"family,omitempty" yaml:"family,omitempty"`
	ParameterSize string   `json:"parameter_size,omitempty" yaml:"parameter_size,omitempty"`
	Quantization  string   `json:"quantization,omitempty" yaml:"quantization,omitempty"`
	SizeBytes     int64    `json:"size_bytes,omitempty" yaml:"size_bytes,omitempty"`
	// ContextWindow is the served context the runtime stated; 0 = unknown.
	ContextWindow int `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	// LoadSignal names the evidence behind State.
	LoadSignal string `json:"load_signal,omitempty" yaml:"load_signal,omitempty"`
	// Port and InstanceID are set for loaded entries; InstanceID matches
	// ModelInventory so lifecycle commands can act on the entry.
	Port       int       `json:"port,omitempty" yaml:"port,omitempty"`
	InstanceID string    `json:"instance_id,omitempty" yaml:"instance_id,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty" yaml:"observed_at,omitempty"`
}

// UnobservedNode records a configured node whose models are unknown in this
// snapshot. Its absence from Entries means "not observed", never "no models".
type UnobservedNode struct {
	Node   string     `json:"node" yaml:"node"`
	Status NodeStatus `json:"status" yaml:"status"`
	Reason string     `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// ModelCatalog is every model the snapshot observed, with the snapshot's
// authority and the nodes it could not see.
type ModelCatalog struct {
	Source        string              `json:"source" yaml:"source"`
	PublicationID string              `json:"publication_id,omitempty" yaml:"publication_id,omitempty"`
	ObservedAt    time.Time           `json:"observed_at,omitempty" yaml:"observed_at,omitempty"`
	Entries       []ModelCatalogEntry `json:"entries" yaml:"entries"`
	Unobserved    []UnobservedNode    `json:"unobserved,omitempty" yaml:"unobserved,omitempty"`
	Warnings      []Warning           `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}
