#!/usr/bin/env bash
# ACP vs Cursor SDK comparison report (offline/synthetic by default; no credentials).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

go test -count=1 ./internal/plugins/backends/cursorsdk/comparison/...
go run ./internal/plugins/backends/cursorsdk/comparison/cmd/report -format=markdown
