#!/usr/bin/env bash
# verify-doc-facts.sh — keep canonical docs in agreement with live code.
#
# hack/verify-repo-truth.sh only covers README.md + docs/current-state.md
# (release-tag / generated-facts freshness). It never reads exit.go, main.go,
# internal/mcp/*, or CHANGELOG.md, so code/doc drift in those surfaces goes
# undetected. This script closes that gap with purely local code<->doc
# cross-checks — no network required:
#
#   1. Every exit-code constant in cmd/axis/exit.go appears in AGENTS.md.
#   2. AGENTS.md command count == root.AddCommand calls in cmd/axis/main.go
#      == the command table row count.
#   3. AGENTS.md MCP tool count (total / read-only / advisory-lease) == the
#      s.AddTool registrations in internal/mcp, with the read-only subset
#      matching WithReadOnlyHintAnnotation(true) in server.go.
#   4. Canonical docs do not contradict live mesh and reservation wiring.
#   5. Every released git tag >= v0.7.0 has a CHANGELOG.md entry.
#
# When these disagree, fix the doc to match the code (the code is the source
# of truth) or, for CHANGELOG, add the missing entry from the GitHub release
# body — do not weaken the check to make it pass.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { printf 'verify-doc-facts: %s\n' "$1" >&2; exit 1; }

# --- 1. Exit codes -----------------------------------------------------------
# Every constant defined in cmd/axis/exit.go must be cited in AGENTS.md.
while IFS= read -r c; do
  [[ -z "$c" ]] && continue
  grep -qF "\`$c\`" AGENTS.md \
    || fail "AGENTS.md exit-code table missing constant \`$c\` (defined in cmd/axis/exit.go)"
done < <(grep -oE 'Exit(OK|Err[A-Za-z]+)' cmd/axis/exit.go | sort -u)

# --- 2. Command count --------------------------------------------------------
main_count="$(grep -cE '\.AddCommand\(' cmd/axis/main.go)"

doc_count="$(grep -oE '[0-9]+ top-level commands registered' AGENTS.md | grep -oE '^[0-9]+' || true)"
[[ -n "$doc_count" ]] || fail "could not find '<N> top-level commands registered' claim in AGENTS.md"
[[ "$doc_count" == "$main_count" ]] \
  || fail "AGENTS.md claims $doc_count top-level commands; cmd/axis/main.go registers $main_count via AddCommand"

paren="$(grep -oE 'one file per subcommand \([0-9]+ commands\)' AGENTS.md | grep -oE '[0-9]+' || true)"
[[ -n "$paren" ]] || fail "could not find 'one file per subcommand (<N> commands)' in AGENTS.md"
[[ "$paren" == "$main_count" ]] \
  || fail "AGENTS.md claims ($paren commands); cmd/axis/main.go registers $main_count via AddCommand"

table_rows="$(grep -cE '^\| `axis' AGENTS.md)"
[[ "$table_rows" == "$main_count" ]] \
  || fail "AGENTS.md command table has $table_rows \`axis\` rows; cmd/axis/main.go registers $main_count"

# --- 3. MCP tool count -------------------------------------------------------
# Registrations are s.AddTool(...) calls in internal/mcp (non-test files).
mcp_code="$(grep -rn 's\.AddTool(' internal/mcp/*.go | grep -v _test | wc -l | tr -d ' ')"
# Read-only tools may be registered from any non-test file under internal/mcp/
# (e.g. server.go, inference_route.go), not only server.go.
ro_code="$(grep -rn 'WithReadOnlyHintAnnotation(true)' internal/mcp/*.go | grep -v _test | wc -l | tr -d ' ')"
lease_code="$(grep -c 's\.AddTool(' internal/mcp/triangle.go)"

# AGENTS.md states "<N> tools (<R> read-only ... + <L> advisory lease ...)",
# possibly wrapped across lines, so pull each number independently.
total_doc="$(grep -oE '[0-9]+ tools \([0-9]+ read-only' AGENTS.md | grep -oE '^[0-9]+' || true)"
ro_doc="$(grep -oE '[0-9]+ tools \([0-9]+ read-only' AGENTS.md | grep -oE '\([0-9]+' | grep -oE '[0-9]+' || true)"
lease_doc="$(grep -oE '[0-9]+ advisory lease' AGENTS.md | grep -oE '^[0-9]+' || true)"
[[ -n "$total_doc" && -n "$ro_doc" && -n "$lease_doc" ]] \
  || fail "could not parse AGENTS.md MCP tool count (expected '<N> tools (<R> read-only ... + <L> advisory lease ...)'"

