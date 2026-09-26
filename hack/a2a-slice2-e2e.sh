#!/usr/bin/env bash
# A2A slice 2v1 E2E harness — runs the Go test that writes the JSON artifact.
# Usage: from repo root: ./hack/a2a-slice2-e2e.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p artifacts
echo "Running A2A slice2 E2E (unix socket, non-empty token)…"
go test ./internal/api/ -run TestA2ASlice2E2E -count=1 -v
echo "Artifacts:"
ls -la artifacts/a2a-slice2-e2e-*.json 2>/dev/null || true
ls -la /workspace/axis-eval/a2a-slice2-e2e-*.json 2>/dev/null || true
