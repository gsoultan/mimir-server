#!/usr/bin/env bash
# Builds the browser runtime, which answers with models on people's own
# devices, into a folder of the built web UI:
#
#   ./scripts/wasm.sh web/dist/wasm
#
# It writes mimir-<hash>.wasm, with a gzipped copy the server sends to
# browsers that take it, and runtime.json naming it. The name changes with
# the contents, so browsers keep it for good. The web app bundles Go's
# wasm_exec.js from web/src/workers, which must match this Go's.
set -euo pipefail

out=${1:?usage: wasm.sh <folder>}
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=auto
mkdir -p "$out"
rm -f "$out"/mimir-*.wasm "$out"/mimir-*.wasm.gz "$out"/runtime.json

if ! cmp -s "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/src/workers/wasm_exec.js; then
	echo "wasm: web/src/workers/wasm_exec.js isn't this Go's; copy it again:" >&2
	echo "      cp \"\$(go env GOROOT)/lib/wasm/wasm_exec.js\" web/src/workers/" >&2
	exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o "$tmp/mimir.wasm" ./cmd/mimir-wasm

hash() { { sha256sum "$1" 2>/dev/null || shasum -a 256 "$1"; } | cut -c1-12; }
bytes() { wc -c < "$1" | tr -d ' '; }
wasm="mimir-$(hash "$tmp/mimir.wasm").wasm"
mv "$tmp/mimir.wasm" "$out/$wasm"
gzip -9 -k "$out/$wasm"
printf '{"wasm":"%s","bytes":%d}\n' "$wasm" "$(bytes "$out/$wasm.gz")" > "$out/runtime.json"
echo "wasm: $out/$wasm ($(($(bytes "$out/$wasm.gz") / 1024)) KB gzipped)"
