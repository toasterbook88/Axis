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
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

printf '\nrelease preflight passed for %s\n' "$(git rev-parse HEAD)"
