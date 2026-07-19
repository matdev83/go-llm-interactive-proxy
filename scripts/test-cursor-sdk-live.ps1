# Optional Cursor SDK live scenarios (Windows).
# Live SDK lane is Node live-scenarios only. Fake-bridge lifecycle is platform lane.
# Without opt-in or key: exit 0 BLOCKED. Opted-in incomplete/blocked: npm exit nonzero.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if ($env:CURSOR_SDK_LIVE -ne "1") {
  Write-Host "BLOCKED: CURSOR_SDK_LIVE=1 not set; skipping cursor-sdk live scenarios"
  exit 0
}
if (-not $env:CURSOR_API_KEY -or $env:CURSOR_API_KEY.Trim().Length -eq 0) {
  Write-Host "BLOCKED: CURSOR_API_KEY missing; skipping cursor-sdk live scenarios"
  exit 0
}

Push-Location internal/plugins/backends/cursorsdk/bridge
try {
  npm run live-scenarios
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} finally {
  Pop-Location
}
