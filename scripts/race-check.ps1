# race-check.ps1 — Go race detector; best-effort unless -Strict.
#
# Race detection is disabled on Windows: the Go race runtime (ThreadSanitizer)
# frequently fails with allocation errors on this platform. Run `bash scripts/race-check.sh`
# under Linux/WSL or rely on CI for `-race`.

param(
    [switch]$Short = $false,
    [switch]$Staged = $false,
    [switch]$Strict = $false
)

$ErrorActionPreference = "Stop"

if ($env:OS -eq "Windows_NT") {
    Write-Host "Race detector skipped on Windows (disabled locally; use Linux CI or WSL for go test -race)." -ForegroundColor Yellow
    exit 0
}

function Remove-FileIfExists([string]$Path) {
    if (-not (Test-Path $Path)) { return }
    for ($i = 0; $i -lt 5; $i++) {
        try {
            Remove-Item $Path -Force -ErrorAction Stop
            return
        } catch {
            Start-Sleep -Milliseconds 100
        }
    }
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: go not found in PATH" -ForegroundColor Red
    exit 1
}

$cgoEnabled = (go env CGO_ENABLED).Trim()
$ccValue = (go env CC).Trim()
$ccParts = $ccValue -split '\s+'
$ccBin = if ($ccParts.Length -gt 0) { $ccParts[0] } else { "" }

if ($cgoEnabled -ne "1") {
    Write-Host "Race detector unavailable (CGO_ENABLED=$cgoEnabled)." -ForegroundColor Yellow
    if ($Strict) { exit 1 }
    exit 0
}

if ($ccBin -and -not (Get-Command $ccBin -ErrorAction SilentlyContinue)) {
    Write-Host "Race detector unavailable (C compiler '$ccBin' not found)." -ForegroundColor Yellow
    if ($Strict) { exit 1 }
    exit 0
}

$null = New-Item -ItemType Directory -Path ".tmp" -Force

$precheckLog = ".tmp/race-precheck.log"
$precheckOut = ".tmp/race-precheck.test"
$precheckArgs = @("test", "-race", "-run", "^$", "-c", "-o", $precheckOut, "./pkg/lipsdk")
$precheck = Start-Process -FilePath "go" -ArgumentList $precheckArgs -NoNewWindow -Wait -PassThru -RedirectStandardOutput $precheckLog -RedirectStandardError ($precheckLog + ".err")
$precheckStatus = $precheck.ExitCode
$precheckCombined = @()
if (Test-Path $precheckLog) { $precheckCombined += Get-Content $precheckLog }
if (Test-Path ($precheckLog + ".err")) { $precheckCombined += Get-Content ($precheckLog + ".err") }
Remove-FileIfExists $precheckOut
Remove-FileIfExists ($precheckOut + ".exe")
Remove-FileIfExists ($precheckLog + ".err")

if ($precheckStatus -ne 0) {
    $precheckText = ($precheckCombined | Out-String)
    if ($precheckText -match "race detector is not supported|cgo\.exe:.*exit status|C compiler|gcc.*not found") {
        Write-Host "Race detector is not available on this environment; skipping." -ForegroundColor Yellow
        if ($Strict) { $precheckCombined | Out-Host; exit 1 }
        exit 0
    }
    $precheckCombined | Out-Host
    exit $precheckStatus
}

$packages = @("./...")
if ($Staged) {
    $stagedFiles = @(git diff --cached --name-only --diff-filter=ACMR | Where-Object { $_ -and $_.Trim() -and $_ -match '\.go$' })
    if (-not $stagedFiles -or $stagedFiles.Count -eq 0) {
        Write-Host "No staged Go files detected; skipping race detector scan." -ForegroundColor Yellow
        exit 0
    }
    $packageSet = New-Object System.Collections.Generic.HashSet[string]
    foreach ($file in $stagedFiles) {
        $normalized = $file -replace '\\', '/'
        $dir = Split-Path -Parent $normalized
        if (-not $dir -or $dir -eq ".") {
            [void]$packageSet.Add("./")
        } else {
            [void]$packageSet.Add("./$dir/...")
        }
    }
    $packages = @($packageSet | Sort-Object)
}

$args = @("test", "-race", "-count=1")
if ($Short) { $args += "-short" }
$args += $packages

Write-Host "Running race detector scan: go $($args -join ' ')" -ForegroundColor Cyan

$stdoutLog = ".tmp/race-check.stdout.log"
$stderrLog = ".tmp/race-check.stderr.log"
Remove-FileIfExists $stdoutLog
Remove-FileIfExists $stderrLog

$proc = Start-Process -FilePath "go" -ArgumentList $args -NoNewWindow -Wait -PassThru -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
$status = $proc.ExitCode

$output = @()
if (Test-Path $stdoutLog) { $output += Get-Content $stdoutLog }
if (Test-Path $stderrLog) { $output += Get-Content $stderrLog }
$output | Tee-Object -FilePath ".tmp/race-check.log" | Out-Host

if ($status -ne 0) {
    $joined = ($output | Out-String)
    if (-not $Strict -and $joined -match "race detector is not supported|cgo\.exe:.*exit status|C compiler|gcc.*not found") {
        Write-Host "Race detector is not available on this environment; skipping." -ForegroundColor Yellow
        exit 0
    }
    exit $status
}

Write-Host "Race detector scan passed." -ForegroundColor Green
exit 0
