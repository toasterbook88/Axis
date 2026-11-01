# SSH transport refuses ProxyJump and ProxyCommand

## Problem

`SSHExecutor.Connect` (`internal/transport/ssh.go`) runs `ssh -G` for each candidate host and then
opens a plain TCP connection to the resolved `HostName` and `Port`. Nothing in the dial path reads
`ProxyJump` or `ProxyCommand`; the parser did not record them at all until #598, which added them
only for the `axis init --generate` report. No doc states a policy. So the omission is silent:

- If `HostName` is reachable only through the jump, the dial times out and the node is reported
  unreachable with an error that never mentions the jump.
- If `HostName` is also reachable directly, Axis connects around the route the operator configured.
- If `HostName` is a private address that some other machine answers, the host-key check fails
  with a mismatch, which points the operator at `known_hosts` instead of the jump.

Host-key verification stays on in every case, so this is a clarity and intent problem, not a
key-trust hole. The fast path (`ResolvedDialTarget`) dials the same way.

## Non-goals

- Supporting `ProxyJump` (a second SSH client to the jump host, its own host key, user and identity
  resolution, then a tunnel). That is a larger feature with its own spec if a topology needs it.
- Supporting `ProxyCommand`. It runs a shell command taken from SSH config; Axis will not execute it.
- Changing `nodes.yaml` generation logic, matching or proposals in `axis init --generate`.
- Changing behaviour when `ssh -G` itself fails: transport keeps its built-in defaults and dials, as
  today, because no directive was observed.

## Design

1. A new exported type in `internal/transport`:

   ```go
   type UnsupportedSSHDirectiveError struct {
       Host      string // the candidate host as passed to ssh -G
       Directive string // "ProxyJump" or "ProxyCommand"
   }
   ```

   Its message names the directive and the host, says Axis did not dial, and gives the remediation:
   point the node's `hostname` (or an endpoint) at an address Axis can reach directly, with no
   proxy directive in that host's SSH config. `ProxyJump` is checked before `ProxyCommand`. A value
   of `none` is no directive (already handled by the parser).

2. `Connect` checks each candidate's resolved config before any dial:
   - Fast path: if the primary host's config has a directive, `ResolvedDialTarget` is not dialed.
   - Primary host and each `DialFallbacks` entry: a candidate with a directive is skipped without a
     TCP dial; the loop moves to the next candidate. A fallback whose own config has no directive is
     dialed as today, so an operator can keep a jump alias and a direct tailnet fallback.
   - If no candidate connects, the returned error matches `errors.As(err,
     *UnsupportedSSHDirectiveError)` whenever any candidate was refused. When the last failure was a
     dial error on another candidate, the error carries both (`%w` twice), dial error first.

3. Callers need no change: discovery, `doctor`, and execution already surface `Connect` errors as
   text, so a refused node shows as unreachable with this message.

4. `axis init --generate`: the flag for these two directives becomes `unsafe: redirected by SSH
   config (ProxyJump); Axis transport refuses it` (same for `ProxyCommand`), so the report says
   what would happen if the YAML were added. Other redirect flags are unchanged. The #598 spec's
   Design 5 gets a one-line pointer to this policy. The `SSHTarget` doc comment says the same.

5. `docs/current-state.md` and `CHANGELOG.md` state the policy.

## Tests

All offline, using the existing `runSSHConfigCommand` stub and the in-process SSH test server.

- Primary with `proxyjump jump.example.com` and a `hostname` pointing at a live test server:
  `Connect` returns an error matching `*UnsupportedSSHDirectiveError` with `Directive ==
  "ProxyJump"`. Today it connects, so the test fails while the directive is dropped.
- The same with `proxycommand nc %h %p`.
- `proxyjump none`: connects as today.
- No dial: a counting TCP listener as `hostname` and as `ResolvedDialTarget` sees zero accepts.
- Fallback: the primary alias has a directive, the fallback resolves without one to the live
  server; `Connect` succeeds and `ConnectedHost()` is the fallback.
- Mixed failure: primary refused, fallback dial fails; the error matches both the typed error and
  the dial error.
- `ssh -G` failure: dials with defaults as today (existing test covers it; asserted unchanged).
- `axis init --generate`: the ProxyJump and ProxyCommand flags carry the new wording.

## Alternatives

- Support `ProxyJump` now: `golang.org/x/crypto/ssh` can tunnel with `Client.Dial`, but each hop
  needs its own config resolution and host-key check, and no configured node uses a jump today.
  Deferred, not ruled out.
- Shell out to the system `ssh` for proxied hosts: two transports with different auth, host-key and
  timeout behaviour; rejected.
- Document only and keep the silent direct dial: leaves the misleading errors and the route bypass.

## Research

- OpenSSH `ssh_config(5)`: `ProxyJump` and `ProxyCommand` semantics, and `none` disabling them
  (<https://man.openbsd.org/ssh_config#ProxyJump>).
- `golang.org/x/crypto/ssh` has no `ssh_config` support; tunnelling is done by the caller through
  `Client.Dial` (<https://pkg.go.dev/golang.org/x/crypto/ssh#Client.Dial>).
- Ansible's `ssh` connection plugin runs the OpenSSH client, so it inherits both directives
  (<https://docs.ansible.com/ansible/latest/collections/ansible/builtin/ssh_connection.html>);
  Paramiko implements `ProxyCommand` by spawning the command
  (<https://docs.paramiko.org/en/stable/api/proxy.html>). Axis uses an in-process client, so
  neither applies without the work listed under Non-goals.
- Optimization: none applies. The check reads fields `ssh -G` already returns; no extra process or
  network call.

## Review
