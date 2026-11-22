# `mcpclient` TLS certificate pinning, secret header resolution, and Streamable HTTP `axis mcp serve`

Date: 2026-11-22. Status: draft spec for `feat/mcpclient-tls-pin-and-http-serve`; no feature code yet.

## Problem

Three gaps block `axis` from interoperating securely over Streamable HTTP with local MCP servers (such as native self-signed local MCP endpoints) without leaking credentials into `~/.axis/nodes.yaml`:

1. **Self-signed local HTTPS servers fail TLS verification.** `internal/mcpclient/connection.go` builds its HTTP client (`newMCPHTTPClient`) with Go's default `http.Transport`, which rejects self-signed leaf certificates (`x509: certificate signed by unknown authority`). Blindly disabling TLS verification would expose bearer tokens and tool traffic to MITM on LAN/loopback; `config.MCPServerConfig` currently has no certificate fingerprint field.
2. **HTTP MCP auth headers require literal secrets in `nodes.yaml`.** `connectHTTP` passes `cfg.Headers` verbatim to `transport.WithHTTPHeaders`. Unlike `AIProviderConfig` (which uses `api_key_env` / `api_key_file` via `internal/secrets.Resolve`), `MCPServerConfig` has no `bearer_token_env` / `bearer_token_file` field and does not expand `${ENV_VAR}` placeholders in header values, pushing users to put raw tokens into `nodes.yaml`.
3. **`axis mcp serve` only supports stdio.** `cmd/axis/mcp.go` rejects `--transport http` (`unsupported transport "http": only stdio is implemented`), even though `github.com/mark3labs/mcp-go v1.5.0` already ships `server.NewStreamableHTTPServer`. External HTTP-only MCP clients cannot connect to `axis`'s 21 MCP tools over Streamable HTTP `/mcp`.

## Non-goals

- No UDP multicast discovery, Bonjour/mDNS browsing, device pairing flows, or new top-level `axis app` CLI command. Those require a separate wire-protocol spec and public-boundary review.
- No mounting `/mcp` onto `axis serve` (`:42425`); `axis mcp serve --transport http` runs as its own explicit command on a separate default loopback port (`127.0.0.1:42426`).
- No TLS termination flags (`--tls-cert`/`--tls-key`) on `axis mcp serve --transport http`; operators needing TLS in front of `axis mcp serve` put a reverse proxy in front of loopback.
- No changes to `axis doctor`'s `doctorProbeHTTP` signature or probe behavior in this change (separate follow-up if needed).
- No change to `axis agent`'s external MCP approval contract: `internal/agent/tools_mcp.go` already enforces exact `auto_approve_tools` matching and ignores server-supplied tool annotations; this change only adds a regression test pinning that behavior for read-only vs write tools.
- No writes to `~/.axis/nodes.yaml` or `~/.axis/ai.yaml`.

## Design

### 1. `config.MCPServerConfig` schema and validation (`internal/config/config.go`)

Add three optional fields to `MCPServerConfig`:

```go
type MCPServerConfig struct {
    Transport          string            `json:"transport" yaml:"transport"`
    Command            []string          `json:"command,omitempty" yaml:"command,omitempty"`
    URL                string            `json:"url,omitempty" yaml:"url,omitempty"`
    Headers            map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
    BearerTokenEnv     string            `json:"bearer_token_env,omitempty" yaml:"bearer_token_env,omitempty"`
    BearerTokenFile    string            `json:"bearer_token_file,omitempty" yaml:"bearer_token_file,omitempty"`
    TLSCertFingerprint string            `json:"tls_cert_fingerprint,omitempty" yaml:"tls_cert_fingerprint,omitempty"`
    AutoApproveTools   []string          `json:"auto_approve_tools,omitempty" yaml:"auto_approve_tools,omitempty"`
}
```

Add `ParseTLSCertFingerprint(raw string) ([32]byte, error)` in `internal/config`:
- Trims surrounding whitespace.
- Strips one optional case-insensitive `sha256:` prefix.
- Accepts either 64 contiguous hex characters or colon-separated hex pairs/groups with no leading, trailing, or consecutive colons (`::`).
- Decodes to `[32]byte` via `encoding/hex`; rejects any other length or non-hex character.

Extend `Config.Validate()` to validate `c.MCPServers` in deterministic sorted-name order:
- If `TLSCertFingerprint != ""`:
  - `ParseTLSCertFingerprint` must succeed.
  - `Transport` must be `"http"` (case-insensitive).
  - If `URL != ""`, it must parse with scheme `"https"`.
- If `BearerTokenEnv != ""`:
  - Must match `^[A-Za-z_][A-Za-z0-9_]*$`.
