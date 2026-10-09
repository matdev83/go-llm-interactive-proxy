#!/usr/bin/env bash
# pr-status.sh reports the delivery state of one pull request in a form a
# reviewer can act on, and refuses to present evidence that has gone stale.
#
#   scripts/pr-status.sh status <PR>          one concise report
#   scripts/pr-status.sh watch  <PR> [--interval N] [--timeout N]
#   scripts/pr-status.sh self-test            proves the contract with a fake gh
#
# Three properties drive the design:
#
#   Read-only. This script never merges, closes, comments, pushes, or edits. A
#   triage tool that can mutate the thing it measures stops being evidence. The
#   self-test greps this file for mutating verbs so that stays true.
#
#   Evidence is per-revision. Every invocation re-reads the checks for the head
#   it observes, so what it prints is never itself stale. What can be stale is
#   the conclusion a reader carried over from an earlier invocation, so a green
#   verdict is downgraded to STALE when the head or base moved since the last
#   observed state. A failure or a block is equally true of the head in front of
#   the reader, so those keep their own codes.
#
#   Blocked is not failed. Pending checks, an unmerged base, and a deleted head
#   branch are all "not ready yet" and must not look like a defect.
#
# Exit codes are the contract:
#   0  ready        all required checks pass on the current head
#   1  failed       at least one required check failed
#   2  usage        bad arguments, missing gh, or an unreadable PR
#   3  blocked      checks pending, draft, base unmerged, or head branch gone
#   4  stale        head or base changed since the recorded evidence
set -euo pipefail

readonly EXIT_READY=0
readonly EXIT_FAILED=1
readonly EXIT_USAGE=2
readonly EXIT_BLOCKED=3
readonly EXIT_STALE=4

# Commands this script must never run. It reports delivery state; a tool that can
# also change it stops being evidence. The self-test greps this file for these
# verbs, so the guarantee is enforced rather than promised.
readonly mutating_pattern='\bgh[[:space:]]+(pr[[:space:]]+)?(merge|close|comment|edit|create|ready|review)[[:space:]]|\bgit[[:space:]]+push\b|--admin'

# Full JSON responses are kept here; stdout stays concise on purpose so a watch
# loop can be read at a glance and its output piped without filtering.
: "${LIP_PR_LOG_DIR:=${XDG_CACHE_HOME:-$HOME/.cache}/lip-pr-logs}"
# Last-observed revision per PR, used to detect staleness across invocations.
: "${LIP_PR_STATE_DIR:=${XDG_CACHE_HOME:-$HOME/.cache}/lip-pr-state}"

pr_view_fields='number,state,isDraft,headRefName,headRefOid,baseRefName,baseRefOid,mergeable,mergeStateStatus,url,mergedAt,title'

die() {
	echo "pr-status: $*" >&2
	exit "$EXIT_USAGE"
}

require_gh() {
	command -v gh >/dev/null 2>&1 || die "gh is required and was not found on PATH"
}

# jq is a hard requirement rather than a fallback to grep: classifying checks by
# substring is exactly the kind of quiet misreading this script exists to stop.
require_jq() {
	command -v jq >/dev/null 2>&1 || die "jq is required and was not found on PATH"
}

# json_get reads one field from a JSON object on stdin.
json_get() {
	jq -r "$1 // empty"
}

fetch_view() {
	local pr=$1
	gh pr view "$pr" --json "$pr_view_fields"
}

# fetch_required_contexts prints the base branch's required status-check
# contexts, one per line. An unreadable inventory fails rather than returning an
# empty list: silently shrinking the required set is exactly how a workflow
# GitHub rejects (no check runs at all) reads as "ready".
fetch_required_contexts() {
	local base_ref=$1 protection
	protection=$(gh api "repos/{owner}/{repo}/branches/$base_ref/protection" 2>/dev/null) || return 1
	jq -r '[(.required_status_checks.contexts // [])[], ((.required_status_checks.checks // [])[] | .context)] | unique | .[]' <<<"$protection"
}

