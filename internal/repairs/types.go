// Package repairs is INTERNAL-ONLY — advisory receipts for automatic authority maintenance.
// Receipts are observable telemetry only; they never drive reconciliation.
package repairs

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Severity describes the urgency of a repair event.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// RepairEvent is a structured record of an automatic repair performed by AXIS.
// Every repair event names the authority that detected the issue, the object
// repaired, and the old/new values where applicable.
type RepairEvent struct {
	// Timestamp is the wall-clock time of the repair (advisory; epoch ordering
	// governs sequence if available).
	Timestamp time.Time `json:"timestamp"`

	// Severity classifies the repair urgency.
	Severity Severity `json:"severity"`

	// SourceAuthority names the subsystem that owns the canonical state that
	// was repaired. Example: "ledger", "snapshot", "freshness".
	SourceAuthority string `json:"source_authority"`

	// ObjectType is the kind of object repaired. Example: "reservation",
	// "node_state", "snapshot".
	ObjectType string `json:"object_type"`

	// ObjectID is the unique identifier of the repaired object, if any.
	ObjectID string `json:"object_id,omitempty"`

	// OldValue is the value before repair (may be omitted for deletions).
	OldValue string `json:"old_value,omitempty"`

	// NewValue is the value after repair (may be omitted for information-only).
	NewValue string `json:"new_value,omitempty"`

	// Description is a human-readable explanation of what was repaired and why.
	Description string `json:"description"`
}

// String returns a compact operator-facing representation.
func (e RepairEvent) String() string {
	return fmt.Sprintf("[%s] %s: %s/%s %s repaired (%s → %s)",
		e.Timestamp.Format(time.RFC3339),
		e.Severity,
		e.SourceAuthority,
		e.ObjectType,
		e.ObjectID,
		e.OldValue,
		e.NewValue,
	)
}

// IsSilent returns true if the repair is informational only and does not require
// operator action.
func (e RepairEvent) IsSilent() bool {
	return e.Severity == SeverityInfo
}

// Emit writes a structured maintenance receipt synchronously. Callers must emit
// only after the repaired authority has been persisted successfully.
func Emit(logger *slog.Logger, event RepairEvent) {
	if logger == nil {
		logger = slog.Default()
	}
	level := slog.LevelInfo
	if event.Severity == SeverityWarning {
		level = slog.LevelWarn
	} else if event.Severity == SeverityCritical {
		level = slog.LevelError
	}
	logger.LogAttrs(context.Background(), level, "maintenance receipt",
		slog.Time("repair_timestamp", event.Timestamp),
		slog.String("severity", string(event.Severity)),
		slog.String("source_authority", event.SourceAuthority),
		slog.String("object_type", event.ObjectType),
		slog.String("object_id", event.ObjectID),
		slog.String("old_value", event.OldValue),
		slog.String("new_value", event.NewValue),
		slog.String("description", event.Description),
	)
}

// EmitAll writes receipts in maintenance order.
func EmitAll(logger *slog.Logger, events []RepairEvent) {
	for _, event := range events {
		Emit(logger, event)
	}
}
