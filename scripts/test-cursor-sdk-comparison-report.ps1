# ACP vs Cursor SDK comparison report (offline/synthetic by default; no credentials).
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location (Join-Path $root "connectors/cursorsdk")
$env:GOWORK = "off"

go test -count=1 ./internal/product/comparison/...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go run ./internal/product/comparison/cmd/report -format=markdown
exit $LASTEXITCODE