# fetch_checks reads evidence bound to the observed head commit only. Checks
# attached to an older head never answer for this one, so the sources are the
# commit's own check runs and legacy statuses, filtered to the required
# contexts. A required context with no entry on this head is reported as
# missing, which keeps the verdict blocked.
fetch_checks() {
	local head=$1 required_json=$2 runs statuses
	runs=$(gh api "repos/{owner}/{repo}/commits/$head/check-runs?per_page=100" 2>/dev/null) || runs=""
	statuses=$(gh api "repos/{owner}/{repo}/commits/$head/status" 2>/dev/null) || statuses=""
	jq -n --argjson runs "${runs:-null}" --argjson statuses "${statuses:-null}" --argjson req "$required_json" '
		def rank: {fail: 4, missing: 3, pending: 2, skipping: 1, pass: 0}[.] // 3;
		[
			($runs.check_runs[]? | select(.name as $n | $req | index($n)) | {
				name,
				state: (.conclusion // .status // "pending"),
				bucket: (
					if .conclusion == null then "pending"
					elif .conclusion == "success" then "pass"
					elif .conclusion == "skipped" or .conclusion == "neutral" then "skipping"
					else "fail" end),
				link: (.details_url // "")
			}),
			($statuses.statuses[]? | select(.context as $c | $req | index($c)) | {
				name: .context,
				state,
				bucket: (if .state == "success" then "pass" elif .state == "failure" or .state == "error" then "fail" else "pending" end),
				link: (.target_url // "")
			})
		] as $found
		| ($found + [$req[] | select(. as $c | ($found | map(.name) | index($c)) | not) | {name: ., bucket: "missing", state: "MISSING", link: ""}])
		| group_by(.name)
		| map(sort_by(.bucket | rank) | last)
		| sort_by(.name)
	'
}

# find_base_pr resolves the PR that owns this PR's base branch. A PR based on
# another PR's head branch is stacked, and the state of that base decides
# whether this one can land at all.
find_base_pr() {
	local base_ref=$1
	[[ -n "$base_ref" ]] || return 0
	[[ "$base_ref" == main || "$base_ref" == dev ]] && return 0
	# The filtering happens in local jq rather than in gh's --jq: one jq
	# implementation to reason about, and a stub gh stays faithful because it only
	# has to return the same JSON.
	gh pr list --state all --head "$base_ref" --json number,state 2>/dev/null |
		jq -r 'map(select(.state != "MERGED")) | first | .number // empty' || true
}

base_pr_state() {
	local base_pr=$1
	[[ -n "$base_pr" ]] || return 0
	gh pr view "$base_pr" --json state 2>/dev/null | jq -r '.state // empty' || true
}

# head_branch_exists distinguishes "PR still open, branch still there" from the
# external-deletion case that leaves a live PR pointing at nothing.
head_branch_exists() {
	local ref=$1
	git ls-remote --exit-code --heads origin "$ref" >/dev/null 2>&1
}

# classify maps the observed facts onto an exit code and a one-line verdict.
# Kept separate from printing so the self-test can assert the mapping directly.
classify() {
	local state=$1 is_draft=$2 mergeable=$3 merge_state=$4 checks=$5 base_state=$6 head_gone=$7
	local failing pending

	failing=$(jq '[.[] | select(.bucket == "fail" or .bucket == "cancel" or .bucket == "canceling")] | length' <<<"$checks")
	pending=$(jq '[.[] | select(.bucket != "pass" and .bucket != "skipping")] | length' <<<"$checks")

	if [[ "$state" == MERGED ]]; then
		echo "$EXIT_READY"
		return
	fi
	if [[ "$state" != OPEN ]] || [[ $(jq 'length' <<<"$checks") == 0 ]]; then
		echo "$EXIT_BLOCKED"
		return
	fi
	if [[ "$head_gone" == true ]]; then
		# A live PR whose branch is gone cannot be delivered and cannot be
		# rebuilt locally; the branch has to come back first.
		echo "$EXIT_BLOCKED"
		return
	fi
	if [[ "$base_state" == MERGED || "$base_state" == CLOSED ]]; then
		# The base landed, so this branch is behind and its checks proved nothing
		# about the merged result.
		echo "$EXIT_BLOCKED"
		return
	fi
	if [[ $failing -gt 0 ]]; then
		echo "$EXIT_FAILED"
		return
	fi
	if [[ "$is_draft" == true || $pending -gt 0 ]]; then
		echo "$EXIT_BLOCKED"
		return
	fi
	if [[ -n "$base_state" && "$base_state" != MERGED && "$base_state" != CLOSED ]]; then
		# Stacked on a PR that has not landed. Its checks passed, but they were
		# computed against a base that is about to change, so green here is not a
		# delivery result yet.
		echo "$EXIT_BLOCKED"
		return
	fi
	if [[ "$mergeable" == CONFLICTING || "$merge_state" == DIRTY ]]; then
		echo "$EXIT_BLOCKED"
		return
	fi
	# Unmergeable for an unstated reason is a conflict the author must resolve,
	# not a green light.
	if [[ "$mergeable" != MERGEABLE || "$merge_state" != CLEAN ]]; then
		echo "$EXIT_BLOCKED"
		return
	fi
	echo "$EXIT_READY"
}

verdict_label() {
	case "$1" in
	"$EXIT_READY") echo "ready: all required checks pass on this head" ;;
	"$EXIT_FAILED") echo "failed: at least one required check failed" ;;
	"$EXIT_BLOCKED") echo "blocked: not deliverable yet (see reasons above)" ;;
	"$EXIT_STALE") echo "stale: evidence was produced for a different revision" ;;
	*) echo "unknown" ;;
	esac
}

