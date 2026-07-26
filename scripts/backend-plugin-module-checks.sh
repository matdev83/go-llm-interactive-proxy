#!/usr/bin/env bash
# Dynamic multi-module checks for connectors/* and connector-support/*.
# Avoids recursive make: this script is invoked by Make once.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export GOWORK=off

echo "== root go list/build/module graph =="
go list ./... >/dev/null
go build -o /dev/null ./cmd/lipstd
if go list -m all | grep -E 'connectors/|connector-support/' >/dev/null; then
  echo "root go list -m all must not contain connector modules" >&2
  go list -m all | grep -E 'connectors/|connector-support/' || true
  exit 1
fi

echo "== root go test ./... =="
go test ./...

echo "== discover modules =="
mapfile -t MODS < <(go run ./tools/backendplugin/discover_modules -root .)
echo "discovered: ${MODS[*]:-}"

for mod in "${MODS[@]:-}"; do
  [ -n "$mod" ] || continue
  echo "== module $mod =="
  (
    cd "$mod"
    go list ./...
    go test ./...
    if [ -d cmd ]; then
      for d in cmd/*/; do
        [ -d "$d" ] || continue
        go build -o /dev/null "./${d%/}"
      done
    fi
    if go list -f '{{.ImportPath}} {{.Imports}} {{.TestImports}} {{.XTestImports}}' ./... | grep 'go-llm-interactive-proxy/internal/' >/dev/null; then
      echo "$mod imports root internal/" >&2
      exit 1
    fi
  )
done

echo "== synthetic connector discovery =="
SYN="$ROOT/connectors/_synthetic_ci_probe"
mkdir -p "$SYN"
printf 'module github.com/matdev83/go-llm-interactive-proxy/connectors/_synthetic_ci_probe\n\ngo 1.26.5\n' >"$SYN/go.mod"
cleanup_syn() { rm -rf "$SYN"; }
trap cleanup_syn EXIT
FOUND="$(go run ./tools/backendplugin/discover_modules -root .)"
echo "$FOUND" | grep -q 'connectors/_synthetic_ci_probe'
cleanup_syn
trap - EXIT

echo "== root build with connectors/ absent (temp copy) =="
TMP="$(mktemp -d "${TMPDIR:-/tmp}/golip-root-no-connectors.XXXXXX")"
cleanup_tmp() { rm -rf "$TMP"; }
trap cleanup_tmp EXIT
if command -v rsync >/dev/null 2>&1; then
  rsync -a --exclude '.git' --exclude 'connectors' --exclude 'connector-support' \
    --exclude '.golip-package-staging' --exclude '.golip-plugins' \
    "$ROOT/" "$TMP/"
else
  tar -C "$ROOT" --exclude='.git' --exclude='connectors' --exclude='connector-support' \
    --exclude='.golip-package-staging' --exclude='.golip-plugins' -cf - . | tar -C "$TMP" -xf -
fi
(
  cd "$TMP"
  export GOWORK=off
  go build -o /dev/null ./cmd/lipstd
)
echo "OK backend-plugin-module-checks"
