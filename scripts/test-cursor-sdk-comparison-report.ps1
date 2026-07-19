# ACP vs Cursor SDK comparison report (offline/synthetic by default; no credentials).
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

go test -count=1 ./internal/plugins/backends/cursorsdk/comparison/...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go run ./internal/plugins/backends/cursorsdk/comparison/cmd/report -format=markdown
exit $LASTEXITCODE