# state_file is the last-observed head/base for this PR.
state_file() {
	echo "$LIP_PR_STATE_DIR/pr-$1.state"
}

read_state_field() {
	local file=$1 field=$2
	[[ -f "$file" ]] || return 0
	sed -n "s/^$field=//p" "$file" | head -n1
}

write_state() {
	local pr=$1 head_oid=$2 base_ref=$3 base_oid=$4 file
	file=$(state_file "$pr")
	mkdir -p "$LIP_PR_STATE_DIR"
	{
		echo "pr=$pr"
		echo "head_oid=$head_oid"
		echo "base_ref=$base_ref"
		echo "base_oid=$base_oid"
		echo "observed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	} >"$file"
}

# log_snapshot keeps the raw JSON for anything the summary does not show.
log_snapshot() {
	local pr=$1 tag=$2 view=$3 checks=$4 dir file
	dir="$LIP_PR_LOG_DIR"
	mkdir -p "$dir"
	file="$dir/pr-$pr-${tag:-$(date -u +%Y%m%dT%H%M%SZ)}.json"
	{
		printf '{"pr":%s,"observed_at":"%s","view":%s,"checks":%s}\n' \
			"$pr" "$(date -u +%Y-%m-%dT%H%M%SZ)" "$view" "$checks"
	} >"$file"
	echo "$file"
}

# report_once prints the concise summary on stdout and sets REPORT_EXIT. It is
# called directly rather than in a command substitution so the summary reaches
# the terminal during `watch`.
REPORT_EXIT=""
REPORT_COUNTS=""
report_once() {
	local pr=$1 view checks state is_draft head_ref head_oid base_ref base_oid
	local mergeable merge_state url merged_at base_pr base_state head_gone
	local prior_head prior_base exit_name summary draft_note=""

	[[ "$pr" =~ ^[1-9][0-9]*$ ]] || die "PR must be a positive number"
	view=$(fetch_view "$pr") || die "cannot read PR $pr"
	local refreshed
	refreshed=$(fetch_view "$pr") || die "cannot refresh PR $pr"
	if [[ $(jq -c '[.headRefOid,.baseRefOid]' <<<"$view") != "$(jq -c '[.headRefOid,.baseRefOid]' <<<"$refreshed")" ]]; then
		echo "stale: head or base moved while reading checks"
		REPORT_EXIT=$EXIT_STALE
		return
	fi
	view=$refreshed

	state=$(json_get '.state' <<<"$view")
	is_draft=$(json_get '.isDraft' <<<"$view")
	head_ref=$(json_get '.headRefName' <<<"$view")
	head_oid=$(json_get '.headRefOid' <<<"$view")
	base_ref=$(json_get '.baseRefName' <<<"$view")
	base_oid=$(json_get '.baseRefOid' <<<"$view")
	mergeable=$(json_get '.mergeable' <<<"$view")
	merge_state=$(json_get '.mergeStateStatus' <<<"$view")
	url=$(json_get '.url' <<<"$view")
	merged_at=$(json_get '.mergedAt' <<<"$view")

	# Required-check evidence must belong to the head observed here. The
	# inventory comes from the base branch's protection, so a context whose run
	# never started is visible as missing instead of absent from the list.
	local required_text required_json inventory_note=""
	if required_text=$(fetch_required_contexts "$base_ref"); then
		required_json=$(jq -Rn --arg v "$required_text" '$v | split("\n") | map(select(length > 0))')
		checks=$(fetch_checks "$head_oid" "$required_json")
	else
		checks='[]'
		inventory_note="required-check inventory unavailable for base $base_ref; refusing to assume readiness"
	fi

	base_pr=$(find_base_pr "$base_ref")
	base_state=$(base_pr_state "$base_pr")
	if [[ -n "$base_ref" && "$base_ref" != main && "$base_ref" != dev ]]; then
		if [[ -n "$base_pr" ]]; then
			head_gone=false
		else
			# The base branch is not main/dev and has no open PR: either it was
			# merged and deleted, or it never existed.
			head_gone=true
		fi
	else
		head_gone=false
		if [[ "$state" != MERGED ]]; then
			head_branch_exists "$head_ref" || head_gone=true
		fi
	fi

	exit_name=$(classify "$state" "$is_draft" "$mergeable" "$merge_state" "$checks" "$base_state" "$head_gone")

	prior_head=$(read_state_field "$(state_file "$pr")" head_oid)
	prior_base=$(read_state_field "$(state_file "$pr")" base_oid)
	# The checks just read always belong to the head observed in this run, so
	# they are never themselves stale. Staleness is about the conclusion a
	# reader carried over from an earlier invocation, and only a green result is
	# something one carries over: a failure or a block is equally true of the
	# head in front of us. So only READY is downgraded, to STALE, when the
	# revision moved under the reader.
	stale_head=""
	stale_base=""
	[[ -n "$prior_head" && "$prior_head" != "$head_oid" ]] && stale_head=$prior_head
	[[ -n "$prior_base" && "$prior_base" != "$base_oid" ]] && stale_base=$prior_base
	if [[ -n "$stale_head" ]]; then
		echo "STALE: recorded evidence was for head $stale_head; this head is $head_oid."
	elif [[ -n "$stale_base" ]]; then
		echo "STALE: recorded evidence was for base $stale_base; this base is $base_oid."
	fi
	if [[ "$exit_name" == "$EXIT_READY" && ( -n "$stale_head" || -n "$stale_base" ) ]]; then
		exit_name="$EXIT_STALE"
	fi

	[[ "$is_draft" == true ]] && draft_note=" (draft)"
	printf 'PR #%s %s%s\n' "$pr" "$state" "$draft_note"
	printf '  title:    %s\n' "$(json_get '.title' <<<"$view")"
	printf '  head:     %s @ %s\n' "$head_ref" "$head_oid"
	printf '  base:     %s @ %s\n' "$base_ref" "$base_oid"
	if [[ -n "$base_pr" ]]; then
		printf '  stacked:  #%s (%s)\n' "$base_pr" "$base_state"
	fi
	printf '  merge:    %s / %s\n' "$mergeable" "$merge_state"
	if [[ "$state" == MERGED ]]; then
		printf '  merged:   %s\n' "$merged_at"
	fi

	jq -r '.[] | "  check:    \(.bucket)  \(.name)"' <<<"$checks" | sort
	local passes fails pendings missing
	passes=$(jq '[.[] | select(.bucket == "pass")] | length' <<<"$checks")
	fails=$(jq '[.[] | select(.bucket == "fail")] | length' <<<"$checks")
	pendings=$(jq '[.[] | select(.bucket == "pending")] | length' <<<"$checks")
	printf '  counts:   pass=%s fail=%s pending=%s\n' "$passes" "$fails" "$pendings"
	REPORT_COUNTS="pass=$passes fail=$fails pending=$pendings"
	missing=$(jq -r '[.[] | select(.bucket == "missing") | .name] | join(", ")' <<<"$checks")
	if [[ -n "$missing" ]]; then
		printf '  reason:   required checks without evidence on this head: %s\n' "$missing"
	fi
	if [[ -n "$inventory_note" ]]; then
		printf '  reason:   %s\n' "$inventory_note"
	fi

	if [[ "$head_gone" == true && "$state" != MERGED ]]; then
		printf '  reason:   head branch is missing on the remote; restore it before delivery\n'
	fi
	if [[ -n "$base_pr" && "$base_state" != MERGED && "$base_state" != CLOSED ]]; then
		printf '  reason:   base PR #%s is %s; this PR cannot land until it does\n' "$base_pr" "$base_state"
	fi
	if [[ -n "$base_pr" && ( "$base_state" == MERGED || "$base_state" == CLOSED ) ]]; then
		printf '  reason:   base PR #%s is %s; rebase onto the updated base and re-run checks\n' "$base_pr" "$base_state"
	fi

	summary=$(log_snapshot "$pr" "" "$view" "$checks")
	printf '  detail:   %s\n' "$summary"
	printf '  verdict:  %s\n' "$(verdict_label "$exit_name")"

	write_state "$pr" "$head_oid" "$base_ref" "$base_oid"
	REPORT_EXIT=$exit_name
}

# exit_from_label maps the classification name onto its process exit code.
exit_from_label() {
	case "$1" in
	"$EXIT_READY") echo "$EXIT_READY" ;;
	"$EXIT_FAILED") echo "$EXIT_FAILED" ;;
	"$EXIT_STALE") echo "$EXIT_STALE" ;;
	*) echo "$EXIT_BLOCKED" ;;
	esac
}

