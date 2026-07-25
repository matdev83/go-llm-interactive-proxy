#!/usr/bin/env bash
# Enforce a tiny allowlist of explicit `go` statements in non-test code.
# Run from repository root. Requires ripgrep (rg).

set -euo pipefail

if ! command -v rg >/dev/null 2>&1; then
	echo "check-adhoc-goroutines: ripgrep (rg) not found; install rg or skip in CI where guaranteed."
	exit 0
fi

mapfile -t hits < <(
	rg --files-with-matches --glob '!*_test.go' '^\s+go\s' internal pkg cmd 2>/dev/null | sed 's#\\#/#g' | sort -u || true
)

# Exact-path allowlist for intentional owned workers / stream pumps only.
# internal/core/terminalwork/app/processor.go: Start/Run/Shutdown owner, ProcessDue
# claim fan-out, per-claim renew loop, and tick/renew ticker pumps (Phase 4.4).
# internal/core/terminalwork/app/ambiguous_append_reconciler.go: one process-owned
# worker draining WorkID-keyed ambiguous append ownership (task 3.6 remediation B).
# internal/infra/runtimehost/shutdown.go: bounded fan-out retiring retained
# generations concurrently so one pinned generation cannot stall unrelated drains.
# internal/infra/runtimehost/manager.go: one-goroutine-per-replaced-generation
# background retirement (scheduleRetire) armed by Publish (task 7.3).
# internal/stdhttp/admin/configreload/server.go: process-owned management HTTP
# listen/serve worker (task 5.3; separate from data-plane generation host).
# cmd/lipstd/reload_signal_adapter_unix.go: one process-owned SIGHUP worker
# delivering bounded reload triggers into the coordinator sink (task 5.2).
bad=()
for f in "${hits[@]}"; do
	case "$f" in
	internal/stdhttp/server.go | internal/stdhttp/generation_host.go | internal/stdhttp/admin/configreload/server.go | internal/infra/runtimehost/shutdown.go | internal/infra/runtimehost/manager.go | internal/core/stream/keepalive.go | internal/core/runtime/parallel_race.go | internal/core/runtime/lease_heartbeat.go | internal/core/extensions/decision_timeout.go | internal/plugins/frontends/holdalive/wait.go | internal/infra/runtimebundle/modelcatalog_refresh_loop.go | internal/plugins/backends/acp/transport_stdio.go | internal/plugins/backends/cursorsdk/bridge_process.go | internal/plugins/backends/cursorsdk/fakebridge/harness.go | internal/core/terminalwork/app/processor.go | internal/core/terminalwork/app/ambiguous_append_reconciler.go | cmd/lipstd/reload_signal_adapter_unix.go) ;;
	*) bad+=("$f") ;;
	esac
done

if [[ ${#bad[@]} -gt 0 ]]; then
	echo "ERROR: disallowed explicit goroutine spawn in non-test code (use long-lived workers / stream pumps; update allowlist in scripts/check-adhoc-goroutines.* only when intentional):"
	printf '  %s\n' "${bad[@]}"
	exit 1
fi

echo "OK: ad-hoc goroutine allowlist check passed (${#hits[@]} allowed file(s))"
