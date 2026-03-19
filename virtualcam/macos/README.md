# macOS Virtual Camera

This directory contains a standalone scaffold for a macOS CoreMediaIO camera system extension.

What is here:

- a `CMIOExtension`-based virtual camera source
- a minimal host app that submits activate/deactivate/properties requests through `OSSystemExtensionRequest`
- a bundle assembly script that compiles the Swift sources and builds an `.app` containing the `.systemextension`

Current behavior:

- the camera extension publishes a generated test pattern
- it does not yet consume frames from the Go streaming pipeline

Why it stops there for now:

- real camera activation requires code signing identities and provisioning
- this workspace currently has no valid signing identities available
- without signing, the scaffold can be compiled and packaged, but macOS will not activate the extension as a usable camera device

## Layout

- `host/`: installer app source
- `extension/`: CoreMediaIO camera extension source
- `config/`: plist and entitlements templates
- `build/build_virtualcam.sh`: compile and assemble bundles

## Build

Unsigned local build:

```bash
./virtualcam/macos/build/build_virtualcam.sh
```

Signed build inputs:

```bash
TEAM_ID=ABCDE12345 \
SIGNING_IDENTITY="Apple Development: Your Name (TEAMID)" \
APP_GROUP_ID="group.com.example.go-webrtp.virtualcam" \
./virtualcam/macos/build/build_virtualcam.sh
```

Useful environment overrides:

- `APP_NAME`
- `HOST_BUNDLE_ID`
- `EXTENSION_BUNDLE_ID`
- `APP_GROUP_ID`
- `TEAM_ID`
- `SIGNING_IDENTITY`
- `SYSTEM_EXTENSION_USAGE_DESCRIPTION`

## Activation

After a signed build, run the installer app executable:

```bash
./build/virtualcam/GoWebRTP\ Virtual\ Camera.app/Contents/MacOS/GoWebRTP\ Virtual\ Camera activate
```

Other commands:

- `deactivate`
- `properties`

## Next Integration Step

The extension currently generates frames internally. To feed actual `go-webrtp` output into the camera, replace the frame generation path in [GoWebRTPCameraProvider.swift](/Users/bronystylecrazy/go-webrtp/virtualcam/macos/extension/GoWebRTPCameraProvider.swift) with one of:

- an app-group shared memory ring buffer
- a local socket bridge
- a shared container file sequence

The current scaffold keeps that boundary isolated in the device stream code so the transport can be swapped later without changing the installer app.
