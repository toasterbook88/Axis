#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

./hack/ci-preflight.sh "$@"

printf '\n==> goreleaser check\n'
go run github.com/goreleaser/goreleaser/v2@v2.18.0 check

printf '\n==> govulncheck ./...\n'
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

printf '\nrelease preflight passed for %s\n' "$(git rev-parse HEAD)"
