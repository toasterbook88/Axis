# `axis init --generate`: report `nodes.yaml` additions from Tailscale

Date: 2026-10-31. Base: `origin/main` 8cf9d74b. Status: after seven `revise` spec reviews, v1 is
narrowed to a read-only report at the maintainer's direction; writing the file is a second spec.
Awaiting an eighth review.

## Problem

`~/.axis/nodes.yaml` is the seed list every AXIS surface trusts, and keeping it current is manual.
The pieces to automate it exist but are only reachable interactively:

- `axis init` (`cmd/axis/init.go`) can list Tailscale peers (`discoverTailscalePeers`, from
  `tailscale status --json`), but only inside a TTY wizard, one menu choice at a time, and it
  drops offline peers (`init.go:943`) and every tailnet address but the first (`init.go:950`).
- Nothing tells an operator which tailnet machines are not configured, or which configured
  tailnet nodes have left the tailnet, short of reading both lists by hand.

## Non-goals

- Writing `nodes.yaml`. v1 only reports, and prints a ready-to-paste YAML block for each proposed
  entry. Writing needs a comment-preserving writer: `config.SaveAtomic`
  (`internal/config/save.go:47`) re-marshals the struct and drops comments and blank lines, and a
  yaml.v3 node-tree round trip drops blank lines and re-indents (Research). That writer, and
  `--write`, are a second spec.
- Removing or changing configured nodes, in the report's suggestions as well.
- Drift detection (a configured node now at another address): without a `stable_id` from the
  source there is no identity to compare across addresses.
- Host-key comparison: a peer's `sshHostKeys` is known only for some peers.
- AXIS beacons as a source. A CLI scan shares the beacon port with the daemon through
  `SO_REUSEPORT`, which spreads datagrams across sockets (`internal/discovery/udp.go:170-178`), and
  a beacon's address is the sender's first non-loopback IPv4 (`udp.go:331-344`). The wizard's
  beacon scan also passes no discovery secret (`init.go:962`, so signed beacons are rejected,
  `udp.go:77-78`) and discards the listener error; that is fixed separately as a bug.
- Suggesting `system_reserve_mb` or any other resource field: AXIS cannot observe a resident
  service's cap (`docs/runbooks/resident-memory-services.md`).
- SSH verification of proposals (`--verify-ssh`). It belongs with writing, where it decides what is
  written; a report-only v1 leaves the check to the operator's first `ssh` to the machine, which
  also records its host key the normal way.
- `ai.yaml`.

## Design

A read-only mode of `axis init`:

```text
axis init --generate [--ssh-user USER] [--format text|json] [--config PATH]
```

1. **Collect.** Run `tailscale status --json` through the existing seam
   (`discoverTailscalePeersFn`, extended), with a 5-second timeout.
   - The tailnet is observed only when the command succeeds, its output parses, and
     `BackendState` is `Running`. The CLI prints JSON and exits 0 in every backend state
     (`Stopped`, `NeedsLogin` ...), so the state is checked here. `Online` is each peer's
     connection to the control plane as this machine last heard it, so peers present and none
     online means this machine has probably lost its own control connection. In all these cases
     the report says why, the exit code is 1 (`ExitErrGeneric`), and no node is called offline or
     missing.
   - The tailnet suffix is `CurrentTailnet.MagicDNSSuffix`, else the deprecated top-level
     `MagicDNSSuffix`. When both are empty (older daemons, some self-hosted control servers), no
     peer is skipped for its suffix and names are never judged by suffix.
   - For every `Peer`, online or offline, keep `HostName`, `DNSName` (trailing dot removed), every
     entry of `TailscaleIPs`, and the IPs of its endpoints (`Addrs`, `CurAddr`). Endpoints include
     NAT public addresses and LAN addresses that peers at other sites can share, so they are never
     treated as identity (Design 3).
   - Some peers are never proposed and are listed as skipped with the reason: `ShareeNode` peers
     (owned by a user this tailnet shared a node to, who may connect to us); peers with a non-zero
     `AltSharerUserID` (shared into this tailnet by another user); peers with a non-empty
     `DNSName` outside the tailnet suffix, which covers Mullvad exit nodes; when the suffix is
     unknown, peers with `ExitNodeOption` (this covers Mullvad without naming its domain, which
     this repo's public-boundary gate rejects in tracked files); and peers with no
     `TailscaleIPs`. Skipped peers still take part in matching (Design 3).
   - `Self`'s addresses are kept to recognise the local node (Design 4). Tailscale reports no SSH
     port, so a peer's port is unknown.
2. **Resolve configured nodes.** Each configured node's address set is built from its `hostname`
   and each `endpoints[].hostname`. Each name goes through `ssh -G`, as `internal/transport` does
   before dialing (so an SSH alias yields its `HostName` and `Port`); that step is exported from
   transport (`resolveSSHConfigForHost`, `resolvedDialHost`, `resolvedPort` behind one function).
   The resulting host then gets a 2-second DNS lookup, which is new code here (transport leaves DNS
   to its dialer). The set holds the names and every IP they resolve to; a lookup that fails
   leaves just the names. Resolution runs at most 8 at a time within a 10-second overall budget; a
   name not resolved by then keeps just its names and is reported as unresolved.
3. **Match.** Every parsed peer, skipped or not, is matched. A peer matches a configured node when
   its `DNSName` or one of its tailnet IPs is in the node's address set, compared
   case-insensitively. A configured short MagicDNS name matches through its DNS lookup, not by
   comparing labels. A peer's unknown port is compatible with any configured port.
   - Names never match on their own: two machines can share an OS hostname, and a LAN alias can
     share a first label with an unrelated tailnet peer.
   - Endpoint IPs never match either. A private endpoint IP that exactly one peer carries and that
     is in a configured node's address set only flags that peer "possible duplicate of <node> (by
     LAN endpoint)", so a node configured by its LAN address is shown next to its own peer. An
     endpoint flag never makes a configured node count as seen.
