# Contributing to AXIS

Thank you for your interest in contributing. This document covers how to build,
test, and contribute effectively.

## Building

```bash
git clone https://github.com/toasterbook88/axis.git
cd axis
go build -o axis ./cmd/axis/
```

Requires Go 1.22+.

## Running Tests

```bash
go test ./...
```

Format the code before submitting:

```bash
gofmt -w .
```

## Scope Discipline

AXIS is intentionally minimal. Contributions should fit within the existing
architecture rather than extending its scope.

**Do:**
- Fix bugs in existing fact collectors, discovery, snapshot assembly, or placement
- Improve error handling or robustness of existing collectors
- Add test coverage for existing behavior
- Improve documentation accuracy

**Do not:**
- Add a daemon / background coordinator without a prior discussion in an issue
- Add mesh networking or peer discovery beyond the static seed file without discussion
- Add heavy dependencies without strong justification — the project intentionally
  keeps its dependency surface small (currently: `cobra`, `golang.org/x/crypto`,
  `gopkg.in/yaml.v3`)
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

## Pull Requests

- Keep PRs small and focused on a single concern
- Include test coverage for new behavior where practical
- Reference the relevant issue if one exists
- All existing tests must pass (`go test ./...`)
