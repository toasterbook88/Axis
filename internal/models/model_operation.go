package models

import "time"

// ModelOperationAction identifies a model lifecycle transition.
type ModelOperationAction string

const (
	ModelOperationStart ModelOperationAction = "start"
	ModelOperationStop  ModelOperationAction = "stop"
	ModelOperationAwait ModelOperationAction = "await"
	ModelOperationQuery ModelOperationAction = "query"
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
	Schema           string               `json:"schema" yaml:"schema"`
	ID               string               `json:"id" yaml:"id"`
	Action           ModelOperationAction `json:"action" yaml:"action"`
	Status           ModelOperationStatus `json:"status" yaml:"status"`
	Disposition      string               `json:"disposition" yaml:"disposition"`
	InstanceID       string               `json:"instance_id,omitempty" yaml:"instance_id,omitempty"`
	GenerationID     string               `json:"generation_id,omitempty" yaml:"generation_id,omitempty"`
	Node             string               `json:"node" yaml:"node"`
	Engine           string               `json:"engine,omitempty" yaml:"engine,omitempty"`
	Port             int                  `json:"port,omitempty" yaml:"port,omitempty"`
	PID              int                  `json:"pid,omitempty" yaml:"pid,omitempty"`
	Model            string               `json:"model,omitempty" yaml:"model,omitempty"`
	Weights          string               `json:"weights,omitempty" yaml:"weights,omitempty"`
	Volume           string               `json:"volume,omitempty" yaml:"volume,omitempty"`
	Executable       string               `json:"executable,omitempty" yaml:"executable,omitempty"`
	SnapshotSource   string               `json:"snapshot_source,omitempty" yaml:"snapshot_source,omitempty"`
	PublicationID    string               `json:"publication_id,omitempty" yaml:"publication_id,omitempty"`
	SnapshotAt       time.Time            `json:"snapshot_at,omitempty" yaml:"snapshot_at,omitempty"`
	StartedAt        time.Time            `json:"started_at" yaml:"started_at"`
	CompletedAt      time.Time            `json:"completed_at" yaml:"completed_at"`
	DurationMS       int64                `json:"duration_ms,omitempty" yaml:"duration_ms,omitempty"`
	PromptTokens     int                  `json:"prompt_tokens,omitempty" yaml:"prompt_tokens,omitempty"`
	CompletionTokens int                  `json:"completion_tokens,omitempty" yaml:"completion_tokens,omitempty"`
	TotalTokens      int                  `json:"total_tokens,omitempty" yaml:"total_tokens,omitempty"`
	EndpointURL      string               `json:"endpoint_url,omitempty" yaml:"endpoint_url,omitempty"`
	ResponseText     string               `json:"response_text,omitempty" yaml:"response_text,omitempty"`
	Error            string               `json:"error,omitempty" yaml:"error,omitempty"`
}
