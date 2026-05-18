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

- **Location:** `mesh.Config.SharedSecret` (`internal/mesh/mesh.go`)
- **Current wiring:** The mesh is initialized in `daemon.NewDefault()` with `mesh.DefaultConfig()`, which has an **empty** `SharedSecret`. The `nodes.yaml` `discovery.secret` is **not** propagated to the mesh layer today.
- **Behavior:** Empty secret means HMAC verification is bypassed (`verifyMessageHMAC` returns `true`).
- **Rotation:** Not supported without code change; mesh config is fixed at daemon construction time.

## 3. Are Secrets Ever Written to State Files?

**No.** The following state files contain **zero secret material**:

| File | Contents | Secrets? |
|------|----------|----------|
| `~/.axis/state.json` | Reservations, failures, observations, decisions | No |
| `~/.axis/ledger.json` | Double-entry reservation ledger | No |
| `~/.axis/skills.json` | Learned skills/failures | No |
| `~/.axis/snapshot.json` | Daemon-cached cluster snapshot | No |

### API Tokens

- `~/.axis/token` holds the local API token for `axis serve`.
- Written atomically (`os.CreateTemp` + `rename`) with `0600` permissions.
- `AXIS_API_TOKEN` env var overrides the file.

### Cloud Provider API Keys

- `internal/secrets/secrets.go` resolves keys via `api_key_env` or `api_key_file` (from `nodes.yaml` `ai_providers` block).
- Keys are **never persisted** by AXIS; they live only in environment variables or operator-managed files.

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
| Mesh gossip secret | **Not supported today** (empty default, not wired to config). | No |
| API token (`~/.axis/token`) | Delete file and/or change `AXIS_API_TOKEN`. AXIS regenerates on next `auth.LoadOrGenerateToken()`. | Yes |
| Cloud provider API keys | Rotate env var or file contents outside AXIS. | Yes (per-request) |

## Summary Table

| Secret | Storage | In-Memory Lifetime | Logged? | Rotatable? |
|--------|---------|-------------------|---------|------------|
| SSH private keys | `~/.ssh/`, SSH agent | Per-connection | No | Yes |
| SSH host keys | `~/.ssh/known_hosts` | Per-connection | No | Yes |
| UDP beacon secret | `nodes.yaml` | Daemon lifetime (listener restart on change) | No | Yes |
| Mesh gossip secret | `mesh.Config` (empty default) | Daemon lifetime | No | No (not wired) |
| API token | `~/.axis/token` or `AXIS_API_TOKEN` | Daemon lifetime | No | Yes |
| Cloud API keys | Env var / file (external) | Per-request | No | Yes |
