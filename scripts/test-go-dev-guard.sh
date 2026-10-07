#!/usr/bin/env bash
# All delegated commands use a fake toolchain, including simulated CI race runs.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/bin" "$fixture/config/go"
cat > "$fixture/bin/hostname" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$GUARD_TEST_HOST"
STUB
cat > "$fixture/bin/real-go" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$GUARD_TEST_CALLS"
printf 'fake toolchain output\n'
exit "${GUARD_TEST_EXIT:-0}"
STUB
chmod +x "$fixture/bin/hostname" "$fixture/bin/real-go"
export PATH="$fixture/bin:$PATH"
export GO_DEV_GUARD_REAL_GO="$fixture/bin/real-go"
export GUARD_TEST_CALLS="$fixture/calls"
export GUARD_TEST_HOST=agent-dev HOSTNAME=agent-dev
export XDG_CONFIG_HOME="$fixture/config"
unset GOFLAGS GOENV COMPUTERNAME CI GITHUB_ACTIONS LIP_ALLOW_RACE_ON_DEV

blocked() {
	rm -f "$GUARD_TEST_CALLS"
	local status=0
	bash "$script_dir/go-dev-guard.sh" "$@" > "$fixture/out" 2> "$fixture/err" || status=$?
	if [[ "$status" != 2 || -e "$GUARD_TEST_CALLS" ]]; then
		echo "FAIL: blocked invocation reached toolchain or returned status $status: $*" >&2
		exit 1
	fi
	grep -q 'GitHub CI' "$fixture/err"
}

allowed() {
	rm -f "$GUARD_TEST_CALLS"
	bash "$script_dir/go-dev-guard.sh" "$@" > "$fixture/out" 2> "$fixture/err"
	[[ -e "$GUARD_TEST_CALLS" && ! -s "$fixture/err" ]]
	grep -qx 'fake toolchain output' "$fixture/out"
	printf '%s\n' "$@" > "$fixture/expected"
	cmp "$fixture/expected" "$GUARD_TEST_CALLS"
}

blocked test -race ./...
blocked test ./... -race=true
blocked -C module test --race=1 ./...
blocked build -race ./cmd/app
blocked run -race main.go
blocked install -race ./cmd/app
blocked tool compile -race input.go
GOFLAGS='-p=2 -race' blocked test ./...
GOFLAGS="'-race=true' -p=2" blocked test ./...
GOFLAGS=$'-p=2\n-race' blocked test ./...
printf 'GOFLAGS=-race\n' > "$fixture/config/go/env"
blocked test ./...
GOFLAGS=-p=2 allowed test ./...
GOENV=off allowed test ./...
GOENV="$fixture/config/go/env" blocked test ./...
allowed env -u GOFLAGS
rm "$fixture/config/go/env"
CI=true GITHUB_ACTIONS=true LIP_ALLOW_RACE_ON_DEV=1 blocked test -race ./...
GUARD_TEST_HOST=ci-runner HOSTNAME=ci-runner COMPUTERNAME=AGENT-DEV.example blocked test -race ./...
GUARD_TEST_HOST=Desktop-I2caj6v.example HOSTNAME=ci-runner blocked test -race ./...
GUARD_TEST_HOST=ci-runner HOSTNAME=ci-runner allowed test -race ./...
allowed version
allowed test -race=false ./...
allowed test ./... -args -race
allowed run ./cmd/app -race
allowed test -run Contains-race ./...
allowed test './path with spaces/...'
status=0
GUARD_TEST_EXIT=17 bash "$script_dir/go-dev-guard.sh" test ./... > /dev/null || status=$?
[[ "$status" == 17 ]]
echo 'PASS: Go development race guard blocks before toolchain execution; normal/CI delegation preserved.'
