#!/usr/bin/env bash
# Validate every independent Go module with bounded parallelism.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Module checks validate isolated builds; local VCS metadata must not affect them.
# CI/release stamping remains enabled in release workflows.
if [[ "${LIP_DISABLE_VCS_STAMPING:-}" == "1" && -z "${GOFLAGS:-}" ]]; then
  export GOFLAGS=-buildvcs=false
fi
JOBS="${LIP_MODULE_CHECK_JOBS:-${CI_NODE_TOTAL:-4}}"
[[ "$JOBS" =~ ^[1-9][0-9]*$ ]] || { echo "LIP_MODULE_CHECK_JOBS must be a positive integer" >&2; exit 2; }

TOOL_TMP=""
DISCOVER_MODULES_BIN="${LIP_DISCOVER_MODULES_BIN:-}"
if [[ -z "$DISCOVER_MODULES_BIN" ]]; then
  TOOL_TMP="$(mktemp -d "${TMPDIR:-/tmp}/golip-module-tools.XXXXXX")"
  DISCOVER_MODULES_BIN="$TOOL_TMP/discover_modules"
  trap 'rm -rf "$TOOL_TMP"' EXIT
  go build -o "$DISCOVER_MODULES_BIN" ./tools/backendplugin/discover_modules
fi

mapfile -t MODULES < <(
  {
    printf '%s\n' "testdata/enterprise_module"
    "$DISCOVER_MODULES_BIN" -root "$ROOT"
  } | awk 'NF && !seen[$0]++'
)

run_module() {
  local module="$1"
  local dir="$ROOT/$module"
  [[ -f "$dir/go.mod" ]] || return 0
  echo "== $module =="
  (
    cd "$dir"
    GOWORK=off go mod tidy -diff
    GOWORK=off go test ./...
    if [[ -d cmd ]]; then
      for command_dir in cmd/*/; do
        [[ -d "$command_dir" ]] || continue
        GOWORK=off go build "./${command_dir%/}"
      done
    fi
  )
}

export ROOT
export -f run_module
printf '%s\n' "Checking ${#MODULES[@]} modules with up to $JOBS workers"
printf '%s\n' "${MODULES[@]}" | xargs -r -P"$JOBS" -I{} bash -c 'run_module "$1"' _ {}
echo "OK: all discovered Go modules passed tidy, tests, and command builds"
