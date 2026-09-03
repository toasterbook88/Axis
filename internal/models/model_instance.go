package models

import "time"

// ModelInstanceState describes an observed model residency state. It does not
// imply that Axis owns the process or can mutate it.
type ModelInstanceState string

const (
	// ModelInstanceResident means a runtime probe reported the model as loaded.
	ModelInstanceResident ModelInstanceState = "resident"
)

// ModelInstance is the canonical read model for one observed resident model
// slot. ID is stable for the node identity, engine, model, and port tuple; it
// is not a process-generation ID because resident facts do not currently
// include a PID or process start time.
type ModelInstance struct {
	ID           string             `json:"id" yaml:"id"`
	Model        string             `json:"model" yaml:"model"`
	Engine       string             `json:"engine,omitempty" yaml:"engine,omitempty"`
	Node         string             `json:"node" yaml:"node"`
	NodeStatus   NodeStatus         `json:"node_status" yaml:"node_status"`
	State        ModelInstanceState `json:"state" yaml:"state"`
	Port         int                `json:"port,omitempty" yaml:"port,omitempty"`
	Processor    string             `json:"processor,omitempty" yaml:"processor,omitempty"`
	Source       string             `json:"source,omitempty" yaml:"source,omitempty"`
	WeightSizeMB int64              `json:"weight_size_mb,omitempty" yaml:"weight_size_mb,omitempty"`
	SizeRAMMB    int64              `json:"size_ram_mb,omitempty" yaml:"size_ram_mb,omitempty"`
	SizeVRAMMB   int64              `json:"size_vram_mb,omitempty" yaml:"size_vram_mb,omitempty"`
	ExpiresAt    time.Time          `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	WarmthScore  float64            `json:"warmth_score,omitempty" yaml:"warmth_score,omitempty"`
	ObservedAt   time.Time          `json:"observed_at,omitempty" yaml:"observed_at,omitempty"`
}

// ModelInventory preserves the authority and warnings of the snapshot from
// which its instances were derived.
type ModelInventory struct {
	Source        string          `json:"source" yaml:"source"`
	PublicationID string          `json:"publication_id,omitempty" yaml:"publication_id,omitempty"`
	ObservedAt    time.Time       `json:"observed_at,omitempty" yaml:"observed_at,omitempty"`
	Instances     []ModelInstance `json:"instances" yaml:"instances"`
	Warnings      []Warning       `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// ModelInspection is the single-instance counterpart to ModelInventory.
type ModelInspection struct {
	Source        string        `json:"source" yaml:"source"`
	PublicationID string        `json:"publication_id,omitempty" yaml:"publication_id,omitempty"`
	ObservedAt    time.Time     `json:"observed_at,omitempty" yaml:"observed_at,omitempty"`
	Instance      ModelInstance `json:"instance" yaml:"instance"`
	Warnings      []Warning     `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}
