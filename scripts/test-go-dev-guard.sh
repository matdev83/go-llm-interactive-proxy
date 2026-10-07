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
resource_nice=
if [[ $(uname -s) == Linux ]]; then resource_nice=$(ps -o ni= -p "$$" | tr -d ' '); fi
printf '%s\n' "${GOMAXPROCS:-}" "${GOFLAGS:-}" "$resource_nice" > "$GUARD_TEST_RESOURCES"
printf '%s\n' "${TMPDIR:-}" > "$GUARD_TEST_TMPDIR"
printf 'fake toolchain output\n'
exit "${GUARD_TEST_EXIT:-0}"
STUB
# Filesystem type is faked so the result does not depend on the runner's /tmp.
cat > "$fixture/bin/stat" <<'STUB'
#!/usr/bin/env bash
case "${!#}" in "$GUARD_TEST_EXT4"*) printf 'ext2/ext3\n' ;; *) printf 'tmpfs\n' ;; esac
STUB
chmod +x "$fixture/bin/hostname" "$fixture/bin/real-go" "$fixture/bin/stat"
export PATH="$fixture/bin:$PATH"
export GO_DEV_GUARD_REAL_GO="$fixture/bin/real-go"
export GUARD_TEST_CALLS="$fixture/calls"
export GUARD_TEST_RESOURCES="$fixture/resources"
export GUARD_TEST_TMPDIR="$fixture/tmpdir"
export GUARD_TEST_EXT4="$fixture/ext4"
export LIP_DEV_TMPDIR="$GUARD_TEST_EXT4/lip-tmp"
export GUARD_TEST_HOST=agent-dev HOSTNAME=agent-dev
export XDG_CONFIG_HOME="$fixture/config"
unset GOFLAGS GOENV GOMAXPROCS COMPUTERNAME CI GITHUB_ACTIONS LIP_ALLOW_RACE_ON_DEV

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
# Observe the delegated fake process, independently of installed Go wrappers.
if [[ $(uname -s) == Linux ]]; then
	inherited_nice=$(ps -o ni= -p "$$" | tr -d ' ')
	expected_nice=$(( inherited_nice > 10 ? inherited_nice : 10 ))
	retained_nice=$(( inherited_nice > 15 ? inherited_nice : 15 ))
	allowed test ./...
	printf '2\n-p=1\n%s\n' "$expected_nice" > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$GUARD_TEST_RESOURCES"
	GOFLAGS='-p=3 -tags="two words"' GOMAXPROCS=4 allowed test ./...
	printf '4\n-p=3 -tags="two words"\n%s\n' "$expected_nice" > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$GUARD_TEST_RESOURCES"
	printf 'GOFLAGS=-tags="two words"\n' > "$fixture/config/go/env"
	allowed test ./...
	printf '2\n-tags="two words" -p=1\n%s\n' "$expected_nice" > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$GUARD_TEST_RESOURCES"
	rm "$fixture/config/go/env"
	CI=true allowed test ./...
	[[ $(sed -n '1p' "$GUARD_TEST_RESOURCES") == '' && $(sed -n '2p' "$GUARD_TEST_RESOURCES") == '' ]]
	OS=Windows_NT allowed test ./...
	[[ $(sed -n '1p' "$GUARD_TEST_RESOURCES") == '' && $(sed -n '2p' "$GUARD_TEST_RESOURCES") == '' ]]
	bash -c 'renice --priority "$2" --pid "$$" >/dev/null; exec bash "$1" test ./...' _ "$script_dir/go-dev-guard.sh" "$retained_nice" >/dev/null
	[[ $(sed -n '3p' "$GUARD_TEST_RESOURCES") == "$retained_nice" ]]
	POSIXLY_CORRECT=1 allowed test ./...
	[[ $(sed -n '3p' "$GUARD_TEST_RESOURCES") == "$expected_nice" ]]
	# Bash gate defaults reach all children, including non-Go linters.
	unset LIP_TEST_PACKAGES LIP_TEST_PARALLEL
	bash -c 'source "$1/dev-cpu-defaults.sh"; printf "%s\n" "$GOMAXPROCS" "$LIP_TEST_PACKAGES" "$LIP_TEST_PARALLEL" "$(ps -o ni= -p "$$" | tr -d " ")"' _ "$script_dir" > "$fixture/gate-resources"
	printf '2\n1\n2\n%s\n' "$expected_nice" > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$fixture/gate-resources"
	GOMAXPROCS=4 LIP_TEST_PACKAGES=3 LIP_TEST_PARALLEL=5 bash -c 'source "$1/dev-cpu-defaults.sh"; printf "%s\n" "$GOMAXPROCS" "$LIP_TEST_PACKAGES" "$LIP_TEST_PARALLEL"' _ "$script_dir" > "$fixture/gate-resources"
	printf '4\n3\n5\n' > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$fixture/gate-resources"
	# A tmpfs or unset TMPDIR moves to ext4; an ext4 TMPDIR is kept; CI is untouched.
	env -u TMPDIR bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$LIP_DEV_TMPDIR" && -d $LIP_DEV_TMPDIR ]]
	TMPDIR="$fixture/ram" bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$LIP_DEV_TMPDIR" ]]
	mkdir -p "$GUARD_TEST_EXT4/own"
	TMPDIR="$GUARD_TEST_EXT4/own" bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$GUARD_TEST_EXT4/own" ]]
	TMPDIR="$fixture/ram" CI=true bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$fixture/ram" ]]
	TMPDIR="$fixture/ram" bash -c 'source "$1/dev-cpu-defaults.sh"; printf "%s\n" "$TMPDIR"' _ "$script_dir" > "$fixture/gate-tmpdir"
	[[ $(cat "$fixture/gate-tmpdir") == "$LIP_DEV_TMPDIR" ]]
	# Orphaned work dirs older than six hours are pruned at most hourly; live ones stay.
	mkdir -p "$LIP_DEV_TMPDIR/go-build-old" "$LIP_DEV_TMPDIR/go-build-live" "$LIP_DEV_TMPDIR/keep-old"
	touch -d '7 hours ago' "$LIP_DEV_TMPDIR/go-build-old" "$LIP_DEV_TMPDIR/keep-old"
	rm -f "$LIP_DEV_TMPDIR/.go-build-pruned"
	env -u TMPDIR bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ ! -e $LIP_DEV_TMPDIR/go-build-old && -d $LIP_DEV_TMPDIR/go-build-live && -d $LIP_DEV_TMPDIR/keep-old ]]
	mkdir -p "$LIP_DEV_TMPDIR/go-build-old" && touch -d '7 hours ago' "$LIP_DEV_TMPDIR/go-build-old"
	env -u TMPDIR bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ -d $LIP_DEV_TMPDIR/go-build-old ]]
fi
echo 'PASS: Go development race guard blocks before toolchain execution; normal/CI delegation preserved; agent-dev TMPDIR stays on ext4.'
