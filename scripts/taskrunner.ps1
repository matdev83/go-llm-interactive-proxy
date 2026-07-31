# Shared Windows child-process boundary for developer-facing scripts.
$ErrorActionPreference = "Stop"
$TaskRunnerRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

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
        "run", "./tools/taskrunner/cmd/lip-taskrunner",
        "--label", $Label,
        "--cwd", $Cwd,
        "--timeout", $Timeout,
        "--output", $Output
    )
    foreach ($entry in $Env) {
        $runnerArgs += @("--env", $entry)
    }
    $runnerArgs += "--"
    $runnerArgs += $Command

    $result = @()
    $exitCode = 0
    Push-Location $TaskRunnerRoot
    try {
        if ($Output -eq "capture") {
            $result = @(& go @runnerArgs 2>&1)
        } else {
            & go @runnerArgs
        }
        $exitCode = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($exitCode -ne 0) {
        if ($Output -eq "capture" -and $result) { $result | ForEach-Object { Write-Error $_ -ErrorAction Continue } }
        throw "taskrunner failed for $Label (exit $exitCode)"
    }
    return $result
}
