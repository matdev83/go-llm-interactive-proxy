#!/usr/bin/env bash
# Installed as the user's PATH-level go command; never replaces the toolchain.
set -euo pipefail

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

if [[ "$blocked_host" == true ]]; then
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

# Keep this installed script standalone. Resource defaults apply only locally
# on the Linux VM; race policy above also applies when CI markers are present.
if [[ ${OS:-} != Windows_NT && $(uname -s) == Linux && -z ${CI:-} && -z ${GITHUB_ACTIONS:-} ]]; then
	local_vm=false
	for host in "${hosts[@]}"; do
		[[ ${host%%.*} == agent-dev ]] && local_vm=true
	done
	if [[ $local_vm == true ]]; then
		lip_dev_tmpdir
		: "${GOMAXPROCS:=2}"
		export GOMAXPROCS
		# Preserve persisted and quoted flags verbatim; explicit -p wins.
		package_flags=${go_flags//\"/}
		package_flags=${package_flags//\'/}
		if [[ ! $package_flags =~ (^|[[:space:]])-p(=|[[:space:]]|$) ]]; then
			GOFLAGS="${go_flags:+$go_flags }-p=1"
			export GOFLAGS
		fi
		current_nice=$(ps -o ni= -p "$$")
		if (( current_nice < 10 )); then
			renice --priority 10 --pid "$$" >/dev/null
		fi
	fi
fi
exec "${GO_DEV_GUARD_REAL_GO:-/usr/local/bin/go}" "$@"
