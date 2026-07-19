# Cross-platform Cursor SDK bridge smoke (Windows). Uses fake bridge; native lane reported separately.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

go test -timeout=5m -run '^TestPlatformSmoke_|^TestProbeNativeBridgeLane_' ./internal/plugins/backends/cursorsdk
exit $LASTEXITCODE
