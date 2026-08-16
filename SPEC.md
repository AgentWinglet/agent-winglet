# Winglet App Packaging Spec

## Goal

Ship first-class downloadable desktop installers from `agentwinglet.com` so users can install Winglet without cloning the repo, installing Go/Node/Wails, or running the source-oriented `install.sh`.

The download page should offer:

- macOS: universal `.dmg` (unsigned, not notarized for v1 — see Non-Goals)
- Windows: signed `.exe` installer
- Ubuntu: `.deb` package, with an `.AppImage` as a portable fallback only if the `.deb` path proves too brittle

## Direct Answer: Intel Macs

Yes, Intel Macs are covered, but only if the macOS release artifact is built as `darwin/universal`.

Wails v2 supports these macOS targets:

- `darwin/amd64`: Intel Macs
- `darwin/arm64`: Apple Silicon Macs
- `darwin/universal`: one app bundle containing both Intel and Apple Silicon slices

Winglet should publish only the universal macOS download. The current `make app` path builds for the host architecture, and `nest-tray-darwin` currently builds the nested tray helper for the host architecture too. For public macOS packaging, both the main `Winglet.app` executable and `Contents/Library/LoginItems/Tray.app/Contents/MacOS/agent-winglet-tray` must be universal. Otherwise an Apple Silicon-built `.app` could appear to support Intel via the main Wails target while the nested login item does not.

## Current State

Winglet already has the right app shape for packaging:

- Main dashboard: Wails v2 app at `cmd/agent-winglet-app`
- Tray helper: native Go/systray helper at `cmd/agent-winglet-tray`
- App build target: `make app`
- Tray helper target for Windows/Linux: `make tray`
- End-to-end CI smoke path: `.github/workflows/app-build.yml`
- Current installer path: `install.sh --app-only`, which builds from source and copies artifacts into user-local locations

The source installer is useful for development, but it is not the public packaging story. Public downloads must install prebuilt artifacts.

## Artifact Matrix

| Platform | Primary Artifact | Architectures | Install Scope | Notes |
| --- | --- | --- | --- | --- |
| macOS | `Winglet-${version}-macOS-universal.dmg` | `amd64` + `arm64` | `/Applications/Winglet.app` | v1 ships unsigned and not notarized (see Non-Goals). Includes nested universal tray login item, also unsigned. |
| Windows | `Winglet-${version}-windows-x64-setup.exe` | `amd64` first | per-user | NSIS installer from Wails. Code signing required before broad distribution. |
| Ubuntu | `winglet_${version}_amd64.deb` | `amd64` first | system package | Target Ubuntu 24.04 LTS and newer first. Add `arm64` after amd64 release flow is stable. |
| Ubuntu fallback | `Winglet-${version}-x86_64.AppImage` | `amd64` first | portable | Optional. Use only if `.deb` support creates too much friction. |

Do not publish bare binaries as the main download. They are acceptable as hidden CI artifacts for debugging, not as user-facing downloads.

## Versioning

Every public artifact must be built from an immutable git tag:

- Tag format: `vMAJOR.MINOR.PATCH`, for example `v0.4.0`
- App display version: same semantic version without the `v`
- Build metadata: git commit SHA recorded in release notes and, if feasible, embedded via Go ldflags
- Artifact naming: stable and parseable, with OS and architecture in the filename

Before packaging work starts, add release metadata to `cmd/agent-winglet-app/wails.json` so Wails and platform installers have product name, company name, version, copyright, and comments.

## macOS Package

**v1 decision: no Developer ID signing or notarization.** Winglet does not have an Apple Developer ID account provisioned yet, and setting one up is not a blocker for shipping the first downloadable build. Users installing the v1 `.dmg` will see Gatekeeper's "Apple could not verify this app" warning and must right-click → Open (or clear the quarantine attribute) to launch it. The download page's install note per OS should explain this step for macOS. Signing and notarization are tracked as a fast-follow once a Developer ID is set up; the steps below are kept as reference for that later pass and marked accordingly.

### Build Output

The public macOS deliverable is:

```text
Winglet-${version}-macOS-universal.dmg
```

The DMG contains:

- `Winglet.app`
- `/Applications` symlink
- No extra installer script unless notarization or login-item registration requires it

### Build Requirements

Use a macOS runner or a local Mac builder with:

- Go version matching CI
- Node version matching CI
- Wails CLI pinned to the repo's current version
- Xcode command line tools

Not needed for v1 (post-v1, once signing/notarization is added):

- Apple Developer ID Application certificate
- App Store Connect API key or notarytool credentials

### Notarization (Post-v1)

Not part of v1 (see decision above). Kept here as reference for when a Developer ID is provisioned.

Notarization is Apple's automated security check for Mac software distributed outside the Mac App Store. It is not App Review: Apple is not manually approving Winglet's product behavior, UI, business model, or usefulness. The notary service scans the signed artifact for known malware and common code-signing problems. If it passes, Apple issues a ticket that Gatekeeper can use to trust the download.

Notarization can be rerun. The normal release loop is:

```sh
build -> sign -> package dmg -> sign dmg -> notarize -> staple -> publish
```

