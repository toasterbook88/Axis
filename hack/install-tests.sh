#!/usr/bin/env bash
#
# Regression tests for install.sh.
#
# These exist because the installer's safety properties were previously verified
# by hand, and a hand test asserted on the wrong file: it checked that a *legacy*
# copy in another directory survived a failed install, while the canonical binary
# at the destination had already been overwritten. The guarantees below are the
# ones that must not silently regress.
#
# Hermetic: a local release tree is served over file:// via AXIS_RELEASE_BASE_URL,
# so no network access and no real /usr/local/bin are involved.
#
# Usage: hack/install-tests.sh          (or: make test-install)

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_SH="$REPO_ROOT/install.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/axis-install-tests-XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  FAIL %s\n' "$1"; [ -n "${2:-}" ] && printf '       %s\n' "$2"; }
skip() { SKIP=$((SKIP+1)); printf '  SKIP %s\n' "$1"; }

if command -v shasum >/dev/null 2>&1; then SHA="shasum -a 256"; else SHA="sha256sum"; fi

# Build a fake release tree: axis_<ver>_<os>_<arch>.tar.gz + checksums.txt.
# $1 = version, $2 = the script body the fake binary should have.
make_release() {
    local ver="$1"
    local body="$2"
    local dir="$WORK/releases/v$ver"
    local os arch
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) arch=$(uname -m) ;; esac
    mkdir -p "$dir" "$WORK/build"
    printf '%s\n' "$body" > "$WORK/build/axis"
    chmod +x "$WORK/build/axis"
    tar -czf "$dir/axis_${ver}_${os}_${arch}.tar.gz" -C "$WORK/build" axis
    ( cd "$dir" && $SHA "axis_${ver}_${os}_${arch}.tar.gz" > checksums.txt )
}

run_install() {  # env overrides passed as VAR=VAL before the call
    env "$@" \
        AXIS_RELEASE_BASE_URL="file://$WORK/releases" \
        bash "$INSTALL_SH" 2>&1
}

GOOD_BODY='#!/bin/sh
echo "axis 9.9.9"'
BROKEN_BODY='#!/bin/sh
exit 127'

# ---------------------------------------------------------------------------
echo "install.sh regression tests"

# 1. THE REGRESSION THAT MOTIVATED THIS FILE.
#    A validly-checksummed but unusable artifact must NOT destroy the working
#    binary already at the canonical path.
make_release 9.9.9 "$BROKEN_BODY"
H="$WORK/t1/home"; D="$WORK/t1/bin"; mkdir -p "$H" "$D"
printf '#!/bin/sh\necho "axis 1.2.3"\n' > "$D/axis"; chmod +x "$D/axis"
out=$(run_install HOME="$H" AXIS_INSTALL_DIR="$D" AXIS_VERSION=v9.9.9); rc=$?
if [ "$rc" -eq 0 ]; then
    bad "unusable artifact must fail the install" "exit was 0"
else
    ok "unusable artifact fails the install"
fi
if "$D/axis" version 2>/dev/null | grep -q '1.2.3'; then
    ok "canonical binary preserved when validation fails"
else
    bad "canonical binary preserved when validation fails" \
        "destination now: $("$D/axis" version 2>&1 | head -1)"
fi
if ls "$D"/.axis-install-* >/dev/null 2>&1; then
    bad "staged file cleaned up on failure" "leftover: $(ls "$D"/.axis-install-* | head -1)"
else
    ok "staged file cleaned up on failure"
fi

# 2. Happy path installs and validates.
make_release 9.9.9 "$GOOD_BODY"
H="$WORK/t2/home"; D="$WORK/t2/bin"; mkdir -p "$H" "$D"
out=$(run_install HOME="$H" AXIS_INSTALL_DIR="$D" AXIS_VERSION=v9.9.9)
if "$D/axis" version 2>/dev/null | grep -q '9.9.9'; then
    ok "happy path installs the new binary"
