# A2A task delegation: transport, credentials, and the off-box bearer gate

Runbook for the `axis task delegate|status|approve|reject` surface (A2A Slice 4,
`internal/a2a` client + `cmd/axis/task_delegate.go`).

## Where the CLI actually sends the request

`resolveA2AClient` (`cmd/axis/task_delegate.go`) resolves a target in this order:

1. **`--addr` override** — used verbatim.
2. **A node listed in `~/.axis/nodes.yaml`** — dialed at `<hostname>:42425`.
3. **Local** (default, or `local`/`localhost`/`127.0.0.1`) — the unix socket at
   `~/.axis/axis.sock` (`api.DefaultAddr()`).

The CLI always attaches the API token (`~/.axis/token`, or `AXIS_API_TOKEN` if set)
as `Authorization: Bearer`.

## The cluster bearer and off-box `--addr`

An explicit `--addr` aimed at a **non-loopback TCP host** is refused by default:

```console
$ axis task delegate some-node --addr http://192.0.2.5:8080
error: refusing off-box --addr "http://192.0.2.5:8080": it would attach the
cluster token (set AXIS_ALLOW_OFFBOX_BEARER=1 to allow)
```

`--addr` to a unix socket, `localhost`, or a loopback IP (`127.0.0.0/8`, `::1`) is
allowed, because that is the local daemon. Anything else would put the cluster-wide
API token on a host the operator did not name in `nodes.yaml`, so it fails closed.

### The break-glass switch

```bash
AXIS_ALLOW_OFFBOX_BEARER=1 axis task delegate some-node --addr http://host:42425
```

Set it only when the target is genuinely yours and you accept that the cluster API
token will be transmitted to it. It is a per-invocation environment variable — there
is no config-file equivalent, by design.

**The classification happens before the token is read.** A refused dial does not
open, create, or modify `~/.axis/token`.

### What counts as "local"

`addrKeepsClusterBearer` (`cmd/axis/task_delegate.go`) treats an address as local if
it is a unix socket path (`/`, `./`, `../`, `unix://` prefixed) or the host is
literally `localhost` or a loopback IP. Note the following are **not** local:

| Address | Why it is off-box |
|---|---|
| `http://localhost.example:42425` | Exact match only; this is a registrable domain |
| `http://user@192.0.2.5:8080` | Userinfo is stripped before the host is read |
| `http://169.254.169.254/` | Cloud metadata endpoint |
| `http://custom:1234` | Unresolvable name; treated as off-box (fails closed) |

## Cross-node delegation does not work yet

`axis task delegate <remote-node>` is wired but **not functional**. Two independent
reasons, both verified on 2026-09-28:

1. **The API token is per-node.** Each daemon validates against its own
   `~/.axis/token` (`withAuth` in `internal/api/server.go`). Seven nodes, seven
   distinct tokens. A token from one node is rejected by another with
   `401 invalid api token`.
2. **No node listens on TCP 42425.** Every daemon runs `--addr <unix socket>`.

The result is a ~30 s timeout (`exit 124`), not an auth error. Treat a hang as
"cross-node transport is not implemented yet", not as a transient failure.

Design work for a cross-node auth model is open. Until it lands, use the local
socket on the target host (SSH in, or a local `axis task` invocation).

## Related surfaces

- `/.well-known/agent-card.json` — served **without** auth (A2A discovery convention).
  Use `axis task delegate <node>` to print a node's card and skill inventory.
- `/a2a/v1/tasks/pending` — the operator board view; requires the bearer.
- `/a2a/v1/tasks/{id}/approve` and `/reject` — approval queue. `approve` requires
  `--confirm YES`.
- A prompt with no `--skill` defaults to `guarded-exec`, which the daemon parks as
  pending under the current observe scope.

## Verifying a node is serving

```bash
S=~/.axis/axis.sock; T=$(cat ~/.axis/token)
curl -s -H "Authorization: Bearer $T" --unix-socket "$S" \
  http://localhost/a2a/v1/tasks/pending          # -> {"tasks":[]}
```

`axis version` prints the `commit:` line, which distinguishes a running binary from
the file on disk. **`axis update` replaces the binary but does not restart a
supervised daemon** — check the process start time, or the API response, not just
`axis version`.
