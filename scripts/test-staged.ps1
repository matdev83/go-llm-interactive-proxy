# test-staged.ps1
# Run tests for packages touched by staged Go files; otherwise ./...

param(
    [switch]$Verbose = $false
)

$ErrorActionPreference = "Stop"

$stagedFilesRaw = git diff --cached --name-only --diff-filter=ACMR 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Host "Error getting staged files. Running all tests..." -ForegroundColor Yellow
    go test -parallel=8 ./...
    exit $LASTEXITCODE
}

$stagedFiles = @($stagedFilesRaw | Where-Object { $_ -and $_.Trim() })
$goFiles = @($stagedFiles | Where-Object { $_ -match '\.go$' -and $_ -notmatch '_test\.go$' })
$testFiles = @($stagedFiles | Where-Object { $_ -match '_test\.go$' })

if (-not $goFiles -and -not $testFiles) {
    Write-Host "No Go files staged. Running all tests..." -ForegroundColor Cyan
    go test -parallel=8 ./...
    exit $LASTEXITCODE
}

$packages = New-Object System.Collections.Generic.HashSet[string]
foreach ($file in ($goFiles + $testFiles)) {
    $file = $file -replace '\\', '/'
    $dir = Split-Path -Parent $file
    if ($dir) {
        [void]$packages.Add("./$dir/...")
    }
}

if ($packages.Count -eq 0) {
    Write-Host "No packages identified. Running all tests..." -ForegroundColor Cyan
    go test -parallel=8 ./...
    exit $LASTEXITCODE
}

Write-Host "Testing packages:" -ForegroundColor Green
foreach ($pkg in $packages) {
    Write-Host "  - $pkg" -ForegroundColor Cyan
}
Write-Host ""

$packageList = @($packages)
# Omit -count=1 so Go's test result cache can skip unchanged packages on repeat runs (build cache is always on via GOCACHE).
go test -parallel=8 @packageList
exit $LASTEXITCODE
