#!/usr/bin/env bash
# race-check.sh — Go race detector; best-effort locally unless --strict.

set -euo pipefail

STAGED=false
STRICT=false
LANE=all

while [[ $# -gt 0 ]]; do
	case "$1" in
	--staged) STAGED=true; shift ;;
	--strict) STRICT=true; shift ;;
	--lane)
		[[ $# -ge 2 ]] || { echo '--lane requires a value' >&2; exit 2; }
		LANE="$2"; shift 2 ;;
	*)
		echo "Unknown argument: $1"
		echo "Usage: $0 [--staged] [--strict] [--lane all|broad|billing|support|runtime|architecture]"
		exit 2
		;;
	esac
done
case "$LANE" in
all|broad|billing|support|runtime|architecture) ;;
*) echo "Unknown race lane: $LANE" >&2; exit 2 ;;
esac
if [[ "$STAGED" == true && "$LANE" != all ]]; then
	echo '--staged cannot be combined with a selected lane' >&2
	exit 2
fi

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

declare -a PACKAGES=()
declare -a ARCH_PACKAGES=()
# staged_nested_module_root prints the slash-separated repo-relative directory
# of the nearest nested Go module enclosing directory $1, or nothing when the
# path belongs to the root module. A staged file under a nested module cannot
# be tested with a root-relative ./... pattern (the root module does not
# resolve it), so those files are grouped per module and scanned in their own
# module context instead of failing the whole scan with [setup failed].
staged_nested_module_root() {
	local dir="$1"
	while [[ "$dir" != "." && -n "$dir" ]]; do
		if [[ -f "$repo_root/$dir/go.mod" ]]; then
			printf '%s' "$dir"
			return 0
		fi
		[[ "$dir" == */* ]] || break
		dir="${dir%/*}"
	done
	return 0
}
if [[ "$STAGED" == true ]]; then
	mapfile -t STAGED_GO_FILES < <(git diff --cached --name-only --diff-filter=ACMRD | sed 's#\\#/#g' | grep -E '\.go$' || true)
	if [[ ${#STAGED_GO_FILES[@]} -eq 0 ]]; then
		echo "No staged Go files detected; skipping race detector scan."
		exit 0
	fi
	declare -A PACKAGE_SET=()
	declare -a NESTED_SCOPES=()
	for file in "${STAGED_GO_FILES[@]}"; do
		dir="$(dirname "$file")"
		if [[ "$dir" == "." || -z "$dir" ]]; then
			PACKAGE_SET["./"]=1
		else
			modroot="$(staged_nested_module_root "$dir")"
			if [[ -n "$modroot" ]]; then
				if [[ "$dir" == "$modroot" ]]; then
					NESTED_SCOPES+=("$modroot|./...")
				else
					NESTED_SCOPES+=("$modroot|./${dir#"$modroot"/}/...")
				fi
			else
				PACKAGE_SET["./${dir}/..."]=1
			fi
		fi
	done
	mapfile -t STAGED_SCOPES < <(printf '%s\n' "${!PACKAGE_SET[@]}" | sort)
	# Staged scopes reach the same archtest subtree as the full scan, so apply the
	# same separation described below (issue #262): a single `go test` that mixes
	# the slow repo-wide archtest AST scans with the other staged scopes re-creates
	# exactly the CPU contention that costs those packages their timeout budget.
	# This is a scheduling partition only; both groups use the identical GO_ARGS
	# and the caller's environment, so no budget, tag, or coverage changes.
	for scope in "${STAGED_SCOPES[@]}"; do
		# Skip the empty scope: with no ordinary entries, the printf above
		# still emits one newline, which mapfile reads as a single empty
		# scope. Running `go test` with it would scan the current directory
		# instead of nothing.
		[[ -z "$scope" ]] && continue
		case "$scope" in
		./internal/archtest | ./internal/archtest/*) ARCH_PACKAGES+=("$scope") ;;
		*) PACKAGES+=("$scope") ;;
		esac
	done
	if [[ ${#PACKAGES[@]} -eq 0 && ${#ARCH_PACKAGES[@]} -eq 0 && ${#NESTED_SCOPES[@]} -eq 0 ]]; then
		echo "ERROR: race scan package set is empty; refusing to run go test with no package args" >&2
		exit 1
	fi
elif [[ "$LANE" == all || "$LANE" == broad ]]; then
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
	# Exhaustive billing sweeps and durable runtime tests must not compete with
	# the broad scan for CPU. Every package still runs once with both tags.
	mapfile -t PACKAGES < <(printf '%s\n' "$packages_list" | grep -vE '/internal/archtest(/|$)|/internal/core/(billing|runtime)$')
	if [[ ${#PACKAGES[@]} -eq 0 ]]; then
		echo "ERROR: race scan package set is empty; refusing to run go test with no package args" >&2
		exit 1
	fi
fi

declare -a GO_ARGS
# Bound package and in-package parallelism independently: Go defaults allow
# both layers to consume the full CPU count at the same time.
GO_ARGS=("test" "-race" "-tags=precommit,integration" "-count=1" "-p=4" "-parallel=4")

LOG_FILE="$repo_root/.tmp/race-check.log"
: >"$LOG_FILE"

STATUS=0
run_race_scan() {
	go "${GO_ARGS[@]}" "$@" 2>&1 | tee -a "$LOG_FILE"
	local scan_status=${PIPESTATUS[0]}
	[[ $scan_status -ne 0 ]] && STATUS=$scan_status
}

echo "Race detector scan: ${#PACKAGES[@]} ordinary scope(s), ${#ARCH_PACKAGES[@]} staged archtest scope(s)"
set +e
if [[ "$STAGED" == true || "$LANE" == all || "$LANE" == broad ]]; then
	if [[ ${#PACKAGES[@]} -gt 0 ]]; then
		echo "Running race detector scan: go ${GO_ARGS[*]} ${PACKAGES[*]}"
		run_race_scan "${PACKAGES[@]}"
	fi
fi
if [[ "$STAGED" == true && ${#ARCH_PACKAGES[@]} -gt 0 ]]; then
	echo "Running staged archtest race scan separately: go ${GO_ARGS[*]} ${ARCH_PACKAGES[*]}"
	run_race_scan "${ARCH_PACKAGES[@]}"
fi
if [[ "$STAGED" == true && ${#NESTED_SCOPES[@]} -gt 0 ]]; then
	while IFS='|' read -r modroot pattern; do
		[[ -n "$modroot" && -n "$pattern" ]] || continue
		echo "Running staged nested-module race scan in $modroot: go ${GO_ARGS[*]} $pattern"
		(
			cd "$repo_root/$modroot" || exit 1
			go "${GO_ARGS[@]}" "$pattern"
		) 2>&1 | tee -a "$LOG_FILE"
		scan_status=${PIPESTATUS[0]}
		[[ $scan_status -ne 0 ]] && STATUS=$scan_status
	done < <(printf '%s\n' "${NESTED_SCOPES[@]}" | sort -u)
fi
if [[ "$STAGED" != true ]]; then
	if [[ "$LANE" == all || "$LANE" == billing ]]; then
		echo "Running billing race scan separately (60m package timeout)"
		run_race_scan -timeout=60m -skip '^TestSupportAgreementShadowPredicate$' ./internal/core/billing
	fi
	if [[ "$LANE" == all || "$LANE" == support ]]; then
		echo "Running exhaustive support-agreement race scan separately (25m package timeout)"
		run_race_scan -timeout=25m -run '^TestSupportAgreementShadowPredicate$' ./internal/core/billing
	fi
	if [[ "$LANE" == all || "$LANE" == runtime ]]; then
		echo "Running durable runtime race scan separately"
		run_race_scan ./internal/core/runtime
	fi
	if [[ "$LANE" == all || "$LANE" == architecture ]]; then
		echo "Running archtest race scan separately: go ${GO_ARGS[*]} -timeout=25m ./internal/archtest/..."
		run_race_scan -timeout=25m ./internal/archtest/...
	fi
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
