#!/usr/bin/env bash
# race-check.sh — Go race detector; best-effort locally unless --strict.

set -euo pipefail

STAGED=false
STRICT=false

while [[ $# -gt 0 ]]; do
	case "$1" in
	--staged) STAGED=true; shift ;;
	--strict) STRICT=true; shift ;;
	*)
		echo "Unknown argument: $1"
		echo "Usage: $0 [--staged] [--strict]"
		exit 2
		;;
	esac
done

if ! command -v go >/dev/null 2>&1; then
	echo "ERROR: go not found in PATH"
	exit 1
fi

# WSL DrvFS (/mnt/c, /mnt/d, ...): ThreadSanitizer often fails to mmap shadow
# memory for binaries and temp files on 9p. Force temp + build cache onto the
# native Linux filesystem (same pattern as many Go+WSL setups).
repo_root="$(pwd -P)"
if [[ "$repo_root" == /mnt/* ]]; then
	race_work="${XDG_CACHE_HOME:-$HOME/.cache}/go-llm-interactive-proxy/race-check"
	mkdir -p "${race_work}/tmp" "${race_work}/gocache"
	export TMPDIR="${race_work}/tmp"
	export GOCACHE="${race_work}/gocache"
fi

CGO_ENABLED="$(go env CGO_ENABLED)"
CC_VALUE="$(go env CC)"
CC_BIN="${CC_VALUE%% *}"

if [[ "$CGO_ENABLED" != "1" ]]; then
	echo "Race detector unavailable (CGO_ENABLED=$CGO_ENABLED)."
	[[ "$STRICT" == true ]] && exit 1
	exit 0
fi

if [[ -n "$CC_BIN" ]] && ! command -v "$CC_BIN" >/dev/null 2>&1; then
	echo "Race detector unavailable (C compiler '$CC_BIN' not found)."
	[[ "$STRICT" == true ]] && exit 1
	exit 0
fi

mkdir -p .tmp
PRECHECK_LOG=".tmp/race-precheck.log"
set +e
go test -race -tags=precommit,integration -run '^$' -c -o .tmp/race-precheck.test ./pkg/lipsdk >"$PRECHECK_LOG" 2>&1
PRECHECK_STATUS=$?
set -e
rm -f .tmp/race-precheck.test .tmp/race-precheck.test.exe 2>/dev/null || true

if [[ $PRECHECK_STATUS -ne 0 ]]; then
	if grep -qiE "race detector is not supported|cgo\.exe:.*exit status|C compiler|gcc.*not found" "$PRECHECK_LOG"; then
		echo "Race detector is not available on this environment; skipping."
		[[ "$STRICT" == true ]] && exit 1
		exit 0
	fi
	cat "$PRECHECK_LOG"
	exit $PRECHECK_STATUS
fi

declare -a PACKAGES
if [[ "$STAGED" == true ]]; then
	mapfile -t STAGED_GO_FILES < <(git diff --cached --name-only --diff-filter=ACMRD | sed 's#\\#/#g' | grep -E '\.go$' || true)
	if [[ ${#STAGED_GO_FILES[@]} -eq 0 ]]; then
		echo "No staged Go files detected; skipping race detector scan."
		exit 0
	fi
	declare -A PACKAGE_SET=()
	for file in "${STAGED_GO_FILES[@]}"; do
		dir="$(dirname "$file")"
		if [[ "$dir" == "." || -z "$dir" ]]; then
			PACKAGE_SET["./"]=1
		else
			PACKAGE_SET["./${dir}/..."]=1
		fi
	done
	mapfile -t PACKAGES < <(printf '%s\n' "${!PACKAGE_SET[@]}" | sort)
else
	# internal/archtest is by far the slowest package under -race: its parallel
	# repo-wide AST scans take ~90s without the detector and blow past the 10m
	# default per-package timeout on CI while competing for CPU with the other
	# concurrent package runs (issue #262). qa.yml already gives archtest its
	# own step; mirror that here by scanning it separately with a dedicated 25m
	# budget (measured ~90s non-race, >10m under CI contention) instead of
	# raising the timeout for every package.
	packages_list="$(go list ./... 2>&1)" || {
		echo "ERROR: go list ./... failed; cannot compute the race scan package set" >&2
		echo "$packages_list" >&2
		exit 1
	}
	mapfile -t PACKAGES < <(printf '%s\n' "$packages_list" | grep -vE '/internal/archtest(/|$)')
	if [[ ${#PACKAGES[@]} -eq 0 ]]; then
		echo "ERROR: race scan package set is empty; refusing to run go test with no package args" >&2
		exit 1
	fi
fi

declare -a GO_ARGS
GO_ARGS=("test" "-race" "-tags=precommit,integration" "-count=1")

LOG_FILE=".tmp/race-check.log"
: >"$LOG_FILE"

STATUS=0
run_race_scan() {
	go "${GO_ARGS[@]}" "$@" 2>&1 | tee -a "$LOG_FILE"
	local scan_status=${PIPESTATUS[0]}
	[[ $scan_status -ne 0 ]] && STATUS=$scan_status
}

echo "Running race detector scan: go ${GO_ARGS[*]} ${PACKAGES[*]}"
set +e
run_race_scan "${PACKAGES[@]}"
if [[ "$STAGED" != true ]]; then
	echo "Running archtest race scan separately: go ${GO_ARGS[*]} -timeout=25m ./internal/archtest/..."
	run_race_scan -timeout=25m ./internal/archtest/...
fi
set -e

if [[ $STATUS -ne 0 ]]; then
	if [[ "$STRICT" == false ]] && grep -qiE "race detector is not supported|cgo\.exe:.*exit status|C compiler|gcc.*not found" "$LOG_FILE"; then
		echo "Race detector is not available on this environment; skipping."
		exit 0
	fi
	exit $STATUS
fi

echo "Race detector scan passed."
