# `mcpclient` TLS certificate pinning, secret header resolution, and Streamable HTTP `axis mcp serve`

Date: 2026-11-22. Status: implemented on `feat/mcpclient-tls-pin-and-http-serve`; spec review and both independent diff reviews `approve-with-changes`, all findings resolved below.

## Problem

Three gaps block `axis` from interoperating securely over Streamable HTTP with local MCP servers (such as native self-signed local MCP endpoints) without leaking credentials into `~/.axis/nodes.yaml`:

1. **Self-signed local HTTPS servers fail TLS verification.** `internal/mcpclient/connection.go` builds its HTTP client (`newMCPHTTPClient`) with Go's default `http.Transport`, which rejects self-signed leaf certificates (`x509: certificate signed by unknown authority`). Blindly disabling TLS verification would expose bearer tokens and tool traffic to MITM on LAN/loopback; `config.MCPServerConfig` currently has no certificate fingerprint field.
2. **HTTP MCP auth headers require literal secrets in `nodes.yaml`.** `connectHTTP` passes `cfg.Headers` verbatim to `transport.WithHTTPHeaders`. Unlike `AIProviderConfig` (which uses `api_key_env` / `api_key_file` via `internal/secrets.Resolve`), `MCPServerConfig` has no `bearer_token_env` / `bearer_token_file` field and does not expand `${ENV_VAR}` placeholders in header values, pushing users to put raw tokens into `nodes.yaml`.
3. **`axis mcp serve` only supports stdio.** `cmd/axis/mcp.go` rejects `--transport http` (`unsupported transport "http": only stdio is implemented`), even though `github.com/mark3labs/mcp-go v1.5.0` already ships `server.NewStreamableHTTPServer`. External HTTP-only MCP clients cannot connect to `axis`'s 21 MCP tools over Streamable HTTP `/mcp`.

## Non-goals

- No UDP multicast discovery, Bonjour/mDNS browsing, device pairing flows, or new top-level `axis app` CLI command. Those require a separate wire-protocol spec and public-boundary review.
- No mounting `/mcp` onto `axis serve` (`:42425`); `axis mcp serve --transport http` runs as its own explicit command on a separate default loopback port (`127.0.0.1:42426`).
- No TLS termination flags (`--tls-cert`/`--tls-key`) on `axis mcp serve --transport http`; operators placing a TLS reverse proxy in front of loopback must rewrite the forwarded `Host` header to `localhost` or `127.0.0.1` because `mcp-go`'s default loopback DNS-rebinding check remains enabled.
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
  - Parses and expands `${VAR}` placeholders in a single pass over each raw header template string (never re-scanning substituted env values, so secrets containing literal `${` succeed). Whenever `${` appears in the raw template, it must close with `}` and contain a valid identifier matching `^[A-Za-z_][A-Za-z0-9_]*$`; malformed or unclosed `${...}` templates fail closed with a syntax error. Each referenced env var is looked up via `os.LookupEnv`, trimmed of surrounding whitespace (matching `internal/secrets.Resolve`), and rejected with an error naming only the variable if unset or whitespace-only.
  - If `cfg.BearerTokenEnv != ""` or `cfg.BearerTokenFile != ""`:
    - Rejects if `cfg.Headers` already has an `Authorization` key (case-insensitive).
    - Resolves via `internal/secrets.Resolve(cfg.BearerTokenEnv, cfg.BearerTokenFile)` and fails closed if not found or unreadable (never including any secret value in errors).
    - Sets `Authorization: Bearer <token>` on the returned header map.
- **`connectHTTP`:**
  - Retains `netutil.ValidateOutboundURL(cfg.URL)` unchanged.
  - Calls `NewHTTPClient(cfg)` and `ResolveHeaders(cfg)` before constructing `mcpclient.NewStreamableHttpClient`.

### 3. Streamable HTTP transport for `axis mcp serve` (`internal/mcp/http.go`, `cmd/axis/mcp.go`)

- In `internal/mcp/http.go`:
  - Internal helper `newStreamableHTTP(cached bool, cacheAddr string, token string) (http.Handler, *mcpserver.StreamableHTTPServer)` creates `mcpSrv := NewServer(cached, cacheAddr, nil)` and `streamSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)` (omitting the no-op `WithEndpointPath` option) and mounts `streamSrv` at `/mcp` on a dedicated `http.ServeMux`. `mcp-go`'s default loopback DNS-rebinding check remains enabled. (Keeping `newStreamableHTTP` unexported avoids adding an unreachable exported symbol to `hack/deadcode-allowlist.txt`.)
  - When `token != ""`, a middleware checks `Authorization` using `strings.Fields` and case-insensitive `strings.EqualFold(parts[0], "bearer")` (matching `internal/api/server.go`) plus `subtle.ConstantTimeCompare` against `token`; missing or wrong credentials return `401 Unauthorized` with `WWW-Authenticate: Bearer`.
  - `ServeStreamableHTTP(ctx context.Context, addr string, cached bool, cacheAddr string, tokenEnv string) error`:
    - Validates `addr` with `net.SplitHostPort`.
    - When `tokenEnv != ""`, rejects whitespace-only names and resolves the bearer token via `secrets.Resolve(strings.TrimSpace(tokenEnv), "")`, failing closed if unset or empty.
    - Refuses to bind a non-loopback host (`host` not `"localhost"` and not a loopback IP) unless `token != ""` (fail closed against accidental unauthenticated LAN/wildcard exposure).
    - Runs `http.Server` (`ReadHeaderTimeout: 10 * time.Second`) on a `net.Listen("tcp", addr)` listener. When `ctx` is canceled, runs a bounded 5-second drain calling `streamSrv.CloseSessions(drainCtx)` to release active SSE `GET /mcp` streams alongside `srv.Shutdown(drainCtx)`, falling back to `srv.Close()` if shutdown still blocks.