The ticket is tied to the exact artifact that was submitted. If the app bundle or DMG changes after notarization, even by one byte, rebuild/sign/notarize/staple again. If notarization fails, read the notary log, fix the signing or packaging issue, then submit again. If the artifact has not changed and only the local stapled ticket is missing, stapling can be rerun.

### Required Build Changes

Add a packaging target equivalent to:

```sh
cd cmd/agent-winglet-app
CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -platform darwin/universal -clean
```

Then build the tray helper as a universal binary, not host-arch only:

```sh
GOOS=darwin GOARCH=amd64 go build -o /tmp/winglet-tray-amd64 ./cmd/agent-winglet-tray
GOOS=darwin GOARCH=arm64 go build -o /tmp/winglet-tray-arm64 ./cmd/agent-winglet-tray
lipo -create -output cmd/agent-winglet-app/build/bin/Winglet.app/Contents/Library/LoginItems/Tray.app/Contents/MacOS/agent-winglet-tray /tmp/winglet-tray-amd64 /tmp/winglet-tray-arm64
```

The v1 order (no signing or notarization):

1. Build universal `Winglet.app`.
2. Create and populate `Contents/Library/LoginItems/Tray.app`.
3. Build the tray helper as universal.
4. Create the DMG.
5. Gatekeeper-test on a clean Mac to confirm the expected "unidentified developer" warning appears and the app still opens via right-click → Open.

Post-v1, once signing/notarization is added, insert these steps between building the app bundle and creating the DMG, then add DMG signing/notarization/stapling after DMG creation:

1. Build universal `Winglet.app`.
2. Create and populate `Contents/Library/LoginItems/Tray.app`.
3. Build the tray helper as universal.
4. Sign the nested tray app.
5. Sign the outer Winglet app with `--deep` only if needed, but prefer signing each nested binary explicitly.
6. Verify signatures.
7. Create the DMG.
8. Sign the DMG.
9. Notarize the DMG.
10. Staple the notarization ticket.
11. Gatekeeper-test on a clean Mac.

### Acceptance Criteria

- `file Winglet.app/Contents/MacOS/Winglet` reports both `x86_64` and `arm64`.
- `file Winglet.app/Contents/Library/LoginItems/Tray.app/Contents/MacOS/agent-winglet-tray` reports both `x86_64` and `arm64`.
- DMG opens cleanly on a fresh Apple Silicon Mac.
- DMG opens cleanly on a fresh Intel Mac.
- Gatekeeper shows the expected "unidentified developer" warning on first launch, and the app opens via right-click → Open.
- Login item registers and launches the tray helper after reboot.
- Post-v1 only: `codesign --verify --deep --strict --verbose=2 Winglet.app` and `spctl --assess --type execute --verbose Winglet.app` pass once signing/notarization is added.

## Windows Package

### Build Output

The public Windows deliverable is:

```text
Winglet-${version}-windows-x64-setup.exe
```

Use the Wails NSIS path. Wails v2 supports generating NSIS installers with:

```sh
cd cmd/agent-winglet-app
wails build -platform windows/amd64 -nsis -clean
```

### Required Build Changes

The current Wails NSIS template packages the main app. Public Windows packaging must also include the tray helper and register it for startup.

Required installer behavior:

- Install `Winglet.exe`.
- Install `agent-winglet-tray.exe` next to it.
- Create Start Menu shortcut for Winglet.
- Create Startup shortcut for the tray helper.
- Launch tray helper after install if possible.
- Stop tray helper before upgrade or uninstall.
- Remove Startup shortcut on uninstall.
- Remove Start Menu shortcut on uninstall.
- Remove installed app files on uninstall.

Reuse the behavior currently expressed in `scripts/lib.sh` and `install.sh`, but implement it in the NSIS installer instead of requiring Git Bash or PowerShell scripts from a checkout.

### Signing

Windows installer signing is required before public distribution. Unsigned installers will trigger SmartScreen warnings and undermine trust.

Use an Authenticode code-signing certificate. Sign:

- `Winglet.exe`
- `agent-winglet-tray.exe`
- final setup `.exe`

### Acceptance Criteria

- Installer runs on clean Windows 10 and Windows 11 x64 machines.
- Winglet appears in Start Menu.
- Tray helper starts at login.
- Upgrade from previous version preserves user data.
- Uninstall removes app files and startup entries.
- No Git, Go, Node, Wails, Git Bash, or MSYS2 dependency is required on the user's machine.

## Ubuntu Package

### Build Output

The primary Ubuntu deliverable is:

```text
winglet_${version}_amd64.deb
```

The package installs:

```text
/opt/winglet/Winglet
/opt/winglet/agent-winglet-tray
/usr/share/applications/winglet.desktop
/usr/share/icons/hicolor/256x256/apps/winglet.png
/usr/bin/winglet -> /opt/winglet/Winglet
```

### Runtime Dependencies

Ubuntu builds need native GTK/WebKit/AppIndicator libraries. Current CI installs:

```sh
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev libayatana-appindicator3-dev
```

The runtime `.deb` should depend on runtime packages, not dev packages. Start with:

