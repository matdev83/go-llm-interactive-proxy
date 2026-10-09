#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OWNER_TEST=TestRuntimebundle_NoCompleteOwnerCallbackEscapes
tmp="$(mktemp -d)"
trap 'rm -rf -- "$tmp"' EXIT

mapfile -t git_local_env < <(git -C "$SCRIPT_DIR/.." rev-parse --local-env-vars)
for name in "${git_local_env[@]}"; do
	unset "$name"
done
SHARED_SCOPE_BINARY="$tmp/localscope"
export SHARED_SCOPE_BINARY
(cd "$SCRIPT_DIR/.." && go build -o "$SHARED_SCOPE_BINARY" ./tools/localscope)

run_case() {
	local case_name="$1" path="$2" module_dir="$3" want_skip="$4" precommit_full="${5:-}"
	local repo="$tmp/$case_name" fakebin="$tmp/$case_name-bin" capture="$tmp/$case_name-go.log" log="$tmp/$case_name-gate.log"
	mkdir -p "$repo/scripts" "$fakebin" "$repo/$(dirname "$path")"
	printf 'module example.test/root\n\ngo 1.26.9\n' >"$repo/go.mod"
	if [[ "$module_dir" != "." ]]; then
		mkdir -p "$repo/$module_dir"
		printf 'module example.test/%s\n\ngo 1.25.0\n' "$case_name" >"$repo/$module_dir/go.mod"
	fi
	printf 'package fixture\n' >"$repo/$path"
	git -C "$repo" -c init.defaultBranch=main init -q
	git -C "$repo" add -- "$path"
	cp "$SCRIPT_DIR/quality-gate.sh" "$repo/scripts/quality-gate.sh"
	for helper in dev-cpu-defaults.sh require-ext4-tmpdir.sh quality-checks.sh lint-all-modules.sh test-staged.sh race-check.sh configsource-certify.sh test-configsource-fault-check.sh configsource-fault-check.sh; do
		case "$helper" in
			dev-cpu-defaults.sh)
				printf '#!/usr/bin/env bash\n:\n' >"$repo/scripts/$helper"
				;;
			test-staged.sh)
				cat >"$repo/scripts/$helper" <<'EOF'
#!/usr/bin/env bash
printf 'test-staged\n' >>"$GO_CAPTURE"
EOF
				;;
			*)
				printf '#!/usr/bin/env bash\nexit 0\n' >"$repo/scripts/$helper"
				;;
		esac
	done
	cat >"$fakebin/go" <<'EOF'
#!/usr/bin/env bash
printf '%s\t' "$@" >>"$GO_CAPTURE"
printf '\n' >>"$GO_CAPTURE"
if [[ " $* " == *" ./tools/localscope "* ]]; then
  shift 3
  exec "$SHARED_SCOPE_BINARY" "$@"
fi
if [[ "${1:-}" == "env" && "${2:-}" == "GOOS" ]]; then
	printf 'linux\n'
fi
EOF
	cat >"$fakebin/govulncheck" <<'EOF'
#!/usr/bin/env bash
printf 'govulncheck\n' >>"$GO_CAPTURE"
EOF
	chmod +x "$fakebin/go" "$fakebin/govulncheck"
	: >"$capture"
	if ! (
		cd "$repo"
		export PATH="$fakebin:$PATH" GO_CAPTURE="$capture" LIP_PRECOMMIT_FULL="$precommit_full"
		bash scripts/quality-gate.sh
	) >"$log" 2>&1; then
		cat "$log" >&2
		return 1
	fi
	if [[ "$precommit_full" == "1" ]]; then
		if grep -Fq -- "-skip-test=$OWNER_TEST" "$capture" || ! grep -Fxq -- "test-staged" "$capture"; then
			printf 'full precommit did not retain its whole-suite test path in %s\n' "$case_name" >&2
			cat "$capture" >&2
			cat "$log" >&2
			return 1
		fi
		return 0
	fi
	if [[ "$want_skip" == "yes" ]]; then
		if ! grep -Fq -- "-skip-test=$OWNER_TEST" "$capture" || ! grep -Fq -- "$OWNER_TEST; full precommit and dedicated CI" "$log"; then
			printf 'owner package did not receive the explicit skip in %s\n' "$case_name" >&2
			cat "$capture" >&2
			cat "$log" >&2
			return 1
		fi
	elif grep -Fq -- "-skip-test=$OWNER_TEST" "$capture" || grep -Fq -- "Fast staged hook excludes $OWNER_TEST" "$log"; then
		printf 'unexpected owner skip in %s\n' "$case_name" >&2
		cat "$capture" >&2
		cat "$log" >&2
		return 1
	fi
}

run_case root-owner internal/infra/runtimebundle/owner_test.go . yes
run_case root-other internal/infra/other/other_test.go . no
run_case nested-owner connectors/local/internal/infra/runtimebundle/owner_test.go connectors/local no
run_case full-owner internal/infra/runtimebundle/owner_test.go . no 1
printf 'quality-gate fast owner-skip scope tests passed\n'
