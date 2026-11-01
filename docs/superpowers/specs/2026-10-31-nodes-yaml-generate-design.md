# `axis init --generate`: propose `nodes.yaml` from discovery

Date: 2026-10-31. Base: `origin/main` 8cf9d74b. Status: revised after its first spec review
(`revise`); awaiting a second.

## Problem

`~/.axis/nodes.yaml` is the seed list every AXIS surface trusts, and keeping it current is manual.
The pieces to automate it exist but are only reachable interactively:

- `axis init` (`cmd/axis/init.go`) can list Tailscale peers (`discoverTailscalePeers`, from
  `tailscale status --json`) and AXIS beacons (`discoverMeshPeers`, a three-second UDP scan),
  but only inside a TTY wizard, one menu choice at a time.
- Its write path, `config.SaveAtomic` (`internal/config/save.go:47`), re-marshals the `Config`
  struct, so every save drops the file's comments and blank lines. Operators record why a value is
  set in those comments (for example why a node has a large `system_reserve_mb`); one wizard run
  erases them.
- The wizard's beacon scan passes no discovery secret (`init.go:962`), so in a cluster where
  `axis init` enabled discovery (it always stores a generated secret, `init.go:543-551`) every
  signed beacon is rejected (`internal/discovery/udp.go:77-78`) and the scan finds nothing. It also
  discards the listener error that `udp.go:101-102` says must not be read as "no peers".
- Nothing reports drift: a node that has left the tailnet, or a beacon from a machine that is not
  in the file, is visible only if someone runs the wizard and reads the lists.

## Non-goals

- Writing without review. The command proposes; the operator applies. No daemon, timer or agent
  edits `nodes.yaml` on its own.
- Removing nodes. A configured node that no source sees is reported, never deleted.
- Changing any field of a configured node. `docs/authority-config.md` §3 lists `name`,
  `hostname`, `stable_id`, `ssh_user`, `ssh_port` and `role` as identity-sensitive; the generator
  only appends.
- Setting `system_reserve_mb` or any other resource field. AXIS cannot observe a resident service's
  cap (`docs/runbooks/resident-memory-services.md`), so a generated value would be a guess.
- New discovery sources (cloud APIs, mDNS, `~/.ssh/config`). Tailscale and beacons only.
- Changing host-key policy. `internal/transport` keeps host-key verification on (AGENTS.md §4);
  the generator never adds keys to `known_hosts`.
- The interactive wizard's own saves. Moving them to the comment-preserving writer is a follow-up.
- `ai.yaml`.

## Design

A non-interactive mode of `axis init`, which already owns validation and writes:

```text
axis init --generate [--source tailscale,beacons] [--ssh-user USER] [--verify-ssh] [--write]
          [--config PATH] [--format text|json]
```

1. **Collect.** Run the selected sources (default both); each returns candidates with every
   address it knows and fails loudly.
   - Tailscale: parse `Peer` entries, online and offline, with `HostName`, `DNSName` and every
     entry of `TailscaleIPs`. Offline peers are kept and flagged offline.
   - Beacons: a new listen-only `discovery.ScanBeacons(ctx, cfg.Discovery, d)` uses the loaded
     config's secret and `udp_port` (defaults when `discovery` is absent), sends no beacon of its
     own, and returns the listener error. The wizard's scan switches to it, which also fixes the
     problem above.
   - A source that fails (no `tailscale` binary, the UDP port cannot be bound) is reported as
     failed with its error and skipped; the other source still runs.
2. **Drop this machine.** Candidates whose addresses or `stable_id` identify the machine running
   the command (`models.IsLocalHost`, `models.CurrentLocalStableID`) are ignored: the local node is
   configured as `localhost` and is never "seen" by its own scan.
3. **Match.** A candidate matches a configured node when:
   - their `stable_id` values are equal; or
   - any candidate address equals, case-insensitively, one of the node's addresses (`hostname`,
     each `endpoints[].hostname`, and the IPs those resolve to with a 2-second lookup), and the
     SSH port agrees (candidate port, default 22, against `EffectiveSSHPort`); or
   - the candidate's suggested name equals the node's `name`, case-insensitively.

   A matched node is not a change. Drift is reported only for a `stable_id` match whose addresses
   share nothing with the node's. Configured nodes that no candidate matched are reported as
   "not seen" (or "offline" when the match was to an offline Tailscale peer, or "address not
   resolvable" when the node's hostname resolved to nothing, as an SSH alias does). The local node
   is never reported.
4. **Propose.** Unmatched candidates are merged when they share an address or `stable_id` (a
   machine seen by both sources), then become new entries:
   - `name`: `normalizeSuggestedName` of the Tailscale `HostName`, else the beacon's name; on a
     case-insensitive collision with a configured or proposed name, suffixed `-2`, `-3` ...;
   - `hostname`: the Tailscale `DNSName` without its trailing dot when present, else the first
     address;
   - `stable_id` when a beacon reported one; `ssh_port` when it is not 22;
   - `ssh_user`: `--ssh-user`, else the first configured node's `ssh_user` (the wizard's
     `defaultNodeUser`), else the current user. Beacons carry no user.
