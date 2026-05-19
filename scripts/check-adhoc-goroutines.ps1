# Enforce a tiny allowlist of explicit `go` statements in non-test code.
# Run from repository root. Requires ripgrep (rg).

$ErrorActionPreference = "Stop"

if (-not (Get-Command rg -ErrorAction SilentlyContinue)) {
    Write-Host "check-adhoc-goroutines: ripgrep (rg) not found; skipping." -ForegroundColor DarkGray
    exit 0
}

$allowed = @(
    "internal/stdhttp/server.go"
    "internal/core/stream/keepalive.go"
    "internal/plugins/frontends/holdalive/wait.go"
    "internal/infra/runtimebundle/modelcatalog_refresh_loop.go"
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
