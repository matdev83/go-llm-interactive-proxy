#!/usr/bin/env bash
# Regression checks for quality-checks.sh --staged. Fake tools capture the
# hook commands; a final local-only Go probe validates -o behavior.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
QUALITY_CHECKS_SOURCE="${QUALITY_CHECKS_SOURCE:-$SCRIPT_DIR/quality-checks.sh}"
REAL_GIT="$(command -v git)"
REAL_GO="$(command -v go)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/quality-checks-test.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/bin"
mkdir -p "$BIN"

fail() {
	echo "FAIL: $*" >&2
	if [[ -f "$TMP/output" ]]; then
		cat "$TMP/output" >&2
	fi
	exit 1
}

assert_contains() {
	local file="$1" needle="$2"
	grep -Fq -- "$needle" "$file" || fail "expected '$needle' in $file"
}

assert_not_contains() {
	local file="$1" needle="$2"
	if grep -Fq -- "$needle" "$file"; then
		fail "did not expect '$needle' in $file"
	fi
}

assert_count() {
	local file="$1" needle="$2" want="$3" got
	got=$(grep -Fc -- "$needle" "$file" || true)
	[[ "$got" == "$want" ]] || fail "expected $want matches for '$needle' in $file, got $got"
}

cat >"$BIN/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -euo pipefail
{
	printf 'PWD=%q GOWORK=%q GO' "$PWD" "${GOWORK:-unset}"
	for arg in "$@"; do printf ' %q' "$arg"; done
	printf '\n'
} >>"$FAKE_GO_LOG"
case "${GO_FAIL:-}:$*" in
	build:build\ *) exit 41 ;;
	vet:vet\ *) exit 42 ;;
	tidy:mod\ tidy\ *) exit 43 ;;
esac
if [[ "${1:-}" == list ]]; then
	shift
	if [[ "${1:-}" == -f ]]; then shift 2; fi
	module=$(awk '$1 == "module" { print $2; exit }' go.mod)
	for package in "$@"; do
		if [[ "$package" == "${FAKE_GO_LIST_EMPTY:-}" && -n "${FAKE_GO_LIST_EMPTY:-}" ]]; then
			printf '\n'
		elif [[ "$package" == "." ]]; then
			printf '%s\n' "$module"
		else
			printf '%s/%s\n' "$module" "${package#./}"
		fi
	done
	exit 0
fi
exit 0
FAKE_GO
chmod +x "$BIN/go"

cat >"$BIN/gofmt" <<'FAKE_GOFMT'
#!/usr/bin/env bash
set -euo pipefail
{
	printf 'PWD=%q GOFMT' "$PWD"
	for arg in "$@"; do printf ' %q' "$arg"; done
	printf '\n'
} >>"$FAKE_GOFMT_LOG"
if [[ "${GOFMT_FAIL:-}" == 1 ]]; then
	echo "fake gofmt failure" >&2
	exit 51
fi
if [[ -n "${GOFMT_UNFORMATTED:-}" ]]; then
	for arg in "$@"; do
		if [[ "$arg" == "$GOFMT_UNFORMATTED" ]]; then
			printf '%s\n' "$arg"
		fi
	done
fi
FAKE_GOFMT
chmod +x "$BIN/gofmt"

cat >"$BIN/buf" <<'FAKE_BUF'
#!/usr/bin/env bash
set -euo pipefail
{
	printf 'BUF'
	for arg in "$@"; do printf ' %q' "$arg"; done
	printf '\n'
} >>"$FAKE_BUF_LOG"
FAKE_BUF
chmod +x "$BIN/buf"

cat >"$BIN/git" <<'FAKE_GIT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${FAKE_GIT_FAIL_DIFF:-}" == 1 && "${1:-}" == diff && "${2:-}" == --cached ]]; then
	exit 61
fi
exec "$REAL_GIT" "$@"
FAKE_GIT
chmod +x "$BIN/git"

