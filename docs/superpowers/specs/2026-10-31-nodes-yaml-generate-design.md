# `axis init --generate`: propose `nodes.yaml` additions from Tailscale

Date: 2026-10-31. Base: `origin/main` 8cf9d74b. Status: revised after two `revise` spec reviews;
narrowed to Tailscale for v1; awaiting a third review.

## Problem

`~/.axis/nodes.yaml` is the seed list every AXIS surface trusts, and keeping it current is manual.
The pieces to automate it exist but are only reachable interactively:

- `axis init` (`cmd/axis/init.go`) can list Tailscale peers (`discoverTailscalePeers`, from
  `tailscale status --json`), but only inside a TTY wizard, one menu choice at a time, and it
  drops offline peers (`init.go:943`) and every tailnet address but the first (`init.go:950`).
- Its write path, `config.SaveAtomic` (`internal/config/save.go:47`), re-marshals the `Config`
  struct, so every save drops the file's comments and blank lines. Operators record why a value is
  set in those comments (for example why a node has a large `system_reserve_mb`); one wizard run
  erases them.
- Nothing tells an operator which tailnet machines are not configured, or which configured
  tailnet nodes have left the tailnet, short of reading both lists by hand.

## Non-goals

- Writing without review. The command proposes; the operator chooses entries and applies them. No
  daemon, timer or agent edits `nodes.yaml` on its own.
- Removing nodes, or changing any field of a configured node (`docs/authority-config.md` §3 lists
  `name`, `hostname`, `stable_id`, `ssh_user`, `ssh_port` and `role` as identity-sensitive). The
  generator only appends.
- Drift detection (a configured node now at another address). Without a `stable_id` from the
  source there is no identity to compare across addresses; v1 reports only "not seen".
- AXIS beacons as a source in v1. A CLI scan shares the beacon port with the daemon through
  `SO_REUSEPORT`, which spreads datagrams across sockets (`internal/discovery/udp.go:170-178`), so
  a scan beside a running daemon can miss peers; and a beacon's address is the sender's first
  non-loopback IPv4 (`udp.go:331-344`), not necessarily a reachable one. A beacon source needs
  the daemon to publish its registry first. The wizard's beacon scan also passes no discovery
  secret (`init.go:962`, so signed beacons are rejected, `udp.go:77-78`) and discards the listener
  error; that is fixed separately as a bug.
- Setting `system_reserve_mb` or any other resource field. AXIS cannot observe a resident service's
  cap (`docs/runbooks/resident-memory-services.md`), so a generated value would be a guess.
- Changing host-key policy. `internal/transport` keeps host-key verification on (AGENTS.md §4);
  the generator never adds keys to `known_hosts`.
- The interactive wizard's own saves. Moving them to the comment-preserving writer is a follow-up.
- `ai.yaml`.

## Design

A non-interactive mode of `axis init`, which already owns validation and writes:

```text
axis init --generate [--ssh-user USER] [--verify-ssh] [--format text|json] [--config PATH]
                     [--write (--only NAME,... | --all)]
```

1. **Collect.** Run `tailscale status --json` through the existing seam
   (`discoverTailscalePeersFn`, extended). For every `Peer`, online or offline, keep `HostName`,
   `DNSName` (trailing dot removed), its first label, and every entry of `TailscaleIPs`. Tailscale
   reports no SSH port, so a candidate's port is unknown. `Self` is not a peer and is never a
   candidate. If the command fails or its output does not parse, nothing was observed: the report
   says so and the exit code is 1 (`ExitErrGeneric`); no node is reported as not seen.
2. **Resolve configured nodes.** Each configured node's address set is built from its `hostname`
   and each `endpoints[].hostname`, resolved the way `internal/transport` dials them: `ssh -G`
   (so an SSH alias yields its `HostName` and `Port`), then a 2-second DNS lookup of that host.
   The resolution is exported from `internal/transport` as one function so both use the same code.
   The set holds the names and every IP they resolve to; a lookup that fails leaves just the names.
