#!/usr/bin/env bash
# test-staged.sh
# Run tests for packages touched by staged Go files; otherwise ./...

set -euo pipefail

VERBOSE=${VERBOSE:-false}

if ! STAGED_FILES=$(git diff --cached --name-only --diff-filter=ACMR 2>&1); then
	echo "Error getting staged files. Running all tests..."
	go test -parallel=8 ./...
	exit $?
fi

GO_FILES=$(echo "$STAGED_FILES" | grep '\.go$' | grep -v '_test\.go$' || true)
TEST_FILES=$(echo "$STAGED_FILES" | grep '_test\.go$' || true)

if [ -z "$GO_FILES" ] && [ -z "$TEST_FILES" ]; then
	echo "No Go files staged. Running all tests..."
	go test -parallel=8 ./...
	exit $?
fi

declare -A PACKAGES
for file in $GO_FILES $TEST_FILES; do
	dir=$(dirname "$file")
	if [ -n "$dir" ] && [ "$dir" != "." ]; then
		PACKAGES["./${dir}/..."]=1
	fi
done

if [ ${#PACKAGES[@]} -eq 0 ]; then
	echo "No packages identified. Running all tests..."
	go test -parallel=8 ./...
	exit $?
fi

echo "Testing packages:"
for pkg in "${!PACKAGES[@]}"; do
	echo "  - $pkg"
done
echo ""

PKG_LIST=""
for pkg in "${!PACKAGES[@]}"; do
	PKG_LIST="$PKG_LIST $pkg"
done

# Omit -count=1 so Go's test result cache can skip unchanged packages on repeat runs (build cache is always on via GOCACHE).
go test -parallel=8 $PKG_LIST
exit $?
