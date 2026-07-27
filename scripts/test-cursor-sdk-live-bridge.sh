#!/usr/bin/env bash
# Opt-in Go→Node live bridge lifecycle harness.
# Requires CURSOR_SDK_LIVE=1 + nonempty CURSOR_API_KEY.
# Uses build tag cursorsdk_live_bridge so ordinary go test never hits the network.
# Without opt-in: exit 0 BLOCKED (safe skip). Not a green live proof.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/connectors/cursorsdk"

if [[ "${CURSOR_SDK_LIVE:-}" != "1" ]]; then
  echo "BLOCKED: CURSOR_SDK_LIVE=1 not set; skipping cursor-sdk live-bridge harness"
  exit 0
fi
if [[ -z "${CURSOR_API_KEY:-}" || -z "${CURSOR_API_KEY// }" ]]; then
  echo "BLOCKED: CURSOR_API_KEY missing; skipping cursor-sdk live-bridge harness"
  exit 0
fi

exec env GOWORK=off go test -v -count=1 -timeout=10m -tags=cursorsdk_live_bridge -run '^TestLiveBridgeHarness_Live$' ./internal/product
