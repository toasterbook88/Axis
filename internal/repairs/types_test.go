package repairs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

func TestRepairEventString(t *testing.T) {
	event := RepairEvent{
		Timestamp:       time.Date(2026, time.July, 10, 18, 30, 0, 0, time.UTC),
		Severity:        SeverityWarning,
		SourceAuthority: "ledger",
		ObjectType:      "reservation",
		ObjectID:        "res-123",
		OldValue:        "stale",
		NewValue:        "released",
	}

	want := "[2026-07-10T18:30:00Z] warning: ledger/reservation res-123 repaired (stale → released)"
	if got := event.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestEmitWritesStructuredMaintenanceReceipt(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	event := RepairEvent{
		Timestamp:       time.Date(2026, time.July, 10, 18, 30, 0, 0, time.UTC),
		Severity:        SeverityWarning,
		SourceAuthority: "ledger",
		ObjectType:      "reservation",
		ObjectID:        "res-123",
		OldValue:        "stale",
		NewValue:        "reclaimed",
		Description:     "automatic reconciliation removed a stale reservation",
	}

	Emit(logger, event)

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	for key, want := range map[string]string{
		"msg":              "maintenance receipt",
		"level":            "WARN",
		"source_authority": "ledger",
		"object_type":      "reservation",
		"object_id":        "res-123",
		"old_value":        "stale",
		"new_value":        "reclaimed",
	} {
		if got[key] != want {
			t.Fatalf("%s = %#v, want %q (receipt=%s)", key, got[key], want, buf.String())
		}
	}
}

func TestRepairEventIsSilent(t *testing.T) {
	tests := []struct {
		severity Severity
		want     bool
	}{
		{severity: SeverityInfo, want: true},
		{severity: SeverityWarning, want: false},
		{severity: SeverityCritical, want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.severity), func(t *testing.T) {
			event := RepairEvent{Severity: tt.severity}
			if got := event.IsSilent(); got != tt.want {
				t.Fatalf("IsSilent() = %t, want %t", got, tt.want)
			}
		})
	}
}
