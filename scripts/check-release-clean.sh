#!/usr/bin/env bash
# Fail unless every file in the candidate repository revision is approved.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$repo_root"

usage() {
	cat >&2 <<'EOF'
Usage: scripts/check-release-clean.sh [--staged | --ref <git-ref>]

  (default)    Check all tracked files in the working tree (for CI).
  --staged     Check the complete staged index (for pre-commit).
  --ref <ref>  Check the complete tree at a Git ref (for pre-push).
EOF
}

mode="tracked"
ref=""
case "${1:-}" in
	"") ;;
	--staged)
		[[ $# -eq 1 ]] || { usage; exit 2; }
		mode="staged"
		;;
	--ref)
		[[ $# -eq 2 ]] || { usage; exit 2; }
		mode="ref"
		ref="$2"
		;;
	*)
		usage
		exit 2
		;;
esac

manifest=".release-files"
case "$mode" in
	tracked)
		[[ -f "$manifest" ]] || { echo "error: missing release file manifest: $manifest" >&2; exit 1; }
		mapfile -t allowed < <(sed '/^[[:space:]]*$/d; /^[[:space:]]*#/d' "$manifest" | LC_ALL=C sort -u)
		mapfile -t files < <(git ls-files | LC_ALL=C sort -u)
		;;
	staged)
		git cat-file -e ":$manifest" 2>/dev/null || { echo "error: $manifest is missing from the staged index" >&2; exit 1; }
		mapfile -t allowed < <(git show ":$manifest" | sed '/^[[:space:]]*$/d; /^[[:space:]]*#/d' | LC_ALL=C sort -u)
		mapfile -t files < <(git ls-files --cached | LC_ALL=C sort -u)
		;;
	ref)
		git cat-file -e "$ref^{tree}" 2>/dev/null || { echo "error: invalid Git ref: $ref" >&2; exit 1; }
		git cat-file -e "$ref:$manifest" 2>/dev/null || { echo "error: $manifest is missing from $ref" >&2; exit 1; }
		mapfile -t allowed < <(git show "$ref:$manifest" | sed '/^[[:space:]]*$/d; /^[[:space:]]*#/d' | LC_ALL=C sort -u)
		mapfile -t files < <(git ls-tree -r --name-only "$ref" | LC_ALL=C sort -u)
		;;
esac

violations=()
while IFS= read -r file; do
	[[ -n "$file" ]] && violations+=("$file")
done < <(comm -23 <(printf '%s\n' "${files[@]}" | LC_ALL=C sort -u) <(printf '%s\n' "${allowed[@]}" | LC_ALL=C sort -u))

missing=()
while IFS= read -r file; do
	[[ -n "$file" ]] && missing+=("$file")
done < <(comm -13 <(printf '%s\n' "${files[@]}" | LC_ALL=C sort -u) <(printf '%s\n' "${allowed[@]}" | LC_ALL=C sort -u))

if ((${#violations[@]} > 0)); then
	echo "error: files not approved by $manifest:" >&2
	printf '  - %s\n' "${violations[@]}" >&2
	echo "Review the files, then explicitly add legitimate release files to $manifest." >&2
	exit 1
fi

if ((${#missing[@]} > 0)); then
	echo "error: stale entries in $manifest:" >&2
	printf '  - %s\n' "${missing[@]}" >&2
	echo "Remove stale entries or stage the corresponding files." >&2
	exit 1
fi

echo "release manifest check passed (${#files[@]} approved files, mode=$mode${ref:+, ref=$ref})"
