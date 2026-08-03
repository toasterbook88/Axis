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
AXIS_INSTALL_DIR="${AXIS_INSTALL_DIR:-/usr/local/bin}"
# Set AXIS_KEEP_LEGACY=1 to leave pre-existing user-local installs in place.
AXIS_KEEP_LEGACY="${AXIS_KEEP_LEGACY:-0}"
REPO="toasterbook88/axis"
CURL_ARGS=(-fsSL)

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

echo ""
echo "=================================================="
echo " AXIS installed successfully at:"
echo " $CANONICAL"
echo "=================================================="
echo ""

# Converge on a single install: a second axis binary earlier in PATH silently
# shadows the one we just wrote, which is how nodes drift between releases.
# Package-manager-owned paths are never touched.
LEGACY_PATHS=()
for cand in "$HOME/.local/bin/axis" "$HOME/go/bin/axis" "/usr/local/bin/axis" "/opt/homebrew/bin/axis"; do
    [ -f "$cand" ] || continue
    [ "$cand" -ef "$CANONICAL" ] && continue
    case "$cand" in
        /opt/homebrew/*|*/Cellar/*|/nix/store/*|/run/current-system/*) continue ;;
    esac
    # Only ever remove something that identifies itself as AXIS.
    if "$cand" version 2>/dev/null | grep -qi '^axis '; then
        LEGACY_PATHS+=("$cand")
    else
        echo "NOTE: $cand exists but does not identify as AXIS; leaving it alone."
    fi
done

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
