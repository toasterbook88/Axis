# `axis init --generate`: propose `nodes.yaml` from discovery

Date: 2026-10-31. Base: `origin/main` 8cf9d74b. Status: draft, awaiting its first spec review.

## Problem

`~/.axis/nodes.yaml` is the seed list every AXIS surface trusts, and keeping it current is manual.
The pieces to automate it exist but are only reachable interactively:

- `axis init` (`cmd/axis/init.go`) can list Tailscale peers (`discoverTailscalePeers`, from
  `tailscale status --json`) and AXIS beacons (`discoverMeshPeers`, a three-second UDP listen),
  but only inside a TTY wizard, one menu choice at a time.
- Its write path, `config.SaveAtomic` (`internal/config/save.go:47`), re-marshals the `Config`
  struct, so every save drops the file's comments. Operators record why a value is set in those
  comments (for example why a node has a large `system_reserve_mb`); one wizard run erases them.
- Nothing reports drift: a node that has left the tailnet, or a beacon from a machine that is not
  in the file, is visible only if someone runs the wizard and reads the lists.

## Non-goals

- Writing without review. The command proposes; the operator applies. No daemon, timer or agent
  edits `nodes.yaml` on its own.
- Removing nodes. A configured node that no source sees is reported as not seen, never deleted.
- Changing identity fields of a configured node (`name`, `hostname`, `stable_id`, `ssh_user`,
  `ssh_port`): `docs/authority-config.md` §3 lists them as identity-sensitive.
- Setting `system_reserve_mb` or any other resource field. AXIS cannot observe a resident service's
  cap (`docs/runbooks/resident-memory-services.md`), so a generated value would be a guess.
- New discovery sources (cloud APIs, mDNS, `~/.ssh/config`). Tailscale and beacons only.
- `ai.yaml`.

## Design

A non-interactive mode of `axis init`, which already owns validation and writes:

```text
axis init --generate [--source tailscale,beacons] [--ssh-user USER] [--verify-ssh] [--write]
          [--config PATH] [--format text|json]
```

1. **Collect.** Run the selected sources (default both) through the existing functions, behind
   their existing seams (`discoverTailscalePeersFn`, `discoverMeshPeersFn`). A source that fails
   (no `tailscale` binary, no beacons) is reported and skipped, not fatal.
2. **Match.** Each candidate is matched to a configured node by `stable_id`, then by `hostname`
   (exact, after normalization), then by `name`. A candidate that matches is not a change; if a
   source reports a different address for a matched node, the address is reported as drift and the
   file is not changed (identity field).
3. **Propose.** Unmatched candidates become new entries with `name` (the existing
   `normalizeSuggestedName`, suffixed `-2`, `-3` ... on collision), `hostname`, `stable_id` when
   the beacon has one, and `ssh_user` from `--ssh-user`, else the beacon's, else the current user.
   Two candidates for the same machine (a Tailscale peer and a beacon with the same address or
   `stable_id`) become one entry.
4. **Verify (optional).** With `--verify-ssh`, each proposed entry is checked with the existing
   `verifySSHConnection`; entries that fail are listed under "not added: ssh failed" and left out.
   Without it, proposed entries are marked unverified in the output.
5. **Report.** Text output has three parts: entries to add, configured nodes with drift, and
   configured nodes not seen by any source. It ends with a unified diff of the file. `--format json`
   gives the same data for agents. Nothing is written.
6. **Write (`--write`).** The proposal is applied to the file's YAML node tree
   (`gopkg.in/yaml.v3`, already a dependency), appending new entries to `nodes:` so existing
   entries, order and comments are untouched. The result is decoded with `config.Load`'s strict
   decoder and validated with `Config.Validate` before an atomic replace with a timestamped backup,
   the same guarantees `SaveAtomic` gives. Each appended entry carries a comment naming its source
   and the date.
7. **Exit codes.** 0 when the run completes, whether or not there are changes; `ExitErrConfigLoad`
   (2) when the existing file cannot be loaded; `ExitErrIO` (6) when the write fails. A `--write`
   with nothing to add makes no write and no backup.

The interactive wizard is unchanged in this PR. Moving its saves to the node-tree writer, so they
also keep comments, is a follow-up.

## Tests

`cmd/axis/init_generate_test.go`, with the source seams stubbed and fixtures using RFC 5737 and
RFC 2606 addresses only:

- a Tailscale peer and a beacon for a new machine become one proposed entry;
- a candidate matching by `stable_id`, by `hostname` and by `name` each proposes nothing;
- a matched node at a new address is reported as drift and the file is unchanged;
- a configured node no source sees is reported as not seen and kept;
- a name collision gets a numeric suffix;
- `--ssh-user` wins over the beacon's user, which wins over the current user;
- `--verify-ssh` leaves out a candidate whose check fails, and lists it;
- a failing source is reported and the other source still runs;
- without `--write`, the file's bytes are unchanged;
- with `--write`, every comment and existing entry is byte-identical, new entries are appended,
  a backup exists, and the file passes `config.Load`;
- a proposal that would fail `Config.Validate` is not written;
- `--write` with no additions writes nothing and makes no backup;
- `--format json` carries the same three lists.

## Alternatives

| Option | Why not chosen |
| --- | --- |
| The daemon registers nodes as beacons arrive (self-registration, like kubelet) | Puts an unauthenticated network signal in charge of operator-authored config; AXIS keeps discovery suggestions advisory. |
| A runtime inventory that replaces `nodes.yaml` (Ansible-style dynamic inventory) | Placement and reservations key on a stable, reviewed node list; a list that changes with each probe would orphan ledger entries keyed by name. |
| A second file, `nodes.generated.yaml`, merged at load time | Two sources of truth for one list, and conflicts would be resolved silently at load. |
| Re-marshal the struct (reuse `SaveAtomic`) | Drops every comment in the file, which is the second problem above. |

## Research

- Kubernetes nodes self-register with the API server by default, and the docs describe manual
  registration as the alternative when the operator wants control over membership
  (<https://kubernetes.io/docs/concepts/architecture/nodes/>). This spec chooses the
  operator-controlled side.
- Consul's cloud auto-join discovers peers from provider APIs at agent start
  (<https://developer.hashicorp.com/consul/docs/install/cloud-auto-join>): discovery feeds
  membership, but the join configuration is still declared by the operator.
- `tailscale status --json` is the documented machine-readable peer list
  (<https://tailscale.com/kb/1080/cli>), which `discoverTailscalePeers` already parses.
- `gopkg.in/yaml.v3`'s `Node` type keeps head, line and foot comments through decode and encode
  (<https://pkg.go.dev/gopkg.in/yaml.v3>), which makes appending entries without losing comments
  possible with the existing dependency.

Optimization chosen: reuse the wizard's sources, matcher inputs and validation, and edit the YAML
node tree instead of the struct, so the new code is the matching, the report and the writer.

## Review
