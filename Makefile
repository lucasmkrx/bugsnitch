.PHONY: dev build test runner macos
dev:
	./scripts/dev
build: runner
	npm --prefix web ci
	npm --prefix web run build
	go build -o bin/bugsnitch-server ./cmd/bugsnitch-server
	go build -o bin/bugsnitch-mcp ./cmd/bugsnitch-mcp
runner:
	mkdir -p bin
	go build -o bin/bugsnitch ./cmd/bugsnitch
test:
	go test ./...
macos:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o bin/bugsnitch-darwin-arm64 ./cmd/bugsnitch
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o bin/bugsnitch-darwin-amd64 ./cmd/bugsnitch

.PHONY: plugin
plugin:
	python3 scripts/package-plugin.py
