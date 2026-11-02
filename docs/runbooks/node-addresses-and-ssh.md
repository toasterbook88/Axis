# Node addresses and SSH config

How AXIS picks the address it dials for each node in `~/.axis/nodes.yaml`, what it reads from your
SSH config, what it refuses, and how to repair a node whose address has drifted.

## How AXIS picks an address

A node has a `hostname` and optional `endpoints`. AXIS tries the endpoints in the order listed, then
`hostname` (`DialHostnames`, `internal/config/config.go`). The first endpoint is the primary dial
target, even when `hostname` differs from it.

To keep the LAN as the primary path with a Tailscale fallback, list the LAN endpoint first:

```yaml
- name: box
  hostname: 192.0.2.40
  endpoints:
  - name: lan
    hostname: 192.0.2.40
  - name: tailscale
    hostname: 100.64.0.40
  ssh_user: operator
```

Adding only a Tailscale endpoint makes Tailscale the primary path, because endpoints come before
`hostname`.

For each candidate, AXIS runs `ssh -G` with the node's port and `ssh_user` and dials the resolved
`HostName` and `Port`, with the resolved identities and known-hosts files. It reads the
system-wide `ssh_config` when `$HOME` is your `/etc/passwd` home, as your own `ssh` does. When
`$HOME` is overridden, or when a run without `-F` fails (OpenSSH checks config file permissions only
then), it uses `-F ~/.ssh/config` and logs a warning for the failure case.

`axis serve` watches `nodes.yaml` and refreshes when its contents change, so an edit takes effect
without a restart.

## What AXIS refuses

AXIS's SSH client does not implement `ProxyJump` or `ProxyCommand`. If `ssh -G` reports either for a
candidate, AXIS does not dial that candidate and moves on to the next one. If none connects, the
error names the directive and the host. `axis doctor` shows the fix on one line: "Axis does not use
ProxyJump: point hostname or an endpoint at an address Axis can reach directly, or add "ProxyJump
none" in a more specific Host block before the wildcard that sets it".

`Match exec` is not refused: `ssh -G` runs those commands while resolving, as your own `ssh` does.

## Spotting address drift

- `axis doctor` shows `unreachable (<addr>): ...` for the node, often with `no route to host` or
  `context deadline exceeded`.
- `axis init --generate` proposes a tailnet machine with the node's name and flags it
  `possible duplicate of <node>` (or `... (by LAN endpoint)`). AXIS matches a configured node to a
  tailnet machine only by an address it would dial, never by name or LAN endpoint alone, because two
  sites can share one private subnet. The flag means the node is configured only by a LAN address,
  or its address is stale.
- Ask the machine itself, over any path that still works (its Tailscale address, a new LAN address,
  or the console): `ip -br addr` or `ifconfig` for LAN addresses, `tailscale ip -4` for the tailnet
  address.

## Repairing a node

1. Back up `~/.axis/nodes.yaml` and `~/.ssh/config`.
2. In `nodes.yaml`, set `hostname` to the address the machine reports, and list endpoints LAN first,
   then any other LAN interface, then Tailscale.
3. If `~/.ssh/config` has a `Host` alias for the node, update its `HostName`. Check that
   `known_hosts` has the machine's key under the new address (`ssh-keygen -F <addr>`); if it does
   not, verify the fingerprint out of band before adding it.
4. Repeat steps 2 and 3 on every node whose own `nodes.yaml` or SSH config lists the machine. Each
   node keeps its own copies.
5. Verify: `ssh <alias>` reaches the machine, `axis doctor` shows the node connected, and
   `axis init --generate` no longer flags it as a possible duplicate.
