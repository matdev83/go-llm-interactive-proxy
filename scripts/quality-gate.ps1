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
Write-Host "Running test suite..." -ForegroundColor Yellow
& "$ScriptDir\test-staged.ps1"
if ($LASTEXITCODE -ne 0) {
    Write-Host "Tests failed!" -ForegroundColor Red
    exit $LASTEXITCODE
}

Write-Host ""
Write-Host "Running precommit-tagged tests (repo hygiene + executor regression matrices)..." -ForegroundColor Yellow
& "$ScriptDir\precommit-extra-tests.ps1"
if ($LASTEXITCODE -ne 0) {
    Write-Host "Precommit-tagged tests failed!" -ForegroundColor Red
    exit $LASTEXITCODE
}

# Race detector is not run on Windows (ThreadSanitizer is unreliable). CI runs
# `bash scripts/race-check.sh` on Linux; for local -race use WSL or Linux.
Write-Host ""
Write-Host "Skipping race detector (Windows; use Linux CI or WSL for go test -race)." -ForegroundColor Yellow

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
