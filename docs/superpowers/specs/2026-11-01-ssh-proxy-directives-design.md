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
- Directives in the system-wide `ssh_config`: transport runs `ssh -G -F ~/.ssh/config` whenever
  that file exists (since `120d84ad`), which skips `/etc/ssh/ssh_config` and its includes, so a
  proxy directive set only there is not observed and the host is still dialed directly. Changing
  `-F` alters every resolved field and is a separate change; the error type, `SSHTarget` comment
  and CHANGELOG state the limit.
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

   Its message names the directive and the host, says Axis did not dial, and gives both ways out:
   point the node's `hostname` (or an endpoint) at an address Axis can reach directly, or, when the
   directive comes from a wildcard `Host` block, add `ProxyJump none` (or `ProxyCommand none`) in a
   more specific `Host` block placed before it. An address alone is not enough in the wildcard case,
   because `ssh -G` applies `Host *` and `Host 192.0.2.*` rules to IP literals too. `ProxyJump` is
   checked before `ProxyCommand`. A value of `none` is no directive (`ssh -G` omits it, and the
   parser also maps it to empty).

2. `Connect` checks each candidate's resolved config before any dial:
   - Fast path: if the primary host's config has a directive, `ResolvedDialTarget` is not dialed.
   - Primary host and each `DialFallbacks` entry: a candidate with a directive is skipped without a
     TCP dial; the loop moves to the next candidate. A fallback whose own config has no directive is
     dialed as today, so an operator can keep a jump alias and a direct tailnet fallback.
   - If no candidate connects and any candidate was refused, the returned error matches `errors.As(err,
     *UnsupportedSSHDirectiveError)` for the first refusal. If any candidate also failed to dial, in
     either order, the error wraps the most recent dial error too (`%w` twice, dial error first),
     so a primary's timeout or host-key remediation is not hidden behind a fallback's refusal.

3. Discovery and execution already surface `Connect` errors as text, so a refused node shows as
   unreachable with this message. `axis doctor` attaches a fixed `Fix:` hint about keys and
   `known_hosts` to every SSH failure; when the error matches `*UnsupportedSSHDirectiveError` it
   uses the directive remediation instead.

4. `axis init --generate`: the flag for these two directives becomes `unsafe: redirected by SSH
   config (ProxyJump); Axis transport refuses it` (same for `ProxyCommand`), so the report says
   what would happen if the YAML were added. The same reason feeds `hostname-withheld: DNSName
   <reason>` when only the DNS name is redirected, so that flag carries the wording too. Other
   redirect flags are unchanged. The #598 spec's
   Design 5 gets a one-line pointer to this policy. The `SSHTarget` doc comment says the same.

5. `docs/current-state.md` and `CHANGELOG.md` state the policy. The CHANGELOG entry is marked as a
   behaviour change: a setup that applies a proxy directive to hosts Axis could reach directly
   (for example `Host *` with `ProxyJump`) worked by accident and now fails with this error until
   those hosts get `ProxyJump none` or `ProxyCommand none`.

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
- Mixed failure, both orders: primary refused then fallback dial fails, and primary dial fails then
  fallback refused; in each the error matches the typed error and the dial error, dial error first.
- Message: `Error()` contains the host, the directive, "did not dial", and both remediations.
- Wildcard: a stub output that a `Host *` rule would produce is refused; the same host with
  `proxyjump none` connects.
- `ssh -G` failure: a `Connect`-level test with a failing stub and default key paths connects to
  the live server, so a refusal on unknown config fails the suite.
- `axis doctor`: a refused node's check carries the directive remediation as `Fix`.
- `axis init --generate`: the ProxyJump and ProxyCommand `unsafe:` and `hostname-withheld:` flags
  carry the new wording.

## Alternatives

- Support `ProxyJump` now: `golang.org/x/crypto/ssh` can tunnel with `Client.Dial`, but each hop
  needs its own config resolution and host-key check, and no configured node on the author's
  workstation resolves to a proxy directive today (0 of 18 hostnames and endpoints).
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

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author (the designated second-family reviewer was unavailable:
usage limit)
Base: e82447db
Head: 9d3eaf9a
Date: 2026-11-01
Verdict: approve-with-changes

1. Minor: no listed test failed if `Connect` refused on an `ssh -G` failure, and none checked the
   message. Fixed: a `Connect`-level `ssh -G` failure test and a message test (Tests).
2. Minor: `axis doctor` would print its keys-and-`known_hosts` hint next to a refusal. Fixed: doctor
   uses the directive remediation for this error, with a test (Design 3).
3. Minor: with a wildcard proxy rule, pointing the hostname at an IP is still refused, and the
   message did not name `ProxyJump none`; such setups go dark on upgrade without notice. Fixed: the
   message names both remediations, the CHANGELOG marks the behaviour change, and a wildcard test
   is listed (Design 1 and 5).
4. Minor: a primary dial failure followed by a refused fallback kept only the refusal. Fixed: the
   most recent dial error is wrapped in either order; both orders are tested (Design 2).

Questions:
- Should `hostname-withheld:` carry the new wording? Verified: it reuses the same reason, so it
  does; Design 4 says so and a test pins it.
- Is "no configured node uses a jump" true fleet-wide? Open: only this workstation's 18 hostnames
  and endpoints were probed; the claim is narrowed to that, and the design does not depend on it.

Kind: diff
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author (one diff review: about 55 non-test `.go` lines in
`cmd/axis` and `internal/transport`, below the §8 larger-feature threshold)
Base: e82447db
Head: 529d4f9c
Date: 2026-11-01
Verdict: approve-with-changes

1. Minor: keeping the last refusal instead of the first survived the suite. Fixed: a test with two
   refused candidates asserts the primary's host and directive.
2. Minor: `-F ~/.ssh/config` skips the system-wide `ssh_config`, so a directive set only there is
   still bypassed. Fixed as documentation: stated as a non-goal and in the error type, `SSHTarget`
   comment and CHANGELOG; changing `-F` is left to a separate change because it affects every
   resolved field.

Questions:
- In the mixed case doctor shows only the directive `Fix`. Verified: the `Message` carries both
  errors, dial error first (`TestConnectDialFailureSurvivesALaterRefusal`); the `Fix` covers the
  refusal, which no other fix resolves, as Design 3 specifies.
- The 0-of-18 workstation count was not rechecked. Open: it needs real `nodes.yaml` and SSH config,
  and the design does not depend on it.
