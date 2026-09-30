# AUTH-7: Secret/Credential Authority

## 1. Where SSH Keys Are Loaded From

SSH authentication is resolved per-connection in `internal/transport/ssh.go` (`SSHExecutor.sshConfig`). Resolution follows the **OpenSSH precedence model**:

### 1.1 SSH Agent

- Environment variable: `SSH_AUTH_SOCK`
- If the Unix socket exists and `agent.NewClient(conn).Signers()` succeeds, agent-provided keys are offered first.
- **Skipped entirely** when `IdentitiesOnly yes` is set in `~/.ssh/config` for the target host.

### 1.2 Explicit Identity Files

- Parsed from `ssh -G` output (`IdentityFile` directives from `~/.ssh/config`).
- Paths are expanded (`~/` → `$HOME`).

### 1.3 Default Key Files

When `IdentitiesOnly` is **not** set, the following are tried in order:

```
~/.ssh/id_ed25519
~/.ssh/id_rsa
~/.ssh/id_ecdsa
```

### 1.4 Host Key Verification

`known_hosts` is **mandatory** and non-configurable:

- Paths: `UserKnownHostsFile` and `GlobalKnownHostsFile` from `ssh -G`, falling back to `~/.ssh/known_hosts`.
- If no known_hosts file exists, the connection fails with a remediation hint (`ssh-keyscan`).
- Host key algorithms are derived from the known_hosts entries, with RSA key expansion (`rsa-sha2-512`, `rsa-sha2-256`, `ssh-rsa`).

## 2. Where Mesh HMAC Secret Is Stored

### 2.1 UDP Discovery Beacon Secret

- **Location:** `nodes.yaml` → `discovery.secret`
- **Used by:** `internal/discovery/udp.go` (`signBeacon` / `verifyBeacon`)
- **Algorithm:** HMAC-SHA256(secret, canonical JSON payload)
- **Behavior:** Empty string means **open mode** (unsigned beacons accepted). Non-empty means **authenticated mode** (only valid signatures accepted).
- **Rotation:** Edit `nodes.yaml`. The daemon’s `WatchDiscovery` will restart the beacon listener on the next 500 ms poll cycle and pick up the new secret.

### 2.2 Mesh Gossip Secret

- **Location:** `nodes.yaml` → `discovery.secret`, copied into `mesh.Config.SharedSecret` by `daemon.NewDefault()`
- **Current wiring:** The daemon uses the same operator-supplied secret for UDP beacons and mesh-gossip HMAC. If the field is omitted, `mesh.DefaultConfig()` leaves `SharedSecret` empty.
- **Behavior:** Empty secret means HMAC verification is bypassed (`verifyMessageHMAC` returns `true`).
- **Rotation:** Edit `nodes.yaml` and restart the daemon. UDP beacon discovery hot-reloads the value, but the mesh config is fixed at daemon construction time.

## 3. Can Operator-Supplied Secret Material Reach Runtime State?

AXIS does not intentionally copy credential fields into state, ledger, skills,
snapshot, event, conversation, or task-log stores. It cannot promise those
files contain zero secret material: task descriptions, commands, model prompts,
stdout/stderr, webhook errors, and learned execution context are
operator-controlled and may contain sensitive values.

AXIS therefore treats all runtime persistence as private even when its schema
has no credential field. AXIS-created directories use `0700`; AXIS-created
runtime files use `0600`. Atomic stores are replaced with private modes, while
append and lock stores tighten an existing file when it is opened. Operator
managed inputs such as `nodes.yaml`, `ai.yaml`, `cortex.token`, and referenced
API-key files should also be `0600`.

### API Tokens

- `~/.axis/token` holds the local API token for `axis serve`.
- Written atomically with `0600` permissions.
- `AXIS_API_TOKEN` env var overrides the file.
- `AXIS_ALLOW_OFFBOX_BEARER=1` is the **break-glass switch** for `axis task delegate/status/approve/reject --addr` aimed at a non-loopback TCP host.
  - **What it does** — permits the A2A task CLI to dial a `--addr` that is neither a Unix socket, `localhost`, nor a loopback IP, and to attach the local API token (`~/.axis/token`, or `AXIS_API_TOKEN` if set). The refusal string still says "cluster token"; that string is this local token. `withAuth` (`internal/api/server.go`) compares it to the target daemon's own token and answers `401 invalid api token` on mismatch. There is no shared cluster secret on this path.
  - **Why the default is off** — the bytes are that long-lived local credential, not a cluster-wide secret. `resolveA2AClient` (`cmd/axis/task_delegate.go`) classifies the dial host via `addrKeepsClusterBearer`/`dialHost` and returns **before** the token is read, so a refused dial never touches `~/.axis/token` and never attaches it. An off-box `--addr` is refused unless this switch is `1`. A name that is not local and is not listed in `nodes.yaml` is also refused; the error points at `--addr`.
  - **What listing a peer does not do** — addressing a `nodes.yaml` name dials `<hostname>:42425` and attaches the same local token with **no** `AXIS_ALLOW_OFFBOX_BEARER` check. That is the A2A TCP dial, not the mesh path (mesh gossip uses `discovery.secret`). The peer's `withAuth` still expects its own token, so adding the name is not a working cross-node credential. Until a cross-node auth model lands, use the local unix socket on the target host. See `docs/runbooks/a2a-task-delegation.md`.
  - **When to set it** — deliberately, for a one-off diagnostic against a host not yet in `nodes.yaml`. Do not export it in a shell profile: `=1` globally disables the protection for every `axis task --addr` invocation.
  - **Fails closed on** userinfo smuggling (`http://127.0.0.1@evil.com/` resolves to `evil.com`, refused), empty host, and non-loopback IP literals.

