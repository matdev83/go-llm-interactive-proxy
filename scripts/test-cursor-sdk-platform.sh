#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

go test -timeout=5m -run '^TestPlatformSmoke_|^TestProbeNativeBridgeLane_' ./internal/plugins/backends/cursorsdk
