#!/usr/bin/env bash
# quality-checks.sh
# Fast quality checks before tests. Order: fastest to slowest, fail-fast.

set -euo pipefail

collect_quality_packages() {
	local -a staged_go_files=()
	local file dir
	local force_full=false
	declare -A package_set=()

	mapfile -t staged_go_files < <(git diff --cached --name-only --diff-filter=ACMR 2>/dev/null | sed 's#\\#/#g' | grep -E '\.go$' || true)

	if [ ${#staged_go_files[@]} -eq 0 ]; then
		printf './...\n'
		return 0
	fi

	for file in "${staged_go_files[@]}"; do
		dir=$(dirname "$file")
		if [ -z "$dir" ] || [ "$dir" = "." ]; then
			force_full=true
			break
		fi
		package_set["./${dir}/..."]=1
	done

	if [ "$force_full" = true ] || [ ${#package_set[@]} -eq 0 ]; then
		printf './...\n'
		return 0
	fi

	printf '%s\n' "${!package_set[@]}" | sort
}

mapfile -t QUALITY_PACKAGES < <(collect_quality_packages)

echo "Quality scope: ${QUALITY_PACKAGES[*]}"
echo ""
echo "=== Quality Checks ==="
echo ""

echo "[1/7] Checking Go formatting..."
unformatted=$(gofmt -l . 2>/dev/null || true)
if [ -n "$unformatted" ]; then
	echo "Unformatted files:"
	echo "$unformatted"
	echo "Run: gofmt -w <files> or go fmt ./..."
	exit 1
fi
echo "OK: Format check passed"
echo ""

echo "[2/7] Checking Go modules..."
pre_tidy_mod=$(git hash-object go.mod 2>/dev/null || printf 'missing-go-mod')
pre_tidy_sum=$(git hash-object go.sum 2>/dev/null || printf 'missing-go-sum')
go mod tidy
post_tidy_mod=$(git hash-object go.mod 2>/dev/null || printf 'missing-go-mod')
post_tidy_sum=$(git hash-object go.sum 2>/dev/null || printf 'missing-go-sum')
if [ "$pre_tidy_mod" != "$post_tidy_mod" ] || [ "$pre_tidy_sum" != "$post_tidy_sum" ]; then
	tidy_changes=$(git diff --name-only go.mod go.sum 2>/dev/null || true)
	echo "ERROR: go.mod/go.sum modified by 'go mod tidy'"
	if [ -n "$tidy_changes" ]; then
		echo "Changes detected:"
		echo "$tidy_changes"
	fi
	echo "Run: go mod tidy && git add go.mod go.sum"
	exit 1
fi
should_verify_module_cache=false
case "${LIP_VERIFY_MODULE_CACHE:-}" in
	1|true|TRUE|yes|YES|on|ON)
		should_verify_module_cache=true
		;;
esac
if [ "$should_verify_module_cache" = false ]; then
	case "${CI:-}" in
		1|true|TRUE|yes|YES|on|ON)
			should_verify_module_cache=true
			;;
	esac
fi
if [ "$should_verify_module_cache" = true ]; then
	echo "Verifying module checksums..."
	if ! go mod verify; then
		echo "ERROR: go mod verify failed (checksum mismatch or corrupt module cache)"
		exit 1
	fi
else
	echo "Skipping module cache verification locally (set LIP_VERIFY_MODULE_CACHE=1 to enable)."
fi
echo "OK: Module check passed"
echo ""

echo "[3/7] Checking build..."
if ! go build "${QUALITY_PACKAGES[@]}"; then
	echo "ERROR: Build failed"
	exit 1
fi
echo "OK: Build check passed"
echo ""

echo "[4/7] Running go vet..."
if ! go vet "${QUALITY_PACKAGES[@]}"; then
	echo "ERROR: go vet failed"
	exit 1
fi
echo "OK: Vet check passed"
echo ""

script_dir=$(cd "$(dirname "$0")" && pwd)
echo "[5/7] Ad-hoc goroutine allowlist (non-test)..."
if ! bash "$script_dir/check-adhoc-goroutines.sh"; then
	exit 1
fi
echo ""

echo "[6/7] Regex hot-path check (regexp compile in frontends/runtime)..."
if ! bash "$script_dir/regex-hotpath-check.sh"; then
	exit 1
fi
echo ""

echo "[7/7] Architecture guardrails (line budgets, no init in bundle path)..."
if ! go test ./internal/archtest/...; then
	echo "ERROR: internal/archtest failed"
	exit 1
fi
echo ""

echo "=== All Quality Checks Passed ==="
exit 0