- In `cmd/axis/mcp.go`:
  - Add `--addr` (default `"127.0.0.1:42426"`) and `--token-env` (default `""`) to `axis mcp serve`.
  - Update `--transport` to accept `"stdio"` (default) and `"http"`; reject any other value with `unsupported transport %q: supported transports are stdio, http`.
  - For `--transport http`, wrap `cmd.Context()` with `signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)` before calling `serveMCPHTTP`, matching `cmd/axis/serve.go`.
  - Add `var serveMCPHTTP = axismcp.ServeStreamableHTTP` test seam matching `serveMCPStdio`.
- Update `AGENTS.md` CLI table row for `axis mcp serve` (preserving the exact 30-command / 21-tool doc-fact counts) and `docs/current-state.md` row for `axis mcp serve` to document both `stdio` and Streamable HTTP transports.

### 4. Tests

- `internal/config/mcp_config_test.go`:
  - `ParseTLSCertFingerprint`: valid bare hex, uppercase/colon-separated, `sha256:` prefix; rejects wrong length, non-hex, leading/trailing/double colons, empty.
  - `Config.Validate`: rejects invalid fingerprint, fingerprint on `http://` or `stdio`, invalid `bearer_token_env` name, `bearer_token_env`/`bearer_token_file` on `stdio`, and conflicting `Authorization` header with `bearer_token_env`.
- `internal/mcpclient/connection_test.go`:
  - Self-signed `httptest.NewTLSServer` fails without pin, succeeds with matching `tls_cert_fingerprint` (including colon-separated / `sha256:` formats), and fails with wrong pin.
  - Rejects `tls_cert_fingerprint` on `http://` URL.
  - Preserves cross-origin and scheme-downgrade redirect blocking when TLS pin is active.
  - `ResolveHeaders`: expands `${VAR}` in a single pass without mutating `cfg.Headers`, trims whitespace, succeeds when the env var's value itself contains literal `${`, fails closed on unset/blank `${VAR}`, whitespace-only `bearer_token_file`/`tls_cert_fingerprint`, and malformed `${...}`; resolves `bearer_token_env` and `bearer_token_file` via `secrets.Resolve`; rejects conflicting `Authorization` header; end-to-end `connectHTTP` over pinned TLS + `bearer_token_env` against an in-process Streamable HTTP MCP server.
- `internal/mcp/http_test.go`:
  - Streamable HTTP `/mcp` initialize + `tools/list` (verifying 21 tools) over `httptest.NewServer` backed by `newStreamableHTTP`.
  - Bearer auth middleware: rejects missing/wrong token with 401 and accepts matching token (including case-insensitive `bearer` scheme).
  - End-to-end `mcpclient.Registry.ConnectAll` over pinned self-signed TLS + `bearer_token_env` against `newStreamableHTTP` (verifying 21 tools).
  - `ServeStreamableHTTP`: refuses non-loopback `--addr` without `--token-env`; fails closed when `--token-env` is unset or whitespace-only; succeeds on loopback `127.0.0.1:0` with valid `--token-env` and shuts down cleanly on context cancel, including while a client holds an active `GET /mcp` SSE stream open.
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

Kind: spec
Reviewer: fresh-context subagent (Antigravity `self`, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Google Gemini (same family as the author)
Base: 340c1bc0da3a1065ed0d8ec7e77cc88735424b02
Head: 5400baa54cfad0ecb85c69c364de2a69e06d31d3
Date: 2026-11-22
Verdict: approve-with-changes

1. Major: `ServeStreamableHTTP` and `axis mcp serve --transport http` shutdown design missed `signal.NotifyContext` in `cmd/axis/mcp.go` and active SSE `GET /mcp` session teardown (`StreamableHTTPServer.CloseSessions`). Fixed in fae02803: wired `signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)` for `--transport http`, kept `*mcpserver.StreamableHTTPServer` accessible in `ServeStreamableHTTP` to call `CloseSessions` alongside `srv.Shutdown` with bounded drain + `srv.Close()` fallback, and added an active `GET /mcp` shutdown test.

2. Major: `ResolveHeaders` post-substitution `${` check broke valid secret env values containing literal `${`. Fixed in fae02803: Design §2 now specifies single-pass parsing over the raw header template string without re-scanning substituted env values, and the test plan covers an env value containing `${`.

3. Minor: Reverse-proxy non-goal overlooked `mcp-go`'s default loopback `Host`-header DNS-rebinding rejection and passed a no-op `WithEndpointPath("/mcp")` option when mounted on `http.ServeMux`. Fixed in fae02803: Non-goals and Design §3 state the reverse-proxy `Host` rewrite requirement and omit `WithEndpointPath`.

4. Minor: Spec cited stale `29-command` count (current main has 30) and omitted updating `docs/current-state.md`. Fixed in fae02803: updated count to `30-command` and added the `docs/current-state.md` update to Design §3.

Questions:

- Should `NewStreamableHTTPHandler`'s bearer middleware accept case-insensitive `bearer` scheme matching `internal/api/server.go`, and should `${VAR}` header expansion trim surrounding whitespace? Verified: updated Design §2 and §3 in fae02803 so bearer auth uses `strings.Fields` + case-insensitive `bearer` matching `internal/api/server.go`, and `${VAR}` expansion trims surrounding whitespace matching `internal/secrets.Resolve`.
