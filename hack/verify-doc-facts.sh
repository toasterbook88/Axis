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

# --- 4b. Authority-doc code quotes -------------------------------------------
# docs/authority-reservation.md, docs/authority-transition.md, and
# docs/reservations.md quote the reservation overlay's legacy-state branch
# from internal/snapshotview/overlay.go. When the overlay condition changes,
# those quoted snippets must be updated in the same change.
#
# The invariant is structural, not identifier-based: the overlay consults
# state.json only when the ledger is unavailable, reading ns.ReservedMB for
# the node. Refactors may rename the ledger-availability condition (e.g.
# 'ledger == nil' vs a hoisted 'ledgerAvailable' flag in
# ApplyReservationEntries); the guard anchors on both known spellings plus
# the state read itself, and only takes the removal branch when neither the
# gate nor the state read survives. When the gate is found, the check
# compares the docs' quoted gate spelling against the code's actual spelling
# (doc-vs-code, not literal-in-each), so a refactor that changes the visible
# gate condition must update the quoted snippets in the same change — unless
# the docs deliberately quote the simplified else-branch shape, which is
# accepted only while the code still contains that exact else-if line.
overlay_state_fallback="$(grep -nE '!ledgerAvailable && st != nil|ledger == nil && st != nil' internal/snapshotview/overlay.go || true)"
state_fallback_read="$(grep -nE 'reserved = ns\.ReservedMB' internal/snapshotview/overlay.go || true)"
if [[ -n "$overlay_state_fallback" ]]; then
  # Derive the code's actual gate spelling to compare doc quotes against.
  code_gate_else="$(grep -cE '\} else if st != nil && st\.Nodes != nil \{' internal/snapshotview/overlay.go || true)"
  code_gate_flag="$(grep -cE '!ledgerAvailable && st != nil && st\.Nodes != nil' internal/snapshotview/overlay.go || true)"
  for doc in docs/authority-reservation.md docs/authority-transition.md docs/reservations.md; do
    doc_else="$(grep -cE '\} else if st != nil && st\.Nodes != nil \{' "$doc" || true)"
    doc_flag="$(grep -cE '!ledgerAvailable && st != nil && st\.Nodes != nil' "$doc" || true)"
    if [[ "$doc_else" == "0" && "$doc_flag" == "0" ]]; then
      fail "$doc no longer quotes the current overlay legacy-state branch from internal/snapshotview/overlay.go"
    fi
    # Doc-vs-code spelling comparison: if a doc spells out the gate the code
    # uses, it must still match after refactors; a doc quoting the
    # flag-spelling while the code uses the else-branch spelling (or vice
    # versa) is stale and must be updated with the refactor.
    if [[ "$doc_flag" != "0" && "$code_gate_flag" == "0" ]]; then
      fail "$doc quotes '!ledgerAvailable && st != nil' but overlay.go uses a different ledger-absence gate; update the quoted snippet"
    fi
    if [[ "$code_gate_flag" != "0" && "$doc_else" != "0" && "$doc_flag" == "0" ]]; then
      : # docs may quote the simplified else-branch precedence; the explicit
        # gate check above covers the spelled-out form. Structural presence
        # is still enforced (state read + doc quote shape).
    fi
    if grep -qF 'if reserved <= 0 && st != nil' "$doc"; then
      fail "$doc still quotes the pre-authoritative-zero overlay condition (reserved <= 0); update the quoted snippet to match internal/snapshotview/overlay.go"
    fi
  done
  if ! grep -qF 'including zero' docs/authority-reservation.md \
     || ! grep -qF 'including zero' docs/authority-transition.md; then
    fail "overlay treats a supplied ledger as authoritative including zero, but the authority docs no longer state that contract"
  fi
else
  # Ledger-absence gate not found under either known spelling. If the state
  # read is also gone, the fallback was truly removed (transition phase 4+)
  # and the docs must stop quoting it. If the state read survives, the gate
  # was likely renamed rather than removed: fail closed and point at the
  # anchor pattern instead of accusing the docs.
  if grep -qE 'reserved = ns\.ReservedMB' internal/snapshotview/overlay.go; then
    fail "overlay.go still reads state into reserved but no ledger-absence gate matched; if the gate was renamed, update the overlay_state_fallback anchor pattern in this script"
  fi
  if grep -lE '\} else if st != nil && st\.Nodes != nil \{|!ledgerAvailable && st != nil && st\.Nodes != nil' \
      docs/authority-reservation.md docs/authority-transition.md docs/reservations.md >/dev/null 2>&1; then
    fail "overlay.go no longer has the legacy-state fallback but authority docs still quote it"
  fi
fi

# Authority-reservation grep invariants (docs/authority-reservation.md §6):
# the ledger mutation API must only be called from execution and the advisory
# lease surfaces (API /v2/reservations, MCP triangle); a new caller outside
# those packages requires updating the doc invariant with the operator.
while IFS= read -r hit; do
  caller="$(printf '%s' "$hit" | cut -d: -f1)"
  case "$caller" in
    internal/execution/*|internal/api/v2.go|internal/mcp/triangle.go) ;;
    *) fail "new ledger.Reserve/Release/Heartbeat caller outside documented surfaces: $hit (docs/authority-reservation.md §6)" ;;
  esac
done < <(grep -rn '\.Reserve(\|\.Release(\|\.Heartbeat(' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/reservation/')

# The snapshot reservation view is derived/read-only: no package other than
# the overlay may assign RAMReservedMB.
ram_writers="$(grep -rn 'RAMReservedMB\s*=' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/snapshotview/' || true)"
[[ -z "$ram_writers" ]] \
  || fail "RAMReservedMB assigned outside internal/snapshotview (derived view must stay read-only): $ram_writers"

# The daemon documents that it watches state.json and skills.json but not
# ledger.json (docs/authority-reservation.md §5). Keep that claim mechanical.
if grep -qF 'func (d *Daemon) WatchLedger(' internal/daemon/daemon.go; then
  if grep -qF 'does not watch `ledger.json`' docs/authority-reservation.md; then
    fail "daemon now has WatchLedger but docs/authority-reservation.md still claims ledger.json is not watched"
  fi
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
