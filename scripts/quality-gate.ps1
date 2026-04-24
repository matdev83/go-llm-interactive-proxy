# Pre-commit quality gate for Windows.

$ErrorActionPreference = "Stop"

Write-Host "=== Pre-Commit Quality Gate ===" -ForegroundColor Cyan
Write-Host ""

$stagedFiles = @(git diff --cached --name-only --diff-filter=ACMR)
$hasGoFiles = $false
foreach ($f in $stagedFiles) {
    if ($f -match "\.go$") {
        $hasGoFiles = $true
        break
    }
}
if (-not $hasGoFiles) {
    Write-Host "No staged Go files detected; skipping quality gate checks." -ForegroundColor Yellow
    exit 0
}

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

Write-Host "Running quality checks..." -ForegroundColor Yellow
& "$ScriptDir\quality-checks.ps1"
if ($LASTEXITCODE -ne 0) {
    Write-Host "Quality checks failed!" -ForegroundColor Red
    exit $LASTEXITCODE
}

Write-Host ""
Write-Host "Running test suite (staged packages + precommit matrices in one go test)..." -ForegroundColor Yellow
$env:LIP_TEST_PRECOMMIT = "1"
try {
    & "$ScriptDir\test-staged.ps1"
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Tests failed!" -ForegroundColor Red
        exit $LASTEXITCODE
    }
} finally {
    Remove-Item Env:LIP_TEST_PRECOMMIT -ErrorAction SilentlyContinue
}

# On Windows, race checks run inside WSL (see scripts/race-check.ps1).
Write-Host ""
Write-Host "Running race detector scan (WSL on Windows)..." -ForegroundColor Yellow
& "$ScriptDir\race-check.ps1" -Staged
if ($LASTEXITCODE -ne 0) {
    Write-Host "Race detector scan failed!" -ForegroundColor Red
    exit $LASTEXITCODE
}

Write-Host ""
Write-Host "Running linter..." -ForegroundColor Yellow
$linterPath = Get-Command golangci-lint -ErrorAction SilentlyContinue
if ($linterPath) {
    golangci-lint run
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Linter found issues!" -ForegroundColor Red
        exit $LASTEXITCODE
    }
} else {
    Write-Host "Warning: golangci-lint not found, skipping" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "Running govulncheck..." -ForegroundColor Yellow
go tool govulncheck ./...
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

Write-Host ""
Write-Host "=== Quality Gate Passed ===" -ForegroundColor Green
exit 0
