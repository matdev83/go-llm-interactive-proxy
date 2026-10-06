#!/usr/bin/env bash
# Enforce container worktree layout on this host only.
# Allowed: the long-lived checkouts at <container>/branches/main and
# <container>/branches/dev, plus task worktrees under
# <container>/worktrees/. Any other `git worktree list` path fails the
# commit. No-op on every other hostname (exact match on `hostname` output).
set -euo pipefail

ALLOWED_HOST="agent-dev"

current_host="$(hostname 2>/dev/null || true)"
if [[ "$current_host" != "$ALLOWED_HOST" ]]; then
	echo "Skipping worktree-layout check (host '$current_host' != '$ALLOWED_HOST')."
	exit 0
fi

toplevel="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$toplevel" ]]; then
	echo "check-worktree-layout: not inside a git repository" >&2
	exit 1
fi

parent_name="$(basename "$(dirname "$toplevel")")"
if [[ "$parent_name" != "worktrees" && "$parent_name" != "branches" ]]; then
	echo "Skipping worktree-layout check (not in managed container layout: $toplevel)."
	exit 0
fi

container="$(cd "$toplevel/../.." && pwd -P)"
main_path="$container/branches/main"
dev_path="$container/branches/dev"
allowed_prefix="$container/worktrees/"

violations=()
while IFS= read -r line; do
	[[ "$line" == worktree\ * ]] || continue
	wt="${line#worktree }"
	wt="${wt%/}"
	if [[ "$wt" == "$main_path" || "$wt" == "$dev_path" ]]; then
		continue
	fi
	if [[ "$wt" == "$allowed_prefix"* ]]; then
		continue
	fi
	violations+=("$wt")
done < <(git worktree list --porcelain)

if ((${#violations[@]} > 0)); then
	echo "error: git worktrees outside ./worktrees/ are not allowed on $ALLOWED_HOST:" >&2
	printf '  - %s\n' "${violations[@]}" >&2
	echo "Allowed: $main_path, $dev_path, and $allowed_prefix<name>" >&2
	echo "Fix with: git worktree move <path> $allowed_prefix<name>" >&2
	exit 1
fi

echo "worktree-layout check passed (host $ALLOWED_HOST, container $container)"
