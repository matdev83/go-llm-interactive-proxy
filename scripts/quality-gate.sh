#!/usr/bin/env bash
# Pre-commit quality gate: fast checks by default; full lint/vulnerability scans
# are opt-in via LIP_PRECOMMIT_FULL=1 or `make precommit-full`.

set -euo pipefail

echo "=== Pre-Commit Quality Gate ==="
echo ""

staged_files="$(git diff --cached --name-only --diff-filter=ACMRD)"
if ! grep -qE '\.go$' <<< "$staged_files"; then
	if grep -qE '(^|/)(go\.mod|go\.sum)$' <<< "$staged_files"; then
		echo "No staged Go source files detected; checking module metadata."
	else
		echo "No staged Go files or module metadata detected; skipping quality gate checks."
		exit 0
	fi
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if grep -Eq '(^|/)(go\.mod|go\.sum)$' <<< "$staged_files"; then
	echo "Checking all independent Go module metadata..."
	bash "$SCRIPT_DIR/tidy-all-modules.sh" --check
	echo ""
fi

echo "Running quality checks..."
# The following cached root test owns compilation, curated vet, and the
# architecture package; avoid repeating those expensive Go phases in the hook.
LIP_SKIP_GO_COMPILE_CHECKS=1 LIP_SKIP_ARCHTEST=1 bash "$SCRIPT_DIR/quality-checks.sh"

echo ""
echo "Running complete root test suite with precommit tags (Go cache enabled)..."
env LIP_TEST_PRECOMMIT=1 bash "$SCRIPT_DIR/test-staged.sh"

echo ""
echo "Running race detector scan..."
bash "$SCRIPT_DIR/race-check.sh" --staged

if [[ "${LIP_SKIP_LINT:-}" != "1" ]]; then
	echo ""
	echo "Running linter..."
	if command -v golangci-lint >/dev/null 2>&1; then
		golangci-lint run
	elif command -v staticcheck >/dev/null 2>&1; then
		staticcheck ./...
	else
		echo "Warning: golangci-lint/staticcheck not found, skipping (run: make lint or install golangci-lint)"
	fi
fi

if [[ "${LIP_SKIP_VULN:-}" != "1" ]]; then
	echo ""
	echo "Running govulncheck..."
	if command -v govulncheck >/dev/null 2>&1; then
		govulncheck ./...
	else
		go tool govulncheck ./...
	fi
fi

echo ""
echo "=== Quality Gate Passed ==="
exit 0
