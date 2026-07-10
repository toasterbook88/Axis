package repairs

import (
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
