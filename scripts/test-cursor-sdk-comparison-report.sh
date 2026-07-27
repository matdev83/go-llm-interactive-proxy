#!/usr/bin/env bash
# ACP vs Cursor SDK comparison report (offline/synthetic by default; no credentials).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/connectors/cursorsdk"

GOWORK=off go test -count=1 ./internal/product/comparison/...
GOWORK=off go run ./internal/product/comparison/cmd/report -format=markdown
