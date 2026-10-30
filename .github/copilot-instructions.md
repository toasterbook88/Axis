# Copilot instructions for AXIS

Read [`AGENTS.md`](../AGENTS.md) — it is the canonical instruction file for every agent in this repo.

Non-negotiables, repeated here because some Copilot surfaces load only this file:

- **Truth Rule:** no output may present itself as cluster truth unless backed by a real snapshot or live probe.
- **Public repo:** no real hostnames, IPs, inventories or secrets anywhere (RFC 2606 / documentation ranges only).
- **Surgical changes**, tests first for bugs, and run `make ci-preflight` before proposing a PR.
- **Never merge** a PR unless the maintainer explicitly asks to merge that PR.
- **New features need a reviewed spec** before code: see `AGENTS.md` §8.
