package agent

import (
	"testing"
	"time"
)

// Fake-clock coverage for approvalWait: wall-clock timedDispatch assertions
// remain useful for the Confirm wiring, but they flake under -race when the
// confirm sleep is only a few hundred milliseconds. This test pins the
// overlapping-wait accounting without sleeping.
func TestApprovalWait_OverlappingWaitsCountOnce(t *testing.T) {
	parent := &approvalWait{}
	child := &approvalWait{parent: parent}

	t0 := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	child.begin(t0)
	parent.begin(t0.Add(100 * time.Millisecond)) // nested wait while child still open
	parent.end(t0.Add(300 * time.Millisecond))
	child.end(t0.Add(500 * time.Millisecond))

	if got := child.total(); got != 500*time.Millisecond {
		t.Fatalf("child waited %s, want 500ms (own open window)", got)
	}
	// Parent was open continuously from t0 (via child.begin) through t0+500ms.
	if got := parent.total(); got != 500*time.Millisecond {
		t.Fatalf("parent waited %s, want 500ms (overlapping waits count once)", got)
	}
}