new_repo() {
	local name="$1" repo="$TMP/$1"
	mkdir -p "$repo/scripts" "$repo/internal/rootpkg" "$repo/internal/secondpkg" "$repo/internal/testonly" \
		"$repo/connectors/nested/pkg" "$repo/connectors/untouched/pkg" \
		"$repo/pkg/lipsdk/feature" "$repo/api" "$repo/.github/workflows"
	cp "$QUALITY_CHECKS_SOURCE" "$repo/scripts/quality-checks.sh"
	cp "$SCRIPT_DIR/dev-cpu-defaults.sh" "$repo/scripts/dev-cpu-defaults.sh"
	cat >"$repo/go.mod" <<'EOF'
module example.test/root

go 1.24
EOF
	cat >"$repo/connectors/nested/go.mod" <<'EOF'
module example.test/nested

go 1.24
EOF
	cat >"$repo/connectors/untouched/go.mod" <<'EOF'
module example.test/untouched

go 1.24
EOF
	cat >"$repo/internal/rootpkg/another.go" <<'EOF'
package rootpkg
EOF
	cat >"$repo/internal/rootpkg/file with space.go" <<'EOF'
package rootpkg
EOF
	cat >"$repo/internal/secondpkg/second.go" <<'EOF'
package secondpkg
EOF
	cat >"$repo/internal/testonly/only_test.go" <<'EOF'
package testonly
EOF
	cat >"$repo/connectors/nested/pkg/service.go" <<'EOF'
package service
EOF
	cat >"$repo/connectors/nested/pkg/keep.go" <<'EOF'
package service
EOF
	cat >"$repo/connectors/nested/pkg/delete.go" <<'EOF'
package service
EOF
	cat >"$repo/connectors/untouched/pkg/untouched.go" <<'EOF'
package untouched
EOF
	printf 'feature input\n' >"$repo/pkg/lipsdk/feature/base.txt"
	printf 'feature to delete\n' >"$repo/pkg/lipsdk/feature/delete.txt"
	printf 'syntax = "proto3";\n' >"$repo/api/base.proto"
	printf 'syntax = "proto3";\n' >"$repo/api/delete.proto"
	printf 'name: CI\n' >"$repo/.github/workflows/ci.yml"
	cat >"$repo/scripts/check-adhoc-goroutines.sh" <<'EOF'
#!/usr/bin/env bash
printf 'adhoc\n' >>"$FAKE_GUARD_LOG"
EOF
	cat >"$repo/scripts/regex-hotpath-check.sh" <<'EOF'
#!/usr/bin/env bash
printf 'regex\n' >>"$FAKE_GUARD_LOG"
EOF
	cat >"$repo/scripts/proto-check.sh" <<'EOF'
#!/usr/bin/env bash
printf 'proto\n' >>"$FAKE_GUARD_LOG"
buf lint
EOF
	cat >"$repo/scripts/lint-all-modules.sh" <<'EOF'
#!/usr/bin/env bash
printf 'lint\n' >>"$FAKE_GUARD_LOG"
EOF
	chmod +x "$repo/scripts/"*.sh
	git -C "$repo" init -q
	git -C "$repo" config user.email quality-checks-test@example.com
	git -C "$repo" config user.name quality-checks-test
	git -C "$repo" add -A
	git -C "$repo" commit -qm baseline
	REPO="$repo"
}

run_gate() {
	local repo="$1" mode="$2" go_fail="${3:-}" fmt_fail="${4:-}" fmt_unformatted="${5:-}" git_fail="${6:-}" subdir="${7:-}"
	local -a args=()
	[[ -z "$mode" ]] || args=("$mode")
	: >"$TMP/go.log"
	: >"$TMP/gofmt.log"
	: >"$TMP/buf.log"
	: >"$TMP/guards.log"
	(
		local script_path="$repo/scripts/quality-checks.sh"
		if [[ -n "$subdir" ]]; then
			cd "$repo/$subdir"
			script_path='../../scripts/quality-checks.sh'
		else
			cd "$repo"
		fi
		env PATH="$BIN:$PATH" REAL_GIT="$REAL_GIT" \
			FAKE_GO_LOG="$TMP/go.log" FAKE_GOFMT_LOG="$TMP/gofmt.log" \
			FAKE_BUF_LOG="$TMP/buf.log" FAKE_GUARD_LOG="$TMP/guards.log" \
			FAKE_GO_LIST_EMPTY="${FAKE_GO_LIST_EMPTY:-}" \
			GO_FAIL="$go_fail" GOFMT_FAIL="$fmt_fail" GOFMT_UNFORMATTED="$fmt_unformatted" \
			FAKE_GIT_FAIL_DIFF="$git_fail" LIP_SKIP_LINT=1 LIP_SKIP_ARCHTEST=1 \
			LIP_TEST_PARALLEL=1 LIP_VERIFY_MODULE_CACHE=0 CI=0 \
			bash "$script_path" "${args[@]}"
	) >"$TMP/output" 2>&1
}

expect_pass() {
	if ! run_gate "$@"; then fail "gate unexpectedly failed for $1"; fi
}

expect_fail() {
	if run_gate "$@"; then fail "gate unexpectedly passed for $1"; fi
}

assert_cheap_guards() {
	assert_contains "$TMP/guards.log" adhoc
	assert_contains "$TMP/guards.log" regex
}

