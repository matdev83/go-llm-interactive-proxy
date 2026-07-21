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
# internal/infra/runtimehost/shutdown.go: bounded fan-out retiring retained
# generations concurrently so one pinned generation cannot stall unrelated drains.
$allowed = @(
    "internal/stdhttp/server.go"
    "internal/stdhttp/generation_host.go"
    "internal/infra/runtimehost/shutdown.go"
    "internal/core/stream/keepalive.go"
    "internal/core/runtime/parallel_race.go"
    "internal/core/runtime/lease_heartbeat.go"
    "internal/core/extensions/decision_timeout.go"
    "internal/plugins/frontends/holdalive/wait.go"
    "internal/infra/runtimebundle/modelcatalog_refresh_loop.go"
    "internal/plugins/backends/acp/transport_stdio.go"
    "internal/plugins/backends/cursorsdk/bridge_process.go"
    "internal/plugins/backends/cursorsdk/fakebridge/harness.go"
    "internal/core/terminalwork/app/processor.go"
)

$raw = @(rg --files-with-matches --glob "!*_test.go" "^\s+go\s" internal pkg cmd 2>$null)
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
