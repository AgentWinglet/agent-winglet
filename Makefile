.PHONY: build test

build:
	go build -o bin/ledger-hook ./cmd/ledger-hook

test:
	go test ./...
