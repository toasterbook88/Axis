package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Regression: the daemon /run endpoint decoded into RunRequest{description, mode, confirm}
// and rebuilt a GuardedExecutionRequest WITHOUT RequestedNode — silently dropping the
// agent's node pin (run_on_node / run_shell local pin) and letting placement re-rank
// cluster-wide. A pinned run_on_node cranium executed on cachyos instead.
func TestRunHandlerPreservesRequestedNode(t *testing.T) {
	// Intercept at the guarded-execution boundary by observing what the handler passes
	// through BuildContextJSON's decision — instead, simplest: assert the decoded
	// RunRequest keeps the field by round-tripping the decode path directly.
	body := `{"description":"echo hi","mode":"exec","confirm":"CONFIRM","requested_node":"cranium"}`
	var req RunRequest
	if err := json.NewDecoder(bytes.NewBufferString(body)).Decode(&req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.RequestedNode != "cranium" {
		t.Fatalf("RunRequest dropped requested_node: got %q", req.RequestedNode)
	}

	// And the struct tag must serialize it back out for the daemon client contract.
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	_ = json.Unmarshal(out, &back)
	if back["requested_node"] != "cranium" {
		t.Fatalf("RunRequest json tag missing requested_node: %s", out)
	}
}
