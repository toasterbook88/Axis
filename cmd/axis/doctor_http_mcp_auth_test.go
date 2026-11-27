package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

// TestDoctorMCPProbeUsesClientAuthAndPin covers a pinned self-signed server
// that needs a bearer token and a ${VAR} header: the setup #678 exists for.
// Doctor must send the same headers and trust the same pin as the MCP client.
func TestDoctorMCPProbeUsesClientAuthAndPin(t *testing.T) {
	t.Setenv("DOCTOR_TEST_TOKEN", "tok-123")
	t.Setenv("DOCTOR_TEST_TENANT", "acme")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" || r.Header.Get("X-Tenant") != "acme" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()
	sum := sha256.Sum256(server.Certificate().Raw)
	pin := "sha256:" + hex.EncodeToString(sum[:])

	cfg := config.MCPServerConfig{
		Transport:          "http",
		URL:                server.URL + "/mcp",
		Headers:            map[string]string{"X-Tenant": "${DOCTOR_TEST_TENANT}"},
		BearerTokenEnv:     "DOCTOR_TEST_TOKEN",
		TLSCertFingerprint: pin,
	}
	status, err := probeDoctorMCPEndpoint(context.Background(), cfg, []string{"127.0.0.1"})
	if err != nil || status != http.StatusMethodNotAllowed {
		t.Fatalf("pinned bearer probe: status=%d err=%v", status, err)
	}

	wrong := cfg
	wrong.TLSCertFingerprint = "sha256:" + strings.Repeat("00", 32)
	if _, err := probeDoctorMCPEndpoint(context.Background(), wrong, []string{"127.0.0.1"}); err == nil ||
		!strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("wrong pin: err=%v, want fingerprint mismatch", err)
	}

	unset := cfg
	unset.BearerTokenEnv = "DOCTOR_TEST_TOKEN_UNSET"
	if _, err := probeDoctorMCPEndpoint(context.Background(), unset, []string{"127.0.0.1"}); err == nil ||
		!strings.Contains(err.Error(), "resolve headers") {
		t.Fatalf("unset token: err=%v, want resolve headers error", err)
	}
}
