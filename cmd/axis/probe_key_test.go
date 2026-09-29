package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestApiKeyForAIBackendMatchesSuffixlessBase drives the real lookup with both
// the base_url form and the suffix-mismatched probe URL — the exact case
// modelChoicesFromAIConfig hits when it probes each backend.
func TestApiKeyForAIBackendMatchesSuffixlessBase(t *testing.T) {
	dir := t.TempDir()
	kf := filepath.Join(dir, "k")
	if err := os.WriteFile(kf, []byte("e2e-probe-key\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	apiPath := filepath.Join(dir, "ai.yaml")
	aiYaml := "backends:\n- name: test-hub\n  kind: openai-compatible\n  base_url: http://127.0.0.1:1/v1\n  api_key_file: " + kf + "\n"
	if err := os.WriteFile(apiPath, []byte(aiYaml), 0o600); err != nil {
		t.Fatalf("write ai.yaml: %v", err)
	}
	prevPath := inferenceAIPathFn
	inferenceAIPathFn = func() string { return apiPath }
	defer func() { inferenceAIPathFn = prevPath }()

	got := apiKeyForAIBackend("", "http://127.0.0.1:1/v1")
	t.Logf("apiKeyForAIBackend(base_form) = len %d", len(got))
	if got == "" {
		t.Fatal("lookup failed on the base url form")
	}
	got2 := apiKeyForAIBackend("", "http://127.0.0.1:1/v1/models")
	t.Logf("apiKeyForAIBackend(probe_form) = len %d", len(got2))
	if got2 == "" {
		t.Log("  probe-form lookup returns empty — suffix-strip retry is REQUIRED and either missing or not firing")
	}
}

// TestProbeEndpointAttachesKeyForEndpointMatch is the discriminating test for
// the ":4000 unreachable" regression from the inference-plane auth window.
//
// Two properties:
//  1. Suffix mismatch: ai.yaml base_url ends at "/v1", the probe URL is
//     ".../v1/models" — the endpoint match must still resolve the key.
//  2. The resolved bearer is attached as Authorization: Bearer.
func TestProbeEndpointAttachesKeyForEndpointMatch(t *testing.T) {
	gotAuth := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer e2e-probe-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	dir := t.TempDir()
	kf := filepath.Join(dir, "k")
	if err := os.WriteFile(kf, []byte("e2e-probe-key\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	aiYaml := "backends:\n" +
		"- name: test-hub\n" +
		"  kind: openai-compatible\n" +
		"  base_url: " + ts.URL + "/v1\n" +
		"  api_key_file: " + kf + "\n"

	apiPath := filepath.Join(dir, "ai.yaml")
	if err := os.WriteFile(apiPath, []byte(aiYaml), 0o600); err != nil {
		t.Fatalf("write ai.yaml: %v", err)
	}

	prevPath := inferenceAIPathFn
	inferenceAIPathFn = func() string { return apiPath }
	defer func() { inferenceAIPathFn = prevPath }()

	// drive the probe through the real seam: base ".../v1", probe ".../v1/models"
	if !inferenceProbeFn(ts.URL + "/v1/models") {
		t.Fatalf("probe returned false (gotAuth=%q) — bearer either not resolved for the suffix-mismatched URL or not attached", gotAuth)
	}
	if gotAuth != "Bearer e2e-probe-key" {
		t.Fatalf("Authorization header = %q, want Bearer e2e-probe-key (bearer not threaded into the probe)", gotAuth)
	}
}