cmd_status() {
	[[ $# -eq 1 ]] || die "usage: pr-status.sh status <PR>"
	report_once "$1"
	return "$(exit_from_label "$REPORT_EXIT")"
}

# watch prints the full report only on a transition and stops as soon as the PR
# reaches a terminal verdict. An unchanged poll is silent, so a long watch costs
# one line per real change instead of a wall of repeated reports. A PR whose
# head moves mid-watch reports the new head rather than the previous result.
cmd_watch() {
	local pr="" interval=20 timeout=0 start=$SECONDS previous="" current signal last="" capture
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--interval)
			interval=${2:?--interval needs a value}
			shift 2
			;;
		--timeout)
			timeout=${2:?--timeout needs a value}
			shift 2
			;;
		-*)
			die "unknown option $1"
			;;
		*)
			[[ -z "$pr" ]] || die "watch takes one PR"
			pr=$1
			shift
			;;
		esac
	done
	[[ -n "$pr" ]] || die "usage: pr-status.sh watch <PR> [--interval N] [--timeout N]"
	[[ "$interval" =~ ^[1-9][0-9]*$ && "$timeout" =~ ^[0-9]+$ ]] || die "interval must be positive and timeout non-negative integer seconds"

	# The report is captured so an unchanged poll reaches no terminal. The file
	# lives with the recorded state instead of /tmp so it survives a restart and
	# is cleaned with the rest of the tool's cache.
	mkdir -p "$LIP_PR_STATE_DIR"
	capture="$LIP_PR_STATE_DIR/pr-$pr.watch-report"

	while :; do
		report_once "$pr" >"$capture"
		current=$REPORT_EXIT
		signal="$current|$REPORT_COUNTS"
		if [[ "$signal" != "$last" ]]; then
			cat "$capture"
		fi
		if [[ "$current" != "$previous" ]]; then
			echo "--- change: $current ---"
			previous=$current
		fi
		last=$signal
		case "$current" in
		"$EXIT_READY")
			return 0
			;;
		"$EXIT_FAILED")
			return "$EXIT_FAILED"
			;;
		"$EXIT_STALE")
			# The head moved. The recorded evidence now describes the previous
			# revision, so the caller re-runs instead of acting on it.
			return "$EXIT_STALE"
			;;
		esac
		if [[ $timeout -gt 0 && $((SECONDS - start)) -ge $timeout ]]; then
			echo "watch timed out after ${timeout}s; last verdict: $current"
			return "$EXIT_BLOCKED"
		fi
		sleep "$interval"
	done
}

