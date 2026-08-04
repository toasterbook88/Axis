#!/usr/bin/env bash

set -euo pipefail

# Default variables
#
# AXIS installs system-wide by default so that every node in a cluster resolves
# the same absolute path, regardless of which user is logged in. Per-user
# installs produce a different path per account (/home/alice/.local/bin vs
# /Users/bob/.local/bin), which is how one host silently runs a stale binary
# while another runs the current one.
#
# Override with AXIS_INSTALL_DIR=... for a user-local install.
AXIS_VERSION="${AXIS_VERSION:-latest}"
# Record whether the target came from the environment or the built-in default,
# so the default path is observable (and testable) without being overridden.
if [ -n "${AXIS_INSTALL_DIR:-}" ]; then
    AXIS_INSTALL_DIR_SOURCE="AXIS_INSTALL_DIR override"
else
    AXIS_INSTALL_DIR_SOURCE="built-in default"
fi
AXIS_INSTALL_DIR="${AXIS_INSTALL_DIR:-/usr/local/bin}"
# Set AXIS_KEEP_LEGACY=1 to leave pre-existing user-local installs in place.
AXIS_KEEP_LEGACY="${AXIS_KEEP_LEGACY:-0}"
# Set AXIS_REQUIRE_PINNED=1 to refuse "latest". A fleet rollout that resolves
# "latest" per node can straddle a release published mid-rollout, leaving nodes
# on different versions — the exact drift this installer exists to prevent.
AXIS_REQUIRE_PINNED="${AXIS_REQUIRE_PINNED:-0}"
# Set AXIS_DRY_RUN=1 to resolve and print the full plan (version, target,
# scope, cleanup set) and exit before writing anything.
AXIS_DRY_RUN="${AXIS_DRY_RUN:-0}"
# Set AXIS_FORCE_REPLACE=1 to overwrite an existing file at the destination that
# does not identify as AXIS. Never bypasses the package-manager or non-regular
# entry refusals — those are not the operator's to override here.
AXIS_FORCE_REPLACE="${AXIS_FORCE_REPLACE:-0}"
REPO="toasterbook88/axis"
CURL_ARGS=(-fsSL)

INSTALL_URL="https://raw.githubusercontent.com/$REPO/main/install.sh"

# Emit a re-runnable pinned command for however this script was invoked.
#
# Under the documented `curl … | bash` flow, $0 is "bash" — printing
# "AXIS_VERSION=… $0" would tell the operator to launch a shell, not to
# reinstall. Detect the piped case and print the piped form instead.
pinned_command_hint() {
    local ver="${1:-vX.Y.Z}"
    local base
    base="$(basename -- "$0" 2>/dev/null || echo "$0")"
    if [ -r "$0" ] && [ "$base" != "bash" ] && [ "$base" != "sh" ] && [ "$base" != "dash" ]; then
        # Quote the path: an installer under a directory containing spaces would
        # otherwise produce a command that splits into separate words.
        printf '    AXIS_VERSION=%s AXIS_REQUIRE_PINNED=1 "%s"\n' "$ver" "$0"
    else
        printf '    curl -fsSL %s \\\n' "$INSTALL_URL"
        printf '      | AXIS_VERSION=%s AXIS_REQUIRE_PINNED=1 bash\n' "$ver"
    fi
}

if [ "$AXIS_REQUIRE_PINNED" = "1" ] && [ "$AXIS_VERSION" = "latest" ]; then
    echo "Error: AXIS_REQUIRE_PINNED=1 but AXIS_VERSION is 'latest'."
    echo "Pin an explicit release so every node installs the same artifact:"
    echo ""
    pinned_command_hint "vX.Y.Z"
    echo ""
    echo "Current releases: https://github.com/$REPO/releases"
    exit 1
fi

