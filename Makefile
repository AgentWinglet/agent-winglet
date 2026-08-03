.PHONY: build test app install uninstall

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
