#!/usr/bin/env bash
# Enforce a tiny allowlist of explicit `go` statements in non-test code.
# Run from repository root. Requires ripgrep (rg).

set -euo pipefail

if ! command -v rg >/dev/null 2>&1; then
	echo "check-adhoc-goroutines: ripgrep (rg) not found; install rg or skip in CI where guaranteed."
	exit 0
fi

mapfile -t hits < <(
	rg --files-with-matches --glob '!*_test.go' '^\s+go\s' internal pkg cmd connector-support connectors 2>/dev/null | sed 's#\\#/#g' | sort -u || true
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
# pkg/lipsdk/backendplugin/forward_execute.go: one bounded cancel watcher per
# plugin Execute stream, disarmed via stopWatch when the pump returns (M3).
# internal/plugins/frontends/openresponses/websocket_upgrade.go: per-session read
# pump + pinger owned and joined by WSSession.Run before it returns (Task 6.1).
# internal/plugins/frontends/openresponses/websocket_turn.go: one peer-close
# watcher per in-flight turn, owned and joined by executeTurn's deferred stop
# (Task 6.2); exits on peer close or derived-context cancel.
# connector-support/acp/scripted_stdio.go: in-process scripted mock ACP agent background loop for connector tests.
# connectors/codex/cmd/fake-codex-cli/main.go: deterministic emulator grandchild process waiter for e2e tests.
# internal/testkit/conformance/acp_connector.go: test harness stderr drain for the
# launched connector process (conformance matrix harness; test-only package).
# internal/testkit/conformance/connector_host.go: test harness stderr drain for the
# launched connector processes (conformance matrix harness; test-only package).
bad=()
for f in "${hits[@]}"; do
	case "$f" in
	internal/stdhttp/server.go | internal/stdhttp/generation_host.go | internal/stdhttp/admin/configreload/server.go | internal/infra/runtimehost/shutdown.go | internal/infra/runtimehost/manager.go | internal/core/stream/keepalive.go | internal/core/runtime/parallel_race.go | internal/core/runtime/lease_heartbeat.go | internal/core/extensions/decision_timeout.go | internal/plugins/frontends/holdalive/wait.go | internal/infra/runtimebundle/modelcatalog_refresh_loop.go | connector-support/acp/transport_stdio.go | connector-support/acp/scripted_stdio.go | connectors/cursorsdk/internal/product/bridge_process.go | connectors/cursorsdk/internal/product/reap.go | connectors/cursorsdk/internal/product/fakebridge/harness.go | connectors/codex/cmd/fake-codex-cli/main.go | internal/core/terminalwork/app/processor.go | internal/core/terminalwork/app/ambiguous_append_reconciler.go | cmd/lipstd/reload_signal_adapter_unix.go | internal/infra/backendplugins/adapter/grpc_session.go | internal/infra/backendplugins/adapter/stream.go | internal/infra/backendplugins/processhost/launch_linux.go | internal/infra/backendplugins/processhost/launch_windows.go | pkg/lipsdk/backendplugin/server.go | pkg/lipsdk/backendplugin/forward_execute.go | internal/plugins/frontends/openresponses/websocket_upgrade.go | internal/plugins/frontends/openresponses/websocket_turn.go | internal/testkit/conformance/acp_connector.go | internal/testkit/conformance/connector_host.go) ;;
	*) bad+=("$f") ;;
	esac
done

if [[ ${#bad[@]} -gt 0 ]]; then
	echo "ERROR: disallowed explicit goroutine spawn in non-test code (use long-lived workers / stream pumps; update allowlist in scripts/check-adhoc-goroutines.* only when intentional):"
	printf '  %s\n' "${bad[@]}"
	exit 1
fi

echo "OK: ad-hoc goroutine allowlist check passed (${#hits[@]} allowed file(s))"
