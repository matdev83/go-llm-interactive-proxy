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

if [[ ${1:-} == --local ]]; then
	lip_local_agent_dev && printf 'yes\n'
	exit 0
fi
if lip_local_agent_dev; then
	: "${GOMAXPROCS:=2}" "${LIP_TEST_PACKAGES:=1}" "${LIP_TEST_PARALLEL:=2}"
	export GOMAXPROCS LIP_TEST_PACKAGES LIP_TEST_PARALLEL
	# Absolute floor: inherited lower priority is retained, never compounded.
	lip_current_nice=$(ps -o ni= -p "$$")
	if (( lip_current_nice < 10 )); then
		renice --priority 10 --pid "$$" >/dev/null
	fi
	unset lip_current_nice
fi
