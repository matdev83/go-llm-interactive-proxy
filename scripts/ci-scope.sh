#!/usr/bin/env bash
# Emit changed-file scope for CI jobs without skipping required workflow jobs.
# Usage: ci-scope.sh --outputs BASE_SHA HEAD_SHA
#        ci-scope.sh --self-test
set -euo pipefail

is_kiro_spec_path() {
  case "$1" in
    .kiro/specs/*) return 0 ;;
    *) return 1 ;;
  esac
}

is_documentation_path() {
  case "$1" in
    # Markdown is documentation regardless of where it lives. A Markdown-only
    # PR must never trigger Go/runtime/test analysis merely because the file is
    # outside docs/.
    *.md|*.markdown)
      return 0
      ;;
    docs/*)
      case "$1" in
        *.txt|*.rst|*.adoc|*.png|*.jpg|*.jpeg|*.gif|*.svg|*.drawio)
          return 0
          ;;
        *)
          return 1
          ;;
      esac
      ;;
    .kiro/specs/*)
      # Kiro SDD metadata/visuals are specification inputs, not runtime/test
      # inputs. Keep executable or otherwise unexpected files fail-closed so
      # the spec tree can never become a generic CI bypass bucket.
      case "$1" in
        *.json|*.txt|*.rst|*.adoc|*.png|*.jpg|*.jpeg|*.gif|*.svg|*.drawio)
          return 0
          ;;
        *)
          return 1
          ;;
      esac
      ;;
    LICENSE|LICENSE.*)
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
    kiro)
      is_kiro_spec_path "$file"
      ;;
    go)
      if is_documentation_path "$file"; then
        return 1
      fi
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
      if is_documentation_path "$file"; then
        return 1
      fi
      case "$file" in
        internal/**|pkg/**|tools/coverage-gate/**|testdata/**|\
        .github/workflows/openresponses-coverage.yml|scripts/ci-scope.sh|Makefile|\
        go.mod|go.sum|*/go.mod|*/go.sum)
        return 0
        ;;
        *) return 1 ;;
      esac
      ;;
    test_cost)
      if is_documentation_path "$file"; then
        return 1
      fi
      case "$file" in
        scripts/test-cost-*|tools/testcost/**|internal/qa/test_cost_policy_test.go)
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
  local kiro=false
  local coverage=false
  local test_cost=false
  local file diff_file

  # Non-PR events and manual dispatches have no base SHA. Run every scope
  # rather than risking a false bypass.
  if [[ -z "$base" ]]; then
    printf 'code=true\ngo=true\ntest=true\nkiro=true\nopenresponses_coverage=true\ntest_cost=true\n'
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
    file_matches kiro "$file" && kiro=true
    file_matches openresponses_coverage "$file" && coverage=true
    file_matches test_cost "$file" && test_cost=true
  done < "$diff_file"
  rm -f "$diff_file"

  for value in "$code" "$go" "$test" "$kiro" "$coverage" "$test_cost"; do
    case "$value" in
      true|false) ;;
      *) echo "invalid CI scope value: $value" >&2; return 1 ;;
    esac
  done
  printf 'code=%s\ngo=%s\ntest=%s\nkiro=%s\nopenresponses_coverage=%s\ntest_cost=%s\n' "$code" "$go" "$test" "$kiro" "$coverage" "$test_cost"
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

  # Markdown is documentation everywhere, including paths that otherwise map
  # to code/coverage scopes. Kiro JSON is also a non-runtime spec artifact.
  for unrelated in \
    docs/README.md \
    README.md \
    CHANGELOG.md \
    notes/README.md \
    internal/design.md \
    pkg/lipapi/README.md \
    .github/workflows/README.md \
    testdata/fixture.md \
    .kiro/specs/example/requirements.md \
    .kiro/specs/example/spec.json; do
    file_matches code "$unrelated" && { echo "code scope included $unrelated" >&2; return 1; }
    file_matches go "$unrelated" && { echo "go scope included $unrelated" >&2; return 1; }
    file_matches test "$unrelated" && { echo "test scope included $unrelated" >&2; return 1; }
    file_matches openresponses_coverage "$unrelated" && { echo "coverage scope included $unrelated" >&2; return 1; }
    file_matches test_cost "$unrelated" && { echo "test-cost scope included $unrelated" >&2; return 1; }
  done

  # Non-Markdown artifacts outside explicitly documentation-only locations stay
  # fail-closed because configs/fixtures can affect runtime or test semantics.
  for relevant in docs/backend-plugins/docs_test.go assets/example.txt testdata/fixture.json scripts/helper.sh; do
    file_matches test "$relevant" || { echo "test scope missed $relevant" >&2; return 1; }
  done

  for relevant in \
    .kiro/specs/example/requirements.md \
    .kiro/specs/example/spec.json \
    .kiro/specs/archive/example/tasks.md; do
    file_matches kiro "$relevant" || { echo "kiro scope missed $relevant" >&2; return 1; }
  done
  for unrelated in docs/README.md internal/core/runtime.go; do
    file_matches kiro "$unrelated" && { echo "kiro scope included $unrelated" >&2; return 1; }
  done

  # Unexpected executable content beneath .kiro/specs must still trigger the
  # ordinary code/test path in addition to the Kiro policy scope.
  relevant=.kiro/specs/example/check.sh
  file_matches code "$relevant" || { echo "code scope missed executable Kiro artifact $relevant" >&2; return 1; }
  file_matches test "$relevant" || { echo "test scope missed executable Kiro artifact $relevant" >&2; return 1; }
  file_matches kiro "$relevant" || { echo "kiro scope missed executable Kiro artifact $relevant" >&2; return 1; }

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
  for relevant in \
    scripts/test-cost-ratchet.ps1 \
    scripts/test-cost-budget.json \
    tools/testcost/measure.go \
    internal/qa/test_cost_policy_test.go; do
    file_matches test_cost "$relevant" || {
      echo "test_cost scope missed $relevant" >&2
      return 1
    }
  done
  for unrelated in docs/README.md internal/core/runtime.go; do
    file_matches test_cost "$unrelated" && {
      echo "test_cost scope included $unrelated" >&2
      return 1
    }
  done
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
