#!/usr/bin/env bash
# Source from Bash entry points; --local lets Make select the same host defaults.
lip_local_agent_dev() {
	[[ ${OS:-} != Windows_NT && $(uname -s) == Linux ]] || return 1
	[[ -z ${CI:-} && -z ${GITHUB_ACTIONS:-} ]] || return 1
	local host
	for host in "${HOSTNAME:-}" "$(hostname -s 2>/dev/null || true)"; do
		[[ ${host%%.*} == agent-dev ]] && return 0
	done
	return 1
}

# /tmp is tmpfs on agent-dev: RAM-backed (charged to the 6 GiB limit, and killed
# runs leave go-build work dirs behind) and not ext4, which the config-source
# tests require. Keep temporary files on ext4 and prune orphaned work dirs.
# Keep in sync with go-dev-guard.sh (installed standalone).
lip_dev_tmpdir() {
	if [[ -z ${TMPDIR:-} || $(stat -f -c %T "$TMPDIR" 2>/dev/null) != ext2/ext3 ]]; then
		TMPDIR=${LIP_DEV_TMPDIR:-$HOME/.cache/lip-tmp}
		mkdir -p "$TMPDIR"
		export TMPDIR
	fi
	local stamp=$TMPDIR/.go-build-pruned
	if [[ ! -e $stamp || -n $(find "$stamp" -mmin +60 2>/dev/null) ]]; then
		touch "$stamp"
		find "$TMPDIR" -mindepth 1 -maxdepth 1 -type d -name 'go-build*' -mmin +360 -exec rm -rf {} + 2>/dev/null || true
	fi
}

if [[ ${1:-} == --local ]]; then
	lip_local_agent_dev && printf 'yes\n'
	exit 0
fi
if lip_local_agent_dev; then
	lip_dev_tmpdir
	: "${GOMAXPROCS:=2}" "${LIP_TEST_PACKAGES:=1}" "${LIP_TEST_PARALLEL:=2}"
	export GOMAXPROCS LIP_TEST_PACKAGES LIP_TEST_PARALLEL
	# Absolute floor: inherited lower priority is retained, never compounded.
	lip_current_nice=$(ps -o ni= -p "$$")
	if (( lip_current_nice < 10 )); then
		renice --priority 10 --pid "$$" >/dev/null
	fi
	unset lip_current_nice
fi