# Resolve a path to its physical location, following symlinks.
#
# `readlink -f` is GNU-first: it is missing or behaves differently on older
# macOS/BSD userlands, and this script is a public `curl | bash` target, so the
# portable options are tried in order.
#
# Returns 0 and prints the resolved path, or returns 1 if it could not resolve.
# Callers must treat a non-zero return as "unknown", never as "safe".
canonicalize_path() {
    local p="$1" out="" dir="" base=""
    if out=$(readlink -f "$p" 2>/dev/null) && [ -n "$out" ]; then printf '%s\n' "$out"; return 0; fi
    if out=$(realpath "$p" 2>/dev/null) && [ -n "$out" ]; then printf '%s\n' "$out"; return 0; fi
    if command -v python3 >/dev/null 2>&1; then
        if out=$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$p" 2>/dev/null) && [ -n "$out" ]; then
            printf '%s\n' "$out"; return 0
        fi
    fi
    # Dependency-free fallback: `cd -P` resolves symlinks physically using only
    # shell builtins, so it works where none of the helpers above exist.
    #
    # Walk up to the deepest ancestor we can physically enter, then re-attach the
    # remaining components. This handles a target whose parents do not exist yet
    # (mkdir -p creates them later) while still refusing a path that passes
    # through a symlink we cannot see through — checking only the leaf would
    # accept "<dangling-link>/bin", since the leaf itself is not a symlink.
    local cur="$p" rest="" parent=""
    while :; do
        if out=$( cd -P "$cur" 2>/dev/null && pwd -P ) && [ -n "$out" ]; then
            if [ -n "$rest" ]; then printf '%s/%s\n' "$out" "$rest"; else printf '%s\n' "$out"; fi
            return 0
        fi
        # Could not enter it. "Cannot enter" has several causes and only one of
        # them is safe to walk past:
        #
        #   - it exists but is unreadable (mode 000): we cannot see through it,
        #     and the install may later run under sudo, which *can* traverse it
        #   - it is a symlink we could not resolve
        #   - it is a regular file being used as an intermediate component
        #   - it genuinely does not exist yet, and mkdir -p will create it
        #
        # Only the last is safe. `-e` follows symlinks and so is false for a
        # dangling one, hence the explicit `-L` as well.
        if [ -e "$cur" ] || [ -L "$cur" ]; then
            return 1
        fi
        parent=$(dirname -- "$cur")
        [ "$parent" = "$cur" ] && return 1
        rest="$(basename -- "$cur")${rest:+/$rest}"
        cur="$parent"
    done
}

