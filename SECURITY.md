# Security Policy

## Supported Versions

AXIS supports the latest tagged release line for security fixes.

| Version | Supported |
| --- | --- |
| `v0.2.x` | yes |
| older tagged releases | no |
| `main` | best effort only; use a tagged release for operator-facing deployments |

## Reporting a Vulnerability

Please do not open a public GitHub issue for an unpatched vulnerability.

Use GitHub private vulnerability reporting for this repository. Include:

- AXIS version (`axis version`)
- host OS and architecture
- affected command or surface (`axis task run`, HTTP `/run`, MCP, release/install, config parsing, etc.)
- impact and repro steps
- whether the issue affects observed-state integrity, secret handling, or remote execution safety

Issues that let generated output present itself as authoritative cluster truth
are security issues in AXIS and should be reported privately.

## Scope Notes

The highest-priority reports for AXIS include:

- remote execution or confirmation bypasses
- host-key or trust-surface failures
- secret disclosure in logs, config handling, or execution context
- model-mediated output being treated as observed cluster truth
- release or installation supply-chain issues
