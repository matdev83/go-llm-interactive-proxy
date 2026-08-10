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
  local path tmp script_path base head special result
  for path in \
    internal/integration/openresponses/handler.go \
    internal/archtest/openresponses_js_tool_boundary_test.go \
    internal/plugins/frontends/openresponses/handler.go \
    internal/plugins/frontends/openairesponses/handler.go \
    internal/plugins/backends/openresponsescompat/handler.go \
    internal/plugins/backends/openairesponses/handler.go \
    internal/plugins/protocols/openresponses/wire.go \
    internal/plugins/protocols/openairesponsesitem/wire.go \
    internal/plugins/protocols/openairesponsestream/wire.go \
    internal/refclient/openresponses/client.go \
    internal/refclient/openairesponses/client.go \
    internal/refbackend/openresponses/server.go \
    internal/refbackend/openairesponses/server.go \
    internal/testkit/openresponses/fixture.go \
    internal/testkit/conformance/matrix.go \
    internal/core/runtime.go \
    internal/stdhttp/server.go \
    internal/infra/runtimebundle/build.go \
    pkg/lipapi/request.go \
    pkg/lipsdk/registration.go \
    tools/openresponses-compliance/src/index.ts \
    scripts/test-openresponses-compliance.sh \
    scripts/openresponses-compliance-scope.sh \
    .github/workflows/openresponses-official-compliance.yml \
    Makefile \
    go.mod \
    go.sum \
    connectors/example/go.mod; do
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
  # Exercise NUL-delimited diff parsing with a filename that contains a newline.
  # Plain line-delimited parsing would split this path and could bypass the suite.
  tmp=$(mktemp -d)
  trap - EXIT
  script_path="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  git -C "$tmp" init -q
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA add -A
  printf 'base\n' > "$tmp/base.txt"
  git -C "$tmp" add base.txt
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm base
  base="$(git -C "$tmp" rev-parse HEAD)"
  special=$'internal/core/scope\nfixture.go'
  mkdir -p "$(dirname "$tmp/$special")"
  printf 'fixture\n' > "$tmp/$special"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm relevant
  head="$(git -C "$tmp" rev-parse HEAD)"
  result="$(cd "$tmp" && bash "$script_path" "$base" "$head")"
  if [[ "$result" != true ]]; then
    rm -rf "$tmp"
    echo "scope self-test: NUL-delimited special-character path was not detected" >&2
    return 1
  fi
  rm -rf "$tmp"

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

while IFS= read -r -d '' file; do
  if file_requires_suite "$file"; then
    printf 'true\n'
    exit 0
  fi
done < <(git diff --name-only -z "$BASE" "$HEAD")
printf 'false\n'
