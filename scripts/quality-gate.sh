#!/usr/bin/env bash
# Pre-commit quality gate. The default is fast: build, vet, tests and lint of
# the staged packages only. CI (ci.yml "Go suite" and
# "Lint") runs the complete tagged suite and lint on every PR, so the hook does
# not repeat them. LIP_PRECOMMIT_FULL=1 or `make precommit-full` restores the
# complete local gate: whole root suite, certification, full lint, govulncheck.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/dev-cpu-defaults.sh"

echo "=== Pre-Commit Quality Gate ==="
echo ""

staged_files="$(git diff --cached --no-renames --name-only --diff-filter=ACMRD)"
if ! grep -qE '\.go$' <<< "$staged_files"; then
	if grep -qE '(^|/)(go\.mod|go\.sum)$' <<< "$staged_files"; then
		echo "No staged Go source files detected; checking module metadata."
	else
		# Spec bookkeeping can still break a repository invariant on its own:
		# marking a spec completed without archiving it fails the Kiro
		# lifecycle contract, and no Go test would notice. Always validate the
		# cheap spec/ownership contracts here rather than letting a
		# docs-only commit bypass every gate.
		if grep -qE '^\.kiro/specs/' <<< "$staged_files"; then
			echo "No staged Go files; validating Kiro spec and ownership contracts."
			echo ""
			go test -count=1 -tags=precommit ./internal/qa/ -run '^TestQAFastPreflight_Kiro'
			go test -count=1 ./tools/kiro/speccheck/
			exit 0
		fi
		echo "No staged Go files or module metadata detected; skipping quality gate checks."
		exit 0
	fi
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Fail fast on the one environmental precondition that makes the suites below
# report misleading failures. On a tmpfs TMPDIR the config-source integrity tests
# fail with 'source-integrity-failed' / 'source_non_atomic_update' across several
# unrelated packages, which reads as a regression in the staged change. Checking
# here costs milliseconds and names the real cause once.
bash "$SCRIPT_DIR/require-ext4-tmpdir.sh"

if grep -Eq '(^|/)(go\.mod|go\.sum)$' <<< "$staged_files"; then
	# Root dependencies can affect every independent module. Nested metadata
	# belongs to that module and is checked by the staged quality pass below.
	if [[ "${LIP_PRECOMMIT_FULL:-}" == "1" ]] || grep -Eq '^(go\.mod|go\.sum)$' <<< "$staged_files"; then
		echo "Checking all independent Go module metadata..."
		bash "$SCRIPT_DIR/tidy-all-modules.sh" --check
		echo ""
	fi
fi

