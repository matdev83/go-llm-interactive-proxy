#!/usr/bin/env bash
# Scan staged content for common credential shapes. Optional: install gitleaks for deeper rules.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "check-staged-secrets: not inside a git repository" >&2
	exit 2
}
cd "$REPO_ROOT"

if git diff --cached --quiet 2>/dev/null; then
	exit 0
fi

if command -v gitleaks >/dev/null 2>&1; then
	echo "Running gitleaks on staged changes..."
	if gitleaks protect --staged --verbose; then
		exit 0
	fi
	echo "gitleaks reported potential secrets. Fix or allowlist in .gitleaks.toml, then restage." >&2
	exit 1
fi

echo "gitleaks not found; using built-in high-signal patterns (install gitleaks for stronger coverage)."

# One extended-regex: PEM keys, AWS key ids, GitHub PATs, Slack, Stripe live, Google API keys.
PATTERN='AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|-----BEGIN[[:space:]]*(RSA|EC|OPENSSH|DSA|PGP|ENCRYPTED)?[[:space:]]*PRIVATE[[:space:]]+KEY-----|ghp_[0-9A-Za-z]{36}|github_pat_[0-9A-Za-z_]+|xox[pbars]-[0-9A-Za-z-]{10,}|sk_live_[0-9a-zA-Z]{24,}|AIza[0-9A-Za-z_-]{35}'

PATHSPECS=(. ':(exclude)testdata' ':(exclude)testdata/**')
ALLOWLIST="$REPO_ROOT/scripts/secret-scan-allowlist.txt"
if [[ -f "$ALLOWLIST" ]]; then
	while IFS= read -r line || [[ -n "$line" ]]; do
		[[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
		line="${line#"${line%%[![:space:]]*}"}"
		line="${line%"${line##*[![:space:]]}"}"
		[[ -z "$line" ]] && continue
		PATHSPECS+=(":(exclude)$line")
	done <"$ALLOWLIST"
fi

set +e
git grep --cached -n -E "$PATTERN" -- "${PATHSPECS[@]}"
grep_rc=$?
set -e

if [[ "$grep_rc" -eq 0 ]]; then
	echo "" >&2
	echo "check-staged-secrets: staged content matches high-risk credential patterns." >&2
	echo "Remove secrets, rotate exposed credentials, or allowlist paths in scripts/secret-scan-allowlist.txt (prefix lines with # for comments)." >&2
	exit 1
fi

if [[ "$grep_rc" -ne 1 ]]; then
	echo "check-staged-secrets: git grep failed (exit $grep_rc)" >&2
	exit "$grep_rc"
fi

exit 0