# True when a path lies inside a package-manager-owned tree.
is_package_managed() {
    case "$1/" in
        /nix/store/*|/run/current-system/*|/opt/homebrew/*|*/Cellar/*|*/linuxbrew/*|/var/lib/flatpak/*|/snap/*) return 0 ;;
    esac
    return 1
}

# Refuse to install into a directory owned by a package manager: the next
# upgrade of that manager would overwrite or orphan the binary. Both the literal
# and the resolved path are checked — a symlink such as
# /usr/local/bin -> /opt/homebrew/bin would otherwise slip past a literal match.
# Reject parent traversal outright. An unresolved ".." suffix is reattached
# literally by the builtin fallback, so "/tmp/new/../../opt/homebrew/bin" is not
# recognised as package-managed even though mkdir -p then lands there.
# Normalising ".." correctly around symlinks is subtle and an install
# destination never needs it, so refuse rather than try to interpret it.
case "/$AXIS_INSTALL_DIR/" in
    */../*)
        echo "Error: AXIS_INSTALL_DIR must not contain '..' components."
        echo "Given: $AXIS_INSTALL_DIR"
        echo "Use the direct path to the destination directory."
        exit 1
        ;;
esac

if AXIS_INSTALL_DIR_REAL=$(canonicalize_path "$AXIS_INSTALL_DIR"); then
    if is_package_managed "$AXIS_INSTALL_DIR" || is_package_managed "$AXIS_INSTALL_DIR_REAL"; then
        echo "Error: refusing to install into $AXIS_INSTALL_DIR — that path is package-manager owned."
        [ "$AXIS_INSTALL_DIR_REAL" != "$AXIS_INSTALL_DIR" ] && echo "       (resolves to $AXIS_INSTALL_DIR_REAL)"
        echo "Install it through that package manager, or choose /usr/local/bin."
        exit 1
    fi
else
    # Fail closed, without inspecting only the leaf: an unresolvable component
    # anywhere in the path could lead into a package-managed tree, and a safety
    # guard must not treat "unknown" as "safe".
    echo "Error: cannot resolve $AXIS_INSTALL_DIR to a physical path on this system."
    echo "It passes through a symlink that could not be followed, so it cannot be"
    echo "checked against package-manager-owned locations."
    echo "Install to the physical directory it points at, or choose /usr/local/bin."
    exit 1
fi

echo "Installing AXIS to $AXIS_INSTALL_DIR..."

# Platform detection
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

if [ "$OS" != "darwin" ] && [ "$OS" != "linux" ]; then
    echo "Error: Unsupported OS '$OS'."
    exit 1
fi

case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)
        echo "Error: Unsupported architecture '$ARCH'."
        exit 1
        ;;
esac

echo "Detected Platform: $OS-$ARCH"

# Dependencies check
for cmd in curl tar mktemp install; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Error: Required command '$cmd' is not installed."
        exit 1
    fi
done

# Checksum command presence
if command -v shasum >/dev/null 2>&1; then
    SHASUM_CMD="shasum -a 256"
elif command -v sha256sum >/dev/null 2>&1; then
    SHASUM_CMD="sha256sum"
else
    echo "Error: Neither 'shasum' nor 'sha256sum' found. Cannot verify binary integrity."
    exit 1
fi

# Determine download URL
if [ "$AXIS_VERSION" = "latest" ]; then
    echo "Fetching latest release tag..."
    LATEST_URL=$(curl "${CURL_ARGS[@]}" -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")
    TAG="${LATEST_URL##*/}"
    if [ -z "$TAG" ] || [[ ! "$TAG" == v* ]]; then
        echo "Error: Failed to fetch latest release tag from GitHub."
        exit 1
    fi
else
    TAG="$AXIS_VERSION"
    if [[ ! "$TAG" == v* ]]; then
        TAG="v$TAG"
    fi
fi

# Strip the leading v for the archive name matching goreleaser defaults
VERSION_NUM="${TAG#v}"
ARCHIVE_NAME="axis_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
# Release location. Overridable so hack/install-tests.sh can serve a local tree
# over file:// and exercise download, checksum, extraction, staging, validation
# and cleanup without network access or a real system path.
AXIS_RELEASE_BASE_URL="${AXIS_RELEASE_BASE_URL:-https://github.com/$REPO/releases/download}"
DOWNLOAD_URL="$AXIS_RELEASE_BASE_URL/$TAG/$ARCHIVE_NAME"
CHECKSUM_URL="$AXIS_RELEASE_BASE_URL/$TAG/checksums.txt"

if [ "$AXIS_VERSION" = "latest" ]; then
    echo "Resolved latest -> $TAG"
    echo "NOTE: for a fleet rollout, pin this explicitly so every node gets the same"
    echo "      artifact even if a release lands mid-rollout:"
    pinned_command_hint "$TAG"
fi

# Every privileged operation from here on uses the resolved physical path.
#
# Validating AXIS_INSTALL_DIR_REAL and then writing through AXIS_INSTALL_DIR
# leaves a window in which a symlink component can be repointed between the
# check and `sudo mkdir` / `mktemp` / the promotion rename. Switching now means
# the package-manager decision and the install destination refer to the same
# object. The original value is kept for display only.
AXIS_INSTALL_DIR_DISPLAY="$AXIS_INSTALL_DIR"
AXIS_INSTALL_DIR="$AXIS_INSTALL_DIR_REAL"
if [ "$AXIS_INSTALL_DIR_DISPLAY" != "$AXIS_INSTALL_DIR" ]; then
    echo "Resolved install directory: $AXIS_INSTALL_DIR_DISPLAY -> $AXIS_INSTALL_DIR"
fi

CANONICAL="$AXIS_INSTALL_DIR/axis"

