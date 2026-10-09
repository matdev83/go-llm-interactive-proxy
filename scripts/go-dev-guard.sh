#!/usr/bin/env bash
# Installed as the user's PATH-level go command; never replaces the toolchain.
set -euo pipefail

# Repository analyzers reuse this standalone guard's admission mechanism.
# Exclusive mode is for explicitly requested cache maintenance only.
resource_mode=go
case "${1:-}" in
--lip-resource-run) resource_mode=analyzer; shift ;;
--lip-resource-exclusive) resource_mode=maintenance; shift ;;
esac
if [[ $resource_mode != go && $# == 0 ]]; then
	echo 'go guard: resource mode requires a command.' >&2
	exit 2
fi
delegate=("${GO_DEV_GUARD_REAL_GO:-/usr/local/bin/go}")
if [[ $resource_mode != go ]]; then delegate=("$1"); shift; fi
resource_args=("$@")

blocked_host=false
hosts=("${HOSTNAME:-}" "${COMPUTERNAME:-}")
if command -v hostname >/dev/null 2>&1; then
	hosts+=("$(hostname -s 2>/dev/null || hostname 2>/dev/null || true)")
fi
for host in "${hosts[@]}"; do
	short=${host%%.*}
	case "${short,,}" in agent-dev|desktop-i2caj6v) blocked_host=true ;; esac
done

race_flag() {
	case "$1" in
	-race|--race|-race=true|--race=true|-race=True|--race=True|-race=TRUE|--race=TRUE|-race=t|--race=t|-race=T|--race=T|-race=1|--race=1) return 0 ;;
	*) return 1 ;;
	esac
}

reject_race() {
	echo 'ERROR: race-enabled Go commands are disabled on development hosts. Run race checks in remote GitHub CI. The PATH guard must not be bypassed with a toolchain path or override.' >&2
	exit 2
}

