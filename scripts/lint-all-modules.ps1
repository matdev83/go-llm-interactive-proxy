# scripts/lint-all-modules.ps1
# Runs linter (golangci-lint with staticcheck fallback) across all or scoped Go modules in parallel.

[CmdletBinding()]
param(
    [switch]$Staged,
    [switch]$Changed,
    [string[]]$Modules = @()
)

$ErrorActionPreference = "Stop"
. "$PSScriptRoot/taskrunner.ps1"
$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

function Test-AgentSkillPath {
    param([string]$NormalizedPath)
    return $NormalizedPath -match '^\.(agents|codex|cursor|kiro|opencode|pi)/skills/'
}

function Get-DiscoveredModules {
    $modules = [System.Collections.Generic.List[string]]::new()
    $modules.Add(".")
    $modules.Add("testdata/enterprise_module")
    $modules.Add("testdata/external_connector")

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

function Get-ModuleDirForFile {
    param([string]$FilePath)
    $normalized = $FilePath -replace '\\', '/'
    $dir = Split-Path -Parent $normalized
    while (-not [string]::IsNullOrWhiteSpace($dir) -and $dir -ne '.') {
        if (Test-Path -LiteralPath (Join-Path $RepositoryRoot "$dir/go.mod")) {
            return ($dir -replace '\\', '/')
        }
        $parent = Split-Path -Parent $dir
        if ([string]::IsNullOrWhiteSpace($parent) -or $parent -eq $dir) {
            break
        }
        $dir = $parent
    }
    return "."
}

function Get-TargetModules {
    if ($Modules -and $Modules.Count -gt 0) {
        return $Modules
    }
    if ($Staged) {
        $files = @(git diff --cached --name-only --diff-filter=ACMRD 2>$null | Where-Object { $_ -match '\.go$' -or $_ -match '(^|/)go\.(mod|sum)$' })
        if (-not $files -or $files.Count -eq 0) {
            return @()
        }
        $set = [System.Collections.Generic.HashSet[string]]::new()
        foreach ($f in $files) {
            if (-not (Test-AgentSkillPath ($f -replace '\\', '/'))) {
                [void]$set.Add((Get-ModuleDirForFile $f))
            }
        }
        return @($set | Sort-Object)
    }
    if ($Changed) {
        $files = @(git diff --cached --name-only --diff-filter=ACMRD 2>$null | Where-Object { $_ -match '\.go$' -or $_ -match '(^|/)go\.(mod|sum)$' })
        $files += @(git diff --name-only --diff-filter=ACMRD 2>$null | Where-Object { $_ -match '\.go$' -or $_ -match '(^|/)go\.(mod|sum)$' })
        $files += @(git ls-files --others --exclude-standard 2>$null | Where-Object { $_ -match '\.go$' -or $_ -match '(^|/)go\.(mod|sum)$' })
        if (-not $files -or $files.Count -eq 0) {
            return @(".")
        }
        $set = [System.Collections.Generic.HashSet[string]]::new()
        foreach ($f in $files) {
            if (-not (Test-AgentSkillPath ($f -replace '\\', '/'))) {
                [void]$set.Add((Get-ModuleDirForFile $f))
            }
        }
        if ($set.Count -eq 0) {
            return @(".")
        }
        return @($set | Sort-Object)
    }
    return @(Get-DiscoveredModules)
}

$targetModules = @(Get-TargetModules)
if ($targetModules.Count -eq 0) {
    Write-Host "No modules to lint." -ForegroundColor DarkGray
    exit 0
}

$linter = $null
$linterArgs = @()

if (Get-Command golangci-lint -ErrorAction SilentlyContinue) {
    $linter = "golangci-lint"
    $linterArgs = @("run", "--allow-parallel-runners")
} elseif (Get-Command staticcheck -ErrorAction SilentlyContinue) {
    $linter = "staticcheck"
    $linterArgs = @("./...")
} else {
    Write-Host "Warning: golangci-lint/staticcheck not found, skipping (install golangci-lint: https://golangci-lint.run/)" -ForegroundColor Yellow
    exit 0
}

Write-Host "Linting $($targetModules.Count) module(s) with $linter in parallel..." -ForegroundColor Cyan

$runnerBinary = Get-TaskRunnerBinary
$sessionState = [System.Management.Automation.Runspaces.InitialSessionState]::CreateDefault()
$pool = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspacePool(1, [Math]::Min($targetModules.Count, [Environment]::ProcessorCount), $sessionState, $Host)
$pool.Open()

$tasks = [System.Collections.Generic.List[PSObject]]::new()
try {
    foreach ($mod in $targetModules) {
        $modDir = Join-Path $RepositoryRoot $mod
        $ps = [System.Management.Automation.PowerShell]::Create()
        $ps.RunspacePool = $pool
        [void]$ps.AddScript({
            param($runnerBinary, $repoRoot, $mod, $modDir, $linter, $linterArgs)
            $runnerArgs = @(
                "--label", "lint:$mod",
                "--cwd", $modDir,
                "--timeout", "5m",
                "--output", "capture"
            )
            $runnerArgs += "--"
            $runnerArgs += @($linter) + $linterArgs

            $output = @(& $runnerBinary @runnerArgs 2>&1)
            $exitCode = $LASTEXITCODE
            return @{
                Module = $mod
                ExitCode = $exitCode
                Output = $output
            }
        }).AddArgument($runnerBinary).AddArgument($RepositoryRoot).AddArgument($mod).AddArgument($modDir).AddArgument($linter).AddArgument($linterArgs)

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