4. **Report what was not seen.** A configured node is judged only if its address set contains a
   tailnet address: an IP in the ranges `isTailscaleAddr` in `internal/snapshot` already tests
   (Tailscale's IPv4 CGNAT range and its own IPv6 /48, not all of IPv6 ULA), or a name under the
   tailnet suffix. The function moves to `internal/netutil` (Support tier), so `internal/snapshot`,
   `internal/multipath` (which has a copy) and `cmd/axis` share one definition within the import
   tiers.
   - Such a node that no online peer matches is "offline" if an offline peer matches it, else "not
     in the tailnet". Other nodes are "not observable by Tailscale" and are not called missing.
   - The local node is never listed. Here a configured node is local when its `stable_id` is this
     machine's, when its address set meets `Self`'s addresses, or when one of its names passes
     `models.IsLocalHost` (exact hostname, loopback or a local interface address).
     `NodeConfig.IsLocal()` is not used: it accepts any name whose first label is this machine's
     short hostname (`internal/models/locality.go`), which would hide a remote `hub.<suffix>` on a
     machine called `hub`.
5. **Propose.** Each online, unmatched, unskipped peer becomes a proposed entry. Peers are handled
   in order of their first tailnet IP, so names are the same on every run:
   - `name`: the `DNSName`'s first label, which Tailscale keeps unique within the tailnet, else
     `normalizeSuggestedName` of `HostName`, else the first tailnet IP with its separators replaced
     by `-` (`normalizeSuggestedName` cuts at the first dot, so an IP must not reach it unchanged);
     on a case-insensitive collision with a configured or proposed name, suffixed `-2`, `-3` ...
     and flagged "possible duplicate of <name>";
   - `hostname`: the `DNSName` when present, `CurrentTailnet.MagicDNSEnabled` is true (the suffix is
     reported even with MagicDNS off), and the name resolves from this machine within 2 seconds
     (it does not with `--accept-dns=false`); else the first tailnet IP. Also the first tailnet IP
     when the `DNSName` would make `NodeConfig.IsLocal()` true while the peer's IPs are not
     `Self`'s: AXIS surfaces use `IsLocal()`, and its first-label match would collect this
     machine's facts under the new node's name. Such entries are flagged;
   - `ssh_user`: `--ssh-user`, else the first configured node's `ssh_user` (the wizard's
     `defaultNodeUser`), else the current user;
   - no `ssh_port`, `role` or resource fields.

   Offline peers are listed for information and never proposed.
