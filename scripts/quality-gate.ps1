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

# Race on Windows is CGO-heavy; use LIP_QA_RACE_STRICT=1 to fail closed locally.
$strictRace = $env:LIP_QA_RACE_STRICT -eq "1"
Write-Host ""
Write-Host "Running race detector scan..." -ForegroundColor Yellow
if ($strictRace) {
    & "$ScriptDir\race-check.ps1" -Staged -Short -Strict
} else {
    & "$ScriptDir\race-check.ps1" -Staged -Short
}
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
$gov = Get-Command govulncheck -ErrorAction SilentlyContinue
if ($gov) {
    Write-Host "Running govulncheck..." -ForegroundColor Yellow
    govulncheck ./...
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
} else {
    Write-Host "Note: govulncheck not in PATH; skipping" -ForegroundColor DarkGray
}

Write-Host ""
Write-Host "=== Quality Gate Passed ===" -ForegroundColor Green
exit 0
