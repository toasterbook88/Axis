# A2A task delegation: transport, credentials, and the off-box bearer gate

Runbook for the `axis task delegate|status|approve|reject` surface (A2A Slice 4,
`internal/a2a` client + `cmd/axis/task_delegate.go`).

## Where the CLI actually sends the request

`resolveA2AClient` (`cmd/axis/task_delegate.go`) resolves a target in this order:

1. **`--addr` override** — used verbatim.
2. **A node listed in `~/.axis/nodes.yaml`** — dialed at `<hostname>:42425`.
3. **Local** (default, or `local`/`localhost`/`127.0.0.1`) — the unix socket at
   `~/.axis/axis.sock` (`api.DefaultAddr()`, unless `AXIS_HOME` is set).

The bytes attached as `Authorization: Bearer` are the local `~/.axis/token` (or
`AXIS_API_TOKEN` if set). `withAuth` (`internal/api/server.go`) compares them to
**that daemon's own token** and answers `401 invalid api token` on mismatch.
There is no shared cluster secret on this path.

## The off-box `--addr` gate (the error says "cluster token")

An explicit `--addr` aimed at a **non-loopback TCP host** is refused by default:

```console
$ axis task delegate some-node --addr http://192.0.2.5:8080
error: refusing off-box --addr "http://192.0.2.5:8080": it would attach the
cluster token (set AXIS_ALLOW_OFFBOX_BEARER=1 to allow)
```

`--addr` to a unix socket, `localhost`, or a loopback IP (`127.0.0.0/8`, `::1`) is
allowed, because that is the local daemon. Anything else would put the local
token on a host the operator did not name in `nodes.yaml`, so it fails closed.

The refusal text says "cluster token". That string means the local token above.
A listed `nodes.yaml` peer still receives that same token on `:42425` with **no**
`AXIS_ALLOW_OFFBOX_BEARER` check.

### The break-glass switch

```bash
AXIS_ALLOW_OFFBOX_BEARER=1 axis task delegate some-node --addr http://host:42425
```

Set it only when the target is genuinely yours and you accept that the local API
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
reasons — do not collapse them into one outcome:

1. **The API token is per-node.** Each daemon validates against its own
   `~/.axis/token` (`withAuth` in `internal/api/server.go`). A peer that answers
   with a different token returns `401 invalid api token`. A 2026-09-28 note
   recorded seven nodes / seven distinct tokens; that is a snapshot, not current
   inventory.
2. **A closed TCP 42425 is a different failure.** A listed `nodes.yaml` name
   is still dialed at `<hostname>:42425` (`resolveA2AClient`). This page does not
   claim whether any daemon is listening on that port.

A live peer with the wrong token is the 401. If nothing accepts the `:42425`
dial, the CLI's own 30s `--timeout` (`task_delegate.go`) fires and `main` exits **1**
(`ExitErrGeneric` for a context error in `cmd/axis/main.go`). Exit 124 is not an
Axis exit path. Treat a hang as "nothing accepted the TCP dial", not as a
transient auth failure.

Design work for a cross-node auth model is open. Until it lands, use the local
socket on the target host (SSH in, or a local `axis task` invocation).

## Related surfaces

- `/.well-known/agent-card.json` — served **without** auth (A2A discovery convention).
  Use `axis task delegate <node>` with no prompt to print a node's card.
- An observe-scope card does **not** advertise `guarded-exec` (`internal/a2a/card.go`).
  That is a card fact.
- A prompt with no `--skill` still defaults to `guarded-exec` on the client
  (`task_delegate.go`). The live handler parks that skill as pending instead of
  running it (`internal/a2a/handler.go`). That is a handler fact, not the same
  as the card omission.
- `/a2a/v1/tasks/pending` — the operator board view; requires the bearer.
- `/a2a/v1/tasks/{id}/approve` and `/reject` — approval queue. `approve` requires
  `--confirm YES`.

## Verifying a node is serving

```bash
S=~/.axis/axis.sock; T=$(cat ~/.axis/token)
curl -s -H "Authorization: Bearer $T" --unix-socket "$S" \
  http://localhost/a2a/v1/tasks/pending          # -> {"tasks":[]}
```

`axis version` prints `commit:` from the ldflag `buildinfo.Commit` when that
value is set (`cmd/axis/main.go`, `internal/buildinfo.ResolvedCommit`). A plain
build whose commit ldflag is empty prints `vcs.revision` and appends `-dirty`
when `vcs.modified` is true. `built:` is only the ldflag build time.
`vcs.time` is the revision time and is not a build time. The command does not
ask the running daemon who it is. **`axis update` replaces the binary but does
not restart a supervised daemon** — check the process start time, or the API
response, not `axis version`.
