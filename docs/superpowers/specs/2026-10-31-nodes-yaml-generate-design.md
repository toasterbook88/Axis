# `axis init --generate`: propose `nodes.yaml` additions from Tailscale

Date: 2026-10-31. Base: `origin/main` 8cf9d74b. Status: revised after four `revise` spec
reviews; narrowed to Tailscale for v1; awaiting a fifth review.

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
   (`discoverTailscalePeersFn`, extended).
   - The tailnet is observed only when the command succeeds, its output parses, and
     `BackendState` is `Running`. The CLI prints JSON and exits 0 in every backend state
     (`Stopped`, `NeedsLogin` ...), so the state is checked here. Otherwise nothing was observed:
     the report says so with the state or error, the exit code is 1 (`ExitErrGeneric`), and no node
     is reported as not seen.
   - The tailnet suffix is `CurrentTailnet.MagicDNSSuffix`, else the deprecated top-level
     `MagicDNSSuffix`.
   - For every `Peer`, online or offline, keep `HostName`, `DNSName` (trailing dot removed) and
     every entry of `TailscaleIPs`.
   - Some peers are never proposed, and are listed as skipped with the reason: `ShareeNode` peers
     (owned by a user this tailnet shared a node to, who may connect to us), and peers with a
     non-empty `DNSName` outside the tailnet suffix (machines shared in from another tailnet, whose
     names are under that tailnet's suffix, and Mullvad exit nodes). Skipped peers still take part
     in matching (Design 3), so a configured node at a skipped peer's address is seen.
   - `Self`'s addresses are kept to recognise the local node (Design 4). Tailscale reports no SSH
     port, so a candidate's port is unknown.
2. **Resolve configured nodes.** Each configured node's address set is built from its `hostname`
   and each `endpoints[].hostname`. Each name goes through `ssh -G`, as `internal/transport` does
   before dialing (so an SSH alias yields its `HostName` and `Port`); that step is exported from
   transport (`resolveSSHConfigForHost`, `resolvedDialHost`, `resolvedPort` behind one function).
   The resulting host then gets a 2-second DNS lookup, which is new generator code (transport
   leaves DNS to its dialer). The set holds the names and every IP they resolve to; a lookup that
   fails leaves just the names.
3. **Match.** Every parsed peer, skipped or not, is matched. A peer matches a configured node when
   its `DNSName` or one of its tailnet IPs is in the node's address set, compared
   case-insensitively. A configured short MagicDNS name matches through its DNS lookup, not by
   comparing labels. A candidate's unknown port is
   compatible with any configured port. Names never match on their own: two machines can share an
   OS hostname, and a LAN alias can share a first label with an unrelated tailnet peer.
4. **Report what was not seen.** A configured node is judged only if its address set contains a
   tailnet address: an IP in the ranges `isTailscaleAddr` in `internal/snapshot` already tests
   (Tailscale's IPv4 CGNAT range and its own IPv6 /48, not all of IPv6 ULA; the function is
   exported and shared), or a name under the tailnet suffix. Such a node that no online peer
   matches is "offline" if an offline peer matches it, else "not in the tailnet". Other nodes are
   "not observable by Tailscale" and are not called missing. The local node is never listed: a
   configured node is local when `IsLocal()` says so or when its address set meets `Self`'s
   addresses (an alias that resolves to this machine's tailnet IP).
5. **Propose.** Each online, unmatched peer becomes a proposed entry:
   - `name`: `normalizeSuggestedName` of `HostName`, else of the `DNSName`'s first label, else
     the first tailnet IP with its separators replaced by `-` (`normalizeSuggestedName` cuts at the
     first dot, so an IP must not reach it unchanged); on a case-insensitive collision with a
     configured or proposed name, suffixed `-2`, `-3` ..., and flagged "possible duplicate of
     <name>";
   - `hostname`: the `DNSName` when present and `CurrentTailnet.MagicDNSEnabled` is true (the
     suffix is reported even with MagicDNS off), else the first tailnet IP;
   - `ssh_user`: `--ssh-user`, else the first configured node's `ssh_user` (the wizard's
     `defaultNodeUser`), else the current user;
   - no `ssh_port`, `role` or resource fields.

   Offline peers are listed for information and never proposed.
6. **Verify (optional).** With `--verify-ssh`, each proposed entry is checked through the
   `verifySSH` seam, changed to return a reason: `unknown-host-key`, `host-key-mismatch`, `auth`,
   `network`. A missing `known_hosts` file is `unknown-host-key`. Failed entries are marked with
   the reason and its remediation; for an unknown key that is to connect once with `ssh` and check
   the key out of band, never to scan keys in automatically. In this mode transport's own hint
   (`ssh-keyscan ... >> known_hosts`, `internal/transport/ssh.go:847`) is replaced by that text.
   Without `--verify-ssh`, entries are marked unverified.
7. **Report.** Text output lists proposed entries (with verification and duplicate flags), offline
   peers, and the not-seen and not-observable nodes, then a unified diff of the file for the
   entries `--write` would add. `--format json` gives the same data for agents. Nothing is written.
8. **Write.** `--write` requires `--only NAME,...` (proposed names) or `--all`, so devices on the
   tailnet that are not cluster nodes are never added by accident. With `--verify-ssh`, entries
   that failed verification are not written even under `--all`.
   - Existing bytes are never re-encoded. The chosen entries alone are encoded with yaml.v3 at
     the indent the file's `nodes:` items use (the column of their `-`, zero included), each
     preceded by a comment naming the source and date.
   - The insertion point is found from the next thing after the sequence, whose line yaml.v3
     reports reliably: the line of the next top-level key, or the end of the file. From there it
     steps back over blank lines and over comment lines at or left of the items' `-` column, which
     belong to what follows. yaml.v3 gives the line where a value starts, not where it ends (a
     block scalar or a multi-line flow collection continues past it), so the last item's own lines
     are never used.
   - A flow-style `nodes: [...]` list has no `-` column; the command refuses to write and points to
     the wizard.
   - The file's newline style (LF or CRLF) is kept, and a final newline is added only where the
     file lacked one before the insertion point.
   - Meaning check: the result is decoded, and the write is refused unless it equals the original
     config with exactly the chosen entries appended to `nodes`, everything else unchanged. Any
     splice mistake therefore fails closed.
   - A new exported `config.SaveBytesAtomic(path, data, original)` decodes the result strictly,
     runs `MigrateProviders` and `Validate`, and refuses if the file's current bytes differ from
     `original` (another writer changed it since it was read). Only then does it back up the old
     file and replace it atomically with mode 0600 and a directory fsync, as `SaveAtomic` does. It
     returns typed errors: `ErrInvalidConfig`, `ErrConfigChanged`, and anything else as an I/O
     error. `SaveAtomic` keeps its behaviour and shares those helpers.
   - With no `nodes.yaml` (`original` nil), the result is a new file. Like the wizard, it lists this
     machine first (`hostname: localhost`, named from the OS hostname) and then the chosen entries.
     It is created with an exclusive create, so a file that appears mid-run is not overwritten, and
     without a backup. With nothing chosen, nothing is written.
9. **Exit codes.**
   - 0: Tailscale was observed, with or without proposals, and any requested write succeeded or
     had nothing to write.
   - 1 (`ExitErrGeneric`): Tailscale was not observed; `--write` without `--only` or `--all`, or
     with both; an `--only` name that is not proposed or failed `--verify-ssh`; `--ssh-user`,
     `--verify-ssh`, `--only`, `--all` or `--write` without `--generate`; a result that fails
     `Validate` or the meaning check (`ErrInvalidConfig`); a flow-style `nodes:` list; the file
     changed during the run (`ErrConfigChanged`). Nothing is written in any of these cases.
   - 2 (`ExitErrConfigLoad`): an existing file cannot be loaded.
   - 6 (`ExitErrIO`): the write itself fails (any other `SaveBytesAtomic` error).
10. **Docs.** `docs/current-state.md` lists the mode, `docs/operator-quickstart.md` shows it next
    to the wizard, and the CHANGELOG records it. The command count is unchanged.

## Tests

`cmd/axis/init_generate_test.go`, with the Tailscale, resolver and SSH seams stubbed and fixtures
using RFC 5737, RFC 2606 and the tailnet ranges' documentation-style values only:

- matching: a configured node by tailnet IP, by `DNSName`, by a short name that the stubbed DNS
  resolves to the peer's IP, by an SSH alias
  whose `ssh -G` HostName resolves to the peer's IP, and by an `endpoints[]` hostname each proposes
  nothing; a configured `ssh_port: 2222` node at a peer's IP proposes nothing;
- two configured machines and two peers with the same OS hostname but different addresses: the
  unmatched peer is proposed with a suffix and a "possible duplicate" flag;
- an offline unmatched peer is listed and not proposed; a configured node matched only by an
  offline peer is "offline"; a configured tailnet node with no peer is "not in the tailnet"; a LAN
  node is "not observable"; the local node is not listed;
- when `tailscale` fails, prints invalid JSON, or reports `BackendState` `Stopped` or
  `NeedsLogin` (with and without peers): exit 1, "not observed", and no node listed as not seen;
- a `ShareeNode` peer, a machine shared in from another tailnet and a Mullvad exit node are
  skipped with their reason and never proposed, even under `--write --all`; a configured node at a
  skipped peer's IP is seen, not "not in the tailnet";
- with MagicDNS disabled, a proposal's `hostname` is its tailnet IP;
- a LAN node at an IPv6 ULA address outside Tailscale's /48 is "not observable", not judged;
- the suffix comes from `CurrentTailnet.MagicDNSSuffix`, else the top-level field;
- a configured alias that resolves to `Self`'s tailnet IP is local and not listed;
- a configured LAN alias `web1` and a peer `web1.<suffix>` at a different IP: the peer is proposed
  (suffixed, flagged), not matched;
- a peer with an empty `HostName` is named from its `DNSName`'s first label, and one with an
  empty `DNSName` (not skipped) from its IP;
- `ssh_user` precedence: `--ssh-user`, then the first configured node's user, then the current user;
- `--verify-ssh` marks an unknown host key, a missing `known_hosts` file and an auth failure with
  their reasons, never prints `ssh-keyscan`, and `--write --all` leaves them out;
- each exit-1 case in Design 9 exits 1 and writes nothing: `--write` without `--only`/`--all`,
  with both, `--only` with an unknown or failed-verification name, a generate flag without
  `--generate`, an invalid result, and a file edited between the read and the write;
- without `--write`, the file's bytes are unchanged;
- with `--write --only`, on fixtures whose last item ends in a block scalar (`>-`, `|`) and in a
  multi-line flow mapping, the new entry lands after the whole value and the meaning check passes;
  a splice placed inside the value (an injected offset) is refused by the meaning check;
- a flow-style `nodes: [...]` file is refused with exit 1;
- exit 2 for an existing file that fails to load; exit 6 for an injected write failure;
- with `--write --only`, on fixtures with 0-, 2- and 4-space item indent, blank lines between
  entries, aligned inline comments, a comment block above a following top-level key, CRLF line
  endings, and no final newline: every original byte is preserved, the new entries follow the last
  item at the file's indent, the following key keeps its comment, a backup exists, and
  `config.Load` accepts the result;
- with no file, `--write --all` creates one that lists this machine first, without a backup; a
  file created between the read and the write is not overwritten;
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
  Observed by the author: decoding a 2-space file with a blank line between entries and an aligned
  comment into a `yaml.Node` and encoding it back gave 4-space indent, no blank line and one space
  before the comment. Hence splicing encoded new entries into the original bytes.
- Tailscale's CLI prints `status --json` before checking the backend state, and the JSON includes
  peers the text view hides (`ShareeNode`, documented as a node owned by a shared-to user that
  might connect to us, and Mullvad exit nodes); `MagicDNSSuffix` at the top level is deprecated in
  favour of `CurrentTailnet.MagicDNSSuffix`, which is reported even when `MagicDNSEnabled` is
  false
  (<https://github.com/tailscale/tailscale/blob/main/cmd/tailscale/cli/status.go>,
  <https://github.com/tailscale/tailscale/blob/main/ipn/ipnstate/ipnstate.go>).

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

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 8cf9d74b
Head: 4efb859b
Date: 2026-10-31
Verdict: revise

1. Major: a stopped or logged-out Tailscale still printed JSON and exited 0, so nodes would be
   reported missing. Fixed: the tailnet is observed only when `BackendState` is `Running`
   (Design 1); fixtures for `Stopped` and `NeedsLogin`.
2. Major: shared-in nodes and Mullvad exit nodes would be proposed. Fixed: `ShareeNode` peers and
   peers outside the tailnet suffix are skipped with a reason (Design 1); fixtures.
3. Minor: the wizard's empty-name fallback was dropped. Fixed: `HostName`, then the `DNSName`'s
   first label, then the IP with separators replaced (Design 5); fixture.
4. Minor: transport does no DNS lookup, so "the same code" could not hold. Fixed: only the
   `ssh -G` step is exported; the lookup is new generator code (Design 2).
5. Minor: several exit codes and flag combinations were unspecified. Fixed: Design 9 lists every
   case; a test for each.
6. Minor: zero-indent items had no splice rule or fixture. Fixed: the rule uses the items' `-`
   column, zero included (Design 8); fixture.
7. Minor: the deprecated top-level `MagicDNSSuffix` was read. Fixed: `CurrentTailnet` first, the
   old field as fallback (Design 1); fixture.

Questions:
- Can a local node that `IsLocal()` misses be reported missing? Became Design 4: a node whose
  addresses meet `Self`'s is local; fixture.
- Can first-label matching hide a different machine? Became Design 3: labels never match; a short
  name matches only through DNS; fixture.
- Is yaml.v3's loss of blank lines observed? Verified: by the author's round-trip probe (Research).
- Can a concurrent edit be lost? Became Design 8: `SaveBytesAtomic` refuses when the file changed
  since it was read; fixture.

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 8cf9d74b
Head: 0d1ff69f
Date: 2026-10-31
Verdict: revise

1. Major: the splice point used each value's start line, so a last item ending in a block scalar
   was split and the file still loaded with changed meaning. Fixed: the point is found from the
   next top-level key or the end of the file, and a meaning check refuses any result that is not
   the original plus the chosen entries (Design 8); fixtures with block scalars, a flow mapping and
   an injected bad offset.
2. Major: skipped peers were left out of matching, so nodes at their addresses were reported
   missing. Fixed: every peer is matched; skipped only means never proposed (Design 1 and 3);
   fixture.
3. Minor: the proposed `hostname` was the MagicDNS name even with MagicDNS off. Fixed: only when
   `MagicDNSEnabled` (Design 5); fixture.
4. Minor: a peer with no `DNSName` was both skipped and named. Fixed: only a non-empty `DNSName`
   outside the suffix is skipped (Design 1).
5. Minor: the IPv6 range was not pinned. Fixed: the ranges of `isTailscaleAddr`, exported and
   shared (Design 4); fixture with a non-Tailscale ULA LAN node.
6. Minor: exit codes 2 and 6 were untested and errors had no mapping. Fixed: typed errors from
   `SaveBytesAtomic` (Design 8 and 9); tests for both codes.
7. Minor: a missing `known_hosts` file printed the `ssh-keyscan` hint. Fixed: it is
   `unknown-host-key` with out-of-band remediation (Design 6); fixture.
8. Minor: the research described `ShareeNode` wrongly. Fixed: corrected, and machines shared in
   from another tailnet are skipped by the suffix rule (Design 1, Research); fixture.

Questions:
- Did the first-label test rely on stubbed DNS? Became Tests: the fixture resolves a short name
  through the stubbed DNS.
- How is a flow-style `nodes: [...]` handled? Became Design 8: refused; fixture.
- How is "no file" told apart from "empty file"? Became Design 8: `original` nil and an exclusive
  create; fixture.
- Does a new file without the local node break other surfaces? Became Design 8: a new file lists
  this machine first, as the wizard does.
