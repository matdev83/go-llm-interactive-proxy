#!/usr/bin/env bash
# quality-checks.sh
# Fast quality checks before tests. Order: fastest to slowest, fail-fast.

set -euo pipefail

# Test parallelism defaults to the machine's logical core count; override with
# LIP_TEST_PARALLEL=<n> (mirrors GO_TEST_FLAGS in the Makefile).
test_parallel="${LIP_TEST_PARALLEL:-}"
if [ -z "$test_parallel" ]; then
	test_parallel=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 8)
fi

under_nested_go_module() {
	local file="$1"
	local dir parent

	dir=$(dirname "$file")
	if [ -z "$dir" ] || [ "$dir" = "." ]; then
		return 1
	fi

	while [ -n "$dir" ] && [ "$dir" != "." ]; do
		if [ -f "${dir}/go.mod" ]; then
			return 0
		fi
		parent=$(dirname "$dir")
		if [ "$parent" = "$dir" ]; then
			break
		fi
		dir=$parent
	done

	return 1
}

is_agent_skill_path() {
	case "$1" in
		.agents/skills/*|.codex/skills/*|.cursor/skills/*|.kiro/skills/*|.opencode/skills/*|.pi/skills/*)
			return 0
			;;
	esac
	return 1
}

collect_quality_packages() {
	local -a staged_go_files=()
	local file dir
	local force_full=false
	declare -A package_set=()

	mapfile -t staged_go_files < <(git diff --cached --name-only --diff-filter=ACMRD 2>/dev/null | sed 's#\\#/#g' | grep -E '\.go$' || true)

	if [ ${#staged_go_files[@]} -eq 0 ]; then
		printf './...\n'
		return 0
	fi

	for file in "${staged_go_files[@]}"; do
		if is_agent_skill_path "$file"; then
			continue
		fi
		dir=$(dirname "$file")
		if [ -z "$dir" ] || [ "$dir" = "." ]; then
			force_full=true
			break
		fi
		if under_nested_go_module "$file"; then
			continue
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

echo "[1/8] Checking generated feature planes..."
if ! go run ./scripts/generate-feature-planes.go -check; then
	echo "ERROR: Feature planes generation check failed"
	exit 1
fi
echo "OK: Generated feature planes check passed"
echo ""

echo "[2/8] Checking Go formatting..."
unformatted=$(gofmt -l . 2>/dev/null | while IFS= read -r file; do
	file=${file//\\//}
	if ! is_agent_skill_path "$file"; then
		printf '%s\n' "$file"
	fi
done || true)
if [ -n "$unformatted" ]; then
	echo "Unformatted files:"
	echo "$unformatted"
	echo "Run: gofmt -w <files> or go fmt ./..."
	exit 1
fi
echo "OK: Format check passed"
echo ""

echo "[3/8] Checking Go modules..."
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

script_dir=$(cd "$(dirname "$0")" && pwd)

if [ "${LIP_SKIP_GO_COMPILE_CHECKS:-}" = "1" ]; then
	echo "Skipping standalone build/vet: the following go test target owns compilation and curated vet checks."
else
	echo "[4/8] Checking build..."
	if ! go build "${QUALITY_PACKAGES[@]}"; then
		echo "ERROR: Build failed"
		exit 1
	fi
	echo "OK: Build check passed"
	echo ""

	echo "[5/8] Running go vet..."
	if ! go vet "${QUALITY_PACKAGES[@]}"; then
		echo "ERROR: go vet failed"
		exit 1
	fi
	echo "OK: Vet check passed"
	echo ""
fi

echo "[6-8/8] Running independent guardrails in parallel..."
guard_tmp=$(mktemp -d "${TMPDIR:-/tmp}/lip-quality.XXXXXX")
guard_cleanup() { rm -rf "$guard_tmp"; }
trap guard_cleanup EXIT

declare -A guard_pids=( )
run_guard() {
	local name="$1"
	shift
	"$@" >"$guard_tmp/${name}.log" 2>&1 &
	guard_pids["$name"]=$!
}

run_guard adhoc bash "$script_dir/check-adhoc-goroutines.sh"
run_guard regex bash "$script_dir/regex-hotpath-check.sh"
if [ "${LIP_SKIP_ARCHTEST:-}" != "1" ]; then
	# Match the test-unit flags (make GO_TEST_FLAGS) so the standalone
	# quality-checks archtest run shares Go's build/test cache with
	# subsequent `make test`/`make qa` executions (see #291).
	run_guard archtest go test "-parallel=$test_parallel" -timeout=10m ./internal/archtest/...
fi

status=0
for name in "${!guard_pids[@]}"; do
	if ! wait "${guard_pids[$name]}"; then
		status=1
	fi
done

for name in "${!guard_pids[@]}"; do
	echo "--- ${name} ---"
	cat "$guard_tmp/${name}.log"
done
if [ "$status" -ne 0 ]; then
	echo "ERROR: one or more parallel guardrails failed"
	exit "$status"
fi
echo ""

echo "=== All Quality Checks Passed ==="
exit 0
