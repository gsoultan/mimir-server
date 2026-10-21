export GOTOOLCHAIN := auto

# Reported by workers and the console; tags give v1.2.3, other commits the
# nearest tag plus the commit.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all gen web wasm build test run dev clean

all: web build

# Regenerate Go and TypeScript code from proto/. Needs protoc-gen-go and
# protoc-gen-connect-go on PATH, and `bun install` in web/.
gen:
	web/node_modules/.bin/buf lint
	web/node_modules/.bin/buf generate

web:
	cd web && bun install --frozen-lockfile && bun run build
	./scripts/wasm.sh web/dist/wasm

# The browser runtime alone, into the built web UI.
wasm:
	./scripts/wasm.sh web/dist/wasm

build:
	go build -ldflags "$(LDFLAGS)" -o bin/mimir-server ./cmd/mimir-server

test:
	go vet ./...
	go test ./...
	cd web && bun run typecheck && bun run test

run: all
	./bin/mimir-server

# Server rebuilds on Go changes, web app hot-reloads; open :5173.
dev:
	./scripts/dev.sh

clean:
	rm -rf bin web/dist/assets web/dist/wasm web/dist/*.html web/dist/*.js web/dist/*.webmanifest
