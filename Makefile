.PHONY: dev build test server runner helpers journal migrator provider macos macos-amd64 macos-arm64 plugin

CARGO_TARGET_DIR ?= target
GO_TEST_FLAGS ?=

dev:
	./scripts/dev

build: runner server
	npm --prefix web ci
	npm --prefix web run build
	go build -o bin/bugsnitch-mcp ./cmd/bugsnitch-mcp

runner: journal
	go build -o bin/bugsnitch ./cmd/bugsnitch

server: migrator provider
	go build -o bin/bugsnitch-domain ./cmd/bugsnitch-domain
	install -m 0755 "$(CARGO_TARGET_DIR)/release/bugsnitch-server" bin/bugsnitch-server

helpers: journal migrator provider

journal:
	cargo build --locked --release --target-dir "$(CARGO_TARGET_DIR)" --manifest-path rust/runner-journal/Cargo.toml
	mkdir -p bin
	install -m 0755 "$(CARGO_TARGET_DIR)/release/bugsnitch-runner-journal" bin/bugsnitch-runner-journal

migrator:
	cargo build --locked --release --target-dir "$(CARGO_TARGET_DIR)" --manifest-path rust/server-migrate/Cargo.toml
	mkdir -p bin
	install -m 0755 "$(CARGO_TARGET_DIR)/release/bugsnitch-server-migrate" bin/bugsnitch-server-migrate

provider:
	cargo build --locked --release --target-dir "$(CARGO_TARGET_DIR)" --manifest-path rust/provider/Cargo.toml
	mkdir -p bin
	install -m 0755 "$(CARGO_TARGET_DIR)/release/bugsnitch-provider" bin/bugsnitch-provider

test: journal server
	cargo test --locked --workspace --target-dir "$(CARGO_TARGET_DIR)"
	BUGSNITCH_TEST_SERVER="$(CURDIR)/bin/bugsnitch-server" BUGSNITCH_JOURNAL_HELPER="$(CURDIR)/bin/bugsnitch-runner-journal" BUGSNITCH_MIGRATOR="$(CURDIR)/bin/bugsnitch-server-migrate" BUGSNITCH_PROVIDER="$(CURDIR)/bin/bugsnitch-provider" go test $(GO_TEST_FLAGS) ./...

macos: macos-arm64 macos-amd64

macos-arm64 macos-amd64:
	CARGO_TARGET_DIR="$(CARGO_TARGET_DIR)" python3 scripts/package-runner.py $(patsubst macos-%,%,$@)

plugin:
	python3 scripts/package-plugin.py
