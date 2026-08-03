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
REPO="toasterbook88/axis"
CURL_ARGS=(-fsSL)

if [ "$AXIS_REQUIRE_PINNED" = "1" ] && [ "$AXIS_VERSION" = "latest" ]; then
    echo "Error: AXIS_REQUIRE_PINNED=1 but AXIS_VERSION is 'latest'."
    echo "Pin an explicit release so every node installs the same artifact:"
    echo ""
    echo "    AXIS_VERSION=v0.14.8 AXIS_REQUIRE_PINNED=1 $0"
    exit 1
fi

# Refuse to install into a directory owned by a package manager: the next
# upgrade of that manager would overwrite or orphan the binary.
case "$AXIS_INSTALL_DIR/" in
    /nix/store/*|/run/current-system/*|/opt/homebrew/*|*/Cellar/*|*/linuxbrew/*|/var/lib/flatpak/*|/snap/*)
        echo "Error: refusing to install into $AXIS_INSTALL_DIR — that path is package-manager owned."
        echo "Install it through that package manager, or choose /usr/local/bin."
        exit 1
        ;;
esac

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
DOWNLOAD_URL="https://github.com/$REPO/releases/download/$TAG/$ARCHIVE_NAME"
CHECKSUM_URL="https://github.com/$REPO/releases/download/$TAG/checksums.txt"

if [ "$AXIS_VERSION" = "latest" ]; then
    echo "Resolved latest -> $TAG"
    echo "NOTE: for a fleet rollout, pin this explicitly so every node gets the same"
    echo "      artifact even if a release lands mid-rollout:"
    echo "          AXIS_VERSION=$TAG AXIS_REQUIRE_PINNED=1 $0"
fi

# Scope decides cleanup direction; resolved here so --dry-run can report it.
case "$AXIS_INSTALL_DIR/" in
    "$HOME"/*) INSTALL_SCOPE="user" ;;
    *)         INSTALL_SCOPE="system" ;;
esac

if [ "$AXIS_DRY_RUN" = "1" ]; then
    echo ""
    echo "===================== DRY RUN — nothing written ====================="
    echo "  platform      : $OS-$ARCH"
    echo "  release       : $TAG"
    echo "  archive       : $ARCHIVE_NAME"
    echo "  install dir   : $AXIS_INSTALL_DIR   (source: ${AXIS_INSTALL_DIR_SOURCE:-default})"
    echo "  target        : $AXIS_INSTALL_DIR/axis"
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
$SUDO install -m 0755 axis "$AXIS_INSTALL_DIR/axis"

CANONICAL="$AXIS_INSTALL_DIR/axis"

# Prove the installed artifact runs and is the version we asked for BEFORE any
# other copy is removed. The checksum validates the archive, not that the
# extracted binary executes on this host (wrong architecture, truncated write,
# or an incompatible libc all pass a checksum). Without this gate a valid-but-
# unusable download would replace a working installation.
# `|| true` is required: under `set -euo pipefail` a non-zero exit inside the
# command substitution would abort the script here, before the checks below can
# explain what went wrong.
INSTALLED_VERSION=$("$CANONICAL" version 2>/dev/null | grep -oiE '^axis [0-9]+\.[0-9]+\.[0-9]+' | awk '{print $2}' | head -1) || true
if [ -z "${INSTALLED_VERSION:-}" ]; then
    echo "Error: $CANONICAL did not run or did not report a version."
    echo "The previous installation was left untouched. Investigate before retrying."
    exit 1
fi
if [ "$INSTALLED_VERSION" != "$VERSION_NUM" ]; then
    echo "Error: expected v$VERSION_NUM at $CANONICAL but it reports v$INSTALLED_VERSION."
    echo "The previous installation was left untouched."
    exit 1
fi

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
    resolved="$cand"
    if command -v readlink >/dev/null 2>&1; then
        resolved=$(readlink -f "$cand" 2>/dev/null || echo "$cand")
    fi
    case "$cand:$resolved" in
        */opt/homebrew/*|*/Cellar/*|*/linuxbrew/*|*/nix/store/*|*/run/current-system/*)
            echo "NOTE: $cand is package-manager owned; leaving it alone."
            continue
            ;;
    esac

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
    RUNNING_AXIS_UNITS=$(systemctl list-units --type=service --state=running --no-legend --no-pager 2>/dev/null \
        | awk '{print $1}' | grep -iE '^axis' || true)
fi
if [ -n "$RUNNING_AXIS_UNITS" ]; then
    echo " Running axis-related services on this host:"
    echo "$RUNNING_AXIS_UNITS" | sed 's/^/     /'
    echo " Restart whichever of these execute the axis binary, then re-check the"
    echo " running version. Units with an absolute ExecStart may point at a"
    echo " different path than the one just installed."
else
    echo " No running axis-* systemd services detected on this host."
fi
echo " Duplicate-install check:  axis doctor"
echo "-------------------------------------------------------------------"
