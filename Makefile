.PHONY: build test app tray install uninstall

build:
	go build -o bin/ledger-hook ./cmd/ledger-hook

test:
	go test ./...

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
else ifeq ($(shell uname -s),Linux)
	cd cmd/agent-winglet-app && wails build $$(pkg-config --exists webkit2gtk-4.1 2>/dev/null && echo -tags webkit2_41)
else
	cd cmd/agent-winglet-app && wails build
endif

# Builds the login-item tray helper (cmd/agent-winglet-tray) — a plain Go
# binary, not a `wails build`, so it needs its own target: getlantern/systray
# uses cgo directly (Cocoa on macOS, GTK + an AppIndicator binding on Linux —
# see .github/workflows/app-build.yml for the Linux dev packages that needs),
# with no Wails-specific link flags or build tags involved. Output name gets
# .exe under Git Bash/MSYS2/Cygwin (uname reports MINGW*/MSYS*/CYGWIN* there,
# same detection scripts/lib.sh's detect_os uses) so install.sh finds a
# directly-executable file on Windows the same way it does for the app.
tray:
	@case "$$(uname -s)" in \
		MINGW*|MSYS*|CYGWIN*) go build -o cmd/agent-winglet-tray/build/bin/agent-winglet-tray.exe ./cmd/agent-winglet-tray ;; \
		*) go build -o cmd/agent-winglet-tray/build/bin/agent-winglet-tray ./cmd/agent-winglet-tray ;; \
	esac