- If `BearerTokenEnv != ""` or `BearerTokenFile != ""`:
  - `Transport` must be `"http"` (case-insensitive).
  - `Headers` must not also contain an `Authorization` header (case-insensitive) to avoid ambiguous credentials.

### 2. Pinned TLS and secret header resolution (`internal/mcpclient/connection.go`)

- **`NewHTTPClient(cfg config.MCPServerConfig) (*http.Client, error)`:**
  - Keeps the existing same-origin redirect policy (`maxMCPRedirects = 10`, same scheme and host).
  - When `cfg.TLSCertFingerprint == ""`, returns the standard client with no custom `TLSClientConfig`.
  - When `cfg.TLSCertFingerprint != ""`:
    - Parses `cfg.URL` and requires scheme `"https"`.
    - Parses the expected digest with `config.ParseTLSCertFingerprint`.
    - Clones `http.DefaultTransport.(*http.Transport)` (or creates `&http.Transport{}`) and sets `TLSClientConfig`:
      - `MinVersion: tls.VersionTLS12`
      - `InsecureSkipVerify: true` (strictly paired with `VerifyPeerCertificate`, never used without pin verification)
      - `VerifyPeerCertificate`: rejects empty `rawCerts`, parses `rawCerts[0]` with `x509.ParseCertificate`, computes `sha256.Sum256(rawCerts[0])`, and compares against the expected digest with `subtle.ConstantTimeCompare`. Any mismatch fails the handshake with a descriptive error.
- **`ResolveHeaders(cfg config.MCPServerConfig) (map[string]string, error)`:**
  - Never mutates `cfg.Headers`.
  - Expands `${VAR}` tokens (matching `\$\{([A-Za-z_][A-Za-z0-9_]*)\}`) in header values via `os.LookupEnv`. If a referenced env var is unset or whitespace-only, returns an error naming the variable (never a secret value). If an unexpanded `${` remains after substitution (for example malformed `${BAD-NAME}` or unclosed `${VAR`), fails closed with a syntax error.
  - If `cfg.BearerTokenEnv != ""` or `cfg.BearerTokenFile != ""`:
    - Rejects if `cfg.Headers` already has an `Authorization` key (case-insensitive).
    - Resolves via `internal/secrets.Resolve(cfg.BearerTokenEnv, cfg.BearerTokenFile)` and fails closed if not found or unreadable (never including any secret value in errors).
    - Sets `Authorization: Bearer <token>` on the returned header map.
- **`connectHTTP`:**
  - Retains `netutil.ValidateOutboundURL(cfg.URL)` unchanged.
  - Calls `NewHTTPClient(cfg)` and `ResolveHeaders(cfg)` before constructing `mcpclient.NewStreamableHttpClient`.

### 3. Streamable HTTP transport for `axis mcp serve` (`internal/mcp/server.go`, `cmd/axis/mcp.go`)

- In `internal/mcp/server.go`:
  - `NewStreamableHTTPHandler(cached bool, cacheAddr string, token string) http.Handler` wraps `mcpserver.NewStreamableHTTPServer(NewServer(cached, cacheAddr, nil), mcpserver.WithEndpointPath("/mcp"))` mounted at `/mcp` on a dedicated `http.ServeMux`. When `token != ""`, a middleware requires `Authorization: Bearer <token>` compared via `subtle.ConstantTimeCompare` and otherwise returns `401 Unauthorized` with `WWW-Authenticate: Bearer`.
  - `ServeStreamableHTTP(ctx context.Context, addr string, cached bool, cacheAddr string, tokenEnv string) error`:
    - Validates `addr` with `net.SplitHostPort`.
    - When `tokenEnv != ""`, resolves the bearer token via `secrets.Resolve(tokenEnv, "")` and fails closed if unset or empty.
    - Refuses to bind a non-loopback host (`host` not `"localhost"` and not a loopback IP) unless `token != ""` (fail closed against accidental unauthenticated LAN/wildcard exposure).
    - Runs `http.Server` (`ReadHeaderTimeout: 10 * time.Second`) and shuts down cleanly when `ctx` is canceled.
- In `cmd/axis/mcp.go`:
  - Add `--addr` (default `"127.0.0.1:42426"`) and `--token-env` (default `""`) to `axis mcp serve`.
  - Update `--transport` to accept `"stdio"` (default) and `"http"`; reject any other value with `unsupported transport %q: supported transports are stdio, http`.
  - Add `var serveMCPHTTP = axismcp.ServeStreamableHTTP` test seam matching `serveMCPStdio`.
- Update `AGENTS.md` CLI table row for `axis mcp serve` to note stdio or Streamable HTTP (preserving the exact 29-command / 21-tool doc-fact counts).

