#!/usr/bin/env bash
# Runs mimir-server for development: the Go server rebuilds and restarts
# when Go or SQL files change, and the web app runs on Vite with hot
# reload, sending API calls to the Go server.
#
#   ./scripts/dev.sh      or   make dev
#
# Open http://localhost:5173. Settings, all optional:
#   MIMIR_DEV_DATA   data folder        (default ./data/dev)
#   MIMIR_DEV_ADDR   Go server address  (default localhost:8080)
#   MIMIR_DEV_ARGS   extra server flags, such as "-role server"
set -euo pipefail

cd "$(dirname "$0")/.."
export GOTOOLCHAIN=auto

data=${MIMIR_DEV_DATA:-./data/dev}
addr=${MIMIR_DEV_ADDR:-localhost:8080}
extra=${MIMIR_DEV_ARGS:-}
bin=bin/mimir-server-dev

for tool in go bun; do
	if ! command -v "$tool" >/dev/null; then
		echo "dev: $tool isn't installed; see README.md, Develop." >&2
		exit 1
	fi
done

mkdir -p "$data" bin
if [ ! -d web/node_modules ]; then
	echo "dev: installing web packages"
	(cd web && bun install --frozen-lockfile)
fi

pids=()
server=""
stamp=$(mktemp)
stop_server() {
	if [ -n "$server" ] && kill -0 "$server" 2>/dev/null; then
		kill "$server" 2>/dev/null || true
		wait "$server" 2>/dev/null || true
	fi
	server=""
}
cleanup() {
	trap - EXIT INT TERM
	stop_server
	for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
	wait 2>/dev/null || true
	rm -f "$stamp"
	exit 0
}
trap cleanup EXIT INT TERM

# The web app, with API calls proxied to the Go server.
(cd web && MIMIR_API="http://$addr" exec ./node_modules/.bin/vite) &
pids+=($!)

# Files that need a rebuild of the server.
sources() {
	find cmd internal worker gen go.mod go.sum -type f \( -name '*.go' -o -name '*.sql' -o -name 'go.mod' -o -name 'go.sum' \) "$@" 2>/dev/null
}

start_server() {
	touch "$stamp"
	echo "dev: building the server"
	if ! go build -o "$bin" ./cmd/mimir-server; then
		echo "dev: build failed; fix it and save, and it rebuilds" >&2
		return
	fi
	stop_server
	# shellcheck disable=SC2086 # extra flags are meant to split
	"$bin" -addr "$addr" -data "$data" -allow-private-webhooks $extra &
	server=$!
	echo "dev: server on http://$addr, web app on http://localhost:5173"
}

start_server
while true; do
	sleep 1
	if [ -n "$(sources -newer "$stamp" -print -quit)" ]; then
		sleep 0.3 # let an editor finish saving several files
		start_server
	fi
done