# The self-test drives the real logic against a fake gh on PATH, so the
# classification contract is proven rather than asserted.
cmd_self_test() {
	local script_dir fixture bin fake
	script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
	script="$script_dir/pr-status.sh"

	# Property: this tool stays read-only. A triage helper that can mutate the
	# subject stops being evidence, so the guarantee is checked, not promised.
	# The pattern's own definition line is excluded: it necessarily contains the
	# very verbs it forbids.
	local offenders
	offenders=$(grep -nE "$mutating_pattern" "$script" | grep -v 'mutating_pattern=' || true)
	if [[ -n "$offenders" ]]; then
		echo "FAIL: pr-status.sh contains a mutating command" >&2
		echo "$offenders" >&2
		return 1
	fi

	fixture=$(mktemp -d)
	# The value is bound now: an EXIT trap runs after this function returns, when
	# a local would already be out of scope under `set -u`.
	trap "rm -rf '$fixture'" EXIT
	bin="$fixture/bin"
	mkdir -p "$bin"
	fake="$bin/gh"
	cat >"$fake" <<'STUB'
#!/usr/bin/env bash
# Fake gh driven by $FAKE_PR_SCENARIO. Records every invocation so the test can
# assert the script never asked for anything mutating. Evidence is keyed by the
# commit the script names: checks for another head must never answer.
printf '%s\n' "$*" >>"$FAKE_GH_CALLS"
scenario="${FAKE_PR_SCENARIO:-green}"
case "$*" in
*"pr list"*)
	if [[ "$scenario" == "stacked-unmerged" ]]; then
		echo '[{"number":7,"state":"OPEN"}]'
	else
		echo '{"number":null}'
	fi
	;;
