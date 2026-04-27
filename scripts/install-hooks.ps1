# Installs this repo's Git hooks path (pre-commit: staged secrets + quality gate on Go changes).
# Mirrors scripts/install-hooks.sh for Windows (PowerShell), consistent with other Makefile Windows targets.
$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $root
& git config core.hooksPath .githooks
Write-Output "Git hooks installed."
Write-Output "core.hooksPath=$(& git config --get core.hooksPath)"
