[CmdletBinding()]
param(
    [string]$BaselineRef = 'e719bb5f042e548296f9cf36950bb261d4354d83',
    [string]$OutputRoot = ''
)

$ErrorActionPreference = 'Stop'
$specRoot = $PSScriptRoot
$repoRoot = (Resolve-Path (Join-Path $specRoot '..\..\..')).Path
if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    $OutputRoot = Join-Path ([IO.Path]::GetTempPath()) ('lip-betterleaks-baseline-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
}
if (Test-Path -LiteralPath $OutputRoot) {
    throw "OutputRoot already exists: $OutputRoot"
}
New-Item -ItemType Directory -Path $OutputRoot | Out-Null

Write-Host "Archiving baseline $BaselineRef from $repoRoot to $OutputRoot"
& git -C $repoRoot archive --format=tar $BaselineRef | tar -xf - -C $OutputRoot
if ($LASTEXITCODE -ne 0) {
    throw "git archive extraction failed with exit code $LASTEXITCODE"
}

$overlay = Join-Path $specRoot 'benchmark-baseline_test.go.txt'
$destination = Join-Path $OutputRoot 'internal\plugins\features\secretguard\baseline_bench_test.go'
Copy-Item -LiteralPath $overlay -Destination $destination

Push-Location $OutputRoot
try {
    Write-Host "Baseline benchmark"
    & go test -run '^$' -bench '^BenchmarkSecretGuardBaselineExact$' -benchmem -benchtime=1x ./internal/plugins/features/secretguard
    if ($LASTEXITCODE -ne 0) {
        throw "baseline benchmark failed with exit code $LASTEXITCODE"
    }
    $binary = Join-Path $OutputRoot 'lipstd-baseline.exe'
    Write-Host "Baseline release build: $binary"
    & go build -o $binary ./cmd/lipstd
    if ($LASTEXITCODE -ne 0) {
        throw "baseline release build failed with exit code $LASTEXITCODE"
    }
    $artifact = Get-Item -LiteralPath $binary
    Write-Host ("Baseline lipstd size: {0} bytes" -f $artifact.Length)
} finally {
    Pop-Location
}

Write-Host "Baseline checkout: $OutputRoot"