6. **Report.** Text output has sections for proposed entries (each with its tailnet IP and any
   flags, then a YAML block indented as a `nodes:` item and preceded by a comment naming the source
   and date), offline peers, skipped peers with reasons, configured nodes not seen (offline, not in
   the tailnet, unresolved) and nodes not observable by Tailscale. `--format json` gives the same
   data for agents, with each proposal's YAML as a string. Nothing is written; the command does
   not open `nodes.yaml` for writing.
7. **Exit codes.** 0 when Tailscale was observed, with or without proposals; 1 when it was not, and
   for `--ssh-user` or `--format` without `--generate`; 2 (`ExitErrConfigLoad`) when an existing
   `nodes.yaml` cannot be loaded. With no `nodes.yaml`, every unskipped online peer is proposed and
   nothing is judged as missing.
8. **Docs.** `docs/current-state.md` lists the mode, `docs/operator-quickstart.md` shows it next
   to the wizard, and the CHANGELOG records it. The command count is unchanged.

## Tests

`cmd/axis/init_generate_test.go`, with the Tailscale and resolver seams stubbed and fixtures using
RFC 5737 and RFC 2606 addresses and tailnet IPs within the public-boundary allowlist only:

- matching: a configured node by tailnet IP, by `DNSName`, by a short name that the stubbed DNS
  resolves to the peer's IP, by an SSH alias whose `ssh -G` HostName resolves to the peer's IP, and
  by an `endpoints[]` hostname each proposes nothing; a configured `ssh_port: 2222` node at a peer's
  IP proposes nothing;
- two configured machines and two peers with the same OS hostname but different addresses: the
  unmatched peer is proposed with a suffix and a "possible duplicate" flag;
- a configured LAN alias `web1` and a peer `web1.<suffix>` at a different IP and endpoints: the
  peer is proposed (suffixed, flagged), not matched;
- a node configured by its LAN IP under another name: its peer is proposed, flagged "possible
  duplicate (by LAN endpoint)", and the node is not counted as seen;
- two peers sharing a public endpoint IP and a configured dynamic-DNS node at that IP: both peers
  are proposed unflagged and the node is not counted as seen; two peers at different sites sharing
  a LAN endpoint IP: no flag;
- a departed tailnet node whose `endpoints[]` LAN IP belongs to another online peer is "not in the
  tailnet";
- three unmatched peers with the same `HostName` and no `DNSName`: two runs give the same
  name-to-IP mapping;
- an offline unmatched peer is listed and not proposed; a configured node matched only by an
  offline peer is "offline"; a configured tailnet node with no peer is "not in the tailnet"; a LAN
  node, including one at an IPv6 ULA address outside Tailscale's /48, is "not observable";
- when `tailscale` fails, hangs (stubbed, bounded by the timeout), prints invalid JSON, reports
  `BackendState` `Stopped` or `NeedsLogin` (with and without peers), or lists peers none of which
  is online: exit 1, "not observed", and no node listed as offline or missing;
- skipped with their reason and never proposed: a `ShareeNode` peer, a peer with a non-zero
  `AltSharerUserID`, a peer outside the tailnet suffix, a peer with `ExitNodeOption` when the
  suffix fields are empty, and a peer with no `TailscaleIPs`; a configured node at a skipped peer's
  IP is seen;
- the suffix comes from `CurrentTailnet.MagicDNSSuffix`, else the top-level field; with both empty,
  no peer is skipped for its suffix;
- with MagicDNS disabled, or a `DNSName` that does not resolve from this machine, a proposal's
  `hostname` is its tailnet IP;
- a configured alias that resolves to `Self`'s tailnet IP is local and not listed; on a machine
  whose short hostname is `hub`, a configured `hub.<suffix>` at another IP is judged, not local;
- on a machine called `hub`, a peer `hub.<suffix>` at another IP is proposed as `hub-2` with its
  tailnet IP as `hostname`, flagged, and `IsLocal()` is false for that proposed entry;
- a peer with an empty `HostName` is named from its `DNSName`'s first label, and one with an empty
  `DNSName` from its IP;
- `ssh_user` precedence: `--ssh-user`, then the first configured node's user, then the current user;
- each proposal's YAML block, appended under a fixture's `nodes:`, loads with `config.Load`;
- resolution of many unresolvable names finishes within the overall budget;
- `--ssh-user` or `--format` without `--generate` exits 1; an unloadable `nodes.yaml` exits 2; with
  no `nodes.yaml`, peers are proposed and nothing is called missing;
