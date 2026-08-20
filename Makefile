.PHONY: build claude-hook codex-hook test installer-smoke app nest-tray-darwin install uninstall package-macos package-macos-verify package-windows package-ubuntu checksums

build: claude-hook codex-hook

claude-hook:
	go build -o bin/claude-hook ./cmd/claude-hook

codex-hook:
	go build -o bin/codex-hook ./cmd/codex-hook

test:
	go test ./...

installer-smoke:
	./scripts/smoke-install-hooks.sh

install:
	./install.sh

uninstall:
	./uninstall.sh

# Builds the desktop dashboard app (Wails). On macOS, CGO_LDFLAGS must link
# UniformTypeIdentifiers explicitly — Wails' darwin frontend code references
# it, but recent Xcode SDKs don't pull it in as a transitive link automatically
# the way they used to, so a plain `wails build`/`go build -tags desktop,production`
# fails at link time with "_OBJC_CLASS_$_UTType ... symbol(s) not found" without it.
# This flag is macOS-only (-framework isn't a linker flag on Windows/Linux).
#
# On Linux, Ubuntu 24.04+ dropped webkit2gtk-4.0 in favor of 4.1's API —
# same distinction .github/workflows/app-build.yml's CI matrix handles —
# so this probes for whichever is actually installed via pkg-config and
# only adds the -tags webkit2_41 build tag when needed.
app:
ifeq ($(shell uname -s),Darwin)
	cd cmd/agent-winglet-app && CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build
	$(MAKE) nest-tray-darwin
else ifeq ($(shell uname -s),Linux)
	cd cmd/agent-winglet-app && wails build $$(pkg-config --exists webkit2gtk-4.1 2>/dev/null && echo -tags webkit2_41)
else
	cd cmd/agent-winglet-app && wails build
endif

# macOS gets the tray helper nested *inside* the dashboard's own bundle
# instead of standing alone. Apple's SMAppService login-item API (see
# cmd/agent-winglet-app/loginitem_darwin.go) requires the helper to live at
# Contents/Library/LoginItems/ inside the app that registers it, and only
# that owning app's own running process can call the registration API — a
# standalone tray.app can't participate in that API at all, which is
# exactly why it only ever showed up in System Settings' generic "App
# Background Activity" list instead of "Open at Login" with a real name and
# icon.
#
# The icon (build/darwin/appicon.icns) and Info.plist are pre-built, static
# assets — not regenerated here, same as the app's own build/windows/icon.ico.
#
# Adding files after `wails build` already signed the outer bundle
# invalidates that signature, so this ends with an ad-hoc re-sign of the
# whole thing — order matters: nest first, sign last.
TRAY_HELPER_BUNDLE := cmd/agent-winglet-app/build/bin/Winglet.app/Contents/Library/LoginItems/Tray.app
nest-tray-darwin:
	rm -rf "$(TRAY_HELPER_BUNDLE)"
	mkdir -p "$(TRAY_HELPER_BUNDLE)/Contents/MacOS" "$(TRAY_HELPER_BUNDLE)/Contents/Resources"
	go build -o "$(TRAY_HELPER_BUNDLE)/Contents/MacOS/agent-winglet-tray" ./cmd/agent-winglet-tray
	cp cmd/agent-winglet-tray/build/darwin/Info.plist "$(TRAY_HELPER_BUNDLE)/Contents/Info.plist"
	cp cmd/agent-winglet-tray/build/darwin/appicon.icns "$(TRAY_HELPER_BUNDLE)/Contents/Resources/appicon.icns"
	codesign --sign - --force --deep cmd/agent-winglet-app/build/bin/Winglet.app

# Public packaging targets for versioned distributable artifacts. Distinct
# from `app`/`tray` above (which
# build host-arch-only, for local dev/install.sh): these build the
# multi-arch, versioned, distributable artifacts for agentwinglet.com.
#
# VERSION defaults to the current commit's exact release tag (see
# scripts/package-lib.sh's resolve_version); override with
# `make package-macos VERSION=0.1.0` for a specific version.
package-macos:
	./scripts/package/macos.sh "$(VERSION)"

package-macos-verify:
	./scripts/package/macos-verify.sh

package-windows:
	./scripts/package/windows.sh "$(VERSION)"

package-ubuntu:
	./scripts/package/ubuntu.sh "$(VERSION)"

checksums:
	./scripts/package/checksums-and-manifest.sh "$(VERSION)"