# Classify the entry we would replace, before anything is downloaded or staged.
#
# The directory guard says nothing about the file inside it. /usr/local/bin is a
# legitimate shared directory on Intel macOS while /usr/local/bin/axis may be a
# Homebrew symlink into Cellar — the promotion would silently replace a
# package-managed entry. The legacy-cleanup loop already refuses to touch
# package-managed paths and non-AXIS files, but it runs *after* promotion, by
# which point the original entry is gone. Apply the same ownership rules here.
CANONICAL_STATE=""
classify_canonical() {
    if [ ! -e "$CANONICAL" ] && [ ! -L "$CANONICAL" ]; then
        CANONICAL_STATE="absent"
        return 0
    fi
    if [ -L "$CANONICAL" ]; then
        local tgt=""
        if tgt=$(canonicalize_path "$CANONICAL"); then
            if is_package_managed "$CANONICAL" || is_package_managed "$tgt"; then
                CANONICAL_STATE="package-manager-owned symlink -> $tgt"
                return 1
            fi
        else
            CANONICAL_STATE="symlink that cannot be resolved"
            return 1
        fi
    fi
    if [ -d "$CANONICAL" ]; then
        CANONICAL_STATE="a directory"
        return 1
    fi
    if [ ! -f "$CANONICAL" ]; then
        CANONICAL_STATE="not a regular file"
        return 1
    fi
    if is_package_managed "$CANONICAL"; then
        CANONICAL_STATE="package-manager-owned file"
        return 1
    fi
    if "$CANONICAL" version 2>/dev/null | grep -qi '^axis '; then
        CANONICAL_STATE="existing AXIS install (upgrade)"
        return 0
    fi
    CANONICAL_STATE="an existing file that does not identify as AXIS"
    if [ "$AXIS_FORCE_REPLACE" = "1" ]; then
        CANONICAL_STATE="$CANONICAL_STATE (replacing: AXIS_FORCE_REPLACE=1)"
        return 0
    fi
    return 1
}

if ! classify_canonical; then
    echo "Error: refusing to replace $CANONICAL"
    echo "       it is $CANONICAL_STATE."
    case "$CANONICAL_STATE" in
        *"does not identify as AXIS"*)
            echo "Set AXIS_FORCE_REPLACE=1 to overwrite it deliberately."
            ;;
        *package-manager*)
            echo "Upgrade it through that package manager, or choose another AXIS_INSTALL_DIR."
            ;;
        *)
            echo "Remove or relocate it, or choose another AXIS_INSTALL_DIR."
            ;;
    esac
    exit 1
fi

