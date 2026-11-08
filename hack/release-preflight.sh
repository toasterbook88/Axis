#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

./hack/ci-preflight.sh "$@"

printf '\n==> goreleaser check\n'
# GoReleaser v2.18.0 requires Go 1.27. Scope its compiler to this command;
# AXIS lint, tests, builds and govulncheck retain the caller's Go 1.26 toolchain.
GOTOOLCHAIN=go1.27.0 go run github.com/goreleaser/goreleaser/v2@v2.18.0 check

printf '\n==> govulncheck ./...\n'
# Same pinned scanner and reviewed allow-list as the govulncheck and Release
# workflows. Text mode exits 3 when a called advisory is allowlisted; the gate
# accepts hack/govulncheck-allowlist.txt until its recheck date and fails on
# any other called advisory.
govuln_bin="$(mktemp -d)"
GOBIN="$govuln_bin" go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
# The gate imports its module. Without this, CPython writes hack/__pycache__
# and the next --require-clean run fails on a tree this script just checked.
PYTHONDONTWRITEBYTECODE=1 PATH="$govuln_bin:$PATH" python3 hack/govulncheck-gate-tests.py
PYTHONDONTWRITEBYTECODE=1 PATH="$govuln_bin:$PATH" python3 hack/govulncheck_gate.py
rm -rf "$govuln_bin"

printf '\nrelease preflight passed for %s\n' "$(git rev-parse HEAD)"