if [[ "${LIP_PRECOMMIT_FULL:-}" != "1" ]]; then
	echo "Running quality checks on staged packages..."
	# Lint runs once, scoped, at the end of this gate.
	LIP_SKIP_LINT=1 LIP_SKIP_ARCHTEST=1 bash "$SCRIPT_DIR/quality-checks.sh" --staged

	echo ""
	# Test only the packages whose files are staged. Nearly every package
	# reaches the repository-wide suites (archtest, qa, runtime, runtimebundle)
	# through reverse dependencies, so a reverse-dependency scope costs about
	# as much as the full suite; CI's Go suite owns those consumers.
	declare -A test_packages=()
	while IFS= read -r file; do
		[[ "$file" == *.go && -f "$file" ]] || continue
		dir=$(dirname "$file")
		module=$dir
		while [[ "$module" != "." && ! -f "$module/go.mod" ]]; do
			module=$(dirname "$module")
		done
		if [[ "$module" == "." ]]; then
			package="./$dir"
		else
			package=".${dir#"$module"}"
		fi
		test_packages["$module"]+=" ${package%/.}"
	done <<< "$staged_files"
	if [[ ${#test_packages[@]} -eq 0 ]]; then
		echo "No staged Go packages to test."
	fi
	# Hook-local Git pins must not bind temporary-repository fixtures to the
	# caller, so the tests run in a subshell without them.
	(
		mapfile -t git_local_env < <(git rev-parse --local-env-vars)
		for name in "${git_local_env[@]}"; do
			unset "$name"
		done
		for module in "${!test_packages[@]}"; do
			packages=$(tr ' ' '\n' <<< "${test_packages[$module]}" | sed '/^$/d' | sort -u | tr '\n' ' ')
			echo "Testing staged packages in module $module (CI runs the complete suite): $packages"
			skip_test_args=()
			if [[ "$module" == "." && " $packages " == *" ./internal/infra/runtimebundle "* ]]; then
				skip_test_args=(-skip-test=TestRuntimebundle_NoCompleteOwnerCallbackEscapes)
				echo "Fast staged hook excludes TestRuntimebundle_NoCompleteOwnerCallbackEscapes; full precommit and dedicated CI still run the whole-module owner-callback gate."
			fi
			go run -buildvcs=false ./tools/devcheck -task=test -module="$module" -packages="$packages" "${skip_test_args[@]}"
		done
	)

	if [[ "$(go env GOOS)" == "linux" ]] && grep -qE '^(internal/infra/(configsource|runtimehost)/|scripts/(test-)?configsource-)' <<< "$staged_files"; then
		echo ""
		echo "Running ext4 source-lifetime certification (config-source change staged)..."
		bash "$SCRIPT_DIR/configsource-certify.sh"
		bash "$SCRIPT_DIR/test-configsource-fault-check.sh"
		bash "$SCRIPT_DIR/configsource-fault-check.sh"
	fi
else
	echo "Running quality checks..."
	# The following cached root test owns compilation, curated vet, and the
	# architecture package; avoid repeating those expensive Go phases in the hook.
	LIP_SKIP_GO_COMPILE_CHECKS=1 LIP_SKIP_ARCHTEST=1 bash "$SCRIPT_DIR/quality-checks.sh"

	echo ""
	echo "Running complete root test suite with precommit tags (Go cache enabled)..."
	env LIP_TEST_PRECOMMIT=1 bash "$SCRIPT_DIR/test-staged.sh"

	if [[ "$(go env GOOS)" == "linux" ]]; then
		echo ""
		echo "Running mandatory ext4 source-lifetime certification..."
		bash "$SCRIPT_DIR/configsource-certify.sh"
		echo ""
		echo "Running mandatory source-ownership fault lifecycle tests..."
		bash "$SCRIPT_DIR/test-configsource-fault-check.sh"
		bash "$SCRIPT_DIR/configsource-fault-check.sh"
	fi
fi

# The race detector is owned by remote CI: nightly race/fuzz
# (.github/workflows/race-fuzz-nightly.yml), the connector race workflows, and
# the release gate. Local commits do not run it, because a full -race scan of
# the billing, runtime, and runtimebundle suites takes hours on a developer
# machine and has repeatedly starved the rest of this gate of its time budget.
# Set LIP_PRECOMMIT_RACE=1 to opt a local commit back into the staged scan.
if [[ "${LIP_PRECOMMIT_RACE:-}" == "1" ]]; then
	echo ""
	echo "Running race detector scan..."
	bash "$SCRIPT_DIR/race-check.sh" --staged
else
	echo ""
	echo "Skipping race detector scan (remote CI owns it; LIP_PRECOMMIT_RACE=1 to opt in)."
fi

if [[ "${LIP_SKIP_LINT:-}" != "1" ]]; then
	echo ""
	if [[ "${LIP_PRECOMMIT_FULL:-}" == "1" ]]; then
		echo "Running complete multi-module linter across all modules (precommit-full)..."
		bash "$SCRIPT_DIR/lint-all-modules.sh"
	else
		# Staged packages only: consumers are linted by CI's Lint job.
		echo "Running multi-module linter on staged packages (CI lints their consumers)..."
		bash "$SCRIPT_DIR/lint-all-modules.sh" --staged --direct
	fi
else
	echo ""
	echo "Skipping linter (LIP_SKIP_LINT=1)."
fi

if [[ "${LIP_PRECOMMIT_FULL:-}" == "1" && "${LIP_SKIP_VULN:-}" != "1" ]]; then
	echo ""
	echo "Running govulncheck (precommit-full)..."
	if command -v govulncheck >/dev/null 2>&1; then
		govulncheck ./...
	else
		go tool govulncheck ./...
	fi
else
	echo ""
	echo "Skipping govulncheck in fast pre-commit mode (opt in: LIP_PRECOMMIT_FULL=1 or 'make precommit-full'; CI runs it anyway)."
fi

echo ""
echo "=== Quality Gate Passed ==="
exit 0