else
    bad "happy path installs the new binary" "$out"
fi
if ls "$D"/.axis-install-* >/dev/null 2>&1; then
    bad "staged file removed after success" "leftover present"
else
    ok "staged file removed after success"
fi

# 3. Version mismatch is rejected and preserves the canonical binary.
make_release 9.9.9 '#!/bin/sh
echo "axis 0.0.1"'
H="$WORK/t3/home"; D="$WORK/t3/bin"; mkdir -p "$H" "$D"
printf '#!/bin/sh\necho "axis 1.2.3"\n' > "$D/axis"; chmod +x "$D/axis"
out=$(run_install HOME="$H" AXIS_INSTALL_DIR="$D" AXIS_VERSION=v9.9.9); rc=$?
if [ "$rc" -ne 0 ] && "$D/axis" version 2>/dev/null | grep -q '1.2.3'; then
    ok "version mismatch rejected, canonical preserved"
else
    bad "version mismatch rejected, canonical preserved" "rc=$rc"
fi

# 4. System-scope install retires a user-local shadow.
make_release 9.9.9 "$GOOD_BODY"
H="$WORK/t4/home"; D="$WORK/t4/bin"; mkdir -p "$H/.local/bin" "$D"
printf '#!/bin/sh\necho "axis 0.0.1"\n' > "$H/.local/bin/axis"; chmod +x "$H/.local/bin/axis"
out=$(run_install HOME="$H" AXIS_INSTALL_DIR="$D" AXIS_VERSION=v9.9.9)
if [ -f "$H/.local/bin/axis" ]; then
    bad "system-scope install removes user shadow" "shadow still present"
else
    ok "system-scope install removes user shadow"
fi

# 5. User-scope install must never delete a copy outside $HOME.
make_release 9.9.9 "$GOOD_BODY"
H="$WORK/t5/home"; SYS="$WORK/t5/system"; mkdir -p "$H/.local/bin" "$SYS"
printf '#!/bin/sh\necho "axis 0.0.1"\n' > "$SYS/axis"; chmod +x "$SYS/axis"
# Point the hardcoded system candidate at our sandbox for this case only.
sed "s#\"/usr/local/bin/axis\"#\"$SYS/axis\"#" "$INSTALL_SH" > "$WORK/t5/install.sh"
out=$(env HOME="$H" AXIS_INSTALL_DIR="$H/.local/bin" AXIS_VERSION=v9.9.9 \
      AXIS_RELEASE_BASE_URL="file://$WORK/releases" bash "$WORK/t5/install.sh" 2>&1)
if [ -f "$SYS/axis" ]; then
    ok "user-scope install preserves the system copy"
else
    bad "user-scope install preserves the system copy" "system copy was deleted"
fi

# 6. A non-AXIS file at a legacy path is never removed.
make_release 9.9.9 "$GOOD_BODY"
H="$WORK/t6/home"; D="$WORK/t6/bin"; mkdir -p "$H/.local/bin" "$D"
printf '#!/bin/sh\necho "not axis"\n' > "$H/.local/bin/axis"; chmod +x "$H/.local/bin/axis"
out=$(run_install HOME="$H" AXIS_INSTALL_DIR="$D" AXIS_VERSION=v9.9.9)
if [ -f "$H/.local/bin/axis" ]; then
    ok "non-AXIS file at a legacy path is preserved"
else
    bad "non-AXIS file at a legacy path is preserved" "it was deleted"
fi

# 7. AXIS_KEEP_LEGACY=1 opts out of cleanup.
make_release 9.9.9 "$GOOD_BODY"
H="$WORK/t7/home"; D="$WORK/t7/bin"; mkdir -p "$H/.local/bin" "$D"
printf '#!/bin/sh\necho "axis 0.0.1"\n' > "$H/.local/bin/axis"; chmod +x "$H/.local/bin/axis"
out=$(run_install HOME="$H" AXIS_INSTALL_DIR="$D" AXIS_VERSION=v9.9.9 AXIS_KEEP_LEGACY=1)
if [ -f "$H/.local/bin/axis" ]; then ok "AXIS_KEEP_LEGACY=1 keeps shadows"; else bad "AXIS_KEEP_LEGACY=1 keeps shadows"; fi

