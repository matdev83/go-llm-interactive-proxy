# quality-checks.ps1
# Fast quality checks before tests. Order: fastest to slowest, fail-fast.

$ErrorActionPreference = "Stop"

function Get-QualityPackages {
    $stagedGoFiles = @(git diff --cached --name-only --diff-filter=ACMR 2>$null | Where-Object { $_ -match '\.go$' })
    if (-not $stagedGoFiles -or $stagedGoFiles.Count -eq 0) {
        return @("./...")
    }

    $forceFull = $false
    $packageSet = [System.Collections.Generic.HashSet[string]]::new()

    foreach ($file in $stagedGoFiles) {
        $normalized = $file -replace '\\', '/'
        $dir = Split-Path -Parent $normalized
        if ([string]::IsNullOrWhiteSpace($dir) -or $dir -eq '.') {
            $forceFull = $true
            break
        }
        [void]$packageSet.Add("./$dir/...")
    }

    if ($forceFull -or $packageSet.Count -eq 0) {
        return @("./...")
    }

    return @($packageSet | Sort-Object)
}

$qualityPackages = @(Get-QualityPackages)

Write-Host "=== Quality Checks ===" -ForegroundColor Cyan
Write-Host ""
Write-Host "Quality scope: $($qualityPackages -join ' ')" -ForegroundColor DarkGray
Write-Host ""

Write-Host "[1/7] Checking Go formatting..." -ForegroundColor Yellow
$unformatted = @(gofmt -l . 2>$null | Where-Object { $_ })
if ($unformatted.Count -gt 0) {
    Write-Host "Unformatted files:" -ForegroundColor Red
    $unformatted | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
    Write-Host "Run: gofmt -w <files> or go fmt ./..." -ForegroundColor Yellow
    exit 1
}
Write-Host "OK: Format check passed" -ForegroundColor Green
Write-Host ""

Write-Host "[2/7] Checking Go modules..." -ForegroundColor Yellow
$preTidyMod = if (Test-Path go.mod) { (git hash-object go.mod 2>$null).Trim() } else { "missing-go-mod" }
$preTidySum = if (Test-Path go.sum) { (git hash-object go.sum 2>$null).Trim() } else { "missing-go-sum" }
go mod tidy
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
$postTidyMod = if (Test-Path go.mod) { (git hash-object go.mod 2>$null).Trim() } else { "missing-go-mod" }
$postTidySum = if (Test-Path go.sum) { (git hash-object go.sum 2>$null).Trim() } else { "missing-go-sum" }
if ($preTidyMod -ne $postTidyMod -or $preTidySum -ne $postTidySum) {
    $modChanges = git diff --name-only go.mod go.sum 2>$null
    Write-Host "ERROR: go.mod/go.sum modified by 'go mod tidy'" -ForegroundColor Red
    if ($modChanges) {
        $modChanges | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
    }
    Write-Host "Run: go mod tidy; git add go.mod go.sum" -ForegroundColor Yellow
    exit 1
}
Write-Host "Verifying module checksums..." -ForegroundColor DarkGray
go mod verify
if ($LASTEXITCODE -ne 0) {
    Write-Host "ERROR: go mod verify failed (checksum mismatch or corrupt module cache)" -ForegroundColor Red
    exit $LASTEXITCODE
}
Write-Host "OK: Module check passed" -ForegroundColor Green
Write-Host ""

Write-Host "[3/7] Checking build..." -ForegroundColor Yellow
go build @qualityPackages
if ($LASTEXITCODE -ne 0) {
    Write-Host "ERROR: Build failed" -ForegroundColor Red
    exit $LASTEXITCODE
}
Write-Host "OK: Build check passed" -ForegroundColor Green
Write-Host ""

Write-Host "[4/7] Running go vet..." -ForegroundColor Yellow
go vet @qualityPackages
if ($LASTEXITCODE -ne 0) {
    Write-Host "ERROR: go vet failed" -ForegroundColor Red
    exit $LASTEXITCODE
}
Write-Host "OK: Vet check passed" -ForegroundColor Green
Write-Host ""

Write-Host "[5/7] Ad-hoc goroutine allowlist (non-test)..." -ForegroundColor Yellow
& "$PSScriptRoot/check-adhoc-goroutines.ps1"
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
Write-Host ""

Write-Host "[6/7] Regex hot-path check (regexp compile in frontends/runtime)..." -ForegroundColor Yellow
& "$PSScriptRoot/regex-hotpath-check.ps1"
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
Write-Host ""

Write-Host "[7/7] Architecture guardrails (line budgets, no init in bundle path)..." -ForegroundColor Yellow
go test ./internal/archtest/...
if ($LASTEXITCODE -ne 0) {
    Write-Host "ERROR: internal/archtest failed" -ForegroundColor Red
    exit $LASTEXITCODE
}
Write-Host ""

Write-Host "=== All Quality Checks Passed ===" -ForegroundColor Green
exit 0
