#!/usr/bin/env bash
set -euo pipefail

git config core.hooksPath .githooks
chmod +x .githooks/pre-commit scripts/check-staged-secrets.sh scripts/quality-gate.sh 2>/dev/null || true

echo "Git hooks installed."
echo "core.hooksPath=$(git config --get core.hooksPath)"