# 8. Package-manager-owned install targets are refused, literal and via symlink.
for p in /opt/homebrew/bin /nix/store/x/bin /snap/x/bin; do
    if run_install AXIS_INSTALL_DIR="$p" AXIS_DRY_RUN=1 >/dev/null 2>&1; then
        bad "refuses package-managed target $p"
    else
        ok "refuses package-managed target $p"
    fi
done
mkdir -p "$WORK/t8/Cellar/axis/bin"
ln -sfn "$WORK/t8/Cellar/axis/bin" "$WORK/t8/link"
if run_install AXIS_INSTALL_DIR="$WORK/t8/link" AXIS_DRY_RUN=1 >/dev/null 2>&1; then
    bad "refuses a symlink into a package-managed tree" "symlink bypassed the guard"
else
    ok "refuses a symlink into a package-managed tree"
fi

# 9. AXIS_REQUIRE_PINNED=1 refuses 'latest' and accepts an explicit pin.
if run_install AXIS_REQUIRE_PINNED=1 AXIS_VERSION=latest >/dev/null 2>&1; then
    bad "AXIS_REQUIRE_PINNED refuses 'latest'"
else
    ok "AXIS_REQUIRE_PINNED refuses 'latest'"
fi
if run_install AXIS_REQUIRE_PINNED=1 AXIS_VERSION=v9.9.9 AXIS_DRY_RUN=1 >/dev/null 2>&1; then
    ok "AXIS_REQUIRE_PINNED accepts an explicit pin"
else
    bad "AXIS_REQUIRE_PINNED accepts an explicit pin"
fi

# 10. Default target is /usr/local/bin with system scope, without installing there.
out=$(env -u AXIS_INSTALL_DIR AXIS_DRY_RUN=1 AXIS_VERSION=v9.9.9 \
      AXIS_RELEASE_BASE_URL="file://$WORK/releases" bash "$INSTALL_SH" 2>&1)
if printf '%s' "$out" | grep -q '/usr/local/bin' && printf '%s' "$out" | grep -q 'built-in default' \
   && printf '%s' "$out" | grep -q 'scope *: system'; then
    ok "default target is system-scoped /usr/local/bin"
else
    bad "default target is system-scoped /usr/local/bin" "$out"
fi

# 11. The pinned hint must be runnable. Under `curl | bash`, $0 is "bash", so a
#     hint of "AXIS_VERSION=… $0" would tell the operator to start a shell.
out=$(printf '%s' "$(cat "$INSTALL_SH")" | env AXIS_REQUIRE_PINNED=1 AXIS_VERSION=latest bash 2>&1 || true)
if printf '%s' "$out" | grep -qE '^[[:space:]]+curl -fsSL .*install\.sh'; then
    ok "piped invocation prints a curl-based pinned hint"
else
    bad "piped invocation prints a curl-based pinned hint" "$(printf '%s' "$out" | tail -4)"
fi
if printf '%s' "$out" | grep -qE 'AXIS_REQUIRE_PINNED=1 (bash|/)'; then
    ok "piped hint does not end at a bare shell name"
else
    bad "piped hint does not end at a bare shell name" "$(printf '%s' "$out" | tail -4)"
fi
out=$(env AXIS_REQUIRE_PINNED=1 AXIS_VERSION=latest bash "$INSTALL_SH" 2>&1 || true)
if printf '%s' "$out" | grep -q "$INSTALL_SH"; then
    ok "file invocation prints a path-based pinned hint"
else
    bad "file invocation prints a path-based pinned hint" "$(printf '%s' "$out" | tail -4)"
fi

