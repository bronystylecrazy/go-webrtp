#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$(dirname "$SCRIPT_DIR")/.local/release"

mkdir -p "$RELEASE_DIR"

echo "Building release binaries to $RELEASE_DIR..."

# macOS Apple Silicon
echo "Building darwin-arm64..."
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o "$RELEASE_DIR/webrtp-darwin-arm64" ./command/webrtp

# macOS Intel
echo "Building darwin-amd64..."
GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o "$RELEASE_DIR/webrtp-darwin-amd64" ./command/webrtp

# Linux amd64
echo "Building linux-amd64..."
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$RELEASE_DIR/webrtp-linux-amd64" ./command/webrtp

# Linux arm64
echo "Building linux-arm64..."
GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o "$RELEASE_DIR/webrtp-linux-arm64" ./command/webrtp

# Windows amd64
echo "Building windows-amd64.exe..."
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o "$RELEASE_DIR/webrtp-windows-amd64.exe" ./command/webrtp

# Windows arm64
echo "Building windows-arm64.exe..."
GOOS=windows GOARCH=arm64 go build -ldflags="-s -w" -o "$RELEASE_DIR/webrtp-windows-arm64.exe" ./command/webrtp

echo "Done! Binaries created:"
ls -la "$RELEASE_DIR"/
