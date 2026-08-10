#!/usr/bin/env bash
# test-staged.sh
# Run the complete root-module test graph through Go's native build/test cache.
# Set LIP_TEST_PRECOMMIT=1 (e.g. from quality-gate) to include the precommit-only
# regression and hygiene tests. The full graph is intentional: staged-package
# filtering can miss reverse dependencies affected by a changed package.

set -euo pipefail

pre_flags=()
if [[ "${LIP_TEST_PRECOMMIT:-}" =~ ^(1|true|yes|on)$ ]]; then
	pre_flags=( -tags=precommit )
fi

echo "Testing complete root-module package graph (Go build/test cache enabled)"
if [ ${#pre_flags[@]} -gt 0 ]; then
	echo "Test tags: ${pre_flags[*]}"
fi
echo ""

# Deliberately omit -count=1. Go can reuse both package builds and successful
# test results, while ./... catches downstream packages that staged-only
# selection would miss after a shared-package change.
go test -parallel=8 "${pre_flags[@]}" ./...
exit $?
