#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || -z "${1:-}" ]]; then
  echo "usage: $0 <SPEC>" >&2
  exit 2
fi

export KIRO_SPEC="$1"
exec go test ${GO_TEST_FLAGS:--parallel=8 -timeout=10m} ./tools/kiro/speccheck/ -run '^TestKiroSpec$' -count=1
