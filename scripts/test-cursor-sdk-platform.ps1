# Cross-platform Cursor SDK bridge smoke (Windows). Uses fake bridge; native lane reported separately.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location (Join-Path $root "connectors/cursorsdk")

$env:GOWORK = "off"
go test -timeout=5m -run '^TestPlatformSmoke_|^TestProbeNativeBridgeLane_' ./internal/product
exit $LASTEXITCODE