### 4. Tests

- `internal/config/mcp_config_test.go`:
  - `ParseTLSCertFingerprint`: valid bare hex, uppercase/colon-separated, `sha256:` prefix; rejects wrong length, non-hex, leading/trailing/double colons, empty.
  - `Config.Validate`: rejects invalid fingerprint, fingerprint on `http://` or `stdio`, invalid `bearer_token_env` name, `bearer_token_env`/`bearer_token_file` on `stdio`, and conflicting `Authorization` header with `bearer_token_env`.
- `internal/mcpclient/connection_test.go`:
  - Self-signed `httptest.NewTLSServer` fails without pin, succeeds with matching `tls_cert_fingerprint` (including colon-separated / `sha256:` formats), and fails with wrong pin.
  - Rejects `tls_cert_fingerprint` on `http://` URL.
  - Preserves cross-origin and scheme-downgrade redirect blocking when TLS pin is active.
  - `ResolveHeaders`: expands `${VAR}` without mutating `cfg.Headers`; fails closed on unset/blank `${VAR}` and malformed `${...}`; resolves `bearer_token_env` and `bearer_token_file` via `secrets.Resolve`; rejects conflicting `Authorization` header; end-to-end `connectHTTP` over pinned TLS + `bearer_token_env` against an in-process `NewStreamableHTTPHandler` MCP server!
- `internal/mcp/http_test.go`:
  - Streamable HTTP `/mcp` initialize + `tools/list` (verifying 21 tools) over `httptest.NewServer(NewStreamableHTTPHandler(...))`.
  - Bearer auth middleware: rejects missing/wrong token with 401 and accepts matching token.
  - `ServeStreamableHTTP`: refuses non-loopback `--addr` without `--token-env`; fails closed when `--token-env` is unset; binds loopback `127.0.0.1:0` and shuts down cleanly on context cancel.
- `cmd/axis/command_surface_test.go`:
  - Update unsupported-transport test (`--transport sse`) and add tests verifying `axis mcp serve --transport http --addr ... --token-env ...` invokes `serveMCPHTTP`.
- `internal/agent/mcp_approval_test.go`:
  - Regression test verifying read-only tools in `auto_approve_tools` auto-approve while unlisted `propose_library_item` requires confirmation even when the server sets `ReadOnlyHint: true`.

## Alternatives

- **Custom CA bundle (`tls_ca_file`) instead of leaf fingerprint pinning.** Useful for private PKIs, but local app servers typically generate a single self-signed leaf certificate without exporting a separate CA chain; leaf SHA-256 pinning requires one string in YAML and zero extra files. A future `tls_ca_file` field remains additive.
- **Boolean `tls_insecure_skip_verify: true`.** Rejected: disables authenticity checks completely and exposes bearer tokens to active MITM.
- **Mount `/mcp` on `axis serve` (`:42425`) instead of `axis mcp serve --transport http`.** `axis serve` has its own daemon lifecycle, token source, and REST route table; keeping Streamable HTTP on `axis mcp serve --transport http` reuses `NewServer` directly, keeps `--cached`/`--cache-addr` parity with stdio, and avoids coupling daemon HTTP routes to MCP session state.
- **Only `${VAR}` header expansion without `bearer_token_env`.** Explicit `bearer_token_env` / `bearer_token_file` matches `AIProviderConfig.APIKeyEnv` / `APIKeyFile` and reuses `internal/secrets.Resolve`, while `${VAR}` expansion also supports existing client snippets that write `Authorization: "Bearer ${TOKEN}"`.

## Research

- `github.com/mark3labs/mcp-go v1.5.0` (already in `go.mod`, zero new dependencies) provides `server.NewStreamableHTTPServer` implementing the MCP 2025-03-26 Streamable HTTP transport (`https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#streamable-http`).
- Go's `crypto/tls` standard pattern for custom certificate pinning without system CA trust sets `InsecureSkipVerify: true` together with a non-nil `VerifyPeerCertificate` callback (`https://pkg.go.dev/crypto/tls#Config`), which still runs the TLS handshake and delegates chain/leaf verification to `VerifyPeerCertificate` before returning from `Dial`.
- Existing AXIS secret-resolution convention: `internal/config/config.go` (`AIProviderConfig.APIKeyEnv`, `APIKeyFile`) + `internal/secrets/secrets.go` (`secrets.Resolve`).
- Optimization: none needed; SHA-256 over a single DER leaf certificate runs once per TLS handshake (~microseconds) and `ResolveHeaders` runs once per `connectHTTP` call.

## Review

(Pending fresh-context spec review before implementation.)
