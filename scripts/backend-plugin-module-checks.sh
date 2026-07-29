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

echo "== acp mirror byte-identical files =="
ROOT_ACP="internal/plugins/backends/acp"
CONN_ACP="connector-support/acp"
MIRROR_FILES=(
  acp_protocol.go call_extract_test.go call_extract.go cancel.go client.go
  connector_config_test.go doc.go handshake.go history_transcript_test.go history_transcript.go
  invoke_test.go invoke.go main_test.go model_index_test.go model_index.go
  process_identity_test.go process_identity_unix_test.go process_identity_unix.go
  process_identity_windows.go process_identity.go rpc_error_test.go rpc_error.go
  rpc_id.go rpc.go runtime_pool_claim_test.go runtime_pool_ensure_test.go runtime_pool_ensure.go
  runtime_pool_test.go runtime_pool.go server_handler.go server_request_test.go
  session_update_test.go session_update.go session.go stderr_sanitize_test.go stderr_sanitize.go
  subprocess_protocol.go subprocess_spec_test.go tool_sink.go tool_summary_test.go tool_summary.go
  transport_stdio_os_unix.go transport_stdio_os_windows.go transport_stdio_os.go transport_stdio.go
  workspace_test.go workspace.go
)
for f in "${MIRROR_FILES[@]}"; do
  if ! cmp -s "$ROOT_ACP/$f" "$CONN_ACP/$f"; then
    echo "acp mirror drift (expected byte-identical): $f" >&2
    exit 1
  fi
done

echo "OK backend-plugin-module-checks"
