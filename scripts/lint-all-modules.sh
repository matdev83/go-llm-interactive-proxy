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

is_agent_skill_path() {
  case "$1" in
    .agents/skills/*|.codex/skills/*|.cursor/skills/*|.kiro/skills/*|.opencode/skills/*|.pi/skills/*)
      return 0
      ;;
  esac
  return 1
}

get_module_for_file() {
  local file="$1"
  local dir
  dir="$(dirname "$file")"
  while [[ -n "$dir" && "$dir" != "." ]]; do
    if [[ -f "$ROOT/$dir/go.mod" ]]; then
      echo "$dir"
      return 0
    fi
    dir="$(dirname "$dir")"
  done
  echo "."
}

LINTER=""
LINTER_FLAGS=()
if command -v golangci-lint >/dev/null 2>&1; then
  LINTER="golangci-lint"
  LINTER_FLAGS=("run" "--allow-parallel-runners")
elif command -v staticcheck >/dev/null 2>&1; then
  LINTER="staticcheck"
  LINTER_FLAGS=("./...")
else
  echo "Warning: golangci-lint/staticcheck not found, skipping (install golangci-lint: https://golangci-lint.run/)" >&2
  exit 0
fi

declare -A MODULE_SET=()

if (( STAGED )); then
  mapfile -t staged_files < <(git diff --cached --name-only --diff-filter=ACMRD 2>/dev/null | sed 's#\\#/#g' | grep -E '(\.go$|(^|/)go\.(mod|sum)$)' || true)
  if [[ ${#staged_files[@]} -eq 0 ]]; then
    exit 0
  fi
  for f in "${staged_files[@]}"; do
    if ! is_agent_skill_path "$f"; then
      m="$(get_module_for_file "$f")"
      MODULE_SET["$m"]=1
    fi
  done
elif (( CHANGED )); then
  mapfile -t changed_files < <(
    {
      git diff --cached --name-only --diff-filter=ACMRD 2>/dev/null
      git diff --name-only --diff-filter=ACMRD 2>/dev/null
      git ls-files --others --exclude-standard 2>/dev/null
    } | sed 's#\\#/#g' | grep -E '(\.go$|(^|/)go\.(mod|sum)$)' | sort -u || true
  )
  if [[ ${#changed_files[@]} -eq 0 ]]; then
    MODULE_SET["."]=1
  else
    for f in "${changed_files[@]}"; do
      if ! is_agent_skill_path "$f"; then
        m="$(get_module_for_file "$f")"
        MODULE_SET["$m"]=1
      fi
    done
  fi
else
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
  echo "== Linting $module =="
  if [[ "$LINTER" == "golangci-lint" ]]; then
    if (( ADVISORY )); then
      (cd "$dir" && golangci-lint run --allow-parallel-runners --concurrency="$LINT_CONCURRENCY")
    else
      (cd "$dir" && golangci-lint run --allow-parallel-runners --concurrency="$LINT_CONCURRENCY" --disable=modernize,paralleltest,thelper)
    fi
  else
    (cd "$dir" && GOMAXPROCS="$LINT_CONCURRENCY" staticcheck ./...)
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
printf '%s\n' "${MODULES[@]}" | xargs -r -P"$JOBS" -I{} bash -c 'run_module_lint "$1"' _ {}
echo "OK: All checked Go modules passed linting."
