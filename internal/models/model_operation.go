package models

import "time"

// ModelOperationAction identifies a model lifecycle transition.
type ModelOperationAction string

const (
	ModelOperationStop ModelOperationAction = "stop"
)

// ModelOperationStatus is the terminal status of a synchronous lifecycle
// operation. Later asynchronous operations may add non-terminal states.
type ModelOperationStatus string

const (
	ModelOperationCompleted ModelOperationStatus = "completed"
	ModelOperationNoOp      ModelOperationStatus = "no_op"
	ModelOperationRejected  ModelOperationStatus = "rejected"
	ModelOperationFailed    ModelOperationStatus = "failed"
)

// ModelOperationReceipt records what Axis attempted and the observed terminal
// result. It is evidence for operators; it does not override fact-plane state.
type ModelOperationReceipt struct {
	Schema         string               `json:"schema" yaml:"schema"`
	ID             string               `json:"id" yaml:"id"`
	Action         ModelOperationAction `json:"action" yaml:"action"`
	Status         ModelOperationStatus `json:"status" yaml:"status"`
	Disposition    string               `json:"disposition" yaml:"disposition"`
	InstanceID     string               `json:"instance_id,omitempty" yaml:"instance_id,omitempty"`
	GenerationID   string               `json:"generation_id,omitempty" yaml:"generation_id,omitempty"`
	Node           string               `json:"node" yaml:"node"`
	Engine         string               `json:"engine,omitempty" yaml:"engine,omitempty"`
	Port           int                  `json:"port,omitempty" yaml:"port,omitempty"`
	PID            int                  `json:"pid,omitempty" yaml:"pid,omitempty"`
	SnapshotSource string               `json:"snapshot_source,omitempty" yaml:"snapshot_source,omitempty"`
	PublicationID  string               `json:"publication_id,omitempty" yaml:"publication_id,omitempty"`
	SnapshotAt     time.Time            `json:"snapshot_at,omitempty" yaml:"snapshot_at,omitempty"`
	StartedAt      time.Time            `json:"started_at" yaml:"started_at"`
	CompletedAt    time.Time            `json:"completed_at" yaml:"completed_at"`
	Error          string               `json:"error,omitempty" yaml:"error,omitempty"`
}
