// Copyright (c) 2026 Smith Software Solutions
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/a2a"
)

// TestA2ASlice2E2E is the slice-2v1 end-to-end harness (not unit-tests-after-code).
// It starts ServeWithContext on a temp unix socket with a NON-EMPTY token +
// cardFn and writes a verifiable JSON artifact covering F1–F5.
// F9: token must be non-empty so withAuth is not a no-op (same policy as /run).
// F6-lite: wrong-bearer GET is proven as F1 (401 at withAuth); store principal
// binding exists in code but this E2E does not prove multi-principal isolation.
func TestA2ASlice2E2E(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "axis-a2a-slice2.sock")
	token := "test-token-a2a-slice2-v1"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = ServeWithContext(ctx, socketPath, nil, token, false, func() a2a.AgentCard {
			return a2a.Card(a2a.CardOptions{
				Name:    "a2a-slice2-e2e",
				Version: "test",
				Scope:   a2a.ScopeObserve,
			})
		})
	}()

	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, time.Second)
		if err == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("unix socket never accepted (F3): %s", socketPath)
	}

	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", socketPath, 2*time.Second)
			},
		},
	}

	sha := gitHeadSHA()
	stamp := time.Now().UTC().Format("20060102T150405Z")
	if len(sha) >= 7 {
		stamp = sha[:7]
	}

	artifact := map[string]any{
		"slice":         "a2a-slice2v1",
		"listener":      "unix",
		"binding":       "POST /a2a/v1/message:send + GET /a2a/v1/tasks/{id}",
		"binding_note":  "HTTP+JSON under /a2a/v1/ matching A2A 1.0 naming; proposal-literal tasks/send is a future alias; no TCK claim",
		"owner_surface": a2a.OwnerSurfaceA2ATask,
		"head_sha":      sha,
		"started_at":    time.Now().Format(time.RFC3339),
		"failure_modes": []string{"F1", "F2", "F3", "F4", "F5", "F6-lite"},
		// F6-lite receipt = F1 foreign-bearer 401; store is principal-hash bound
		// but E2E did not exercise Store.Get cross-principal mismatch.
		"f6_lite_note":  "F1 foreign-bearer + store principal binding present (unproven multi-principal)",
		"no_fleet_roll": true,
		"no_tcp_opened": true,
		"streaming":     false,
	}

	// --- Card public (F1 contrast) ---
	cardResp, err := httpClient.Get("http://axis/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("GET card: %v", err)
	}
	cardBody, _ := io.ReadAll(cardResp.Body)
	cardResp.Body.Close()
	artifact["card_http_status"] = cardResp.StatusCode
	if cardResp.StatusCode != http.StatusOK {
		t.Fatalf("card status = %d, want 200", cardResp.StatusCode)
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(cardBody, &card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	skillIDs := make([]string, 0, len(card.Skills))
	for _, s := range card.Skills {
		skillIDs = append(skillIDs, s.ID)
	}
	artifact["skill_ids"] = skillIDs
	artifact["streaming"] = card.Capabilities.Streaming
	if card.Capabilities.Streaming {
		t.Fatal("card Streaming must remain false")
	}

	// --- F1: unauth task send → 401 ---
	unauthReq, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/message:send", strings.NewReader(
		`{"skillId":"axis-status","message":{"role":"user","parts":[{"type":"text","text":"ping"}]}}`,
	))
	unauthReq.Header.Set("Content-Type", "application/json")
	unauthResp, err := httpClient.Do(unauthReq)
	if err != nil {
		t.Fatalf("unauth send: %v", err)
	}
	unauthResp.Body.Close()
	artifact["task_unauth_status"] = unauthResp.StatusCode
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth task send = %d, want 401 (F1)", unauthResp.StatusCode)
	}

	// --- F5: observe axis-status via read helpers only ---
	obsReq, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/message:send", strings.NewReader(
		`{"skillId":"axis-status","message":{"role":"user","parts":[{"type":"text","text":"status canary"}]}}`,
	))
	obsReq.Header.Set("Content-Type", "application/json")
	obsReq.Header.Set("Authorization", "Bearer "+token)
	obsResp, err := httpClient.Do(obsReq)
	if err != nil {
		t.Fatalf("observe send: %v", err)
	}
	obsRaw, _ := io.ReadAll(obsResp.Body)
	obsResp.Body.Close()
	artifact["task_observe_http_status"] = obsResp.StatusCode
	if obsResp.StatusCode != http.StatusOK {
		t.Fatalf("observe send status = %d body=%s", obsResp.StatusCode, obsRaw)
	}
	var obsTask a2a.Task
	if err := json.Unmarshal(obsRaw, &obsTask); err != nil {
		t.Fatalf("decode observe task: %v", err)
	}
	artifact["task_observe_status"] = string(obsTask.Status.State)
	artifact["task_id"] = obsTask.ID
	canary := ""
	if len(obsTask.Artifacts) > 0 && len(obsTask.Artifacts[0].Parts) > 0 {
		canary = obsTask.Artifacts[0].Parts[0].Text
	}
	artifact["task_observe_artifact_canary"] = strings.Contains(canary, "a2a-slice2-observe")
	artifact["task_observe_artifact_preview"] = truncateStr(canary, 240)
	if obsTask.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("observe state = %s, want completed", obsTask.Status.State)
	}
	if !strings.Contains(canary, "a2a-slice2-observe") {
		t.Fatalf("observe artifact missing canary: %q", canary)
	}
	if meta, _ := obsTask.Metadata["ownerSurface"].(string); meta != a2a.OwnerSurfaceA2ATask {
		t.Fatalf("ownerSurface = %v, want %s", obsTask.Metadata["ownerSurface"], a2a.OwnerSurfaceA2ATask)
	}

	getReq, _ := http.NewRequest(http.MethodGet, "http://axis/a2a/v1/tasks/"+obsTask.ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	getResp, err := httpClient.Do(getReq)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	getResp.Body.Close()
	artifact["task_get_status"] = getResp.StatusCode
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get own task = %d, want 200", getResp.StatusCode)
	}

	// --- F2 / F4: guarded-exec rejected fail-closed ---
	execReq, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/message:send", strings.NewReader(
		`{"skillId":"guarded-exec","confirm":"YES","message":{"role":"user","parts":[{"type":"text","text":"echo should-not-run"}]}}`,
	))
	execReq.Header.Set("Content-Type", "application/json")
	execReq.Header.Set("Authorization", "Bearer "+token)
	execResp, err := httpClient.Do(execReq)
	if err != nil {
		t.Fatalf("exec send: %v", err)
	}
	execRaw, _ := io.ReadAll(execResp.Body)
	execResp.Body.Close()
	var execTask a2a.Task
	_ = json.Unmarshal(execRaw, &execTask)
	artifact["task_exec_http_status"] = execResp.StatusCode
	artifact["task_exec_rejected"] = execTask.Status.State == a2a.TaskStateRejected
	artifact["task_exec_state"] = string(execTask.Status.State)
	if execTask.Status.State != a2a.TaskStateRejected {
		t.Fatalf("guarded-exec state = %s, want rejected (F2/F4)", execTask.Status.State)
	}

	unkReq, _ := http.NewRequest(http.MethodPost, "http://axis/a2a/v1/message:send", strings.NewReader(
		`{"skillId":"not-a-real-skill","message":{"role":"user","parts":[{"type":"text","text":"x"}]}}`,
	))
	unkReq.Header.Set("Content-Type", "application/json")
	unkReq.Header.Set("Authorization", "Bearer "+token)
	unkResp, err := httpClient.Do(unkReq)
	if err != nil {
		t.Fatalf("unknown skill send: %v", err)
	}
	unkRaw, _ := io.ReadAll(unkResp.Body)
	unkResp.Body.Close()
	var unkTask a2a.Task
	_ = json.Unmarshal(unkRaw, &unkTask)
	artifact["task_unknown_rejected"] = unkTask.Status.State == a2a.TaskStateRejected
	if unkTask.Status.State != a2a.TaskStateRejected {
		t.Fatalf("unknown skill state = %s, want rejected (F4)", unkTask.Status.State)
	}

	// --- F6-lite / F1: wrong bearer → 401 at withAuth (does not reach Store.Get) ---
	foreignReq, _ := http.NewRequest(http.MethodGet, "http://axis/a2a/v1/tasks/"+obsTask.ID, nil)
	foreignReq.Header.Set("Authorization", "Bearer foreign-token-not-valid")
	foreignResp, err := httpClient.Do(foreignReq)
	if err != nil {
		t.Fatalf("foreign get: %v", err)
	}
	foreignResp.Body.Close()
	artifact["foreign_principal_get_status"] = foreignResp.StatusCode
	artifact["foreign_get_note"] = "wrong bearer rejected by withAuth (F1); not a Store.Get principal-isolation proof"
	if foreignResp.StatusCode != http.StatusUnauthorized &&
		foreignResp.StatusCode != http.StatusForbidden &&
		foreignResp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign get = %d, want 401/403/404 (F1 foreign-bearer / F6-lite)", foreignResp.StatusCode)
	}

	artifact["f3_unix_dial_ok"] = true
	artifact["passed"] = true
	artifact["finished_at"] = time.Now().Format(time.RFC3339)

	payload, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}

	name := fmt.Sprintf("a2a-slice2-e2e-%s.json", stamp)
	repoRoot, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	root := strings.TrimSpace(string(repoRoot))
	targets := []string{
		filepath.Join(root, "artifacts", name),
		filepath.Join(root, "artifacts", "a2a-slice2-e2e-latest.json"),
		filepath.Join("/workspace/axis-eval", name),
	}
	written := make([]string, 0, len(targets))
	for _, p := range targets {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Logf("mkdir %s: %v", p, err)
			continue
		}
		if err := os.WriteFile(p, payload, 0o644); err != nil {
			t.Logf("write %s: %v", p, err)
			continue
		}
		written = append(written, p)
		t.Logf("wrote E2E artifact: %s", p)
	}
	if len(written) == 0 {
		t.Fatal("failed to write any E2E artifact")
	}
	artifact["artifact_paths"] = written
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// gitHeadSHA returns git rev-parse HEAD of the repository under test.
// Run this harness on the committed tip (after the feature commit) so the
// artifact filename and head_sha match the PR head, not an earlier base SHA.
func gitHeadSHA() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
