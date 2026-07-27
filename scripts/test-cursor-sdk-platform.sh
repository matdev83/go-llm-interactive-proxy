#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/connectors/cursorsdk"

GOWORK=off go test -timeout=5m -run '^TestPlatformSmoke_|^TestProbeNativeBridgeLane_' ./internal/product
