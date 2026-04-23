# race-check.ps1 — Go race detector; best-effort unless -Strict.
#
# On Windows, native `go test -race` (ThreadSanitizer) is unreliable, so this
# script runs scripts/race-check.sh inside WSL (see Invoke-RaceCheckViaWsl).
# Override distro with LIP_WSL_DISTRO (e.g. Ubuntu-22.04); otherwise the first
# WSL distro whose name starts with "Ubuntu" is used, or the default WSL distro.

param(
    [switch]$Staged = $false,
    [switch]$Strict = $false
)

$ErrorActionPreference = "Stop"

function Escape-BashSingleQuoted([string]$s) {
    if ([string]::IsNullOrEmpty($s)) { return "" }
    return ($s -replace "'", "'\''")
}

function Resolve-WslDistroArgs {
    $named = $env:LIP_WSL_DISTRO
    if ($named -and $named.Trim()) {
        return @("-d", $named.Trim())
    }
    try {
        $raw = & wsl.exe -l -q 2>$null
        if (-not $raw) { return @() }
        $ubuntu = @($raw | ForEach-Object { $_.Trim() } | Where-Object { $_ -match '^(?i)ubuntu' } | Select-Object -First 1)
        if ($ubuntu) {
            return @("-d", $ubuntu)
        }
    } catch {
        return @()
    }
    return @()
}

function Invoke-RaceCheckViaWsl {
    param(
        [switch]$Staged,
        [switch]$Strict
    )

    if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
        Write-Host "ERROR: wsl.exe not found. Install WSL with a Linux distro (Ubuntu) to run the race detector from Windows." -ForegroundColor Red
        exit 1
    }

    $distroArgs = @(Resolve-WslDistroArgs)
    $winCwd = (Get-Location).ProviderPath
    # wslpath expects a Windows path; use forward slashes so argv is not mangled.
    $winForWslpath = $winCwd -replace '\\', '/'

    $unixPath = ""
    try {
        $wslpathOut = & wsl.exe @distroArgs wslpath -u $winForWslpath 2>&1
        if ($LASTEXITCODE -ne 0) {
            $text = ($wslpathOut | Out-String).Trim()
            Write-Host "ERROR: wslpath failed (exit $LASTEXITCODE): $text" -ForegroundColor Red
            Write-Host "Hint: set LIP_WSL_DISTRO to your Ubuntu WSL name (see: wsl -l -v)." -ForegroundColor Yellow
            exit 1
        }
        $unixPath = ($wslpathOut | Out-String).Trim()
    } catch {
        Write-Host "ERROR: could not map Windows path to WSL: $_" -ForegroundColor Red
        exit 1
    }

    if ([string]::IsNullOrWhiteSpace($unixPath)) {
        Write-Host "ERROR: wslpath returned an empty path." -ForegroundColor Red
        exit 1
    }

    $esc = Escape-BashSingleQuoted $unixPath
    $flagParts = @()
    if ($Staged) { $flagParts += "--staged" }
    if ($Strict) { $flagParts += "--strict" }
    $flagStr = ($flagParts -join " ").Trim()

    $bashLine = "set -euo pipefail; cd '$esc' && exec bash scripts/race-check.sh"
    if ($flagStr) {
        $bashLine += " $flagStr"
    }

    $distroLabel = if ($distroArgs.Count -ge 2) { $distroArgs[1] } else { "(default WSL distro)" }
    Write-Host "Running race detector via WSL distro $distroLabel (same repo on /mnt/...)." -ForegroundColor Cyan

    & wsl.exe @distroArgs bash -lc $bashLine
    return $LASTEXITCODE
}

if ($env:OS -eq "Windows_NT") {
    exit (Invoke-RaceCheckViaWsl -Staged:$Staged -Strict:$Strict)
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