# A mixed root and nested-module change checks direct package grouping, NUL-safe
# path handling, and isolation from an unstaged root file.
new_repo mixed-scope
printf 'package rootpkg\n' >"$REPO/internal/rootpkg/unstaged.go"
printf '// staged\n' >>"$REPO/internal/rootpkg/another.go"
printf '// staged\n' >>"$REPO/internal/rootpkg/file with space.go"
printf '// staged\n' >>"$REPO/internal/secondpkg/second.go"
printf '// staged\n' >>"$REPO/connectors/nested/pkg/service.go"
git -C "$REPO" add 'internal/rootpkg/another.go' 'internal/rootpkg/file with space.go' \
	'internal/secondpkg/second.go' 'connectors/nested/pkg/service.go'
expect_pass "$REPO" --staged
assert_contains "$TMP/gofmt.log" 'file\ with\ space.go'
assert_not_contains "$TMP/gofmt.log" unstaged.go
assert_count "$TMP/go.log" 'GO build ' 2
assert_count "$TMP/go.log" 'GO vet ' 2
assert_contains "$TMP/go.log" './internal/rootpkg ./internal/secondpkg'
assert_contains "$TMP/go.log" "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false -o "
assert_not_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false -o "
grep -Eq "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false -o [^[:space:]]+/build\\.[0-9]+ " "$TMP/go.log" || \
	fail "single-package build should write its archive to a scratch file"
assert_contains "$TMP/go.log" './pkg'
assert_not_contains "$TMP/go.log" './...'
assert_count "$TMP/go.log" 'GO mod tidy -diff' 2
assert_not_contains "$TMP/go.log" 'connectors/untouched'
assert_not_contains "$TMP/go.log" 'generate-feature-planes.go'
assert_not_contains "$TMP/buf.log" BUF
assert_cheap_guards

# Relative invocation from a package directory still finds its guard scripts.
new_repo relative-invocation
printf '// staged\n' >>"$REPO/internal/rootpkg/another.go"
git -C "$REPO" add internal/rootpkg/another.go
expect_pass "$REPO" --staged '' '' '' '' internal/rootpkg
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false -o "
grep -Eq "PWD=$REPO GOWORK=off GO build -buildvcs=false -o [^[:space:]]+/build\\.[0-9]+ " "$TMP/go.log" || \
	fail "single-package build should write its archive to a scratch file"

# Test-only packages remain in vet and later test scope; build skips packages
# with no production files.
new_repo test-only-package
printf '// staged\n' >>"$REPO/internal/testonly/only_test.go"
git -C "$REPO" add internal/testonly/only_test.go
FAKE_GO_LIST_EMPTY=./internal/testonly expect_pass "$REPO" --staged
unset FAKE_GO_LIST_EMPTY
assert_contains "$TMP/go.log" 'GO list -f'
assert_not_contains "$TMP/go.log" 'GO build '
assert_contains "$TMP/go.log" 'GO vet ./internal/testonly'

# A deleted Go file still checks its package while a sibling source file remains.
new_repo deleted-file
git -C "$REPO" rm -q connectors/nested/pkg/delete.go
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false -o "
grep -Eq "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false -o [^[:space:]]+/build\\.[0-9]+ " "$TMP/go.log" || \
	fail "single-package build should write its archive to a scratch file"
assert_contains "$TMP/go.log" './pkg'
assert_contains "$TMP/go.log" 'GO vet ./pkg'
assert_contains "$TMP/go.log" 'GO mod tidy -diff'
assert_not_contains "$TMP/gofmt.log" delete.go

# A removed module contributes no packages. Surviving sources widen checks to
# their parent module, including when go.mod was renamed away.
new_repo removed-module
git -C "$REPO" rm -qr connectors/nested
expect_pass "$REPO" --staged
assert_not_contains "$TMP/go.log" 'connectors/nested'
assert_not_contains "$TMP/go.log" 'GO build '
assert_not_contains "$TMP/go.log" 'GO mod tidy'

new_repo removed-module-metadata-only
git -C "$REPO" rm -q connectors/nested/go.mod
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO mod tidy -diff"
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false ./..."
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO vet ./..."
assert_count "$TMP/go.log" 'GO mod tidy -diff' 1
expect_fail "$REPO" --staged build
assert_contains "$TMP/output" 'build failed in staged module .'

new_repo renamed-module-metadata
git -C "$REPO" mv connectors/nested/go.mod connectors/nested/old.go.mod
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO mod tidy -diff"
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false ./..."
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO vet ./..."

new_repo removed-root-module
git -C "$REPO" rm -q go.mod
expect_fail "$REPO" --staged
assert_contains "$TMP/output" 'Go source has no surviving parent module'

# Metadata-only commits check all packages in the affected module. Root metadata
# retains root build/vet safety and triggers both global contract generators.
new_repo nested-metadata
printf '\n// changed metadata\n' >>"$REPO/connectors/nested/go.mod"
git -C "$REPO" add connectors/nested/go.mod
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false ./..."
assert_contains "$TMP/go.log" 'GO vet ./...'
assert_contains "$TMP/go.log" 'GO mod tidy -diff'
assert_not_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build"
assert_not_contains "$TMP/buf.log" BUF

