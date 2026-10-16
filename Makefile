export GOTOOLCHAIN := auto

.PHONY: all gen web build test run clean

all: web build

# Regenerate Go and TypeScript code from proto/. Needs protoc-gen-go and
# protoc-gen-connect-go on PATH, and `bun install` in web/.
gen:
	web/node_modules/.bin/buf lint
	web/node_modules/.bin/buf generate

web:
	cd web && bun install --frozen-lockfile && bun run build

build:
	go build -o bin/mimir-server ./cmd/mimir-server

test:
	go vet ./...
	go test ./...
	cd web && bun run typecheck && bun run test

run: all
	./bin/mimir-server

clean:
	rm -rf bin web/dist/assets web/dist/*.html web/dist/*.js web/dist/*.webmanifest
