.PHONY: build test app

build:
	go build -o bin/ledger-hook ./cmd/ledger-hook

test:
	go test ./...

# Builds the desktop dashboard app (Wails). On macOS, CGO_LDFLAGS must link
# UniformTypeIdentifiers explicitly — Wails' darwin frontend code references
# it, but recent Xcode SDKs don't pull it in as a transitive link automatically
# the way they used to, so a plain `wails build`/`go build -tags desktop,production`
# fails at link time with "_OBJC_CLASS_$_UTType ... symbol(s) not found" without it.
# This flag is macOS-only (-framework isn't a linker flag on Windows/Linux).
app:
ifeq ($(shell uname -s),Darwin)
	cd cmd/agent-winglet-app && CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build
else
	cd cmd/agent-winglet-app && wails build
endif
