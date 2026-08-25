package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAxisRunTaskToolValidation(t *testing.T) {
	tc := NewToolContext(&RuntimeView{}, nil)
	r := NewToolRegistry(tc)

	if !r.HasTool("axis_run_task") {
		t.Fatal("expected registry to have axis_run_task tool")
	}

	// Test 1: missing description
	_, err := r.Execute(context.Background(), "axis_run_task", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing description")
	}
	if !strings.Contains(err.Error(), "requires a non-empty") {
		t.Errorf("expected non-empty description error, got: %v", err)
	}

	// Test 2: malformed JSON arguments
	_, err = r.Execute(context.Background(), "axis_run_task", json.RawMessage(`{malformed}`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}

	// Test 3: correct arguments but direct call (must return safety gate warning)
	_, err = r.Execute(context.Background(), "axis_run_task", json.RawMessage(`{"description":"run some command"}`))
	if err == nil {
		t.Fatal("expected error for direct execution of axis_run_task")
	}
	if !strings.Contains(err.Error(), "must be dispatched through the agent safety gate") {
		t.Errorf("expected safety gate error, got: %v", err)
	}
}

func TestFleetExecToolValidation(t *testing.T) {
	tc := NewToolContext(&RuntimeView{}, nil)
	r := NewToolRegistry(tc)

	if !r.HasTool("fleet_exec") {
		t.Fatal("expected registry to have fleet_exec tool")
	}

	_, err := r.Execute(context.Background(), "fleet_exec", json.RawMessage(`{"command":"uptime","nodes":["all"]}`))
	if err == nil || !strings.Contains(err.Error(), "must be dispatched through the agent safety gate") {
		t.Errorf("expected safety gate dispatch error, got: %v", err)
	}
}

func TestRemoteWriteFileValidation(t *testing.T) {
	tc := NewToolContext(&RuntimeView{}, nil)
	r := NewToolRegistry(tc)

	if !r.HasTool("remote_write_file") {
		t.Fatal("expected registry to have remote_write_file tool")
	}

	_, err := r.Execute(context.Background(), "remote_write_file", json.RawMessage(`{"node":""}`))
	if err == nil || !strings.Contains(err.Error(), "requires \"node\" and \"path\"") {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestRemoteTailLogsValidation(t *testing.T) {
	tc := NewToolContext(&RuntimeView{}, nil)
	r := NewToolRegistry(tc)

	if !r.HasTool("remote_tail_logs") {
		t.Fatal("expected registry to have remote_tail_logs tool")
	}

	_, err := r.Execute(context.Background(), "remote_tail_logs", json.RawMessage(`{"node":""}`))
	if err == nil || !strings.Contains(err.Error(), "requires \"node\"") {
		t.Errorf("expected validation error, got: %v", err)
	}
}
