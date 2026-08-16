# Enforce a tiny allowlist of explicit `go` statements in non-test code.
# Run from repository root. Requires ripgrep (rg).

$ErrorActionPreference = "Stop"

if (-not (Get-Command rg -ErrorAction SilentlyContinue)) {
    Write-Host "check-adhoc-goroutines: ripgrep (rg) not found; skipping." -ForegroundColor DarkGray
    exit 0
}

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
# connector-support/acp/scripted_stdio.go: in-process scripted mock ACP agent background loop for connector tests.
# connectors/codex/cmd/fake-codex-cli/main.go: deterministic emulator grandchild process waiter for e2e tests.
$allowed = @(
    "internal/stdhttp/server.go"
    "internal/stdhttp/generation_host.go"
    "internal/stdhttp/admin/configreload/server.go"
    "internal/infra/runtimehost/shutdown.go"
    "internal/infra/runtimehost/manager.go"
    "internal/core/stream/keepalive.go"
    "internal/core/runtime/parallel_race.go"
    "internal/core/runtime/lease_heartbeat.go"
    "internal/core/billing/handoff_retry_worker.go"
    "internal/core/extensions/decision_timeout.go"
    "internal/plugins/frontends/holdalive/wait.go"
    "internal/infra/runtimebundle/modelcatalog_refresh_loop.go"
    "connector-support/acp/transport_stdio.go"
    # connector-support/acp/scripted_stdio.go: in-process scripted mock ACP agent background loop for connector tests.
    "connector-support/acp/scripted_stdio.go"
    "connectors/cursorsdk/internal/product/bridge_process.go"
    "connectors/cursorsdk/internal/product/reap.go"
    "connectors/cursorsdk/internal/product/fakebridge/harness.go"
    # connectors/codex/cmd/fake-codex-cli/main.go: deterministic emulator grandchild process waiter for e2e tests.
    "connectors/codex/cmd/fake-codex-cli/main.go"
    "internal/core/terminalwork/app/processor.go"
    "internal/core/terminalwork/app/ambiguous_append_reconciler.go"
    "internal/core/billing/post_turn_worker.go"
    "internal/core/billing/call_post_usage_worker.go"
    "internal/core/billing/call_provider_cost_worker.go"
    "internal/core/billing/append_outbox.go"
    "cmd/lipstd/reload_signal_adapter_unix.go"
    # Backend plugin host: bidi Execute pumps, gRPC session bridge, process waiters.
    # pkg/lipsdk/backendplugin/host/session.go: bidi Execute stream pump moved from
    # the internal adapter's grpc_session.go when the public host package became
    # the single host implementation (spec extension-scalability task 2.4).
    "pkg/lipsdk/backendplugin/host/session.go"
    "internal/infra/backendplugins/adapter/stream.go"
    "internal/infra/backendplugins/processhost/launch_linux.go"
    "internal/infra/backendplugins/processhost/launch_windows.go"
    "pkg/lipsdk/backendplugin/server.go"
    # ForwardExecute: one bounded cancel watcher per plugin Execute stream, disarmed
    # via stopWatch when the pump returns (review finding M3 remediation).
    "pkg/lipsdk/backendplugin/forward_execute.go"
    # OpenResponses WS transport: per-session read pump + pinger owned and joined
    # by WSSession.Run before it returns (spec openresponses Task 6.1).
    "internal/plugins/frontends/openresponses/websocket_upgrade.go"
    "internal/plugins/frontends/openresponses/websocket_session.go"
    # OpenResponses WS turn runner: one peer-close watcher per in-flight turn,
    # owned and joined by executeTurn's deferred stop (spec openresponses
    # Task 6.2). The watcher exits on peer close or derived-context cancel.
    "internal/plugins/frontends/openresponses/websocket_turn.go"
    # internal/testkit/conformance/acp_connector.go: test harness stderr drain for
    # the launched connector process (conformance matrix harness; test-only package).
    "internal/testkit/conformance/acp_connector.go"
    # internal/testkit/conformance/connector_host.go: test harness stderr drain for
    # the launched connector processes (conformance matrix harness; test-only package).
    "internal/testkit/conformance/connector_host.go"
    # internal/testkit/contract/frontend/harness.go: in-flight request cancellation TCK runner (test-only package).
    "internal/testkit/contract/frontend/harness.go"
)

$raw = @(rg --files-with-matches --glob "!*_test.go" "^\s+go\s" internal pkg cmd connector-support connectors 2>$null)
$hits = @()
foreach ($line in $raw) {
    if ([string]::IsNullOrWhiteSpace($line)) { continue }
    $norm = $line -replace "\\", "/"
    if ($hits -notcontains $norm) {
        $hits += $norm
    }
}
$hits = $hits | Sort-Object

$bad = @()
foreach ($f in $hits) {
    $ok = $false
    foreach ($a in $allowed) {
        if ($f -eq $a) {
            $ok = $true
            break
        }
    }
    if (-not $ok) {
        $bad += $f
    }
}

if ($bad.Count -gt 0) {
    Write-Host "ERROR: disallowed explicit goroutine spawn in non-test code (use long-lived workers / stream pumps; update allowlist in scripts/check-adhoc-goroutines.* only when intentional):" -ForegroundColor Red
    $bad | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
    exit 1
}

Write-Host "OK: ad-hoc goroutine allowlist check passed ($($hits.Count) allowed file(s))" -ForegroundColor Green
exit 0
