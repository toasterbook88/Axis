#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

require_clean=0
if [[ "${1:-}" == "--require-clean" ]]; then
  require_clean=1
  shift
fi
if (($# > 0)); then
  printf 'usage: %s [--require-clean]\n' "$0" >&2
  exit 2
fi

if ((require_clean == 1)) && [[ -n "$(git status --porcelain)" ]]; then
  printf 'CI preflight requires a clean working tree\n' >&2
  exit 1
fi

# actionlint silently skips its shell analysis when shellcheck is unavailable,
# which would make the local preflight weaker than GitHub CI.
if ! command -v shellcheck >/dev/null 2>&1; then
  printf 'CI preflight requires shellcheck for workflow run-block validation\n' >&2
  exit 1
fi

run() {
  printf '\n==> %s\n' "$*"
  "$@"
}

run make lint
run make test
run make test-race
run make test-install
run go build -buildvcs=false ./...
run make coverage
run ./hack/verify-public-boundary.sh
run ./hack/verify-repo-truth.sh
run ./hack/verify-doc-facts.sh
run python3 hack/claude-workflow-tests.py
run python3 hack/workflow-action-pins.py
run go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
run go run hack/lifecycle-check.go

printf '\nCI preflight passed for %s\n' "$(git rev-parse HEAD)"