[[ "$total_doc" == "$mcp_code" ]] \
  || fail "AGENTS.md claims $total_doc MCP tools; internal/mcp registers $mcp_code via s.AddTool"
[[ "$ro_doc" == "$ro_code" ]] \
  || fail "AGENTS.md claims $ro_doc read-only MCP tools; internal/mcp has $ro_code WithReadOnlyHintAnnotation(true)"
[[ "$lease_doc" == "$lease_code" ]] \
  || fail "AGENTS.md claims $lease_doc advisory lease tools; internal/mcp/triangle.go registers $lease_code via s.AddTool"
[[ $((ro_doc + lease_doc)) == "$total_doc" ]] \
  || fail "AGENTS.md MCP counts don't add up: $ro_doc read-only + $lease_doc lease != $total_doc total"

for doc in docs/architecture.md docs/current-state.md docs/lifecycle.md; do
  grep -qF "$ro_code read-only" "$doc" \
    || fail "$doc missing current MCP read-only tool count ($ro_code)"
  grep -qF "$lease_code advisory" "$doc" \
    || fail "$doc missing current MCP advisory lease count ($lease_code)"
done

# --- 4. Live wiring claims ---------------------------------------------------
# These surfaces previously remained documented as scaffolded long after their
# startup, config, API, and CLI paths shipped. Anchor the prohibition to the
# corresponding code so future removal can update code and docs together.
reject_stale_claim() {
  local pattern="$1"
  shift
  if grep -Ein "$pattern" "$@" >/dev/null; then
    fail "canonical docs contain a stale live-wiring claim matching /$pattern/"
  fi
}

if grep -qF 'd.WatchMesh(ctx, selfPeer)' cmd/axis/serve.go; then
  reject_stale_claim 'mesh.*(scaffold|not wired)|scaffold.*mesh|not wired.*mesh' \
    README.md docs/architecture.md docs/current-state.md docs/lifecycle.md
fi

if grep -qF 'meshCfg.SharedSecret = cfg.Discovery.Secret' internal/daemon/daemon.go; then
  reject_stale_claim 'mesh.*(does not consume|not propagated|not wired to config|empty default)' \
    docs/authority-config.md docs/authority-secrets.md
fi

reservation_handler="$(sed -n '/^func (h \*v2Handlers) handleReservations/,/^}/p' internal/api/v2.go)"
if grep -qF 'case http.MethodPost:' <<<"$reservation_handler"; then
  reject_stale_claim '(reservations|/v2/reservations).*(returns|return).*501|POST /v2/reservations.*not.*implemented' \
    docs/current-state.md docs/lifecycle.md docs/reservations.md
fi

if grep -qF 'cmd.AddCommand(reservationsDoctorCmd())' cmd/axis/reservations.go; then
  reject_stale_claim 'no (standalone|dedicated) CLI' \
    docs/current-state.md docs/lifecycle.md docs/reservations.md
fi

if grep -qF 'cmdAI := aiCmd()' cmd/axis/main.go; then
  reject_stale_claim 'Powers `axis llm`' docs/current-state.md
fi

# --- 5. CHANGELOG completeness ----------------------------------------------
# Every released tag >= v0.7.0 (CHANGELOG's coverage floor) must have a
# "## vX.Y.Z" header. Skipped silently when no tags are available (e.g. a
# shallow checkout without fetch-tags) so local runs still work.
tags="$(git tag --list 'v*' 2>/dev/null || true)"
if [[ -z "$tags" ]]; then
  printf 'verify-doc-facts: no git tags available; skipping CHANGELOG completeness check\n'
else
  missing=()
  while IFS= read -r tag; do
    [[ -z "$tag" ]] && continue
    ver="${tag#v}"
    major="${ver%%.*}"
    rest="${ver#*.}"
    minor="${rest%%.*}"
    if (( major < 1 && minor < 7 )); then
      continue   # below CHANGELOG coverage floor (v0.7.0)
    fi
    grep -qE "^## v${ver}([^0-9]|$)" CHANGELOG.md || missing+=("$tag")
  done < <(git tag --list 'v*' --sort=v:refname)
  if (( ${#missing[@]} > 0 )); then
    fail "CHANGELOG.md missing entries for released tags: ${missing[*]}"
  fi
fi

./hack/verify-public-boundary.sh

printf 'doc facts guardrails passed\n'
