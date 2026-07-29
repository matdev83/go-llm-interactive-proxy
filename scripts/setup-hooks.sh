#!/usr/bin/env bash
# Point this repository at the versioned hooks under scripts/hooks/.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

git config core.hooksPath scripts/hooks
chmod +x scripts/check-release-clean.sh scripts/hooks/pre-commit scripts/hooks/pre-push

echo "Installed pre-commit and pre-push hooks via core.hooksPath=scripts/hooks"
