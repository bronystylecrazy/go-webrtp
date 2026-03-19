#!/bin/zsh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
VIRTUALCAM_DIR="$ROOT_DIR/virtualcam/macos"
BUILD_ROOT="${BUILD_ROOT:-$ROOT_DIR/build/virtualcam}"
MODULE_CACHE_DIR="$BUILD_ROOT/module-cache"

APP_NAME="${APP_NAME:-GoWebRTP Virtual Camera}"
HOST_EXECUTABLE="${HOST_EXECUTABLE:-$APP_NAME}"
EXTENSION_NAME="${EXTENSION_NAME:-GoWebRTP Virtual Camera Extension}"
EXTENSION_EXECUTABLE="${EXTENSION_EXECUTABLE:-GoWebRTPVirtualCameraExtension}"

HOST_BUNDLE_ID="${HOST_BUNDLE_ID:-com.bronystylecrazy.go-webrtp.virtualcam}"
EXTENSION_BUNDLE_ID="${EXTENSION_BUNDLE_ID:-com.bronystylecrazy.go-webrtp.virtualcam.extension}"
APP_GROUP_ID="${APP_GROUP_ID:-group.com.bronystylecrazy.go-webrtp.virtualcam}"
SYSTEM_EXTENSION_USAGE_DESCRIPTION="${SYSTEM_EXTENSION_USAGE_DESCRIPTION:-Install the GoWebRTP virtual camera extension.}"

TEAM_ID="${TEAM_ID:-}"
SIGNING_IDENTITY="${SIGNING_IDENTITY:-}"
CMIO_MACH_SERVICE_NAME="${CMIO_MACH_SERVICE_NAME:-${TEAM_ID:+$TEAM_ID.}$EXTENSION_BUNDLE_ID}"

APP_BUNDLE="$BUILD_ROOT/$APP_NAME.app"
APP_CONTENTS="$APP_BUNDLE/Contents"
APP_MACOS="$APP_CONTENTS/MacOS"
APP_SYSTEM_EXTENSIONS="$APP_CONTENTS/Library/SystemExtensions"
EXTENSION_BUNDLE="$APP_SYSTEM_EXTENSIONS/$EXTENSION_NAME.systemextension"
EXTENSION_CONTENTS="$EXTENSION_BUNDLE/Contents"
EXTENSION_MACOS="$EXTENSION_CONTENTS/MacOS"

HOST_BINARY="$BUILD_ROOT/$HOST_EXECUTABLE"
EXTENSION_BINARY="$BUILD_ROOT/$EXTENSION_EXECUTABLE"

render_template() {
  local template_path="$1"
  local output_path="$2"
  sed \
    -e "s|__APP_NAME__|$APP_NAME|g" \
    -e "s|__HOST_EXECUTABLE__|$HOST_EXECUTABLE|g" \
    -e "s|__HOST_BUNDLE_ID__|$HOST_BUNDLE_ID|g" \
    -e "s|__EXTENSION_NAME__|$EXTENSION_NAME|g" \
    -e "s|__EXTENSION_EXECUTABLE__|$EXTENSION_EXECUTABLE|g" \
    -e "s|__EXTENSION_BUNDLE_ID__|$EXTENSION_BUNDLE_ID|g" \
    -e "s|__APP_GROUP_ID__|$APP_GROUP_ID|g" \
    -e "s|__SYSTEM_EXTENSION_USAGE_DESCRIPTION__|$SYSTEM_EXTENSION_USAGE_DESCRIPTION|g" \
    -e "s|__CMIO_MACH_SERVICE_NAME__|$CMIO_MACH_SERVICE_NAME|g" \
    "$template_path" > "$output_path"
}

rm -rf "$BUILD_ROOT"
mkdir -p "$APP_MACOS" "$APP_SYSTEM_EXTENSIONS" "$EXTENSION_MACOS" "$MODULE_CACHE_DIR"

swiftc \
  -module-cache-path "$MODULE_CACHE_DIR" \
  -framework Foundation \
  -framework SystemExtensions \
  "$VIRTUALCAM_DIR/host/main.swift" \
  -o "$HOST_BINARY"

swiftc \
  -module-cache-path "$MODULE_CACHE_DIR" \
  -framework Foundation \
  -framework CoreMediaIO \
  -framework CoreMedia \
  -framework CoreVideo \
  -framework IOKit \
  "$VIRTUALCAM_DIR/extension/GoWebRTPCameraProvider.swift" \
  "$VIRTUALCAM_DIR/extension/main.swift" \
  -o "$EXTENSION_BINARY"

cp "$HOST_BINARY" "$APP_MACOS/$HOST_EXECUTABLE"
cp "$EXTENSION_BINARY" "$EXTENSION_MACOS/$EXTENSION_EXECUTABLE"

render_template "$VIRTUALCAM_DIR/config/HostInfo.plist.template" "$APP_CONTENTS/Info.plist"
render_template "$VIRTUALCAM_DIR/config/ExtensionInfo.plist.template" "$EXTENSION_CONTENTS/Info.plist"
render_template "$VIRTUALCAM_DIR/config/Host.entitlements.template" "$BUILD_ROOT/Host.entitlements"
render_template "$VIRTUALCAM_DIR/config/Extension.entitlements.template" "$BUILD_ROOT/Extension.entitlements"

plutil -lint "$APP_CONTENTS/Info.plist" >/dev/null
plutil -lint "$EXTENSION_CONTENTS/Info.plist" >/dev/null
plutil -lint "$BUILD_ROOT/Host.entitlements" >/dev/null
plutil -lint "$BUILD_ROOT/Extension.entitlements" >/dev/null

if [[ -n "$SIGNING_IDENTITY" ]]; then
  codesign --force --sign "$SIGNING_IDENTITY" --entitlements "$BUILD_ROOT/Extension.entitlements" "$EXTENSION_BUNDLE"
  codesign --force --sign "$SIGNING_IDENTITY" --entitlements "$BUILD_ROOT/Host.entitlements" "$APP_BUNDLE"
  echo "Signed app bundle: $APP_BUNDLE"
else
  echo "Built unsigned app bundle: $APP_BUNDLE"
  echo "Unsigned bundles cannot be activated as system extensions."
fi

echo "Host app executable: $APP_MACOS/$HOST_EXECUTABLE"
echo "Extension executable: $EXTENSION_MACOS/$EXTENSION_EXECUTABLE"