# 12. The package-manager guard must still hold when every external
#     canonicalization helper is missing — the case that previously failed open.
#     Stub readlink/realpath/python3 to failure and point the target at a symlink
#     into a Cellar tree. The builtin `cd -P` fallback must still resolve it and
#     the install must be refused.
mkdir -p "$WORK/t12/Cellar/axis/bin" "$WORK/t12/stub" "$WORK/t12/plain"
ln -sfn "$WORK/t12/Cellar/axis/bin" "$WORK/t12/pkglink"
ln -sfn "$WORK/t12/plain" "$WORK/t12/safelink"
for s in readlink realpath python3; do
    printf '#!/bin/sh\nexit 1\n' > "$WORK/t12/stub/$s"; chmod +x "$WORK/t12/stub/$s"
done
STUBBED_PATH="$WORK/t12/stub:$PATH"

if env PATH="$STUBBED_PATH" AXIS_INSTALL_DIR="$WORK/t12/pkglink" AXIS_DRY_RUN=1 \
       AXIS_VERSION=v9.9.9 AXIS_RELEASE_BASE_URL="file://$WORK/releases" \
       bash "$INSTALL_SH" >/dev/null 2>&1; then
    bad "symlink into a package tree is refused with no helpers available" \
        "guard failed open — this is the regression"
else
    ok "symlink into a package tree is refused with no helpers available"
fi

# When even `cd -P` cannot resolve (dangling symlink), the guard must refuse
# rather than assume safety. This is the fail-closed branch specifically; the
# assertion above is satisfied by successful fallback resolution instead.
ln -sfn "$WORK/t12/does-not-exist" "$WORK/t12/danglink"
if env PATH="$STUBBED_PATH" AXIS_INSTALL_DIR="$WORK/t12/danglink" AXIS_DRY_RUN=1 \
       AXIS_VERSION=v9.9.9 AXIS_RELEASE_BASE_URL="file://$WORK/releases" \
       bash "$INSTALL_SH" >/dev/null 2>&1; then
    bad "unresolvable symlink fails closed" "accepted an unresolvable target"
else
    ok "unresolvable symlink fails closed"
fi

# A path *beneath* an unresolved symlink must also fail closed. Checking only
# the leaf accepts "<dangling-link>/bin", since the leaf itself is not a symlink.
mkdir -p "$WORK/t12b"
ln -sfn "$WORK/t12b/missing-target" "$WORK/t12b/parent-link"
if env PATH="$STUBBED_PATH" AXIS_INSTALL_DIR="$WORK/t12b/parent-link/bin" AXIS_DRY_RUN=1 \
       AXIS_VERSION=v9.9.9 AXIS_RELEASE_BASE_URL="file://$WORK/releases" \
       bash "$INSTALL_SH" >/dev/null 2>&1; then
    bad "path beneath an unresolved symlink fails closed" "nested symlink bypassed the guard"
else
    ok "path beneath an unresolved symlink fails closed"
fi

# An ancestor that exists but cannot be entered must fail closed. "cd -P failed"
# does not imply "does not exist": a mode-000 directory is unreadable to this
# process while a later sudo install could traverse it.
if [ "$(id -u)" -eq 0 ]; then
    skip "permission-denied ancestor fails closed (running as root: mode 000 does not restrict traversal)"
else
    mkdir -p "$WORK/t12d/locked"
    chmod 000 "$WORK/t12d/locked"
    if env PATH="$STUBBED_PATH" AXIS_INSTALL_DIR="$WORK/t12d/locked/unknown/bin" AXIS_DRY_RUN=1 \
           AXIS_VERSION=v9.9.9 AXIS_RELEASE_BASE_URL="file://$WORK/releases" \
           bash "$INSTALL_SH" >/dev/null 2>&1; then
        bad "permission-denied ancestor fails closed" "unreadable ancestor was walked past"
    else
        ok "permission-denied ancestor fails closed"
    fi
    chmod 700 "$WORK/t12d/locked" 2>/dev/null || true