*"pr view 7"*) echo '{"state":"OPEN"}' ;;
*"api repos/{owner}/{repo}/branches/"*)
	if [[ "$scenario" == "no-inventory" ]]; then exit 1; fi
	echo '{"required_status_checks":{"contexts":["Go suite","Lint"]}}'
	;;
*"api repos/{owner}/{repo}/commits/"*"/check-runs"*)
	case "$scenario" in
	missing) echo '{"check_runs":[]}' ;;
	failing) echo '{"check_runs":[{"name":"Go suite","status":"completed","conclusion":"failure","details_url":"l"}]}' ;;
	pending) echo '{"check_runs":[{"name":"Go suite","status":"in_progress","conclusion":null,"details_url":"l"}]}' ;;
	skipped) echo '{"check_runs":[{"name":"Go suite","status":"completed","conclusion":"success","details_url":"l"},{"name":"Lint","status":"completed","conclusion":"skipped","details_url":"l"}]}' ;;
	*) echo '{"check_runs":[{"name":"Go suite","status":"completed","conclusion":"success","details_url":"l"},{"name":"Lint","status":"completed","conclusion":"success","details_url":"l"}]}' ;;
	esac
	;;
*"api repos/{owner}/{repo}/commits/"*"/status"*)
	echo '{"statuses":[]}'
	;;
*"pr view"*)
	case "$scenario" in
	merged) echo '{"number":1,"state":"MERGED","isDraft":false,"title":"slice","headRefName":"feat/x","headRefOid":"aaaa111","baseRefName":"main","baseRefOid":"bbbb222","mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN","url":"https://example.invalid/1","mergedAt":"2026-10-08T00:00:00Z"}' ;;
	stacked-unmerged) echo '{"number":1,"state":"OPEN","isDraft":false,"title":"slice","headRefName":"feat/y","headRefOid":"aaaa111","baseRefName":"feat/x","baseRefOid":"bbbb222","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","url":"https://example.invalid/1","mergedAt":null}' ;;
	newhead) echo '{"number":1,"state":"OPEN","isDraft":false,"title":"slice","headRefName":"feat/x","headRefOid":"cccc333","baseRefName":"main","baseRefOid":"bbbb222","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","url":"https://example.invalid/1","mergedAt":null}' ;;
	failing) echo '{"number":1,"state":"OPEN","isDraft":false,"title":"slice","headRefName":"feat/x","headRefOid":"aaaa111","baseRefName":"main","baseRefOid":"bbbb222","mergeable":"MERGEABLE","mergeStateStatus":"UNSTABLE","url":"https://example.invalid/1","mergedAt":null}' ;;
	pending) echo '{"number":1,"state":"OPEN","isDraft":false,"title":"slice","headRefName":"feat/x","headRefOid":"aaaa111","baseRefName":"main","baseRefOid":"bbbb222","mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED","url":"https://example.invalid/1","mergedAt":null}' ;;
	*) echo '{"number":1,"state":"OPEN","isDraft":false,"title":"slice","headRefName":"feat/x","headRefOid":"aaaa111","baseRefName":"main","baseRefOid":"bbbb222","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","url":"https://example.invalid/1","mergedAt":null}' ;;
	esac
	;;
esac
STUB
	chmod +x "$fake"
	# git is stubbed too: branch existence is remote state, and the self-test
	# must not depend on the host's origin.
	cat >"$bin/git" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_GH_CALLS"