5. **Verify (optional).** With `--verify-ssh`, each proposed entry is checked through the
   `verifySSH` seam, changed to return a reason: `unknown-host-key`, `host-key-mismatch`, `auth`,
   `network`. Entries that fail are listed under "not added" with the reason and its remediation.
   For an unknown key the remediation is to connect once with `ssh` and check the key out of
   band, never to scan keys in automatically. Without `--verify-ssh`, proposed entries are marked
   unverified.
6. **Report.** Text output lists entries to add, drift, configured nodes not seen (with the reason
   above) and failed sources, then a unified diff of the file. `--format json` gives the same data
   for agents. Nothing is written.
7. **Write (`--write`).** Existing bytes are never re-encoded. The new entries alone are encoded
   with yaml.v3 at the indent the file's `nodes:` items use, each preceded by a comment naming its
   source and date. Their bytes are inserted after the last line of the `nodes:` sequence: the line
   before the next top-level key (from its `yaml.Node` line), or the end of the file. A new
   exported `config.SaveBytesAtomic(path, data)` then decodes the result strictly, runs
   `MigrateProviders` and `Validate`, and only then backs up the old file and replaces it atomically
   with mode 0600 and a directory fsync, as `SaveAtomic` does. `SaveAtomic` keeps its behaviour and
   shares those helpers.
   - With no `nodes.yaml`, the proposal is a new file: a `nodes:` list of the new entries, written
     without a backup.
   - `--write` with nothing to add writes nothing and makes no backup.
8. **Exit codes.** 0 when the run completes, with or without changes or failed sources;
   `ExitErrConfigLoad` (2) when an existing file cannot be loaded; `ExitErrIO` (6) when the write
   fails.
9. **Docs.** `docs/current-state.md` lists the mode, `docs/operator-quickstart.md` shows it next
   to the wizard, and the CHANGELOG records it and the wizard's beacon-secret fix. The command
   count is unchanged (a flag on `axis init`).

## Tests

`cmd/axis/init_generate_test.go`, with the source and SSH seams stubbed and fixtures using
RFC 5737 and RFC 2606 addresses only:

- matching: a candidate matching by `stable_id`, by an `endpoints[]` address, by a configured DNS
  name that resolves (stubbed resolver) to the candidate's IP, and by `name` case-insensitively
  each proposes nothing;
- a machine seen by Tailscale (tailnet IP and `DNSName`) and by a beacon (LAN IP, `stable_id`)
  becomes one proposed entry with the `DNSName` as `hostname` and the `stable_id`;
- two candidates on one address with different SSH ports stay two entries, and a non-22 port is
  written as `ssh_port`;
- a `stable_id` match at unrelated addresses is drift and the file is unchanged;
- a configured node that nothing matches is "not seen"; one matched only by an offline peer is
  "offline"; one with an unresolvable hostname says so; the local node is never listed;
- a candidate that is this machine (by address or `stable_id`) is not proposed;
- `ssh_user`: `--ssh-user` wins; else the first configured node's user; else the current user;
- a name collision, including one that differs only in case, gets a numeric suffix;
- `--verify-ssh` lists an unknown host key and an auth failure with their reasons and leaves both
  out;
- a failing source is reported and the other source still runs;
- without `--write`, the file's bytes are unchanged;
- with `--write` on a fixture with 2-space indentation, blank lines between entries, aligned
  inline comments and a top-level key after `nodes:`, every original byte is preserved, the new
  entries sit at the end of `nodes:` at the file's indent, a backup exists, and `config.Load`
  accepts the result;
- with no file, `--write` creates one and makes no backup; with no additions, it writes nothing;
- a proposal that would fail `Validate` is not written and the file is unchanged;
- `--format json` carries the same lists.

`internal/discovery`: `ScanBeacons` collects a signed beacon with the configured secret, rejects an
unsigned one when a secret is set, sends nothing, and returns a bind error.
`internal/config`: `SaveBytesAtomic` rejects unknown keys and invalid configs without touching the
file, and on success leaves mode 0600 and a backup.

