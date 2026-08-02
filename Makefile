.PHONY: build test

build:
	go build -o bin/ledger-hook ./cmd/ledger-hook
	go build -o bin/measure ./cmd/measure
	go build -o bin/usage-per-solve ./cmd/usage-per-solve

test:
	go test ./...
