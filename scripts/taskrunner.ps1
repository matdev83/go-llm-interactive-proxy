# Shared Windows child-process boundary for developer-facing scripts.
$ErrorActionPreference = "Stop"
$TaskRunnerRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$script:TaskRunnerBinary = $null

function Get-TaskRunnerBinary {
    if ($script:TaskRunnerBinary -and (Test-Path $script:TaskRunnerBinary)) {
        return $script:TaskRunnerBinary
    }

    $cacheDir = Join-Path ([System.IO.Path]::GetTempPath()) "golip-taskrunner"
    if (-not (Test-Path $cacheDir)) {
        New-Item -ItemType Directory -Path $cacheDir -Force | Out-Null
    }
    $path = Join-Path $cacheDir "lip-taskrunner.exe"

    $needsBuild = $true
    if (Test-Path $path) {
        $binTime = (Get-Item $path).LastWriteTimeUtc
        $srcDir = Join-Path $TaskRunnerRoot "tools/taskrunner"
        $newestSrc = (Get-ChildItem -Path $srcDir -Recurse -File | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1)
        if ($newestSrc -and $binTime -gt $newestSrc.LastWriteTimeUtc) {
            $needsBuild = $false
        }
    }

    if ($needsBuild) {
        $tmpPath = Join-Path $cacheDir ("lip-taskrunner-" + $PID + "-" + [Guid]::NewGuid().ToString("N") + ".exe")
        Push-Location $TaskRunnerRoot
        try {
            # The helper is a local diagnostic executable; do not require repository
            # VCS metadata merely to compile the process-tree boundary.
            & go build -buildvcs=false -o $tmpPath ./tools/taskrunner/cmd/lip-taskrunner
            if ($LASTEXITCODE -ne 0) {
                throw "failed to build temporary lip-taskrunner (exit $LASTEXITCODE)"
            }
        } finally {
            Pop-Location
        }
        try {
            Move-Item -LiteralPath $tmpPath -Destination $path -Force -ErrorAction Stop
        } catch {
            if (Test-Path -LiteralPath $path) {
                Remove-Item -LiteralPath $tmpPath -Force -ErrorAction SilentlyContinue
            } else {
                $path = $tmpPath
            }
        }
    }
    $script:TaskRunnerBinary = $path
    return $path
}

function Invoke-TaskRunner {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$Cwd,
        [Parameter(Mandatory = $true)][string]$Timeout,
        [Parameter(Mandatory = $true)][string[]]$Command,
        [string[]]$Env = @(),
        [ValidateSet("stream", "capture")][string]$Output = "stream"
    )

    $runnerArgs = @(
        "--label", $Label,
        "--cwd", $Cwd,
        "--timeout", $Timeout,
        "--output", $Output
    )
    # Keep VCS stamping intact by default, including local release checks.
    # Worktrees with incomplete metadata opt into the diagnostic override at
    # their local boundary with LIP_DISABLE_VCS_STAMPING=1.
    $effectiveEnv = @($Env)
    $hasGoFlags = @($effectiveEnv | Where-Object { $_ -like "GOFLAGS=*" }).Count -gt 0
    if (-not $hasGoFlags -and $env:LIP_DISABLE_VCS_STAMPING -eq "1") {
        $effectiveEnv += "GOFLAGS=-buildvcs=false"
    }
    foreach ($entry in $effectiveEnv) {
        $runnerArgs += @("--env", $entry)
    }
    $runnerArgs += "--"
    $runnerArgs += $Command

    $binary = Get-TaskRunnerBinary
    $result = @()
    $exitCode = 0
    if ($Output -eq "capture") {
        $result = @(& $binary @runnerArgs 2>&1)
    } else {
        & $binary @runnerArgs
    }
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        if ($Output -eq "capture" -and $result) { $result | ForEach-Object { Write-Error $_ -ErrorAction Continue } }
        throw "taskrunner failed for $Label (exit $exitCode)"
    }
    return $result
}