exit "${FAKE_GIT_LSREMOTE_EXIT:-0}"
STUB
	chmod +x "$bin/git"

	export FAKE_GH_CALLS="$fixture/calls"
	export LIP_PR_LOG_DIR="$fixture/logs"
	export LIP_PR_STATE_DIR="$fixture/state"
	export PATH="$bin:$PATH"
	export FAKE_GIT_LSREMOTE_EXIT=0

	local out rc
	out=$(FAKE_PR_SCENARIO=green bash "$script" status 1) && rc=0 || rc=$?
	[[ $rc -eq $EXIT_READY ]] || { echo "FAIL: green expected ready($EXIT_READY), got $rc" >&2; echo "$out" >&2; return 1; }
	grep -q 'head:     feat/x @ aaaa111' <<<"$out" || { echo "FAIL: head SHA not reported" >&2; echo "$out" >&2; return 1; }
	grep -q 'verdict:  ready' <<<"$out" || { echo "FAIL: ready verdict not printed" >&2; return 1; }
	# Evidence is keyed to the observed commit: the check-runs read must name it.
	grep -q 'commits/aaaa111/check-runs' "$FAKE_GH_CALLS" || { echo "FAIL: checks not bound to the observed head" >&2; return 1; }
	out=$(FAKE_PR_SCENARIO=skipped bash "$script" status 1) && rc=0 || rc=$?
	[[ $rc -eq $EXIT_READY ]] || { echo "FAIL: intentionally skipped check blocked readiness ($rc)" >&2; return 1; }
	out=$(FAKE_PR_SCENARIO=missing bash "$script" status 1) && rc=0 || rc=$?
	[[ $rc -eq $EXIT_BLOCKED ]] || { echo "FAIL: missing required checks reported ready ($rc)" >&2; return 1; }
	grep -q 'required checks without evidence on this head: Go suite, Lint' <<<"$out" || { echo "FAIL: missing checks not named on this head" >&2; echo "$out" >&2; return 1; }
	if out=$(FAKE_PR_SCENARIO=no-inventory bash "$script" status 5); then
		echo "FAIL: unreadable required-check inventory expected non-zero" >&2
		return 1
	else
		rc=$?
	fi
	[[ $rc -eq $EXIT_BLOCKED ]] || { echo "FAIL: unreadable inventory expected blocked($EXIT_BLOCKED), got $rc" >&2; return 1; }
	grep -q 'required-check inventory unavailable for base main' <<<"$out" || { echo "FAIL: unreadable inventory not explained" >&2; echo "$out" >&2; return 1; }
	[[ $(classify OPEN false MERGEABLE BEHIND '[{"bucket":"pass"}]' '' false) == "$EXIT_BLOCKED" ]] || { echo "FAIL: behind branch reported ready" >&2; return 1; }
	[[ $(classify CLOSED false MERGEABLE CLEAN '[{"bucket":"pass"}]' '' false) == "$EXIT_BLOCKED" ]] || { echo "FAIL: closed PR reported ready" >&2; return 1; }
	local snapshot
	snapshot=$(compgen -G "$fixture/logs/pr-1-*.json" | sort | head -n1 || true)
	if [[ -z "$snapshot" ]] || [[ ! -s $snapshot ]]; then
		echo "FAIL: no detail snapshot retained in $fixture/logs: $(ls -A "$fixture/logs" 2>&1)" >&2
		return 1
	fi

	# A second run at the same head must not claim staleness.
	FAKE_PR_SCENARIO=green bash "$script" status 1 >/dev/null
	grep -q 'head_oid=aaaa111' "$fixture/state/pr-1.state" || { echo "FAIL: state not recorded" >&2; return 1; }
	out=$(FAKE_PR_SCENARIO=green bash "$script" status 1) && rc=0 || rc=$?
	[[ $rc -eq $EXIT_READY ]] || { echo "FAIL: unchanged head reported rc=$rc, want ready" >&2; return 1; }

	# A different head at the same PR is stale, not ready: this is the case that
	# would otherwise turn an old green result into a claim about the new one.
	if out=$(FAKE_PR_SCENARIO=newhead bash "$script" status 1); then
		echo "FAIL: moved head expected non-zero" >&2
		return 1
	else
		rc=$?
	fi
	[[ $rc -eq $EXIT_STALE ]] || { echo "FAIL: moved head expected stale($EXIT_STALE), got $rc" >&2; echo "$out" >&2; return 1; }
	grep -q 'STALE: recorded evidence was for head aaaa111' <<<"$out" || { echo "FAIL: stale head not named" >&2; echo "$out" >&2; return 1; }
	# Recording the newly observed head is what lets the next run re-verify.
	grep -q 'head_oid=cccc333' "$fixture/state/pr-1.state" || { echo "FAIL: moved head not recorded" >&2; return 1; }
	grep -q 'commits/cccc333/check-runs' "$FAKE_GH_CALLS" || { echo "FAIL: moved head checks were not read for the new revision" >&2; return 1; }

	if out=$(FAKE_PR_SCENARIO=failing bash "$script" status 1); then
		echo "FAIL: failing expected non-zero, got success" >&2
		return 1
	else
		rc=$?
	fi
	[[ $rc -eq $EXIT_FAILED ]] || { echo "FAIL: failing expected $EXIT_FAILED, got $rc" >&2; return 1; }
	grep -q 'verdict:  failed' <<<"$out" || { echo "FAIL: failed verdict not printed" >&2; return 1; }

	if out=$(FAKE_PR_SCENARIO=pending bash "$script" status 1); then
		echo "FAIL: pending expected non-zero" >&2
		return 1
	else
		rc=$?
	fi
	[[ $rc -eq $EXIT_BLOCKED ]] || { echo "FAIL: pending expected blocked($EXIT_BLOCKED), got $rc" >&2; return 1; }

	if out=$(FAKE_PR_SCENARIO=stacked-unmerged bash "$script" status 1); then
		echo "FAIL: unmerged base expected non-zero" >&2
		return 1
	else
		rc=$?
	fi
	[[ $rc -eq $EXIT_BLOCKED ]] || { echo "FAIL: unmerged base expected blocked, got $rc" >&2; return 1; }
	grep -q 'stacked:  #7 (OPEN)' <<<"$out" || { echo "FAIL: stacked base not reported" >&2; echo "$out" >&2; return 1; }

	out=$(FAKE_PR_SCENARIO=merged bash "$script" status 1) && rc=0 || rc=$?
	[[ $rc -eq $EXIT_READY ]] || { echo "FAIL: merged expected ready, got $rc" >&2; return 1; }

	# An unchanged poll must reach no terminal: the full report prints on the
	# first observation and on a real transition, not on every interval.
	out=$(FAKE_PR_SCENARIO=pending bash "$script" watch 6 --interval 1 --timeout 2) && rc=0 || rc=$?
	[[ $rc -eq $EXIT_BLOCKED ]] || { echo "FAIL: watch of a blocked PR expected blocked($EXIT_BLOCKED), got $rc" >&2; return 1; }
	if [[ $(grep -c '^  verdict:' <<<"$out") -ne 1 ]]; then
		echo "FAIL: watch reprinted an unchanged report" >&2
		echo "$out" >&2
		return 1
	fi
	grep -q 'watch timed out after 2s' <<<"$out" || { echo "FAIL: watch timeout not reported" >&2; return 1; }

	# A head branch that vanished leaves a live PR undeliverable.
	if out=$(FAKE_GIT_LSREMOTE_EXIT=1 FAKE_PR_SCENARIO=green bash "$script" status 1); then
		echo "FAIL: deleted head branch expected non-zero" >&2
		return 1
	else
		rc=$?
	fi
	[[ $rc -eq $EXIT_BLOCKED ]] || { echo "FAIL: deleted head expected blocked, got $rc" >&2; return 1; }
	grep -q 'head branch is missing on the remote' <<<"$out" || { echo "FAIL: deleted head not explained" >&2; return 1; }

	if grep -qE '(^| )(merge|close|comment|edit|push)( |$)' "$FAKE_GH_CALLS"; then
		echo "FAIL: a mutating call was made: $FAKE_GH_CALLS" >&2
		return 1
	fi

	echo "PASS: pr-status read-only, per-revision evidence, blocked/failed separated, stacked and deleted-branch handling"
}

main() {
	[[ $# -ge 1 ]] || die "usage: pr-status.sh {status|watch|self-test} ..."
	require_jq
	case "$1" in
	self-test)
		# Deliberately before require_gh: the self-test puts a stub gh on PATH,
		# so demanding the real one would make the contract untestable where it
		# matters most.
		shift
		cmd_self_test
		;;
	status | watch)
		require_gh
		case "$1" in
		status)
			shift
			cmd_status "$@"
			;;
		watch)
			shift
			cmd_watch "$@"
			;;
		esac
		;;
	-h | --help | help)
		sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
		;;
	*)
		die "unknown command $1"
		;;
	esac
}

main "$@"
