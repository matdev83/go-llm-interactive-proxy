#!/usr/bin/env bash
# race-check.sh — Go race detector; best-effort locally unless --strict.

set -euo pipefail

SHORT=false
STAGED=false
STRICT=false

while [[ $# -gt 0 ]]; do
	case "$1" in
	--short) SHORT=true; shift ;;
	--staged) STAGED=true; shift ;;
	--strict) STRICT=true; shift ;;
	*)
		echo "Unknown argument: $1"
		echo "Usage: $0 [--short] [--staged] [--strict]"
		exit 2
		;;
	esac
done

if ! command -v go >/dev/null 2>&1; then
	echo "ERROR: go not found in PATH"
	exit 1
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
go test -race -run '^$' -c -o .tmp/race-precheck.test ./pkg/lipsdk >"$PRECHECK_LOG" 2>&1
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
PACKAGES=("./...")

if [[ "$STAGED" == true ]]; then
	mapfile -t STAGED_GO_FILES < <(git diff --cached --name-only --diff-filter=ACMR | sed 's#\\#/#g' | grep -E '\.go$' || true)
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
fi

declare -a GO_ARGS
GO_ARGS=("test" "-race" "-count=1")
[[ "$SHORT" == true ]] && GO_ARGS+=("-short")
GO_ARGS+=("${PACKAGES[@]}")

echo "Running race detector scan: go ${GO_ARGS[*]}"

LOG_FILE=".tmp/race-check.log"
set +e
go "${GO_ARGS[@]}" 2>&1 | tee "$LOG_FILE"
STATUS=${PIPESTATUS[0]}
set -e

if [[ $STATUS -ne 0 ]]; then
	if [[ "$STRICT" == false ]] && grep -qiE "race detector is not supported|cgo\.exe:.*exit status|C compiler|gcc.*not found" "$LOG_FILE"; then
		echo "Race detector is not available on this environment; skipping."
		exit 0
	fi
	exit $STATUS
fi

echo "Race detector scan passed."