```text
libgtk-3-0
libwebkit2gtk-4.1-0
libayatana-appindicator3-1
ca-certificates
```

Confirm exact package names on the target Ubuntu LTS release before publishing.

### Build Command

For Ubuntu 24.04+:

```sh
cd cmd/agent-winglet-app
wails build -platform linux/amd64 -tags webkit2_41 -clean
cd ../..
GOOS=linux GOARCH=amd64 go build -o cmd/agent-winglet-tray/build/bin/agent-winglet-tray ./cmd/agent-winglet-tray
```

### Package Scripts

The `.deb` should include maintainer scripts:

- `postinst`: refresh desktop database if available; optionally install per-user autostart only when running in a real desktop session
- `prerm`: stop running tray helper best-effort
- `postrm`: remove package-owned files only

System packages should not write into arbitrary users' home directories during install. Because `install.sh` currently writes per-user autostart entries, the packaged Ubuntu app should register autostart on first app launch, or provide a small user-level helper command run by the app.

### Acceptance Criteria

- Installs with `sudo apt install ./winglet_${version}_amd64.deb` on clean Ubuntu 24.04 LTS or newer.
- App appears in the launcher.
- `winglet` launches from a terminal.
- Tray helper can be registered for the logged-in user.
- Uninstall removes package-owned files.
- No Go, Node, Wails, or repo checkout is required.

## Download Page Contract

`agentwinglet.com` should expose a simple download manifest so the website can render current downloads without hardcoding filenames:

```json
{
  "version": "0.4.0",
  "released_at": "2026-08-16T00:00:00Z",
  "downloads": {
    "macos": {
      "label": "Download for macOS",
      "artifact": "Winglet-0.4.0-macOS-universal.dmg",
      "sha256": "..."
    },
    "windows": {
      "label": "Download for Windows",
      "artifact": "Winglet-0.4.0-windows-x64-setup.exe",
      "sha256": "..."
    },
    "ubuntu": {
      "label": "Download for Ubuntu",
      "artifact": "winglet_0.4.0_amd64.deb",
      "sha256": "..."
    }
  }
}
```

The page should show SHA-256 checksums and a short install note per OS. It should not expose internal CI artifact names.

## CI/CD Plan

Add a release workflow triggered by version tags:

```yaml
on:
  push:
    tags:
      - "v*.*.*"
```

Jobs:

- `test`: run existing `ci.yml` checks.
- `macos-package`: build universal app, sign, DMG, notarize, upload artifact.
- `windows-package`: build app and tray, NSIS package, sign, upload artifact.
- `ubuntu-package`: build app and tray, assemble `.deb`, upload artifact.
- `checksums`: generate SHA-256 checksums and download manifest.
- `publish`: create GitHub Release or upload artifacts to the storage backend used by `agentwinglet.com`.

Keep `.github/workflows/app-build.yml` as the PR smoke test. The release workflow is allowed to be slower and to use signing secrets.

## Security and Trust

Public downloads must satisfy:

- Signed Windows binaries and installer
- SHA-256 checksums in the download manifest
- Reproducible release inputs: tag, commit SHA, pinned Go/Node/Wails versions
- No unsigned auto-update path until a signed updater design exists

macOS app and DMG signing/notarization are deferred past v1 (see Non-Goals). The v1 macOS `.dmg` is unsigned and unnotarized; users rely on the download page's SHA-256 checksum and the documented right-click → Open step instead of Gatekeeper's automated trust check.

Do not add silent background network behavior as part of packaging. Installation should only install Winglet and its tray helper.

## Non-Goals For The First Packaging Pass

- macOS Developer ID signing and notarization (v1 ships unsigned and unnotarized; Gatekeeper's "unidentified developer" warning is expected and documented for users)
- Microsoft Store package
- Mac App Store package
- Homebrew cask
- Winget package
- Snap package
- Flatpak package
- Auto-updater
- Windows ARM64
- Ubuntu ARM64

These can come later after the direct downloads are stable.

## Implementation Checklist

- Add release metadata to `wails.json`.
- Add `make package-macos` that builds universal main app and universal nested tray helper.
- Add macOS DMG creation and verification scripts (unsigned, not notarized for v1).
- Add a macOS install note to the download page explaining the right-click → Open Gatekeeper workaround.
- Update Windows NSIS template to include tray helper and startup registration.
- Add `make package-windows` for signed NSIS installer output.
- Add Debian packaging files or an `nfpm` config for Ubuntu `.deb`.
- Add first-run per-user tray autostart registration for packaged Linux installs.
- Add `make package-ubuntu` for `.deb` output.
- Add release workflow for tag-triggered builds.
- Add checksum and manifest generation.
- Add manual QA runbook for clean macOS Intel, macOS Apple Silicon, Windows 10/11, and Ubuntu.

## References

- Wails v2 CLI platform targets: https://wails.io/docs/reference/cli/
- Wails v2 Windows NSIS installer: https://wails.io/docs/guides/windows-installer/
- Wails v2 Linux WebKit 4.1 build tag note: https://wails.io/docs/gettingstarted/building/
