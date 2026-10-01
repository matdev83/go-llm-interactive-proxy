# scripts/lint-all-modules.ps1
# Runs linter (golangci-lint with staticcheck fallback) across all or scoped Go modules in parallel.

[CmdletBinding()]
param(
    [switch]$Staged,
    [switch]$Changed,
    [switch]$Advisory,
    [string[]]$Modules = @()
)

# Style linters that are advisory for the canonical gate: they stay enabled in
# .golangci.yml for the full report (`-Advisory`, `make lint-advisory`) but are
# disabled by default so repository-wide style debt cannot block a release.
$AdvisoryStyleLinters = @("modernize", "paralleltest", "thelper")

$ErrorActionPreference = "Stop"
. "$PSScriptRoot/taskrunner.ps1"
$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

# Bound the module fan-out and each analyzer independently, matching POSIX.
$LintJobs = 2
$LintConcurrency = 2
foreach ($setting in @("LIP_LINT_JOBS", "LIP_LINT_CONCURRENCY")) {
    $value = [Environment]::GetEnvironmentVariable($setting, "Process")
    if ($value) {
        if ($value -notmatch '^[1-9][0-9]*$') { throw "$setting must be a positive integer" }
        if ($setting -eq "LIP_LINT_JOBS") { $LintJobs = [int]$value }
        else { $LintConcurrency = [int]$value }
    }
}

function Get-DiscoveredModules {
    $modules = [System.Collections.Generic.List[string]]::new()
    $modules.Add(".")
    $modules.Add("testdata/enterprise_module")
    $modules.Add("testdata/external_billing_binding")
    $modules.Add("testdata/external_connector")
    $modules.Add("testdata/external_feature_sdk")

    foreach ($base in @("connectors", "connector-support")) {
        $baseDir = Join-Path $RepositoryRoot $base
        if (Test-Path $baseDir) {
            Get-ChildItem -Path $baseDir -Directory | ForEach-Object {
                $modFile = Join-Path $_.FullName "go.mod"
                if (Test-Path $modFile) {
                    $rel = "$base/$($_.Name)" -replace '\\', '/'
                    $modules.Add($rel)
                }
            }
        }
    }
    return @($modules | Sort-Object -Unique)
}

function Get-TargetModules {
    if ($Modules -and $Modules.Count -gt 0) { return $Modules }
    return @(Get-DiscoveredModules)
}

$modulePackages = @{}
if (($Changed -or $Staged) -and -not $Modules) {
    $scopeMode = if ($Staged) { "staged" } else { "changed" }
    $scopeJSON = & go -C $RepositoryRoot run -buildvcs=false ./tools/lintscope -mode $scopeMode
    if ($LASTEXITCODE -ne 0) { throw "Local lint scope discovery failed; refusing to omit evidence." }
    $scopePlan = ($scopeJSON -join "`n") | ConvertFrom-Json
    if ($scopePlan.full) {
        $targetModules = @(Get-DiscoveredModules)
    } else {
        $targetModules = @($scopePlan.modules | ForEach-Object { $_.directory })
        foreach ($module in $scopePlan.modules) {
            $modulePackages[$module.directory] = @($module.packages)
        }
    }
} else {
    $targetModules = @(Get-TargetModules)
}
if ($targetModules.Count -eq 0) {
    Write-Host "No modules to lint." -ForegroundColor DarkGray
    exit 0
}

$linter = $null
$linterArgs = @()

if (Get-Command golangci-lint -ErrorAction SilentlyContinue) {
    $linter = "golangci-lint"
    $linterArgs = @("run", "--allow-parallel-runners", "--concurrency=$LintConcurrency")
    if (-not $Advisory) {
        $linterArgs += "--disable=$($AdvisoryStyleLinters -join ',')"
    }
} elseif (Get-Command staticcheck -ErrorAction SilentlyContinue) {
    $linter = "staticcheck"
    $linterArgs = @()
} else {
    Write-Host "Warning: golangci-lint/staticcheck not found, skipping (install golangci-lint: https://golangci-lint.run/)" -ForegroundColor Yellow
    exit 0
}

Write-Host "Linting $($targetModules.Count) module(s) with $linter in parallel..." -ForegroundColor Cyan
Write-Host "Lint budget: modules=$LintJobs analyzers/module=$LintConcurrency" -ForegroundColor DarkGray
if ($linter -eq "golangci-lint") {
    if ($Advisory) {
        Write-Host "Mode: ADVISORY (full set incl. $($AdvisoryStyleLinters -join ', '); non-blocking style report)." -ForegroundColor Yellow
    } else {
        Write-Host "Mode: MANDATORY correctness gate (--disable=$($AdvisoryStyleLinters -join ',')); style debt via 'make lint-advisory'." -ForegroundColor Cyan
    }
}

$runnerBinary = Get-TaskRunnerBinary
$sessionState = [System.Management.Automation.Runspaces.InitialSessionState]::CreateDefault()
$pool = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspacePool(1, [Math]::Min($targetModules.Count, $LintJobs), $sessionState, $Host)
$pool.Open()

$tasks = [System.Collections.Generic.List[PSObject]]::new()
try {
    foreach ($mod in $targetModules) {
        $modDir = Join-Path $RepositoryRoot $mod
        $packages = if ($modulePackages.ContainsKey($mod)) { $modulePackages[$mod] } else { @("./...") }
        $moduleLintArgs = @($linterArgs) + @($packages)
        Write-Host "Scope ${mod}: $($packages -join ' ')"
        $ps = [System.Management.Automation.PowerShell]::Create()
        $ps.RunspacePool = $pool
        [void]$ps.AddScript({
            param($runnerBinary, $repoRoot, $mod, $modDir, $linter, $linterArgs, $lintConcurrency)
            $runnerArgs = @(
                "--label", "lint:$mod",
                "--cwd", $modDir,
                "--timeout", "5m",
                "--output", "capture"
            )
            $runnerArgs += "--"
            if ($linter -eq "staticcheck") {
                $runnerArgs = @("--env", "GOMAXPROCS=$lintConcurrency") + $runnerArgs
            }
            $runnerArgs += @($linter) + $linterArgs

            $output = @(& $runnerBinary @runnerArgs 2>&1)
            $exitCode = $LASTEXITCODE
            return @{
                Module = $mod
                ExitCode = $exitCode
                Output = $output
            }
        }).AddArgument($runnerBinary).AddArgument($RepositoryRoot).AddArgument($mod).AddArgument($modDir).AddArgument($linter).AddArgument($moduleLintArgs).AddArgument($LintConcurrency)

        $asyncResult = $ps.BeginInvoke()
        $tasks.Add([PSCustomObject]@{
            PowerShell = $ps
            AsyncResult = $asyncResult
            Module = $mod
        })
    }

    $failed = $false
    foreach ($task in $tasks) {
        $res = $task.PowerShell.EndInvoke($task.AsyncResult)
        $task.PowerShell.Dispose()
        if ($res) {
            $exitCode = $res[0].ExitCode
            $output = $res[0].Output
            $mod = $res[0].Module
            if ($exitCode -ne 0) {
                $failed = $true
                Write-Host "FAILED: $mod" -ForegroundColor Red
                if ($output) { $output | ForEach-Object { Write-Host "  $_" -ForegroundColor Red } }
            } else {
                Write-Host "PASS: $mod" -ForegroundColor Green
            }
        }
    }
    if ($failed) {
        throw "Linter found issues in one or more Go modules."
    }
} finally {
    $pool.Close()
    $pool.Dispose()
}

Write-Host "OK: All checked Go modules passed linting." -ForegroundColor Green
exit 0
