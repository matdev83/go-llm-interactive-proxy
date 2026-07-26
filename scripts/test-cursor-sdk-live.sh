#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

# Live SDK lane is Node live-scenarios only. Fake-bridge lifecycle is platform lane.
if [[ "${CURSOR_SDK_LIVE:-}" != "1" ]]; then
  echo "BLOCKED: CURSOR_SDK_LIVE=1 not set; skipping cursor-sdk live scenarios"
  exit 0
fi
if [[ -z "${CURSOR_API_KEY:-}" ]]; then
  echo "BLOCKED: CURSOR_API_KEY missing; skipping cursor-sdk live scenarios"
  exit 0
fi

(
  cd internal/plugins/backends/cursorsdk/bridge
  npm run live-scenarios
)