### Cloud Provider API Keys

- `internal/secrets/secrets.go` resolves keys via `api_key_env` or `api_key_file` (from `nodes.yaml` `ai_providers` block).
- Keys entered through the provider configuration flow may be written to an
  operator-selected key file with `0600`; otherwise they remain in environment
  variables or operator-managed files.

## 4. Are Secrets Logged or Exposed in Error Messages?

### 4.1 SSH Keys

- Parse failures are **silently continued** (`continue` on `ssh.ParsePrivateKey` error).
- No key paths, fingerprints, or material appear in returned errors.
- `handshakeRemediation` surfaces only host/key mismatch advice, never key content.

### 4.2 API Keys / Cloud Tokens

- `internal/secrets/secrets.go` explicitly documents: *"Keys are never logged, printed, or included in error messages."*
- Error messages contain only the **source** (`env var` or `file path`), never the value.
- `Resolve` error example: `api key not found: neither env var nor file contained a value`

### 4.3 Mesh / Discovery Secret

- The HMAC secret is **not logged** during beacon signing or verification.
- `mesh.go` logs only peer names, states, and counts—not HMAC values.

### 4.4 API Bearer Token

- The `withAuth` middleware in `internal/api/server.go` uses `subtle.ConstantTimeCompare` to avoid timing side-channels.
- Invalid tokens produce the generic message `invalid api token` without echoing the received value.

## 5. Is Credential Rotation Supported?

| Credential | Rotation Mechanism | Hot-Reloadable? |
|------------|-------------------|-----------------|
| SSH private keys | Replace files in `~/.ssh/` or rotate agent keys. AXIS reconnects on next probe. | Yes (per-connection) |
| SSH known_hosts | Update file manually or via `ssh-keyscan`. AXIS reads it on next connection. | Yes (per-connection) |
| UDP beacon secret | Edit `nodes.yaml` `discovery.secret`. Daemon restarts listener on next poll. | Yes |
| Mesh gossip secret | Edit `nodes.yaml` `discovery.secret`, then restart the daemon. | No |
| API token (`~/.axis/token`) | **Restart required.** Stop the daemon, delete the file or set `AXIS_API_TOKEN`, restart the daemon, then restart any long-lived process that read the old value. `auth.LoadOrGenerateToken()` regenerates on next read, but `withAuth` (`internal/api/server.go:187`) captures the token **by value in a handler closure at route-registration time** (`registerRoutes` ← `cmd/axis/serve.go:53`), so a live daemon compares the old string for its whole lifetime while clients read fresh per call (`internal/daemon/client.go:35,133,151`) — deleting the file alone yields 401 across the API. See also: `docs/evaluations/2026-07-25-truth-integrity-audit.md` ("Deleting a token is not a cleanup; it invalidates live sessions."). | No (daemon restart) |
| Cloud provider API keys | Rotate env var or file contents outside AXIS. | Yes (per-request) |

## Summary Table

| Secret | Storage | In-Memory Lifetime | Logged? | Rotatable? |
|--------|---------|-------------------|---------|------------|
| SSH private keys | `~/.ssh/`, SSH agent | Per-connection | No | Yes |
| SSH host keys | `~/.ssh/known_hosts` | Per-connection | No | Yes |
| UDP beacon secret | `nodes.yaml` | Daemon lifetime (listener restart on change) | No | Yes |
| Mesh gossip secret | `nodes.yaml` → `mesh.Config` | Daemon lifetime | No | Yes (restart required) |
| API token | `~/.axis/token` or `AXIS_API_TOKEN` | Daemon lifetime | No | Yes |
| Cloud API keys | Env var / file (external) | Per-request | No | Yes |
