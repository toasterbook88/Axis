#!/usr/bin/env bash

set -euo pipefail

# Default variables
AXIS_VERSION="${AXIS_VERSION:-latest}"
AXIS_INSTALL_DIR="${AXIS_INSTALL_DIR:-$HOME/.local/bin}"
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

mkdir -p "$AXIS_INSTALL_DIR"
install -m 0755 axis "$AXIS_INSTALL_DIR/axis"

echo ""
echo "=================================================="
echo " AXIS installed successfully at:"
echo " $AXIS_INSTALL_DIR/axis"
echo "=================================================="
echo ""

# Provide PATH guidance if missing
if [[ ":$PATH:" != *":$AXIS_INSTALL_DIR:"* ]]; then
    echo "WARNING: $AXIS_INSTALL_DIR is not in your current PATH."
    echo "To use 'axis', add the following to your profile (e.g. ~/.bashrc or ~/.zshrc):"
    echo ""
    echo "    export PATH=\"\$PATH:$AXIS_INSTALL_DIR\""
    echo ""
    echo "Then restart your shell or run: source ~/.zshrc (or .bashrc)"
else
    echo "You can now run 'axis version' to verify the installation."
fi
