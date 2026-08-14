#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# Keep the operator's Go caches warm while isolating every AXIS path beneath a
# disposable HOME. AXIS_HOME must be explicitly empty because it outranks HOME.
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export GOPATH="${GOPATH:-$(go env GOPATH)}"
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"

axis_test_home="$(mktemp -d "${TMPDIR:-/tmp}/axis-test-home.XXXXXX")"
trap 'rm -rf "$axis_test_home"' EXIT

HOME="$axis_test_home" AXIS_HOME= go test "$@"
