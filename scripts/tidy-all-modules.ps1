param(
    [switch]$Check
)

$ErrorActionPreference = "Stop"
. "$PSScriptRoot/taskrunner.ps1"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$taskRunnerScript = Join-Path $PSScriptRoot "taskrunner.ps1"
$localEnv = if ($env:LIP_DISABLE_VCS_STAMPING -eq "1" -and -not $env:GOFLAGS) { @("GOFLAGS=-buildvcs=false") } else { @() }
$jobs = if ($env:LIP_MODULE_CHECK_JOBS) { [int]$env:LIP_MODULE_CHECK_JOBS } elseif ($env:CI_NODE_TOTAL) { [int]$env:CI_NODE_TOTAL } else { 4 }
if ($jobs -lt 1) { throw "LIP_MODULE_CHECK_JOBS must be a positive integer" }

$pending = @{}
$failed = @()
$runnerBinary = $null

function Start-TidyJob {
    param([string]$Module)
    $dir = if ($Module -eq ".") { $Root } else { Join-Path $Root $Module }
    $label = "tidy-all-modules:$Module"
    $envArgs = @($localEnv) + @("GOWORK=off")
    $commandArgs = if ($Check) { @("go", "mod", "tidy", "-diff") } else { @("go", "mod", "tidy") }
    Start-Job -ScriptBlock {
        param($runnerScriptArg, $runnerBinaryArg, $labelArg, $cwdArg, $commandArg, $envArg)
        # Child jobs share the parent's temporary binary; only the parent owns
        # its cleanup after all jobs have been received and removed.
        $env:LIP_TASKRUNNER_NO_CLEANUP = "1"
        . $runnerScriptArg
        $script:TaskRunnerBinary = $runnerBinaryArg
        Invoke-TaskRunner -Label $labelArg -Cwd $cwdArg -Timeout "8m" -Env $envArg -Command $commandArg | ForEach-Object { Write-Output $_ }
    } -ArgumentList $taskRunnerScript, $runnerBinary, $label, $dir, $commandArgs, $envArgs
}

try {
    $runnerBinary = Get-TaskRunnerBinary
    $modules = @(".", "testdata/enterprise_module")
    $discovered = @(Invoke-TaskRunner -Label "tidy-all-modules:discovery" -Cwd $Root -Timeout "8m" -Env $localEnv -Output capture -Command @("go", "run", "./tools/backendplugin/discover_modules", "-root", "."))
    $modules += $discovered | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    $modules = @($modules | ForEach-Object { $_.Trim() } | Where-Object { $_ } | Select-Object -Unique | Where-Object {
        Test-Path (Join-Path $Root (Join-Path $_ "go.mod"))
    })

    Write-Host ("Tidying {0} modules with up to {1} workers" -f $modules.Count, $jobs)
    $next = 0
    while ($next -lt $modules.Count -or $pending.Count -gt 0) {
        while ($next -lt $modules.Count -and $pending.Count -lt $jobs) {
            $module = $modules[$next]
            $next++
            $pending[$module] = Start-TidyJob $module
        }

        $completed = @($pending.GetEnumerator() | Where-Object { $_.Value.State -in @("Completed", "Failed", "Stopped") })
        if ($completed.Count -eq 0) {
            Start-Sleep -Milliseconds 100
            continue
        }
        foreach ($entry in $completed) {
            $job = $entry.Value
            $jobOutput = @(Receive-Job $job -ErrorAction Continue 2>&1)
            $jobOutput | ForEach-Object { Write-Host $_ }
            $childReason = $job.ChildJobs[0].JobStateInfo.Reason
            if ($job.State -ne "Completed" -or $childReason) {
                $reason = if ($childReason) { $childReason.Exception.Message } else { "taskrunner returned a non-zero exit code" }
                $failed += ("{0}: {1}; output: {2}" -f $entry.Key, $reason, (($jobOutput | Out-String).Trim()))
            }
            Remove-Job $job -Force
            $pending.Remove($entry.Key)
        }
    }

    if ($failed.Count -gt 0) {
        throw ("module tidy failed: " + ($failed -join "; "))
    }
    if ($Check) { Write-Host "OK: all discovered Go modules are tidy" }
    else { Write-Host "OK: all discovered Go modules synchronized" }
}
finally {
    foreach ($entry in @($pending.GetEnumerator())) {
        Stop-Job $entry.Value -ErrorAction SilentlyContinue
        Remove-Job $entry.Value -Force -ErrorAction SilentlyContinue
    }
    if ($runnerBinary) {
        Remove-TaskRunnerBinary
    }
}
