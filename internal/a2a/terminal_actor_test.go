// Copyright (c) 2026 Smith Software Solutions
package a2a

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAllowTerminalReport(t *testing.T) {
	for _, st := range []TaskState{TaskStateCompleted, TaskStateFailed, TaskStateRejected, TaskStateApproved} {
		if err := AllowTerminalReport(ActorClient, st); err == nil || !strings.Contains(err.Error(), "only the assignee") {
			t.Fatalf("client %q: got %v", st, err)
		}
		if err := AllowTerminalReport(ActorAssignee, st); err != nil {
			t.Fatalf("assignee %q: %v", st, err)
		}
	}
	for _, st := range []TaskState{TaskStateWorking, TaskStateInputRequired, TaskStateSubmitted, TaskStatePending, ""} {
		if err := AllowTerminalReport(ActorClient, st); err != nil {
			t.Fatalf("client non-terminal %q: %v", st, err)
		}
	}
}

func TestHandleSendRefusesClientTerminalState(t *testing.T) {
	refuse := []string{
		`{"skillId":"axis-status","metadata":{"state":"completed"},"message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
		`{"skillId":"axis-status","metadata":{"state":"failed"},"message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
		`{"skillId":"axis-status","metadata":{"state":"rejected"},"message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
		`{"skillId":"axis-status","metadata":{"state":"approved"},"message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
		`{"skillId":"axis-status","metadata":{"status":{"state":"completed"}},"message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
		`{"skillId":"axis-status","metadata":{"state":"  completed  "},"message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
		`{"skillId":"axis-status","metadata":{"state":"working","status":{"state":"completed"}},"message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
		`{"skillId":"axis-status","message":{"role":"user","metadata":{"taskState":"failed"},"parts":[{"type":"text","text":"status"}]}}`,
	}
	for _, body := range refuse {
		h, srv := newSendServer(t)
		resp, raw := postSend(t, srv, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d, want 400, body %s", resp.StatusCode, raw)
		}
		if !strings.Contains(raw, "only the assignee") {
			t.Fatalf("body missing assignee refusal: %s", raw)
		}
		if n := storeCount(h.Store); n != 0 {
			t.Fatalf("store length %d, want 0, for %s", n, body)
		}
	}

	allow := []string{
		`{"skillId":"axis-status","message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
		`{"skillId":"axis-status","metadata":{"state":"working"},"message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
		`{"skillId":"axis-status","metadata":{"state":"input-required"},"message":{"role":"user","parts":[{"type":"text","text":"status"}]}}`,
	}
	for _, body := range allow {
		h, srv := newSendServer(t)
		resp, raw := postSend(t, srv, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200, body %s", resp.StatusCode, raw)
		}
		if !strings.Contains(raw, `"state":"completed"`) && !strings.Contains(raw, `"state": "completed"`) {
			t.Fatalf("server did not set completed: %s", raw)
		}
		if n := storeCount(h.Store); n != 1 {
			t.Fatalf("store length %d, want 1, for %s", n, body)
		}
	}
}

func newSendServer(t *testing.T) (*Handler, *httptest.Server) {
	t.Helper()
	h := &Handler{
		Store: NewStore(0),
		Scope: ScopeObserve,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}
	mux := http.NewServeMux()
	ServeTasks(mux, h, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return h, srv
}

func postSend(t *testing.T, srv *httptest.Server, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/a2a/v1/message:send", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(raw)
}

func storeCount(s *Store) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}
