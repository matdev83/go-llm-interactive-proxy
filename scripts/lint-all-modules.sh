#!/usr/bin/env bash
# Runs linter (golangci-lint with staticcheck fallback) across all or scoped Go modules in parallel.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGED=0
CHANGED=0
ADVISORY=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --staged)
      STAGED=1
      shift
      ;;
    --changed)
      CHANGED=1
      shift
      ;;
    --advisory)
      ADVISORY=1
      shift
      ;;
    *)
      echo "usage: $0 [--staged|--changed] [--advisory]" >&2
      exit 2
      ;;
  esac
done

LINTER=""
if command -v golangci-lint >/dev/null 2>&1; then
  LINTER="golangci-lint"
elif command -v staticcheck >/dev/null 2>&1; then
  LINTER="staticcheck"
else
  echo "Warning: golangci-lint/staticcheck not found, skipping (install golangci-lint: https://golangci-lint.run/)" >&2
  exit 0
fi

declare -A MODULE_SET=()

declare -A MODULE_PACKAGES=()
FULL=0
if (( STAGED || CHANGED )); then
  mode=changed
  if (( STAGED )); then mode=staged; fi
  plan="$(go -C "$ROOT" run -buildvcs=false ./tools/lintscope -mode "$mode" -format=lines)"
  if [[ "$plan" == "FULL" ]]; then
    FULL=1
  elif [[ -n "$plan" ]]; then
    while IFS=$'\t' read -r module packages; do
      MODULE_SET["$module"]=1
      MODULE_PACKAGES["$module"]="$packages"
    done <<< "$plan"
  fi
else
  FULL=1
fi
if (( FULL )); then
  # Discover all modules
  MODULE_SET["."]=1
  MODULE_SET["testdata/enterprise_module"]=1
  MODULE_SET["testdata/external_billing_binding"]=1
  MODULE_SET["testdata/external_connector"]=1
  MODULE_SET["testdata/external_feature_sdk"]=1
  for base in connectors connector-support; do
    if [[ -d "$ROOT/$base" ]]; then
      for d in "$ROOT/$base"/*; do
        if [[ -d "$d" && -f "$d/go.mod" ]]; then
          rel="${base}/$(basename "$d")"
          MODULE_SET["$rel"]=1
        fi
      done
    fi
  done
fi

MODULES=("${!MODULE_SET[@]}")
if [[ ${#MODULES[@]} -eq 0 ]]; then
  exit 0
fi

# Each analyzer already schedules parallel work. Avoid cores x cores workers
# and competing whole-module loads; both platforms share this budget.
JOBS="${LIP_LINT_JOBS:-2}"
LINT_CONCURRENCY="${LIP_LINT_CONCURRENCY:-2}"
for budget in "$JOBS" "$LINT_CONCURRENCY"; do
  if [[ ! "$budget" =~ ^[1-9][0-9]*$ ]]; then
    echo "LIP_LINT_JOBS and LIP_LINT_CONCURRENCY must be positive integers" >&2
    exit 2
  fi
done
echo "Lint budget: modules=$JOBS analyzers/module=$LINT_CONCURRENCY"

run_module_lint() {
  local module="$1"
  local dir="$ROOT/$module"
  [[ -f "$dir/go.mod" ]] || return 0
  local packages
  read -r -a packages <<< "${2:-./...}"
  echo "== Linting $module: ${packages[*]} =="
  if [[ "$LINTER" == "golangci-lint" ]]; then
    if (( ADVISORY )); then
      (cd "$dir" && golangci-lint run --allow-parallel-runners --concurrency="$LINT_CONCURRENCY" "${packages[@]}")
    else
      (cd "$dir" && golangci-lint run --allow-parallel-runners --concurrency="$LINT_CONCURRENCY" --disable=modernize,paralleltest,thelper "${packages[@]}")
    fi
  else
    (cd "$dir" && GOMAXPROCS="$LINT_CONCURRENCY" staticcheck "${packages[@]}")
  fi
}

if [[ "$LINTER" == "golangci-lint" ]]; then
  if (( ADVISORY )); then
    echo "Mode: ADVISORY (full set incl. modernize, paralleltest, thelper; non-blocking style report)."
  else
    echo "Mode: MANDATORY correctness gate (--disable=modernize,paralleltest,thelper); style debt via 'make lint-advisory'."
  fi
fi

export ROOT LINTER ADVISORY LINT_CONCURRENCY
export -f run_module_lint
for module in "${MODULES[@]}"; do
  printf '%s\0%s\0' "$module" "${MODULE_PACKAGES[$module]:-./...}"
done | xargs -0 -r -n2 -P"$JOBS" bash -c 'run_module_lint "$1" "$2"' _
echo "OK: All checked Go modules passed linting."
