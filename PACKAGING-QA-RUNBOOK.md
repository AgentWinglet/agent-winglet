# Packaging QA Runbook

Manual verification for a tagged release, on real hardware/VMs — the release workflow (`.github/workflows/release.yml`) builds, signs, and uploads the artifacts, but nothing in CI can confirm a fresh install actually launches, shows the right Gatekeeper/SmartScreen/apt behavior, or survives an upgrade. Run this against the actual `dist/manifest.json` artifacts for the version being released, downloaded the same way a real user would (browser download, not `scp`'d from a build machine) so the OS's own quarantine/provenance checks are exercised too.

Machines needed: a clean (or snapshot-restorable) macOS Intel Mac, a macOS Apple Silicon Mac, a Windows 10 x64 VM, a Windows 11 x64 VM, and an Ubuntu 24.04 LTS (or newer) VM. "Clean" means Winglet has never been installed there before, or the previous install was fully removed including `~/.agent-winglet`.

## macOS (Intel)

- [ ] Download the `.dmg` via a browser (so it's quarantined, matching a real download).
- [ ] Open the `.dmg` — no Gatekeeper "unidentified developer" warning (this build is signed and notarized; that warning would mean signing/notarization silently regressed).
- [ ] Drag `Winglet.app` to `/Applications`, launch it. It opens without a second Gatekeeper prompt.
- [ ] `file /Applications/Winglet.app/Contents/MacOS/Winglet` reports `x86_64` (this Mac's native slice of the universal binary).
- [ ] Dashboard window shows the Overview screen. No console errors need checking here — just that it renders.
- [ ] Menu bar tray icon appears. Its tooltip shows a version string matching the release (not "dev").
- [ ] Quit the dashboard (Cmd+Q) — tray icon stays.
- [ ] Tray "Open Winglet" relaunches the dashboard.
- [ ] Reboot. Tray icon and dashboard's login-item registration both survive (System Settings > General > Login Items shows Winglet).
- [ ] In-app Settings > Uninstall Winglet removes the app and unregisters the login item; confirm the Login Items entry is gone after.

## macOS (Apple Silicon)

Same checklist as Intel, with:
- [ ] `file /Applications/Winglet.app/Contents/MacOS/Winglet` reports `arm64` instead.
- [ ] No Rosetta prompt at any point (confirms this Mac ran its native slice, not translated Intel code).

## Windows 10 x64

- [ ] Download the `-setup.exe` via a browser.
- [ ] Run it. `release.yml`'s `windows-package` job signs automatically once `WINDOWS_CERTIFICATE_BASE64` is set, and builds unsigned otherwise (current default — see `scripts/package/windows.sh`'s header comment). If this release is signed, no SmartScreen "Windows protected your PC" block should appear; that warning would mean signing regressed. If unsigned, the warning is expected — click More info → Run anyway.
- [ ] Installer completes without requiring elevation (per-user install scope — see `project.nsi`'s `WAILS_INSTALL_SCOPE`/`REQUEST_EXECUTION_LEVEL` overrides).
- [ ] Winglet appears in the Start Menu.
- [ ] Dashboard launches from the Start Menu shortcut.
- [ ] Tray icon appears within a few seconds of install finishing, without a reboot.
- [ ] Reboot. Tray icon reappears at login (Startup folder entry).
- [ ] Re-run the same installer (upgrade over an existing install): previously saved usage stats/preferences are preserved, tray helper is stopped and relaunched cleanly (no duplicate tray icons, no "file in use" error).
- [ ] Uninstall via Settings > Apps: app files, Start Menu shortcut, and the tray helper's Startup shortcut are all gone. No leftover tray icon after the next login.

## Windows 11 x64

Same checklist as Windows 10.

## Ubuntu 24.04 LTS (or newer)

- [ ] Download the `.deb` via a browser.
- [ ] `sudo apt install ./winglet_<version>_amd64.deb` — dependencies (`libgtk-3-0`, `libwebkit2gtk-4.1-0`, `libayatana-appindicator3-1`, `ca-certificates`) resolve automatically, no manual `apt-get install` of anything else needed first.
- [ ] Winglet appears in the applications launcher with its icon (not a blank/default icon).
- [ ] `winglet` launches the dashboard from a terminal.
- [ ] On this first launch, a tray helper autostart entry appears at `~/.config/autostart/winglet-tray.desktop` (written by the app itself on first run — see `cmd/agent-winglet-app/autostart_linux.go`), and the tray icon appears in the system tray/top bar (desktop-environment-dependent — GNOME needs an AppIndicator extension enabled; note this if testing on stock GNOME).
- [ ] Log out and back in (or reboot): tray icon reappears without relaunching the app manually.
- [ ] `sudo apt remove winglet`: `/opt/winglet`, the `.desktop` entry, and the icon are gone; `~/.config/autostart/winglet-tray.desktop` is left in place (package removal doesn't touch user-owned files — matches `uninstall.sh`'s own default of not purging user data).

## Cross-cutting

- [ ] All three `manifest.json` SHA-256 checksums match the actual downloaded files (`shasum -a 256` / `sha256sum` against `dist/SHA256SUMS`).
- [ ] All three artifacts' embedded version (About panel on macOS, tray tooltip on all three) matches the release tag.
- [ ] None of the three installs made any network call other than the ones the app already made pre-packaging (sign-in, entitlement check) — no telemetry added by packaging itself.