# Scope decides cleanup direction. Compare physical against physical: a symlinked
# home would otherwise misclassify a user-local install as system-wide.
HOME_REAL=$(canonicalize_path "$HOME" 2>/dev/null) || HOME_REAL="$HOME"
case "$AXIS_INSTALL_DIR/" in
    "$HOME_REAL"/*|"$HOME"/*) INSTALL_SCOPE="user" ;;
    *)                        INSTALL_SCOPE="system" ;;
esac

if [ "$AXIS_DRY_RUN" = "1" ]; then
    echo ""
    echo "============ DRY RUN — path resolution only, nothing written ========"
    echo "  Checks target/scope/cleanup direction. Does NOT exercise download,"
    echo "  checksum, extraction, permissions, staging, binary validation, or"
    echo "  which specific copies cleanup would remove."
    echo "  platform      : $OS-$ARCH"
    echo "  release       : $TAG"
    echo "  archive       : $ARCHIVE_NAME"
    echo "  requested     : $AXIS_INSTALL_DIR_DISPLAY   (source: ${AXIS_INSTALL_DIR_SOURCE:-default})"
    echo "  install dir   : $AXIS_INSTALL_DIR   (physical)"
    echo "  target        : $CANONICAL"
    echo "  existing entry: $CANONICAL_STATE"
    echo "  scope         : $INSTALL_SCOPE"
    if [ "$INSTALL_SCOPE" = "system" ]; then
        echo "  cleanup       : may remove verified user-local AXIS copies"
    else
        echo "  cleanup       : user-local install — system copies reported, never removed"
    fi
    echo "  keep legacy   : $AXIS_KEEP_LEGACY"
    echo "====================================================================="
    exit 0
fi

WORKDIR=$(mktemp -d "/tmp/axis-install-XXXXXX")
trap 'rm -rf "$WORKDIR"' EXIT
cd "$WORKDIR"

echo "Downloading $ARCHIVE_NAME (version $TAG)..."
if ! curl "${CURL_ARGS[@]}" "$DOWNLOAD_URL" -o "$ARCHIVE_NAME"; then
    echo "Error: Failed to download $DOWNLOAD_URL"
    exit 1
fi

echo "Downloading checksums.txt..."
if ! curl "${CURL_ARGS[@]}" "$CHECKSUM_URL" -o "checksums.txt"; then
    echo "Error: Failed to download checksums.txt"
    exit 1
fi

echo "Verifying checksum..."
EXPECTED_SHA=$(awk -v name="$ARCHIVE_NAME" '$2 == name { print $1; exit }' checksums.txt)
if [ -z "${EXPECTED_SHA:-}" ]; then
    echo "Error: Checksum not found for $ARCHIVE_NAME in checksums.txt"
    exit 1
fi

ACTUAL_SHA=$($SHASUM_CMD "$ARCHIVE_NAME" | awk '{print $1}')
if [ "$EXPECTED_SHA" != "$ACTUAL_SHA" ]; then
    echo "Error: Checksum verification failed!"
    echo "Expected: $EXPECTED_SHA"
    echo "Got:      $ACTUAL_SHA"
    exit 1
fi

echo "Checksum verified!"

# Extract and install
echo "Extracting binary..."
tar -xzf "$ARCHIVE_NAME" axis

# Elevate only when the destination is not writable by the current user.
dir_needs_sudo() {
    local dir="$1"
    if [ -d "$dir" ]; then
        [ -w "$dir" ] && return 1 || return 0
    fi
    local parent
    parent=$(dirname "$dir")
    while [ ! -d "$parent" ] && [ "$parent" != "/" ]; do
        parent=$(dirname "$parent")
    done
    [ -w "$parent" ] && return 1 || return 0
}

SUDO=""
if dir_needs_sudo "$AXIS_INSTALL_DIR"; then
    if [ "$(id -u)" -eq 0 ]; then
        SUDO=""
    elif command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
        echo "Note: $AXIS_INSTALL_DIR requires elevated privileges; using sudo."
    else
        echo "Error: $AXIS_INSTALL_DIR is not writable and 'sudo' is unavailable."
        echo "Re-run as root, or install to a user-local path:"
        echo ""
        echo "    AXIS_INSTALL_DIR=\"\$HOME/.local/bin\" $0"
        exit 1
    fi
fi

$SUDO mkdir -p "$AXIS_INSTALL_DIR"

# Stage into the destination directory, validate the STAGED file, then rename
# over the canonical path only after it proves good.
#
# Staging in the destination directory (not /tmp) keeps the final step a
# same-filesystem rename(2), which is atomic: readers see either the old binary
# or the new one, never a partial write. Installing directly over the canonical
# path and validating afterwards would destroy a working binary before
# discovering the replacement is unusable.
# mktemp, not $$: a PID-derived name is predictable, which matters when the
# destination is an operator-chosen directory writable by others. mktemp creates
# the file exclusively, so it cannot be pre-created or aimed at via symlink.
STAGED=$($SUDO mktemp "$AXIS_INSTALL_DIR/.axis-install-XXXXXX")
cleanup_staged() { [ -n "${STAGED:-}" ] && $SUDO rm -f "$STAGED" 2>/dev/null || true; }
trap 'rm -rf "$WORKDIR"; cleanup_staged' EXIT

$SUDO install -m 0755 axis "$STAGED"

# Prove the installed artifact runs and is the version we asked for BEFORE any
# other copy is removed. The checksum validates the archive, not that the
# extracted binary executes on this host (wrong architecture, truncated write,
# or an incompatible libc all pass a checksum). Without this gate a valid-but-
# unusable download would replace a working installation.
# `|| true` is required: under `set -euo pipefail` a non-zero exit inside the
# command substitution would abort the script here, before the checks below can
# explain what went wrong.
INSTALLED_VERSION=$("$STAGED" version 2>/dev/null | grep -oiE '^axis [0-9]+\.[0-9]+\.[0-9]+' | awk '{print $2}' | head -1) || true
if [ -z "${INSTALLED_VERSION:-}" ]; then
    echo "Error: the downloaded binary did not run or did not report a version."
    echo "Staged copy discarded; $CANONICAL is unchanged. Investigate before retrying."
    exit 1
fi
if [ "$INSTALLED_VERSION" != "$VERSION_NUM" ]; then
    echo "Error: expected v$VERSION_NUM but the downloaded binary reports v$INSTALLED_VERSION."
    echo "Staged copy discarded; $CANONICAL is unchanged."
    exit 1
fi

# Validation passed — promote atomically. Same directory, so this is rename(2).
$SUDO mv -f "$STAGED" "$CANONICAL"
STAGED=""

echo ""
echo "=================================================="
echo " AXIS installed successfully at:"
echo " $CANONICAL"
echo " Verified: v$INSTALLED_VERSION"
echo "=================================================="
echo ""

# Converge on a single install: a second axis binary earlier in PATH silently
# shadows the one we just wrote, which is how nodes drift between releases.
#
# Cleanup is directional. A system-wide install may retire user-local shadows,
# because it is the copy every account and service resolves. A user-local
# install must never delete the system copy: that would be one account removing
# a binary other users and systemd units depend on.
LEGACY_PATHS=()
FOREIGN_PATHS=()
for cand in "$HOME/.local/bin/axis" "$HOME/go/bin/axis" "/usr/local/bin/axis" "/opt/homebrew/bin/axis"; do
    [ -f "$cand" ] || continue
    [ "$cand" -ef "$CANONICAL" ] && continue

    # Resolve before testing ownership: on Intel macOS /usr/local/bin/axis may
    # be a Homebrew symlink into /usr/local/Cellar, which an unresolved match
    # would miss.
    if resolved=$(canonicalize_path "$cand"); then
        if is_package_managed "$cand" || is_package_managed "$resolved"; then
            echo "NOTE: $cand is package-manager owned; leaving it alone."
            continue
        fi
    else
        # Unresolvable — cannot rule out a package-managed target, so do not
        # delete it. Fail closed on removal too, not just on install.
        echo "NOTE: $cand could not be resolved; leaving it alone."
        continue
    fi

    # A user-local install reports other copies but does not remove them.
    case "$cand" in
        "$HOME"/*) ;;
        *)
            if [ "$INSTALL_SCOPE" = "user" ]; then
                FOREIGN_PATHS+=("$cand")
                continue
            fi
            ;;
    esac

    # Only ever remove something that identifies itself as AXIS.
    if "$cand" version 2>/dev/null | grep -qi '^axis '; then
        LEGACY_PATHS+=("$cand")
    else
        echo "NOTE: $cand exists but does not identify as AXIS; leaving it alone."
    fi
done

if [ ${#FOREIGN_PATHS[@]} -gt 0 ]; then
    echo "NOTE: this is a user-local install; the following system installs were left in place:"
    for p in "${FOREIGN_PATHS[@]}"; do echo "    $p"; done
    echo "They may take precedence for services and other accounts. To converge on one"
    echo "system-wide binary instead, re-run without AXIS_INSTALL_DIR."
    echo ""
fi

if [ ${#LEGACY_PATHS[@]} -gt 0 ]; then
    if [ "$AXIS_KEEP_LEGACY" = "1" ]; then
        echo "WARNING: other AXIS installs remain (AXIS_KEEP_LEGACY=1):"
        for p in "${LEGACY_PATHS[@]}"; do echo "    $p"; done
        echo "These may shadow $CANONICAL depending on PATH order."
    else
        echo "Removing superseded AXIS installs:"
        for p in "${LEGACY_PATHS[@]}"; do
            rm_sudo=""
            dir_needs_sudo "$(dirname "$p")" && [ "$(id -u)" -ne 0 ] && rm_sudo="sudo"
            if $rm_sudo rm -f "$p" 2>/dev/null; then
                echo "    removed $p"
            else
                echo "    WARNING: could not remove $p (permission denied)"
            fi
        done
    fi
    echo ""
fi

# Provide PATH guidance if missing
if [[ ":$PATH:" != *":$AXIS_INSTALL_DIR:"* ]]; then
    echo "WARNING: $AXIS_INSTALL_DIR is not in your current PATH."
    if [ -e /etc/NIXOS ] || [ -d /run/current-system ]; then
        echo "This is NixOS, which omits $AXIS_INSTALL_DIR from PATH by default."
        echo "Add it declaratively in your configuration.nix:"
        echo ""
        echo "    environment.sessionVariables.PATH = [ \"$AXIS_INSTALL_DIR\" ];"
        echo ""
        echo "then run: sudo nixos-rebuild switch"
        echo ""
        echo "This is set through PAM and so reaches non-interactive SSH."
        echo "It does NOT configure systemd: units carry a hermetic PATH, so any"
        echo "unit invoking axis needs an absolute ExecStart."
    else
        echo "Add the following to your profile (e.g. ~/.bashrc or ~/.zshrc):"
        echo ""
        echo "    export PATH=\"\$PATH:$AXIS_INSTALL_DIR\""
        echo ""
        echo "Then restart your shell or run: source ~/.zshrc (or .bashrc)"
    fi
else
    echo "You can now run 'axis version' to verify the installation."
fi

# Installing a file does not update a running process. A systemd unit or launchd
# job that already mapped the old executable keeps running it until restarted,
# so "the file is v$INSTALLED_VERSION" and "the service is v$INSTALLED_VERSION"
# are separate claims requiring separate verification.
#
# This script deliberately does not restart anything: service management has its
# own blast radius (in-flight work, dependency ordering, restart storms) and does
# not belong to a binary installer.
echo ""
echo "-------------------------------------------------------------------"
echo " Installed FILE is v$INSTALLED_VERSION. Running PROCESSES are not updated."
RUNNING_AXIS_UNITS=""
if command -v systemctl >/dev/null 2>&1; then
    # System scope and user scope are separate registries; a service can be
    # running in either. Checking only the system bus under-reports.
    RUNNING_AXIS_UNITS=$(
        { systemctl list-units --type=service --state=running --no-legend --no-pager 2>/dev/null | awk '{print "system: "$1}'
          systemctl --user list-units --type=service --state=running --no-legend --no-pager 2>/dev/null | awk '{print "user:   "$1}'
        } | grep -iE 'axis|cortex|supermemory|qmd' || true
    )
fi
if command -v launchctl >/dev/null 2>&1; then
    RUNNING_AXIS_UNITS="$RUNNING_AXIS_UNITS$(printf '\n'; launchctl list 2>/dev/null \
        | awk 'NR>1 && $1 != "-" {print "launchd: "$3}' | grep -iE 'axis|cortex' || true)"
fi
RUNNING_AXIS_UNITS=$(printf '%s\n' "$RUNNING_AXIS_UNITS" | sed '/^$/d')
if [ -n "$RUNNING_AXIS_UNITS" ]; then
    echo " Possibly affected services on this host:"
    echo "$RUNNING_AXIS_UNITS" | sed 's/^/     /'
    echo " Restart whichever of these execute the axis binary, then re-check the"
    echo " running version. Units with an absolute ExecStart may point at a"
    echo " different path than the one just installed."
else
    echo " No running axis-related services detected (systemd system+user, launchd)."
fi
echo " Duplicate-install check:  axis doctor"
echo "-------------------------------------------------------------------"
