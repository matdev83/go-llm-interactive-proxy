#!/usr/bin/env bash
# Print true when a diff touches a surface covered by the official
# OpenResponses compliance suite; print false for unrelated changes.
set -euo pipefail

file_requires_suite() {
  local file="$1"
  case "$file" in
    go.mod|go.sum|*/go.mod|*/go.sum|\
    internal/integration/openresponses/**|\
    internal/archtest/**|\
    internal/plugins/frontends/openresponses/**|\
    internal/plugins/frontends/openairesponses/**|\
    internal/plugins/backends/openresponsescompat/**|\
    internal/plugins/backends/openairesponses/**|\
    internal/plugins/protocols/openresponses/**|\
    internal/plugins/protocols/openairesponsesitem/**|\
    internal/plugins/protocols/openairesponsestream/**|\
    internal/refclient/openresponses/**|\
    internal/refclient/openairesponses/**|\
    internal/refbackend/openresponses/**|\
    internal/refbackend/openairesponses/**|\
    internal/testkit/openresponses/**|\
    internal/testkit/conformance/**|\
    internal/core/**|\
    internal/stdhttp/**|\
    internal/infra/runtimebundle/**|\
    pkg/lipapi/**|\
    pkg/lipsdk/**|\
    tools/openresponses-compliance/**|\
    scripts/test-openresponses-compliance.*|\
    scripts/openresponses-compliance-scope.sh|\
    .github/workflows/openresponses-official-compliance.yml|\
    Makefile)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

self_test() {
  local path
  for path in \
    internal/plugins/frontends/openairesponses/handler.go \
    internal/plugins/protocols/openairesponsesitem/wire.go \
    internal/archtest/openresponses_js_tool_boundary_test.go \
    internal/testkit/conformance/matrix.go \
    tools/openresponses-compliance/src/index.ts \
    go.mod \
    .github/workflows/openresponses-official-compliance.yml \
    scripts/openresponses-compliance-scope.sh \
    Makefile; do
    file_requires_suite "$path" || {
      echo "scope self-test: expected relevant path to run suite: $path" >&2
      return 1
    }
  done
  for path in docs/README.md internal/infra/endpoint/parser.go .github/workflows/ci.yml; do
    if file_requires_suite "$path"; then
      echo "scope self-test: expected unrelated path to bypass suite: $path" >&2
      return 1
    fi
  done
  echo "OK: OpenResponses scope matcher self-test"
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
  exit 0
fi

BASE=${1:-}
HEAD=${2:-HEAD}
if [[ -z "$BASE" ]]; then
  printf 'true\n'
  exit 0
fi

while IFS= read -r file; do
  if file_requires_suite "$file"; then
    printf 'true\n'
    exit 0
  fi
done < <(git diff --name-only "$BASE" "$HEAD")
printf 'false\n'
