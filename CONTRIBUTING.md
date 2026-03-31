# Contributing to AXIS

Thank you for your interest in contributing. This document covers how to build,
test, and contribute effectively.

## Building

```bash
git clone https://github.com/toasterbook88/axis.git
cd axis
go build -o axis ./cmd/axis/
```

Requires Go 1.26.1+.

## Running Tests

```bash
go test ./...
go test -race ./...
./hack/coverage-check.sh
```

## Releasing

AXIS release artifacts are built from signed `v*` tags by GitHub Actions and
GoReleaser.

1. Update `internal/buildinfo/version.go`.
2. Run the full local validation set:

```bash
go test ./... -count=1
go test -race ./... -count=1
go build ./...
./hack/coverage-check.sh
```

3. Commit the version bump.
4. Create and push a signed tag such as `v0.2.1`.

The release workflow verifies that the pushed tag matches
`internal/buildinfo/version.go` before publishing binaries.

Format the code before submitting:

```bash
gofmt -w .
```

## Scope Discipline

AXIS is intentionally narrow. Contributions should reduce operator confusion and
keep model-mediated or execution-heavy surfaces subordinate to observed state.

Phase 2 (UDS+bearer-auth API and advisory task placement) is complete and stable;
those surfaces are part of the existing scope and can be improved or bug-fixed.

**Do:**
- Fix bugs in existing fact collectors, discovery, snapshot assembly, placement, API server, or authentication layer
- Improve error handling or robustness of existing collectors
- Add test coverage for existing behavior
- Improve documentation accuracy
- Prefer deleting dead or duplicate complexity over preserving it

**Do not:**
- Add new duplicate control surfaces or background authority paths without a prior discussion
- Treat generated output as cluster truth unless it is backed by a real snapshot or live probe
- Add heavy dependencies without strong justification — the project intentionally
  keeps its dependency surface small (currently: `cobra`, `golang.org/x/crypto`,
  `gopkg.in/yaml.v3`, `mcp-go`, and `shellescape`)
- Overfit to a specific private cluster topology (e.g., hardcoded interface names,
  private hostnames, or vendor-specific tool names that are not broadly installed)

## Accuracy Expectations

If you change the behavior of a collector or output format:
- Explain why the new behavior is more accurate or correct
- Provide evidence (e.g., test output, platform documentation) for
  platform-dependent changes
- Prefer tolerating failures gracefully — a degraded node status is better than
  a crash or a silent omission

## Reporting Issues

Open a GitHub issue with:
- AXIS version (`axis version`)
- OS and architecture
- Relevant command output or error message

For security-sensitive issues, follow [SECURITY.md](SECURITY.md) and use
private vulnerability reporting instead of a public issue.

## Pull Requests

- Keep PRs small and focused on a single concern
- Include test coverage for new behavior where practical
- Reference the relevant issue if one exists
- All existing tests must pass (`go test ./...`)
