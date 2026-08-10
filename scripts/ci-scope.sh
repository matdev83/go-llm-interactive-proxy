#!/usr/bin/env bash
# Emit changed-file scope for CI jobs without skipping required workflow jobs.
# Usage: ci-scope.sh --outputs BASE_SHA HEAD_SHA
#        ci-scope.sh --self-test
set -euo pipefail

is_documentation_path() {
  case "$1" in
    docs/**|README.md|README.*.md|CHANGELOG.md|CHANGELOG.*.md|LICENSE|LICENSE.*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

file_matches() {
  local scope="$1"
  local file="$2"
  case "$scope" in
    code)
      if is_documentation_path "$file"; then
        return 1
      fi
      return 0
      ;;
    test)
      case "$file" in
        scripts/ci-scope.sh|scripts/openresponses-compliance-scope.sh)
          return 1
          ;;
      esac
      if is_documentation_path "$file"; then
        return 1
      fi
      return 0
      ;;
    go)
      case "$file" in
        scripts/ci-scope.sh|scripts/openresponses-compliance-scope.sh)
          return 1
          ;;
      esac
      case "$file" in
        *.go|go.mod|go.sum|*/go.mod|*/go.sum|go.work|go.work.sum|Makefile|\
        .golangci.yml|.golangci.yaml|.goreleaser.yml|.goreleaser.yaml|\
        .github/workflows/*|\
        scripts/quality-checks.*|scripts/test-*|scripts/windows-task.*|\
        scripts/race-check.*|scripts/tidy-all-modules.*|scripts/check-all-modules.*)
        return 0
        ;;
        *) return 1 ;;
      esac
      ;;
    openresponses_coverage)
      case "$file" in
        internal/plugins/protocols/openresponses/**|\
        internal/refclient/openresponses/**|\
        internal/refbackend/openresponses/**|\
        internal/plugins/frontends/openresponses/**|\
        internal/plugins/backends/openresponsescompat/**|\
        pkg/lipapi/**|pkg/lipsdk/**|internal/core/**|\
        internal/stdhttp/**|internal/infra/runtimebundle/**|\
        internal/**|pkg/**|tools/coverage-gate/**|testdata/**|\
        .github/workflows/openresponses-coverage.yml|scripts/ci-scope.sh|Makefile|\
        go.mod|go.sum|*/go.mod|*/go.sum)
        return 0
        ;;
        *) return 1 ;;
      esac
      ;;
    *)
      echo "unknown scope: $scope" >&2
      return 2
      ;;
  esac
}

classify_diff() {
  local base="$1"
  local head="$2"
  local code=false
  local go=false
  local test=false
  local coverage=false
  local file diff_file

  # Non-PR events and manual dispatches have no base SHA. Run every scope
  # rather than risking a false bypass.
  if [[ -z "$base" ]]; then
    printf 'code=true\ngo=true\ntest=true\nopenresponses_coverage=true\n'
    return 0
  fi

  diff_file=$(mktemp)
  if ! git diff --name-only -z "$base" "$head" > "$diff_file"; then
    rm -f "$diff_file"
    echo "unable to classify changes between $base and $head" >&2
    return 1
  fi
  while IFS= read -r -d '' file; do
    file_matches code "$file" && code=true
    file_matches go "$file" && go=true
    file_matches test "$file" && test=true
    file_matches openresponses_coverage "$file" && coverage=true
  done < "$diff_file"
  rm -f "$diff_file"

  for value in "$code" "$go" "$test" "$coverage"; do
    case "$value" in
      true|false) ;;
      *) echo "invalid CI scope value: $value" >&2; return 1 ;;
    esac
  done
  printf 'code=%s\ngo=%s\ntest=%s\nopenresponses_coverage=%s\n' "$code" "$go" "$test" "$coverage"
}

self_test() {
  local relevant unrelated output tmp base head script_path

  for relevant in \
    internal/core/runtime.go \
    go.mod \
    connectors/example/go.mod \
    .github/workflows/ci.yml \
    scripts/quality-checks.sh; do
    file_matches code "$relevant" || { echo "code scope missed $relevant" >&2; return 1; }
    file_matches go "$relevant" || { echo "go scope missed $relevant" >&2; return 1; }
    file_matches test "$relevant" || { echo "test scope missed $relevant" >&2; return 1; }
  done
  for unrelated in docs/README.md README.md CHANGELOG.md; do
    file_matches go "$unrelated" && { echo "go scope included $unrelated" >&2; return 1; }
    file_matches test "$unrelated" && { echo "test scope included $unrelated" >&2; return 1; }
  done
  for relevant in .kiro/specs/example/requirements.md notes/README.md assets/example.txt; do
    file_matches test "$relevant" || { echo "test scope missed $relevant" >&2; return 1; }
  done
  for relevant in testdata/fixture.json scripts/helper.sh; do
    file_matches test "$relevant" || { echo "test scope missed $relevant" >&2; return 1; }
  done
  for safe_scope in scripts/ci-scope.sh scripts/openresponses-compliance-scope.sh; do
    file_matches test "$safe_scope" && { echo "safe scope script was classified as test-relevant: $safe_scope" >&2; return 1; }
  done
  for relevant in \
    internal/plugins/protocols/openresponses/wire.go \
    internal/refclient/openresponses/client.go \
    pkg/lipapi/request.go \
    internal/core/runtime.go \
    tools/coverage-gate/main.go; do
    file_matches openresponses_coverage "$relevant" || {
      echo "coverage scope missed $relevant" >&2
      return 1
    }
  done
  file_matches openresponses_coverage scripts/openresponses-compliance-scope.sh && {
    echo "coverage scope included unrelated matcher scripts/openresponses-compliance-scope.sh" >&2
    return 1
  }
  if classify_diff invalid-base HEAD >/dev/null 2>&1; then
    echo "invalid base revision did not fail closed" >&2
    return 1
  fi

  # Confirm NUL-delimited parsing does not split a newline-containing path.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  script_path="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  git -C "$tmp" init -q
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit --allow-empty -qm base
  base="$(git -C "$tmp" rev-parse HEAD)"
  relevant=$'internal/plugins/protocols/openresponses/scope\nfixture.go'
  mkdir -p "$(dirname "$tmp/$relevant")"
  printf 'fixture\n' > "$tmp/$relevant"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm relevant
  head="$(git -C "$tmp" rev-parse HEAD)"
  output="$(cd "$tmp" && bash "$script_path" --outputs "$base" "$head")"
  grep -qx 'openresponses_coverage=true' <<< "$output" || {
    echo "NUL-delimited coverage path was not detected" >&2
    return 1
  }
  rm -rf "$tmp"
  trap - RETURN

  echo 'OK: CI scope self-test'
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
  exit 0
fi

if [[ "${1:-}" != "--outputs" || $# -ne 3 ]]; then
  echo "usage: $0 --outputs BASE_SHA HEAD_SHA | --self-test" >&2
  exit 2
fi
classify_diff "$2" "$3"