go_flags=${GOFLAGS:-}
if [[ -z "$go_flags" && "${GOENV:-}" != off ]]; then
	go_env=${GOENV:-${XDG_CONFIG_HOME:-$HOME/.config}/go/env}
	if [[ -f "$go_env" ]]; then
		while IFS= read -r entry || [[ -n "$entry" ]]; do
			case "$entry" in GOFLAGS=*) go_flags=${entry#GOFLAGS=} ;; esac
		done < "$go_env"
	fi
fi

if [[ "$blocked_host" == true && $resource_mode == go ]]; then
	args=("$@")
	index=0
	# Go's global -C precedes its subcommand.
	case "${args[0]:-}" in
	-C|--C) index=2 ;;
	-C=*|--C=*) index=1 ;;
	esac
	command_name=${args[index]:-}
	case "$command_name" in
	build|test|run|install|list|vet|generate|tool)
		# GOFLAGS uses whitespace-separated, optionally quoted flags. Inspect
		# tokens without evaluating shell expansions or executing their contents.
		inspected_flags=${go_flags//\"/}
		inspected_flags=${inspected_flags//\'/}
		inspected_flags=${inspected_flags//$'\n'/ }
		inspected_flags=${inspected_flags//$'\r'/ }
		read -r -a env_flags <<< "$inspected_flags"
		for flag in "${env_flags[@]}"; do
			race_flag "$flag" && reject_race
		done
		needs_value=false
		for ((index+=1; index<${#args[@]}; index++)); do
			arg=${args[index]}
			[[ "$arg" == -args || "$arg" == -- ]] && break
			if [[ "$needs_value" == true ]]; then needs_value=false; continue; fi
			race_flag "$arg" && reject_race
			# go run's program arguments are not Go flags. Its flags precede
			# the package/file; separate values belong to the preceding flag.
			if [[ "$command_name" == run ]]; then
				case "$arg" in
				-*=*|-a|-n|-v|-work|-x|-trimpath|-cover|-race|-msan|-asan) ;;
				-*) needs_value=true ;;
				*) break ;;
				esac
			fi
		done
		;;
	esac
fi
# /tmp is tmpfs on agent-dev: RAM-backed (charged to the 6 GiB limit, and killed
# runs leave go-build work dirs behind) and not ext4, which the config-source
# tests require. Keep temporary files on ext4 and prune orphaned work dirs.
# Keep in sync with dev-cpu-defaults.sh.
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

# The limits above are per command; sessions sharing this host still stack.
# Heavy commands (build, test, vet, install) take one of LIP_GO_SLOTS
# host-wide slots (default 2) and wait for a free one, so concurrent agents
# queue instead of slowing each other into timeouts and memory kills. Nested
# commands inherit the parent's slot. LIP_GO_SLOT_MODE=advisory (default)
# runs anyway after LIP_GO_SLOT_WAIT seconds (default 900), preserving the
# escape for children that scrub their environment. Hard mode exits 75 instead
# without starting the toolchain. A zero wait attempts each slot once.
# Print one line per held slot so a waiting agent can see what it queues
# behind (pid, elapsed time, worktree, command) without inspecting processes.
lip_go_slot_holders() {
	local dir=$1 pid etime args slot cmd cwd
	while read -r pid etime args; do
		[[ $args == flock\ * && $args == *" $dir/slot"* ]] || continue
		slot=${args#*" $dir/"}
		slot=${slot%% *}
		cmd=${args#*" $dir/$slot "}
		cmd=${cmd#* }
		cwd=$(readlink "/proc/$pid/cwd" 2>/dev/null || echo '?')
		echo "go guard:   $slot held for $etime by pid $pid in $cwd: go ${cmd:0:160}" >&2
	done < <(ps -eo pid=,etime=,args= 2>/dev/null || true)
}

lip_go_slot_run() {
	[[ -z ${LIP_GO_SLOT_HELD:-} ]] || return 0
	local index=0 command_name
	case "${1:-}" in -C|--C) index=2 ;; -C=*|--C=*) index=1 ;; esac
	local args=("$@")
	command_name=${args[index]:-}
	if [[ $resource_mode == go ]]; then
		case "$command_name" in build|test|vet|install) ;; *) return 0 ;; esac
	fi
	local slots=${LIP_GO_SLOTS:-2} wait=${LIP_GO_SLOT_WAIT:-900}
	local mode=${LIP_GO_SLOT_MODE:-advisory}
	case "$mode" in
	advisory|hard) ;;
	*) echo "go guard: invalid LIP_GO_SLOT_MODE: $mode (expected advisory or hard)." >&2; exit 2 ;;
	esac
	[[ $slots =~ ^[1-9][0-9]*$ ]] || slots=2
	[[ $wait =~ ^[0-9]+$ ]] || wait=900
	local dir=${LIP_GO_SLOT_DIR:-$HOME/.cache/lip-go-slots} slot status announced=false
	local deadline=$((SECONDS + wait))
	mkdir -p "$dir"
	while :; do
		for ((slot = 0; slot < slots; slot++)); do
			# flock -o keeps the lock in flock itself, so a leaked test
			# subprocess cannot hold the slot after go exits.
			status=0
			LIP_GO_SLOT_HELD=$slot flock -n -o -E 211 "$dir/slot$slot" "${delegate[@]}" "$@" || status=$?
			((status == 211)) || exit "$status"
		done
		((SECONDS < deadline)) || break
		if [[ $announced == false ]]; then
			echo "go guard: $slots heavy Go commands are already running on this host; waiting for a slot (LIP_GO_SLOTS=$slots)." >&2
			lip_go_slot_holders "$dir"
			announced=true
		fi
		sleep 2
	done
	if [[ $mode == hard ]]; then
		echo "go guard: resource-blocked: no Go slot freed within ${wait}s (LIP_GO_SLOT_MODE=hard); toolchain not started." >&2
		lip_go_slot_holders "$dir"
		exit 75
	fi
	echo "go guard: no Go slot freed within ${wait}s; running without one (LIP_GO_SLOT_MODE=advisory)." >&2
	export LIP_GO_SLOT_HELD=none
}

# Cache exclusion never fails open: advisory slot overflow still holds a shared
# cache lock. flock owns the lock, without leaking its descriptor to workers.
lip_go_cache_run() {
	local dir=${LIP_GO_SLOT_DIR:-$HOME/.cache/lip-go-slots}
	local wait=${LIP_GO_SLOT_WAIT:-900} status=0
	[[ $wait =~ ^[0-9]+$ ]] || wait=900
	mkdir -p "$dir"
	if [[ $resource_mode == maintenance ]]; then
		if [[ -n ${LIP_GO_SLOT_HELD:-} || -n ${LIP_GO_CACHE_HELD:-} ]]; then
			echo 'go guard: resource-blocked: maintenance cannot upgrade an inherited resource lock.' >&2
			exit 75
		fi
		flock -x -w "$wait" -o -E 211 "$dir/cache" env LIP_GO_CACHE_HELD=exclusive LIP_GO_SLOT_HELD=maintenance "${delegate[@]}" "${resource_args[@]}" || status=$?
	else
		[[ -z ${LIP_GO_SLOT_HELD:-} && -z ${LIP_GO_CACHE_HELD:-} ]] || return 0
		local command_args=("${resource_args[@]}")
		if [[ $resource_mode == analyzer ]]; then command_args=(--lip-resource-run "${delegate[@]}" "${resource_args[@]}"); fi
		flock -s -w "$wait" -o -E 211 "$dir/cache" env LIP_GO_CACHE_HELD=shared bash "$0" "${command_args[@]}" || status=$?
	fi
	if ((status == 211)); then
		echo "go guard: resource-blocked: cache lock unavailable within ${wait}s; command not started." >&2
		exit 75
	fi
	exit "$status"
}

if [[ $resource_mode == maintenance ]]; then lip_go_cache_run; fi

# Keep this installed script standalone. Resource defaults apply only locally
# on the Linux VM; race policy above also applies when CI markers are present.
if [[ ${OS:-} != Windows_NT && $(uname -s) == Linux && -z ${CI:-} && -z ${GITHUB_ACTIONS:-} ]]; then
	local_vm=false
	for host in "${hosts[@]}"; do
		[[ ${host%%.*} == agent-dev ]] && local_vm=true
	done
	if [[ $local_vm == true ]]; then
		index=0
		case "${1:-}" in -C|--C) index=2 ;; -C=*|--C=*) index=1 ;; esac
		if [[ $resource_mode == analyzer || ${resource_args[index]:-} =~ ^(build|test|vet|install)$ ]]; then
			lip_go_cache_run
		fi
		lip_dev_tmpdir
		: "${GOMAXPROCS:=2}"
		export GOMAXPROCS
		# Preserve persisted and quoted flags verbatim; explicit -p wins.
		package_flags=${go_flags//\"/}
		package_flags=${package_flags//\'/}
		if [[ $resource_mode == go && ! $package_flags =~ (^|[[:space:]])-p(=|[[:space:]]|$) ]]; then
			GOFLAGS="${go_flags:+$go_flags }-p=1"
			export GOFLAGS
		fi
		current_nice=$(ps -o ni= -p "$$")
		if (( current_nice < 10 )); then
			renice --priority 10 --pid "$$" >/dev/null
		fi
		lip_go_slot_run "$@"
	fi
fi
exec "${delegate[@]}" "$@"
