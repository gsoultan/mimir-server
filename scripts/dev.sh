#!/usr/bin/env bash
# Runs mimir-server for development: the Go server rebuilds and restarts
# when Go or SQL files change, and the web app runs on Vite with hot
# reload, sending API calls to the Go server.
#
#   ./scripts/dev.sh      or   make dev
#
# Open http://localhost:5173. The server's database is MIMIR_DATABASE_URL,
# a PostgreSQL URL; without it, the script starts PostgreSQL 16 in a
# container (mimir-dev-postgres, its data in the volume of the same name)
# and stops it again on Ctrl+C. On macOS it uses Apple's container tool
# when its services are running (container system start), and otherwise
# Docker or Podman. Other settings, optional:
#   MIMIR_DEV_DATA     data folder        (default ./data/dev)
#   MIMIR_DEV_ADDR     Go server address  (default localhost:8080)
#   MIMIR_DEV_ARGS     extra server flags, such as "-role console"
#   MIMIR_DEV_PG_PORT  local port of the PostgreSQL container (default 55432)
#   MIMIR_DEV_ENGINE   container tool: container, docker or podman
#                      (default: the first of these that is running)
#
#   ./scripts/dev.sh reset   deletes the data folder and the container's
#                            database, to start over
set -euo pipefail

cd "$(dirname "$0")/.."
export GOTOOLCHAIN=auto

data=${MIMIR_DEV_DATA:-./data/dev}
addr=${MIMIR_DEV_ADDR:-localhost:8080}
extra=${MIMIR_DEV_ARGS:-}
bin=bin/mimir-server-dev
pg_name=mimir-dev-postgres
pg_port=${MIMIR_DEV_PG_PORT:-55432}

# The container tool. Apple's container runs each container in its own
# small VM and has its own commands for a few things, so the steps that
# differ go through the functions below.
engine_up() {
	command -v "$1" >/dev/null || return 1
	case $1 in
	container) container system status >/dev/null 2>&1 ;;
	*) "$1" info >/dev/null 2>&1 ;;
	esac
}
engine=""
if [ -n "${MIMIR_DEV_ENGINE:-}" ]; then
	engine=$MIMIR_DEV_ENGINE
	case $engine in
	container | docker | podman) ;;
	*)
		echo "dev: MIMIR_DEV_ENGINE is container, docker or podman, not $engine" >&2
		exit 2
		;;
	esac
	if ! engine_up "$engine"; then
		echo "dev: $engine isn't installed or isn't running" >&2
		exit 1
	fi
else
	candidates="docker podman"
	if [ "$(uname -s)" = Darwin ]; then
		candidates="container $candidates"
	fi
	for e in $candidates; do
		if engine_up "$e"; then
			engine=$e
			break
		fi
	done
fi

pg_running() {
	case $engine in
	container) container ls -q 2>/dev/null | grep -qx "$pg_name" ;;
	*) [ -n "$("$engine" ps -q --filter "name=^${pg_name}\$")" ] ;;
	esac
}
pg_remove() {
	case $engine in
	container) container delete --force "$pg_name" >/dev/null 2>&1 || true ;;
	*) "$engine" rm -f "$pg_name" >/dev/null 2>&1 || true ;;
	esac
}
pg_remove_volume() {
	case $engine in
	container) container volume delete "$pg_name" >/dev/null 2>&1 || true ;;
	*) "$engine" volume rm "$pg_name" >/dev/null 2>&1 || true ;;
	esac
}

if [ "${1:-}" = reset ]; then
	if [ -n "$engine" ]; then
		pg_remove
		pg_remove_volume
	fi
	rm -rf "$data"
	echo "dev: deleted $data and the $pg_name database"
	exit 0
elif [ -n "${1:-}" ]; then
	echo "usage: dev.sh [reset]" >&2
	exit 2
fi

for tool in go bun; do
	if ! command -v "$tool" >/dev/null; then
		echo "dev: $tool isn't installed; see README.md, Develop." >&2
		exit 1
	fi
done

# PostgreSQL in a container, when no database is given. The server
# creates its tables at start (storm's automigrate).
pg_started=""
if [ -z "${MIMIR_DATABASE_URL:-}" ] && [[ "$extra" != *"-database-url"* ]]; then
	if [ -z "$engine" ]; then
		echo "dev: set MIMIR_DATABASE_URL to a PostgreSQL URL, such as" >&2
		echo "     postgres://mimir:secret@localhost:5432/mimir_dev?sslmode=disable" >&2
		if [ "$(uname -s)" = Darwin ] && command -v container >/dev/null; then
			echo "     or run: container system start, and the script runs PostgreSQL itself" >&2
		else
			echo "     or start Docker or Podman, and the script runs PostgreSQL itself" >&2
		fi
		exit 1
	fi
	if ! pg_running; then
		echo "dev: starting PostgreSQL in $engine ($pg_name, port $pg_port)"
		pg_remove
		pg_env=()
		if [ "$engine" = container ]; then
			# Its volume is a new disk with lost+found at the top, which
			# initdb refuses, so the database goes in a folder inside it.
			container volume create "$pg_name" >/dev/null 2>&1 || true
			pg_env=(-e PGDATA=/var/lib/postgresql/data/pgdata)
		fi
		"$engine" run -d --name "$pg_name" \
			-e POSTGRES_USER=mimir -e POSTGRES_PASSWORD=mimir -e POSTGRES_DB=mimir_dev \
			${pg_env[@]+"${pg_env[@]}"} \
			-p "127.0.0.1:$pg_port:5432" -v "$pg_name:/var/lib/postgresql/data" \
			docker.io/library/postgres:16 >/dev/null
		pg_started=1
	fi
	for _ in $(seq 60); do
		if "$engine" exec "$pg_name" pg_isready -q -U mimir -d mimir_dev; then
			break
		fi
		sleep 0.5
	done
	if ! "$engine" exec "$pg_name" pg_isready -q -U mimir -d mimir_dev; then
		echo "dev: PostgreSQL didn't start; see: $engine logs $pg_name" >&2
		exit 1
	fi
	export MIMIR_DATABASE_URL="postgres://mimir:mimir@127.0.0.1:$pg_port/mimir_dev?sslmode=disable"
fi

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
	if [ -n "$pg_started" ]; then
		echo "dev: stopping PostgreSQL"
		"$engine" stop "$pg_name" >/dev/null 2>&1 || true
	fi
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

# The browser runtime; rebuild it with `make wasm` after changing it.
if [ ! -f web/dist/wasm/runtime.json ]; then
	echo "dev: building the browser runtime"
	./scripts/wasm.sh web/dist/wasm
fi

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