3. **Match.** A candidate matches a configured node when any candidate address (its `DNSName`,
   that name's first label, or a tailnet IP) is in the node's address set, compared
   case-insensitively. A candidate's unknown port is compatible with any configured port.
   Suggested names alone never match: two machines can share an OS hostname.
4. **Report what was not seen.** A configured node is judged only if its address set contains a
   tailnet address (an IP in Tailscale's IPv4 CGNAT range or its IPv6 ULA prefix, or a name
   under the tailnet's MagicDNS suffix from `MagicDNSSuffix` in the status output). Such a node that no online peer
   matches is "offline" if an offline peer matches it, else "not in the tailnet". Other nodes are
   "not observable by Tailscale" and are not called missing. The local node (`IsLocal()`) is never
   listed.
5. **Propose.** Each online, unmatched peer becomes a proposed entry:
   - `name`: `normalizeSuggestedName(HostName)`; on a case-insensitive collision with a configured
     or proposed name, suffixed `-2`, `-3` ..., and flagged "possible duplicate of <name>";
   - `hostname`: the `DNSName` when present, else the first tailnet IP;
   - `ssh_user`: `--ssh-user`, else the first configured node's `ssh_user` (the wizard's
     `defaultNodeUser`), else the current user;
   - no `ssh_port`, `role` or resource fields.

   Offline peers are listed for information and never proposed.
6. **Verify (optional).** With `--verify-ssh`, each proposed entry is checked through the
   `verifySSH` seam, changed to return a reason: `unknown-host-key`, `host-key-mismatch`, `auth`,
   `network`. Failed entries are marked with the reason and its remediation; for an unknown key
   that is to connect once with `ssh` and check the key out of band, never to scan keys in
   automatically. Without `--verify-ssh`, entries are marked unverified.
7. **Report.** Text output lists proposed entries (with verification and duplicate flags), offline
   peers, and the not-seen and not-observable nodes, then a unified diff of the file for the
   entries `--write` would add. `--format json` gives the same data for agents. Nothing is written.
8. **Write.** `--write` requires `--only NAME,...` (proposed names) or `--all`, so devices on the
   tailnet that are not cluster nodes are never added by accident. With `--verify-ssh`, entries
   that failed verification are not written even under `--all`.
   - Existing bytes are never re-encoded. The chosen entries alone are encoded with yaml.v3 at
     the indent the file's `nodes:` items use, each preceded by a comment naming the source and
     date, and inserted right after the last line of the last `nodes:` item: the end of its last
     value plus any comment lines indented inside it, before any blank line or comment that
     precedes the next top-level key. The file's newline style (LF or CRLF) is kept, and a final
     newline is added only where the file lacked one before the insertion point.
   - A new exported `config.SaveBytesAtomic(path, data)` decodes the result strictly, runs
     `MigrateProviders` and `Validate`, and only then backs up the old file and replaces it
     atomically with mode 0600 and a directory fsync, as `SaveAtomic` does. `SaveAtomic` keeps its
     behaviour and shares those helpers.
   - With no `nodes.yaml`, the result is a new file holding a `nodes:` list of the chosen entries,
     written without a backup. With nothing chosen, nothing is written.
9. **Exit codes.** 0 when Tailscale was observed, with or without proposals; 1 when it was not;
   `ExitErrConfigLoad` (2) when an existing file cannot be loaded; `ExitErrIO` (6) when the write
   fails; 1 for `--write` without `--only` or `--all`, or with an `--only` name that is not
   proposed.
10. **Docs.** `docs/current-state.md` lists the mode, `docs/operator-quickstart.md` shows it next
    to the wizard, and the CHANGELOG records it. The command count is unchanged.

## Tests

`cmd/axis/init_generate_test.go`, with the Tailscale, resolver and SSH seams stubbed and fixtures
using RFC 5737, RFC 2606 and the tailnet ranges' documentation-style values only:

- matching: a configured node by tailnet IP, by `DNSName`, by its first label, by an SSH alias
  whose `ssh -G` HostName resolves to the peer's IP, and by an `endpoints[]` hostname each proposes
  nothing; a configured `ssh_port: 2222` node at a peer's IP proposes nothing;
- two configured machines and two peers with the same OS hostname but different addresses: the
  unmatched peer is proposed with a suffix and a "possible duplicate" flag;
- an offline unmatched peer is listed and not proposed; a configured node matched only by an
  offline peer is "offline"; a configured tailnet node with no peer is "not in the tailnet"; a LAN
  node is "not observable"; the local node is not listed;
- when `tailscale` fails or prints invalid JSON: exit 1, "not observed", and no node listed as
  not seen;
- `ssh_user` precedence: `--ssh-user`, then the first configured node's user, then the current user;
- `--verify-ssh` marks an unknown host key and an auth failure with their reasons, and `--write
  --all` leaves them out;
- `--write` without `--only` or `--all`, and `--only` with an unknown name, exit 1 and write nothing;
- without `--write`, the file's bytes are unchanged;
- with `--write --only`, on fixtures with 2-space and 4-space item indent, blank lines between
  entries, aligned inline comments, a comment block above a following top-level key, CRLF line
  endings, and no final newline: every original byte is preserved, the new entries follow the last
  item at the file's indent, the following key keeps its comment, a backup exists, and
  `config.Load` accepts the result;
- with no file, `--write --all` creates one without a backup;
- a proposal that would fail `Validate` is not written and the file is unchanged;
- `--format json` carries the same lists.

`internal/transport`: the exported resolver returns the alias's HostName and Port from a stubbed
`ssh -G`. `internal/config`: `SaveBytesAtomic` rejects unknown keys and invalid configs without
touching the file, and on success leaves mode 0600 and a backup.

## Alternatives

| Option | Why not chosen |
| --- | --- |
| The daemon registers nodes as beacons arrive (self-registration, like kubelet) | Puts a network signal in charge of operator-authored config; AXIS keeps discovery suggestions advisory. |
| A runtime inventory that replaces `nodes.yaml` (Ansible-style dynamic inventory) | Placement and reservations key on a stable, reviewed node list; a list that changes with each probe would orphan ledger entries keyed by name. |
| A second file, `nodes.generated.yaml`, merged at load time | Two sources of truth for one list, and conflicts would be resolved silently at load. |
| Include AXIS beacons in v1 | A CLI scan competes with the daemon for beacon datagrams and reports a sender-chosen address (Non-goals); it needs the daemon to publish its registry first. |
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

Optimization chosen: one source in v1, reuse the wizard's Tailscale parsing, naming, user default
and validation and transport's resolution, and write by splicing bytes, so the new code is the
matcher, the report and the byte writer.

## Review

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author (Claude Opus 5.5 and GPT-5.6 Sol out of weekly usage)
Base: 8cf9d74b
Head: de55d59b
Date: 2026-10-31
Verdict: revise

1. Major: the beacon source passed no secret and discarded the listener error. Fixed: beacons are
   out of v1 (Non-goals), and the wizard's secret bug is a separate bug fix.
2. Major: matching compared one discovered IP with unresolved configured names. Fixed: configured
   names are resolved as transport dials them, and candidates carry every tailnet address
   (Design 1 to 3); fixtures for aliases, DNS names and endpoints.
3. Major: re-encoding the yaml.v3 tree cannot keep the file byte-identical. Fixed: chosen entries
   are encoded alone and spliced into the original bytes (Design 8); fixtures for indent, blank
   lines and aligned comments.
4. Major: beacons carry no SSH user. Fixed: `--ssh-user`, else the first configured node's user,
   else the current user (Design 5).
5. Minor: `ssh_port` was dropped and matching ignored it. Fixed: Tailscale reports no port, so it
   is unknown and compatible, and no `ssh_port` is written (Design 3 and 5); fixture.
6. Minor: the writer named helpers `cmd/axis` cannot call. Fixed: a new exported
   `config.SaveBytesAtomic` with the same guarantees (Design 8); tests in `internal/config`.
7. Minor: `--verify-ssh` would drop new machines for unknown host keys with no reason. Fixed: the
   seam returns a reason and the report gives remediation that keeps host-key checks on (Design 6,
   Non-goals); fixture.

Questions:
- Does the scan hear its own beacon? Became a non-goal: no beacon scan in v1.
- Is broadcasting during a propose-only run acceptable? Became a non-goal: no beacon scan in v1.
- Are names compared case-insensitively? Became Design 5: yes, for collisions; names never match.
- What happens with no `nodes.yaml`? Became Design 8: a new file, no backup.
- Doc updates? Became Design 10.
- The research URLs were not fetched by the reviewer. Verified: by the author, all four returned
  HTTP 200 and the Kubernetes page has both cited sections.

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 8cf9d74b
Head: 7d515a64
Date: 2026-10-31
Verdict: revise

1. Major: drift fired every run for a machine both sources saw, because matching ran before
   merging. Fixed: drift detection is a v1 non-goal, and with one source there is nothing to merge.
2. Major: SSH-alias nodes never matched by address and were proposed again. Fixed: configured
   names are resolved with `ssh -G`, then DNS, through transport's own resolver (Design 2);
   fixture.
3. Major: a Tailscale candidate's implied port 22 broke matches for non-22 nodes. Fixed: the port
   is unknown and compatible with any configured port (Design 3); fixture with `ssh_port: 2222`.
4. Major: "not seen" was reported when a source had failed or could not see the node. Fixed: a
   failed source means "not observed" and exit 1, and only nodes with a tailnet address are judged
   (Design 1, 4, 9); fixtures.
5. Major: offline and non-server devices were proposed, and `--write` was all-or-nothing. Fixed:
   offline peers are never proposed, `--write` needs `--only` or `--all`, and failed verification
   is never written (Design 5 and 8); fixtures.
6. Major: a beacon's address is its sender's first interface, which drove the hostname and the
   self-drop. Fixed: beacons are out of v1 (Non-goals).
7. Minor: the splice point put new entries under the next key's head comment. Fixed: they go after
   the last item, before any comment or blank line above the next key (Design 8); fixture with a
   comment above the following key.
8. Minor: the scan duration was unspecified. Fixed: no beacon scan in v1; `tailscale status` is a
   single call.

Questions:
- Does `ScanBeacons` listen when `discovery.enabled` is false? Became a non-goal: no beacon scan.
- Does a name-only match hide a second machine with the same OS hostname? Became Design 3 and 5:
  names never match; a collision is proposed with a suffix and a "possible duplicate" flag.
- Does a listen-only scan share beacons with the daemon under `SO_REUSEPORT`? Verified: the code's
  own comment (`udp.go:176-178`) says datagrams are spread across sharing sockets; that is why
  beacons are out of v1.
- CRLF and a missing final newline at the splice point? Became Design 8; fixtures for both.
- The Consul and Tailscale URLs were not fetched by the reviewer. Verified: by the author, both
  returned HTTP 200.
