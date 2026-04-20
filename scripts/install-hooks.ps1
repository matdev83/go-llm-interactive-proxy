$ErrorActionPreference = "Stop"

git config core.hooksPath .githooks

Write-Host "Git hooks installed." -ForegroundColor Green
Write-Host "core.hooksPath=$(git config --get core.hooksPath)" -ForegroundColor Cyan
