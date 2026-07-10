# Contributing to AXIS

Thank you for your interest in contributing. This document covers how to build,
test, and contribute effectively.

## Building

```bash
git clone https://github.com/toasterbook88/axis.git
cd axis
make build                 # ./axis with commit/date ldflags
make install-user          # ~/.local/bin/axis (preferred on Cranium)
# make install              # $GOPATH/bin/axis (legacy; often not on PATH)
```

Requires Go 1.26.1+ (`go.mod` is authoritative).

Prove which binary you are running:

```bash
which -a axis
axis version               # must show commit: + built: when installed via Make
# commit c32c761 (etc.) = tip of main; release tags embed the tagged commit
```

After installing a new binary on a host that uses the daemon:

```bash
axis daemon restart && axis daemon status
```

## Running Tests

```bash
make lint
make test
make test-race
make coverage
```

## Releasing

AXIS release artifacts are built from `v*` tags by GitHub Actions and GoReleaser.
**GoReleaser creates the GitHub Release.** Do not run `gh release create` before
the tag workflow — that breaks repo-truth gates and leaves releases without assets.

1. Update `internal/buildinfo/version.go` and `CHANGELOG.md`.
2. Ensure generated facts in `docs/current-state.md` describe repo version relative
   to the **previous** published tag as "ahead" (tag name only; no live timestamps).
3. Run local validation:

```bash
make lint
make test
make test-race
make coverage
```

4. Merge the release-prep PR to `main`.
5. Create and push a tag matching `version.go` (lightweight tags are fine today):

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

The release workflow verifies the tag matches `internal/buildinfo/version.go`,
then GoReleaser publishes multi-arch assets. Operators install with
`axis update` or `install.sh`.

Format the code before submitting (`make lint` must pass).

## Scope Discipline

AXIS is intentionally narrow. Contributions should reduce operator confusion and
keep model-mediated or execution-heavy surfaces subordinate to observed state.

Phase 2 (UDS+bearer-auth API and advisory task placement) and Phase 3 (daemon
hardening, event-driven cache, context export, UDP HMAC) are complete and stable;
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