new_repo root-metadata
printf '\n// changed metadata\n' >>"$REPO/go.mod"
git -C "$REPO" add go.mod
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false ./..."
assert_contains "$TMP/go.log" 'GO vet ./...'
assert_contains "$TMP/go.log" 'GO mod tidy -diff'
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_contains "$TMP/buf.log" 'BUF lint'

# Feature-plane and protobuf checks run for changed inputs, including deletions.
new_repo feature-add
printf 'added feature input\n' >"$REPO/pkg/lipsdk/feature/added.txt"
git -C "$REPO" add pkg/lipsdk/feature/added.txt
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_not_contains "$TMP/buf.log" BUF

new_repo feature-delete
git -C "$REPO" rm -q pkg/lipsdk/feature/delete.txt
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'

new_repo proto-add
printf 'syntax = "proto3";\n' >"$REPO/api/added.proto"
git -C "$REPO" add api/added.proto
expect_pass "$REPO" --staged
assert_contains "$TMP/buf.log" 'BUF lint'
assert_not_contains "$TMP/go.log" 'generate-feature-planes.go'

new_repo proto-delete
git -C "$REPO" rm -q api/delete.proto
expect_pass "$REPO" --staged
assert_contains "$TMP/buf.log" 'BUF lint'

new_repo ci-policy
printf 'name: changed CI\n' >"$REPO/.github/workflows/ci.yml"
git -C "$REPO" add .github/workflows/ci.yml
expect_pass "$REPO" --staged
assert_contains "$TMP/buf.log" 'BUF lint'

new_repo script-policy
printf '\n# staged policy trigger\n' >>"$REPO/scripts/quality-checks.sh"
git -C "$REPO" add scripts/quality-checks.sh
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_contains "$TMP/buf.log" 'BUF lint'

# Empty staged scope does not widen to the root module, while the two cheap
# source guardrails continue to run.
new_repo empty-scope
expect_pass "$REPO" --staged
assert_not_contains "$TMP/gofmt.log" GOFMT
assert_not_contains "$TMP/go.log" 'GO build '
assert_not_contains "$TMP/go.log" 'GO vet '
assert_not_contains "$TMP/go.log" 'GO mod tidy'
assert_not_contains "$TMP/go.log" 'generate-feature-planes.go'
assert_not_contains "$TMP/buf.log" BUF
assert_cheap_guards

# Formatting, build, vet, tidy, and Git-scope failures must fail the staged gate.
new_repo failures
printf '// staged\n' >>"$REPO/internal/rootpkg/another.go"
git -C "$REPO" add internal/rootpkg/another.go
expect_fail "$REPO" --staged '' 1
assert_contains "$TMP/output" 'gofmt failed on staged Go files'
expect_fail "$REPO" --staged '' '' 'internal/rootpkg/another.go'
assert_contains "$TMP/output" 'Unformatted files:'
expect_fail "$REPO" --staged build
assert_contains "$TMP/output" 'build failed in staged module'
expect_fail "$REPO" --staged vet
assert_contains "$TMP/output" 'vet failed in staged module'
expect_fail "$REPO" --staged tidy
assert_contains "$TMP/output" 'go.mod/go.sum drift detected'
expect_fail "$REPO" --staged '' '' '' 1
assert_contains "$TMP/output" 'unable to read staged paths from Git'

# No-argument invocation retains the comprehensive whole-tree checks.
new_repo standalone
expect_pass "$REPO" ""
assert_contains "$TMP/gofmt.log" 'GOFMT -l .'
assert_contains "$TMP/go.log" 'GO mod tidy'
assert_contains "$TMP/go.log" 'GO build ./...'
assert_contains "$TMP/go.log" 'GO vet ./...'
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_contains "$TMP/buf.log" 'BUF lint'

# Verify the exact Go build semantics used by staged scope: a single non-main
# package accepts -o to a scratch file, while multiple packages build without
# -o and therefore create no executables in the module. GOPROXY=off keeps this
# probe independent of network access.
PROBE="$TMP/real-go-build-probe"
mkdir -p "$PROBE/first" "$PROBE/second"
cat >"$PROBE/go.mod" <<'EOF'
module example.test/build-probe

go 1.24
EOF
printf 'package first\nconst Value = 1\n' >"$PROBE/first/first.go"
printf 'package second\nconst Value = 2\n' >"$PROBE/second/second.go"
(
	cd "$PROBE"
	GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off \
		"$REAL_GO" build -buildvcs=false -o "$PROBE/single-package.a" ./first
	test -f "$PROBE/single-package.a"
	GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off \
		"$REAL_GO" build -buildvcs=false ./first ./second
)

echo 'OK: staged quality-check behavior and standalone compatibility'
