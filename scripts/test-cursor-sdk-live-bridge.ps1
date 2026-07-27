# Opt-in Go→Node live bridge lifecycle harness (Windows).
# Requires CURSOR_SDK_LIVE=1 + nonempty CURSOR_API_KEY (Process, else User, else Machine).
# Uses build tag cursorsdk_live_bridge so ordinary go test never hits the network.
# Without opt-in: exit 0 BLOCKED (safe skip). Not a green live proof.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location (Join-Path $root "connectors/cursorsdk")
$env:GOWORK = "off"

if ($env:CURSOR_SDK_LIVE -ne "1") {
  Write-Host "BLOCKED: CURSOR_SDK_LIVE=1 not set; skipping cursor-sdk live-bridge harness"
  exit 0
}

$clearProcessKey = $false
$key = $env:CURSOR_API_KEY
if (-not $key -or $key.Trim().Length -eq 0) {
  $userKey = [Environment]::GetEnvironmentVariable("CURSOR_API_KEY", "User")
  $machineKey = [Environment]::GetEnvironmentVariable("CURSOR_API_KEY", "Machine")
  if ($userKey -and $userKey.Trim().Length -gt 0) {
    $env:CURSOR_API_KEY = $userKey.Trim()
    $clearProcessKey = $true
  } elseif ($machineKey -and $machineKey.Trim().Length -gt 0) {
    $env:CURSOR_API_KEY = $machineKey.Trim()
    $clearProcessKey = $true
  }
}

if (-not $env:CURSOR_API_KEY -or $env:CURSOR_API_KEY.Trim().Length -eq 0) {
  Write-Host "BLOCKED: CURSOR_API_KEY missing; skipping cursor-sdk live-bridge harness"
  exit 0
}

try {
  go test -v -count=1 -timeout=10m -tags=cursorsdk_live_bridge -run '^TestLiveBridgeHarness_Live$' ./internal/product
  exit $LASTEXITCODE
} finally {
  if ($clearProcessKey) {
    Remove-Item Env:CURSOR_API_KEY -ErrorAction SilentlyContinue
  }
}