fi

# A regular file used as an intermediate path component is not a directory that
# mkdir -p can create, and must not be treated as "does not exist yet".
mkdir -p "$WORK/t12e"
printf 'not a directory\n' > "$WORK/t12e/afile"
if env PATH="$STUBBED_PATH" AXIS_INSTALL_DIR="$WORK/t12e/afile/bin" AXIS_DRY_RUN=1 \
       AXIS_VERSION=v9.9.9 AXIS_RELEASE_BASE_URL="file://$WORK/releases" \
       bash "$INSTALL_SH" >/dev/null 2>&1; then
    bad "regular-file ancestor fails closed" "a file was accepted as a parent directory"
else
    ok "regular-file ancestor fails closed"
fi

# ".." in the target is refused. The builtin fallback reattaches an unresolved
# ".." suffix literally, so "/tmp/new/../../opt/homebrew/bin" would not match the
# package-manager patterns even though mkdir -p lands there.
if env PATH="$STUBBED_PATH" AXIS_INSTALL_DIR="/tmp/axis-new-parent/../../opt/homebrew/bin" \
       AXIS_DRY_RUN=1 AXIS_VERSION=v9.9.9 AXIS_RELEASE_BASE_URL="file://$WORK/releases" \
       bash "$INSTALL_SH" >/dev/null 2>&1; then
    bad "'..' traversal into a package tree is refused" "dot-dot bypassed the guard"
else
    ok "'..' traversal into a package tree is refused"
fi

# A target whose parents simply do not exist yet must still be allowed: mkdir -p
# creates them, and refusing would break legitimate installs.
if env PATH="$STUBBED_PATH" AXIS_INSTALL_DIR="$WORK/t12c/newdir/bin" AXIS_DRY_RUN=1 \
       AXIS_VERSION=v9.9.9 AXIS_RELEASE_BASE_URL="file://$WORK/releases" \
       bash "$INSTALL_SH" >/dev/null 2>&1; then
    ok "not-yet-existing target directory is allowed"
else
    bad "not-yet-existing target directory is allowed" "over-refused a creatable path"
fi

# The same fallback must not produce false positives on an ordinary symlink.
if env PATH="$STUBBED_PATH" AXIS_INSTALL_DIR="$WORK/t12/safelink" AXIS_DRY_RUN=1 \
       AXIS_VERSION=v9.9.9 AXIS_RELEASE_BASE_URL="file://$WORK/releases" \
       bash "$INSTALL_SH" >/dev/null 2>&1; then
    ok "ordinary symlink target still allowed with no helpers available"
else
    bad "ordinary symlink target still allowed with no helpers available" \
        "guard is over-refusing"
fi

# 13. A corrupted archive must fail the checksum and never reach the canonical path.
make_release 9.9.9 "$GOOD_BODY"
H="$WORK/t13/home"; D="$WORK/t13/bin"; mkdir -p "$H" "$D"
printf '#!/bin/sh\necho "axis 1.2.3"\n' > "$D/axis"; chmod +x "$D/axis"
printf 'corrupted' >> "$WORK/releases/v9.9.9"/axis_9.9.9_*.tar.gz
out=$(run_install HOME="$H" AXIS_INSTALL_DIR="$D" AXIS_VERSION=v9.9.9); rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -qi 'checksum'; then
    ok "corrupted archive fails checksum verification"
else
    bad "corrupted archive fails checksum verification" "rc=$rc"
fi
if "$D/axis" version 2>/dev/null | grep -q '1.2.3'; then
    ok "canonical binary preserved on checksum failure"
else
    bad "canonical binary preserved on checksum failure"
fi
if ls "$D"/.axis-install-* >/dev/null 2>&1; then
    bad "no staged file left after checksum failure"
else
    ok "no staged file left after checksum failure"
fi

echo
printf 'install.sh: %d assertions passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
