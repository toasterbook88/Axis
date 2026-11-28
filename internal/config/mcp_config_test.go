package config

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTLSCertFingerprint(t *testing.T) {
	rawSum := sha256.Sum256([]byte("test-leaf-cert"))
	bare := hex.EncodeToString(rawSum[:])
	upper := strings.ToUpper(bare)

	var colons []string
	for i := 0; i < len(upper); i += 2 {
		colons = append(colons, upper[i:i+2])
	}
	colonHex := strings.Join(colons, ":")

	valid := []struct {
		name string
		in   string
	}{
		{name: "bare lowercase", in: bare},
		{name: "bare uppercase", in: upper},
		{name: "colon separated uppercase", in: colonHex},
		{name: "sha256 prefix lowercase", in: "sha256:" + bare},
		{name: "SHA256 prefix with colons and spaces", in: "  SHA256: " + colonHex + "  "},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTLSCertFingerprint(tc.in)
			if err != nil {
				t.Fatalf("ParseTLSCertFingerprint(%q) unexpected error: %v", tc.in, err)
			}
			if got != rawSum {
				t.Fatalf("ParseTLSCertFingerprint(%q) = %x, want %x", tc.in, got, rawSum)
			}
		})
	}

	invalid := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "prefix only", in: "sha256:"},
		{name: "too short", in: bare[:62]},
		{name: "too long", in: bare + "aa"},
		{name: "non hex", in: strings.Repeat("z", 64)},
		{name: "leading colon", in: ":" + colonHex},
		{name: "trailing colon", in: colonHex + ":"},
		{name: "double colon", in: strings.Replace(colonHex, ":", "::", 1)},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseTLSCertFingerprint(tc.in); err == nil {
				t.Fatalf("ParseTLSCertFingerprint(%q) succeeded, want error", tc.in)
			}
		})
	}
}

func TestConfigValidateMCPServers(t *testing.T) {
	validFP := strings.Repeat("ab", 32)
	baseYAML := `nodes:
  - name: node-a
    hostname: node-a.example.com
    ssh_user: ops
`

	t.Run("valid pinned https with bearer_token_env", func(t *testing.T) {
		y := baseYAML + `allowed_internal_hosts:
  - "127.0.0.1"
mcp_servers:
  local_app:
    transport: http
    url: "https://127.0.0.1:5648/mcp"
    bearer_token_env: LOCAL_MCP_TOKEN
    tls_cert_fingerprint: "sha256:` + validFP + `"
    auto_approve_tools:
      - query_device_info
`
		cfg, err := Parse([]byte(y))
		if err != nil {
			t.Fatalf("Parse valid config: %v", err)
		}
		srv := cfg.MCPServers["local_app"]
		if srv.BearerTokenEnv != "LOCAL_MCP_TOKEN" {
			t.Fatalf("BearerTokenEnv = %q, want LOCAL_MCP_TOKEN", srv.BearerTokenEnv)
		}
		if srv.TLSCertFingerprint != "sha256:"+validFP {
			t.Fatalf("TLSCertFingerprint = %q, want sha256:%s", srv.TLSCertFingerprint, validFP)
		}
	})

	cases := []struct {
		name    string
		snippet string
		wantErr string
	}{
		{
			name: "invalid fingerprint",
			snippet: `mcp_servers:
  bad:
    transport: http
    url: "https://mcp.example.com/mcp"
    tls_cert_fingerprint: "not-hex"
`,
			wantErr: "tls_cert_fingerprint",
		},
		{
			name: "fingerprint on plain http",
			snippet: `mcp_servers:
  bad:
    transport: http
    url: "http://mcp.example.com/mcp"
    tls_cert_fingerprint: "` + validFP + `"
`,
			wantErr: "requires an https URL",
		},
		{
			name: "fingerprint on stdio",
			snippet: `mcp_servers:
  bad:
    transport: stdio
    command: ["echo"]
    tls_cert_fingerprint: "` + validFP + `"
`,
			wantErr: "requires http transport",
		},
		{
			name: "invalid bearer_token_env name",
			snippet: `mcp_servers:
  bad:
    transport: http
    url: "https://mcp.example.com/mcp"
    bearer_token_env: "BAD-NAME"
`,
			wantErr: "invalid bearer_token_env",
		},
		{
			name: "bearer_token_env on stdio",
			snippet: `mcp_servers:
  bad:
    transport: stdio
    command: ["echo"]
    bearer_token_env: "VALID_TOKEN"
`,
			wantErr: "requires http transport",
		},
		{
			name: "bearer_token_file on stdio",
			snippet: `mcp_servers:
  bad:
    transport: stdio
    command: ["echo"]
    bearer_token_file: "/tmp/token"
`,
			wantErr: "requires http transport",
		},
		{
			name: "whitespace-only fingerprint",
			snippet: `mcp_servers:
  bad:
    transport: stdio
    command: ["echo"]
    tls_cert_fingerprint: "   "
`,
			wantErr: "tls_cert_fingerprint is empty",
		},
		{
			name: "whitespace-only bearer_token_file",
			snippet: `mcp_servers:
  bad:
    transport: stdio
    command: ["echo"]
    bearer_token_file: "   "
`,
			wantErr: "bearer_token_file cannot be whitespace-only",
		},
		{
			name: "conflicting Authorization header and bearer_token_env",
			snippet: `mcp_servers:
  bad:
    transport: http
    url: "https://mcp.example.com/mcp"
    bearer_token_env: "VALID_TOKEN"
    headers:
      authorization: "Bearer raw"
`,
			wantErr: "cannot set both bearer_token_env/bearer_token_file and Authorization header",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(baseYAML + tc.snippet))
			if err != nil {
				t.Fatalf("Parse rejected the whole config for one bad mcp_servers entry: %v", err)
			}
			err = cfg.ValidateMCPServers()
			if err == nil {
				t.Fatalf("ValidateMCPServers succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestSaveAtomicRejectsInvalidMCPServer(t *testing.T) {
	cfg, err := Parse([]byte(`nodes:
  - name: node-a
    hostname: node-a.example.com
    ssh_user: ops
mcp_servers:
  bad:
    transport: http
    url: "https://mcp.example.com/mcp"
    tls_cert_fingerprint: "not-hex"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	if _, err := SaveAtomic(path, cfg); err == nil || !strings.Contains(err.Error(), "tls_cert_fingerprint") {
		t.Fatalf("SaveAtomic err = %v, want tls_cert_fingerprint error", err)
	}
}