- the command never writes: `nodes.yaml`'s bytes and modification time are unchanged after a run;
- `--format json` carries the same lists.

`internal/transport`: the exported resolver returns the alias's HostName and Port from a stubbed
`ssh -G`. `internal/netutil`: the moved `isTailscaleAddr` keeps its current cases, and the import
tiers gate passes.

## Alternatives

| Option | Why not chosen |
| --- | --- |
| The daemon registers nodes as beacons arrive (self-registration, like kubelet) | Puts a network signal in charge of operator-authored config; AXIS keeps discovery suggestions advisory. |
| A runtime inventory that replaces `nodes.yaml` (Ansible-style dynamic inventory) | Placement and reservations key on a stable, reviewed node list; a list that changes with each probe would orphan ledger entries keyed by name. |
| A second file, `nodes.generated.yaml`, merged at load time | Two sources of truth for one list, and conflicts would be resolved silently at load. |
| Report and write in one spec | Seven reviews found most of the remaining risk in the write path (splice point, comments, new files, concurrency); the report stands on its own and is the input a writer needs. |
| Include AXIS beacons in v1 | A CLI scan competes with the daemon for beacon datagrams and reports a sender-chosen address (Non-goals). |

## Research

- Kubernetes nodes self-register with the API server by default, and the docs describe manual
  node administration as the alternative when the operator wants control over membership
  (<https://kubernetes.io/docs/concepts/architecture/nodes/>, sections "Self-registration of
  Nodes" and "Manual Node administration"). This spec chooses the operator-controlled side.
- Consul's cloud auto-join discovers peers from provider APIs at agent start
  (<https://developer.hashicorp.com/consul/docs/install/cloud-auto-join>): discovery feeds
  membership, but the join configuration is still declared by the operator.
- `tailscale status --json` is the documented machine-readable peer list
  (<https://tailscale.com/kb/1080/cli>). Its CLI prints the JSON before checking the backend state,
  the JSON includes peers the text view hides (`ShareeNode`, documented as a node owned by a
  shared-to user that might connect to us, and Mullvad exit nodes), and the top-level
  `MagicDNSSuffix` is deprecated in favour of `CurrentTailnet.MagicDNSSuffix`, which is reported
  even when `MagicDNSEnabled` is false
  (<https://github.com/tailscale/tailscale/blob/main/cmd/tailscale/cli/status.go>,
  <https://github.com/tailscale/tailscale/blob/main/ipn/ipnstate/ipnstate.go>).
- `gopkg.in/yaml.v3` v3.0.1 keeps comments on a `Node` but not blank lines or per-node
  indentation, and the encoder uses one indent (<https://pkg.go.dev/gopkg.in/yaml.v3>;
  `encode.go:61`, `emitterc.go:1151-1155` in the module). Observed by the author: decoding a
  2-space file with a blank line between entries and an aligned comment and encoding it back gave
  4-space indent, no blank line and one space before the comment. This is why writing is its own
  spec.

Optimization chosen: one source and no writes in v1, reusing the wizard's Tailscale parsing,
naming and user default and transport's resolution, so the new code is the matcher and the
report. The command runs on demand, so the only cost that matters is name resolution. Measured on
a 13-node `nodes.yaml`: `ssh -G` for all names took 51 ms; `ssh -G` plus a lookup each, one after
another, took 2.1 s, about one 2-second timeout. Hence the 2-second lookup, 8 at a time, and a
10-second overall budget.

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

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 8cf9d74b
Head: 7e4e6b62
Date: 2026-10-31
Verdict: revise

1. Major: `IsLocal()` matches by first label, so a proposed `hub.<suffix>` on a machine called
   `hub` would be read as this machine, and a configured remote one was never judged. Fixed:
   locality here uses `stable_id`, `Self`'s addresses and `IsLocalHost`, and such a proposal gets
   its tailnet IP as `hostname` (Design 4 and 5); fixtures.
2. Minor: in a new file, a peer named like this machine failed the whole write. Fixed: the local
   entry counts in collisions (Design 5); fixture.
3. Minor: transport's local SSH setup errors had no typed form. Fixed: sentinel errors and a
   `local-ssh-config` reason (Design 6); fixtures.
4. Minor: a peer with no tailnet IPs produced an invalid entry. Fixed: skipped with a reason, still
   matched (Design 1); fixture.

Questions:
- Does `MagicDNSEnabled` guarantee the name resolves here? Became Design 5: the name is used only
  if it resolves from this machine; fixture.
- What if both suffix fields are empty? Became Design 1 and 4: no suffix skip or judging; fixture.
- Are shared-in machines always under the sharer's suffix? Open: not observed on this repo or a
  captured status; `--only`/`--all` and `--verify-ssh` limit what a misclassified peer can do.
- Do nested entries and keep-chomp scalars survive the splice? Became Tests: fixtures for both;
  the meaning check fails closed.
- Is resolution bounded? Became Design 2: 8 at a time within 10 seconds; fixture.
- Is the compare-then-rename window accepted? Became Design 8: yes, stated.
- What does `--format` do without `--generate`? Became Design 9: exit 1.

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 8cf9d74b
Head: 081d98de
Date: 2026-10-31
Verdict: revise

1. Major: a machine configured by its LAN address or an alias was proposed again, usually
   unflagged. Fixed: the peer's endpoint IPs (`Addrs`, `CurAddr`) take part in matching, and
   `--all` leaves out flagged entries (Design 1, 3 and 8); fixtures.
2. Major: suffix order followed Go map order, so `--only` could pick another machine. Fixed:
   peers are named in tailnet-IP order, preferring the unique `DNSName` label, and `--only`
   accepts a tailnet IP (Design 5 and 8); fixture with three same-named peers run twice.
3. Minor: the `|+` test contradicted the splice rule. Fixed: that case, a document-end marker and
   a flow list are refused (Design 8); fixtures assert exit 1 and no change.
4. Minor: the reason classifier had no real test and `auth` no typed source. Fixed: the mapping
   is stated and table-tested with real errors (Design 6, Tests).
5. Minor: a new file had no `discovery:` block, so gossip would start without a secret. Fixed: it
   ends with `discovery: {enabled: false}` and has the wizard's local fields (Design 8); fixture.
6. Minor: the spec-review gate fails while the last review is `revise`. Fixed: this record; a
   further review follows before code.
7. Minor: the optimization had no measurement. Fixed: resolution timed on a 13-node file
   (Research).

Questions:
- Are shared-in machines always under the sharer's suffix? Became Design 1: a non-zero
  `AltSharerUserID` is skipped as well as the suffix rule.
- Is `sshHostKeys` populated for peers without Tailscale SSH? Became a non-goal: v1 does not use
  host keys.
- Does yaml.v3 agree on keep-chomp semantics? Became Design 8: the case is refused, so either
  reading fails closed.
- Can Mullvad exit nodes be proposed when the suffix is empty? Became Design 1: Mullvad's suffix
  is skipped regardless of the tailnet suffix; fixture.
- A file ending in `...`? Became Design 8: refused with a pointer to the wizard; fixture.

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 8cf9d74b
Head: bd1bb486
Date: 2026-10-31
Verdict: revise

1. Major: endpoint IPs were treated as identity, but peers behind one NAT or at sites with the
   same LAN range share them. Fixed: endpoints never match or count as seen; a private endpoint
   unique to one peer only flags it as a possible duplicate (Design 1 and 3); fixtures for a
   shared public endpoint, a shared LAN endpoint and a departed node.
2. Major: the Mullvad domain literal fails the public-boundary gate. Fixed: the suffix rule covers
   Mullvad when the suffix is known, and `ExitNodeOption` peers are skipped when it is not, with no
   literal (Design 1); fixture.
3. Minor: a fourth local SSH error would read as `network`. Fixed: `ErrNoKnownHostsPaths` maps to
   `local-ssh-config` (Design 6); in the sentinel and classifier tests.

Questions:
- Does losing the control connection mark every peer offline? Became Design 1: peers present and
  none online means not observed; fixture.
- Can suffixes shift between the review and write runs? Became Design 8: suffixed names are
  refused in `--only`; the tailnet IP is used instead; fixture.
- Are `tailscale status` and `--verify-ssh` bounded? Became Design 1 and 6: 5 seconds, and 4 at a
  time within 60 seconds; fixture for a hung command.
- Docker bridge endpoints? Became Design 3: endpoints are never identity, so they cannot hide a
  peer.
- Can `internal/multipath` share `isTailscaleAddr`? Became Design 4: it moves to `internal/netutil`
  (Support), importable from every tier.