## Alternatives

| Option | Why not chosen |
| --- | --- |
| The daemon registers nodes as beacons arrive (self-registration, like kubelet) | Puts a network signal in charge of operator-authored config; AXIS keeps discovery suggestions advisory. |
| A runtime inventory that replaces `nodes.yaml` (Ansible-style dynamic inventory) | Placement and reservations key on a stable, reviewed node list; a list that changes with each probe would orphan ledger entries keyed by name. |
| A second file, `nodes.generated.yaml`, merged at load time | Two sources of truth for one list, and conflicts would be resolved silently at load. |
| Re-encode the whole YAML node tree | yaml.v3 keeps comments but not blank lines, indentation or comment alignment (one global indent; one space before a line comment), so every write would rewrite the file. |
| Re-marshal the struct (reuse `SaveAtomic`) | Drops every comment in the file, which is the second problem above. |

## Research

- Kubernetes nodes self-register with the API server by default, and the docs describe manual
  node administration as the alternative when the operator wants control over membership
  (<https://kubernetes.io/docs/concepts/architecture/nodes/>, sections "Self-registration of
  Nodes" and "Manual Node administration"). This spec chooses the operator-controlled side.
- Consul's cloud auto-join discovers peers from provider APIs at agent start
  (<https://developer.hashicorp.com/consul/docs/install/cloud-auto-join>): discovery feeds
  membership, but the join configuration is still declared by the operator.
- `tailscale status --json` is the documented machine-readable peer list
  (<https://tailscale.com/kb/1080/cli>); `discoverTailscalePeers` already parses part of it.
- `gopkg.in/yaml.v3` v3.0.1: `Node` keeps head, line and foot comments and each node's line, but
  not blank lines or per-node indentation, and the encoder uses one indent
  (<https://pkg.go.dev/gopkg.in/yaml.v3>; `encode.go:61`, `emitterc.go:1151-1155` in the module).
  Hence splicing encoded new entries into the original bytes rather than re-encoding the file.

Optimization chosen: reuse the wizard's sources, naming, user default and validation; read the
file's structure with yaml.v3 but write by splicing bytes, so the only new code is the matcher, the
report, the listen-only scan and the byte writer.

## Review

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author (Claude Opus 5.5 and GPT-5.6 Sol out of weekly usage)
Base: 8cf9d74b
Head: de55d59b
Date: 2026-10-31
Verdict: revise

1. Major: the beacon source passed no secret and discarded the listener error, so in a secured
   cluster it found nothing and called that success. Fixed: a listen-only `ScanBeacons` uses the
   configured secret and port and returns the error (Design 1); tests in `internal/discovery`.
2. Major: matching compared one discovered IP with unresolved configured names, so DNS-named or
   endpoints-only nodes were "not seen", drift fired every run, one machine seen by both sources
   became two entries, and the local node and offline peers were misreported. Fixed: matching on
   address sets with resolved names and ports, drift only on a `stable_id` match, the local machine
   dropped, offline and unresolvable reported as such (Design 2 and 3); fixtures for each.
3. Major: re-encoding the yaml.v3 tree cannot keep the file byte-identical. Fixed: new entries are
   encoded alone and spliced into the original bytes (Design 7); a fixture with 2-space indent,
   blank lines and aligned comments.
4. Major: beacons carry no SSH user, so "the beacon's user" was a constant. Fixed: `--ssh-user`,
   else the first configured node's user, else the current user (Design 4).
5. Minor: `ssh_port` was dropped and matching ignored it. Fixed: written when not 22, and part of
   matching and merging (Design 3 and 4); fixture.
6. Minor: the writer named helpers `cmd/axis` cannot call. Fixed: a new exported
   `config.SaveBytesAtomic` with the same guarantees (Design 7); tests in `internal/config`.
7. Minor: `--verify-ssh` would drop new machines for unknown host keys with no reason. Fixed: the
   seam returns a reason and the report gives remediation that keeps host-key checks on (Design 5,
   Non-goals); fixture.

Questions:
- Does the scan hear its own beacon? Became Design 1 and 2: the scan is listen-only and this
  machine is dropped.
- Is broadcasting during a propose-only run acceptable? Became Design 1: it sends nothing.
- Are names compared case-insensitively? Became Design 3 and 4: yes, for matching and collisions.
- What happens with no `nodes.yaml`? Became Design 7: a new file, no backup.
- Doc updates? Became Design 9.
- The research URLs were not fetched by the reviewer. Verified: by the author, all four returned
  HTTP 200 and the Kubernetes page has both cited sections.
