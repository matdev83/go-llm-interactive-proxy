#!/usr/bin/env bash
# Pre-commit quality gate: quality checks, staged tests, race (staged), optional linters.

set -euo pipefail

echo "=== Pre-Commit Quality Gate ==="
echo ""

if ! git diff --cached --name-only --diff-filter=ACMR | grep -qE '\.go$'; then
	echo "No staged Go files detected; skipping quality gate checks."
	exit 0
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Running quality checks..."
bash "$SCRIPT_DIR/quality-checks.sh"

echo ""
echo "Running test suite..."
bash "$SCRIPT_DIR/test-staged.sh"

echo ""
echo "Running race detector scan..."
bash "$SCRIPT_DIR/race-check.sh" --staged --short

echo ""
echo "Running linter..."
if command -v golangci-lint >/dev/null 2>&1; then
	golangci-lint run
else
	echo "Warning: golangci-lint not found, skipping (run: make lint or install golangci-lint)"
fi

echo ""
if command -v govulncheck >/dev/null 2>&1; then
	echo "Running govulncheck..."
	govulncheck ./...
else
	echo "Note: govulncheck not in PATH; skipping (install: go install golang.org/x/vuln/cmd/govulncheck@latest)"
fi

echo ""
echo "=== Quality Gate Passed ==="
exit 0
