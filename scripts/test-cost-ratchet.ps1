# Windows test-cost ratchet orchestrator.
#
# The current checkout is the head under test.  Only the detached anchor
# worktree is created and it is removed in the finally block.  Measurements,
# reports, and child-process logs stay under OutputRoot for CI upload or local
# inspection.  All external command arguments are arrays so paths containing
# spaces remain single arguments.

[CmdletBinding()]
param(
    [string]$BaseSHA,
    [string]$OutputRoot,
    [int]$Parallel = 0,
    [switch]$Preflight
)

$ErrorActionPreference = "Stop"

Set-StrictMode -Version Latest

$PolicyRelativePath = "scripts/test-cost-budget.json"
$Targets = @("test-unit", "quality-checks")

function Test-IsWindows {
    return $env:OS -eq "Windows_NT" -or [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
}

function Test-Truthy {
    param([AllowNull()][string]$Value)
    return $Value -match "^(?i:1|true|yes|on)$"
}

function Get-RepositoryRoot {
    param([Parameter(Mandatory = $true)][string]$ScriptRoot)

    $candidate = Join-Path $ScriptRoot ".."
    return (Resolve-Path -LiteralPath $candidate).Path
}

function Convert-ToAbsolutePath {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$RelativeBase
    )

    if ([IO.Path]::IsPathRooted($Path)) {
        return [IO.Path]::GetFullPath($Path)
    }
    return [IO.Path]::GetFullPath((Join-Path $RelativeBase $Path))
}

function Get-GitText {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)

    $output = @(& git @Arguments 2>$null)
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "git $($Arguments -join ' ') failed (exit $exitCode)"
    }
    return ($output -join "`n").Trim()
}

function Invoke-GitChecked {
    param([Parameter(Mandatory = $true)][string[]]$Arguments)

    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        $output = @(& git @Arguments 2>&1)
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
    }
    foreach ($line in $output) {
        Write-Host $line
    }
    if ($exitCode -ne 0) {
        throw "git $($Arguments -join ' ') failed (exit $exitCode)"
    }
}

function Test-CleanCheckout {
    param([Parameter(Mandatory = $true)][string]$RepositoryRoot)

    $status = @(Get-GitText @("-C", $RepositoryRoot, "status", "--porcelain=v1", "--untracked-files=all"))
    if ($status.Count -gt 0 -and -not [string]::IsNullOrWhiteSpace(($status -join ""))) {
        Write-Host "Head checkout is not clean:" -ForegroundColor Red
        foreach ($line in $status) {
            Write-Host "  $line" -ForegroundColor Red
        }
        throw "head checkout must be clean before running the test-cost ratchet (CI checkouts must remain clean too)"
    }
}

function Resolve-Commit {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$Revision,
        [Parameter(Mandatory = $true)][string]$Description
    )

    $resolved = Get-GitText @("-C", $RepositoryRoot, "rev-parse", "--verify", "${Revision}^{commit}")
    if ([string]::IsNullOrWhiteSpace($resolved)) {
        throw "$Description does not resolve to a commit: $Revision"
    }
    return $resolved
}

function Test-PathAtCommit {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$Commit,
        [Parameter(Mandatory = $true)][string]$RelativePath
    )

    $object = "${Commit}:$RelativePath"
    $null = @(& git -C $RepositoryRoot cat-file -e $object 2>$null)
    return $LASTEXITCODE -eq 0
}

function Test-PolicyProtection {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$HeadCommit,
        [AllowEmptyString()][string]$BaseRevision,
        [Parameter(Mandatory = $true)][bool]$AllowGrowth
    )

    if ([string]::IsNullOrWhiteSpace($BaseRevision)) {
        Write-Host "Policy protection: skipped (no -BaseSHA supplied)" -ForegroundColor DarkGray
        return
    }

    $baseCommit = Resolve-Commit $RepositoryRoot $BaseRevision "base revision"
    $policyAtBase = Test-PathAtCommit $RepositoryRoot $baseCommit $PolicyRelativePath
    if (-not $policyAtBase) {
        Write-Host "Policy protection: bootstrap allowed (budget file is absent at base $baseCommit)" -ForegroundColor Yellow
        return
    }

    $null = @(& git -C $RepositoryRoot diff --quiet $baseCommit $HeadCommit -- $PolicyRelativePath 2>$null)
    $diffExitCode = $LASTEXITCODE
    if ($diffExitCode -eq 0) {
        Write-Host "Policy protection: budget unchanged from base $baseCommit" -ForegroundColor Green
        return
    }
    if ($diffExitCode -ne 1) {
        throw "unable to compare $PolicyRelativePath with base $baseCommit (git diff exit $diffExitCode)"
    }

    if (-not $AllowGrowth) {
        throw "budget policy changed relative to -BaseSHA $baseCommit; set LIP_ALLOW_TEST_COST_GROWTH=1 only for an authorized ratchet update"
    }
    Write-Host "Policy protection: budget change allowed by LIP_ALLOW_TEST_COST_GROWTH=1" -ForegroundColor Yellow
}

function Get-TestParallel {
    param([int]$Requested)

    if ($Requested -lt 0) {
        throw "-Parallel must be zero (auto) or a positive integer"
    }
    if ($Requested -ge 1) {
        return $Requested
    }

    $fromEnvironment = 0
    if ([int]::TryParse([string]$env:LIP_TEST_PARALLEL, [ref]$fromEnvironment) -and $fromEnvironment -ge 1) {
        return $fromEnvironment
    }
    return [Math]::Max(1, [Environment]::ProcessorCount)
}

function Get-EffectiveOutputRoot {
    param(
        [AllowEmptyString()][string]$Requested,
        [Parameter(Mandatory = $true)][string]$RepositoryRoot
    )

    if ([string]::IsNullOrWhiteSpace($Requested)) {
        $name = "golip-test-cost-ratchet-" + [Guid]::NewGuid().ToString("N")
        return [IO.Path]::GetFullPath((Join-Path ([IO.Path]::GetTempPath()) $name))
    }
    return Convert-ToAbsolutePath $Requested $RepositoryRoot
}

function Assert-OutputRoot {
    param(
        [Parameter(Mandatory = $true)][string]$OutputRoot,
        [Parameter(Mandatory = $true)][string]$RepositoryRoot
    )

    $outputNormalized = [IO.Path]::GetFullPath($OutputRoot).TrimEnd([char[]]@("\", "/"))
    $repositoryNormalized = [IO.Path]::GetFullPath($RepositoryRoot).TrimEnd([char[]]@("\", "/"))
    if ([StringComparer]::OrdinalIgnoreCase.Equals($outputNormalized, $repositoryNormalized)) {
        throw "-OutputRoot must not be the repository root"
    }
    $repositoryPrefix = $repositoryNormalized + [IO.Path]::DirectorySeparatorChar
    if ($outputNormalized.StartsWith($repositoryPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "-OutputRoot must be outside the repository so the head checkout remains clean: $OutputRoot"
    }
    if (Test-Path -LiteralPath $OutputRoot -PathType Leaf) {
        throw "-OutputRoot points to a file: $OutputRoot"
    }
}

function Invoke-External {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$TempRoot
    )

    # Set TEMP/TMP only for this synchronous child invocation.  Restoring both
    # values and the location in finally prevents process-wide leakage while
    # ensuring taskrunner helpers built by each tree cannot cross-use a cache.
    $previousTemp = [Environment]::GetEnvironmentVariable("TEMP", "Process")
    $previousTmp = [Environment]::GetEnvironmentVariable("TMP", "Process")
    $exitCode = 1
    try {
        $env:TEMP = $TempRoot
        $env:TMP = $TempRoot
        Push-Location -LiteralPath $WorkingDirectory
        try {
            $previousPreference = $ErrorActionPreference
            try {
                $ErrorActionPreference = "Continue"
                $output = @(& $FilePath @Arguments 2>&1)
                $exitCode = $LASTEXITCODE
            } finally {
                $ErrorActionPreference = $previousPreference
            }
            foreach ($line in $output) {
                Write-Host $line
            }
        } finally {
            Pop-Location
        }
    } finally {
        if ($null -eq $previousTemp) {
            Remove-Item Env:TEMP -ErrorAction SilentlyContinue
        } else {
            $env:TEMP = $previousTemp
        }
        if ($null -eq $previousTmp) {
            Remove-Item Env:TMP -ErrorAction SilentlyContinue
        } else {
            $env:TMP = $previousTmp
        }
    }
    return [int]$exitCode
}

function Invoke-RequiredExternal {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$TempRoot
    )

    Write-Host "[$Label] $FilePath $($Arguments -join ' ')" -ForegroundColor DarkGray
    $exitCode = Invoke-External $FilePath $Arguments $WorkingDirectory $TempRoot
    if ($exitCode -ne 0) {
        throw "$Label failed (exit $exitCode)"
    }
}

function Warm-Tree {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$TreeRoot,
        [Parameter(Mandatory = $true)][string]$TempRoot,
        [Parameter(Mandatory = $true)][int]$TestParallel
    )

    # Keep this exact no-test warm-up separate from measured runs so the
    # anchor/head pair starts with the same compile cache shape.
    Invoke-RequiredExternal $Label "go" @("test", "-run", '^$', "-count=1", "-parallel=$TestParallel", "-timeout=10m", "./...") $TreeRoot $TempRoot
}

function Apply-AnchorCompatibilityPatch {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$AnchorRoot,
        [Parameter(Mandatory = $true)][string]$AnchorCommit
    )

    $bootstrapAnchor = "a7a00cedddc4e49d7f96502ee28a6ea1d9603315"
    if ($AnchorCommit -ne $bootstrapAnchor) {
        return
    }

    # PR #553's merge commit renamed the representative parity component in
    # the catalog but retained two stale test-only selectors. PR #555 corrected
    # only those selectors. Apply that correction to a temporary commit so the
    # pinned anchor can execute and remains clean for quality-check scope.
    Write-Host "Anchor compatibility: applying the PR #555 test-selector correction to pinned PR #553 anchor" -ForegroundColor Yellow
    $correctionCommit = "1f69c577983cd60b03120ae855bc215e8e5138af"
    $null = Resolve-Commit $RepositoryRoot $correctionCommit "anchor compatibility correction"
    $patchText = Get-GitText @(
        "-C", $RepositoryRoot, "diff", $bootstrapAnchor, $correctionCommit, "--",
        "internal/testkit/dbparity/cmd/main_test.go",
        "internal/testkit/postgres_makefile_gate_test.go"
    )
    if ([string]::IsNullOrWhiteSpace($patchText)) {
        throw "anchor compatibility correction produced an empty patch"
    }
    $patchPath = Join-Path ([IO.Path]::GetTempPath()) ("lip-testcost-anchor-compat-" + [Guid]::NewGuid().ToString("N") + ".patch")
    try {
        [IO.File]::WriteAllText($patchPath, $patchText + "`n", [Text.UTF8Encoding]::new($false))
        Invoke-GitChecked @("-C", $AnchorRoot, "apply", "--check", $patchPath)
        Invoke-GitChecked @("-C", $AnchorRoot, "apply", $patchPath)
    } finally {
        Remove-Item -LiteralPath $patchPath -Force -ErrorAction SilentlyContinue
    }
    Invoke-GitChecked @("-C", $AnchorRoot, "diff", "--check")
    Invoke-GitChecked @("-C", $AnchorRoot, "add", "--", "internal/testkit/dbparity/cmd/main_test.go", "internal/testkit/postgres_makefile_gate_test.go")
    Invoke-GitChecked @(
        "-C", $AnchorRoot,
        "-c", "user.name=Go-LIP test-cost ratchet",
        "-c", "user.email=test-cost-ratchet@invalid.local",
        "-c", "commit.gpgsign=false",
        "commit", "-m", "test: make pinned performance anchor executable"
    )
    Test-CleanCheckout $AnchorRoot
}

function Build-TestCostBinary {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$TreeRoot,
        [Parameter(Mandatory = $true)][string]$BinaryPath,
        [Parameter(Mandatory = $true)][string]$TempRoot
    )

    Invoke-RequiredExternal "build-$Label-lip-testcost" "go" @("build", "-o", $BinaryPath, "./cmd/lip-testcost") $TreeRoot $TempRoot
}

function Measure-Tree {
    param(
        [Parameter(Mandatory = $true)][string]$BinaryPath,
        [Parameter(Mandatory = $true)][string]$Target,
        [Parameter(Mandatory = $true)][string]$TreeRoot,
        [Parameter(Mandatory = $true)][string]$Revision,
        [Parameter(Mandatory = $true)][string]$MeasurementPath,
        [Parameter(Mandatory = $true)][string]$TempRoot,
        [Parameter(Mandatory = $true)][int]$TestParallel
    )

    $arguments = @(
        "measure", "--target", $Target,
        "--root", $TreeRoot,
        "--revision", $Revision,
        "--out", $MeasurementPath,
        "--temp-root", $TempRoot,
        "--parallel", [string]$TestParallel
    )
    Invoke-RequiredExternal "measure-$Target-$Revision" $BinaryPath $arguments $TreeRoot $TempRoot
    if (-not (Test-Path -LiteralPath $MeasurementPath -PathType Leaf)) {
        throw "lip-testcost did not write measurement: $MeasurementPath"
    }
}

function Compare-TreePair {
    param(
        [Parameter(Mandatory = $true)][string]$BinaryPath,
        [Parameter(Mandatory = $true)][string]$Target,
        [Parameter(Mandatory = $true)][string]$BaselinePath,
        [Parameter(Mandatory = $true)][string]$CurrentPath,
        [Parameter(Mandatory = $true)][string]$PolicyPath,
        [Parameter(Mandatory = $true)][string]$ReportPath,
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$TempRoot,
        [Parameter(Mandatory = $true)][bool]$AllowOverride
    )

    $arguments = @(
        "compare", "--target", $Target,
        "--baseline", $BaselinePath,
        "--current", $CurrentPath,
        "--policy", $PolicyPath,
        "--out", $ReportPath
    )
    if ($AllowOverride) {
        $arguments += "--allow-override"
    }

    Write-Host "[compare-$Target] validating budget with lip-testcost" -ForegroundColor DarkGray
    $exitCode = Invoke-External $BinaryPath $arguments $WorkingDirectory $TempRoot
    if ($exitCode -ne 0 -and $exitCode -ne 1) {
        throw "compare-$Target failed operationally (exit $exitCode)"
    }
    if (-not (Test-Path -LiteralPath $ReportPath -PathType Leaf)) {
        throw "lip-testcost did not write comparison report: $ReportPath"
    }

    $report = Get-Content -LiteralPath $ReportPath -Raw | ConvertFrom-Json
    return [PSCustomObject]@{
        Target = $Target
        Passed = [bool]$report.passed
        Overridden = [bool]$report.overridden
        Violations = @($report.violations).Count
        Warnings = @($report.warnings).Count
        ExitCode = [int]$exitCode
        Report = $ReportPath
    }
}

function Write-FinalTables {
    param(
        [Parameter(Mandatory = $true)][string]$OutputRoot,
        [Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Measurements,
        [Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Comparisons
    )

    Write-Host ""
    Write-Host "=== Test-cost ratchet artifacts ===" -ForegroundColor Cyan
    Write-Host ("{0,-12} {1,-8} {2}" -f "tree", "target", "measurement")
    foreach ($measurement in $Measurements) {
        Write-Host ("{0,-12} {1,-8} {2}" -f $measurement.Tree, $measurement.Target, $measurement.Path)
    }

    Write-Host ""
    Write-Host "=== Test-cost ratchet comparisons ===" -ForegroundColor Cyan
    Write-Host ("{0,-16} {1,-7} {2,-10} {3,-8} {4,-8} {5}" -f "target", "passed", "overridden", "violations", "warnings", "report")
    foreach ($comparison in $Comparisons) {
        Write-Host ("{0,-16} {1,-7} {2,-10} {3,-8} {4,-8} {5}" -f $comparison.Target, $comparison.Passed, $comparison.Overridden, $comparison.Violations, $comparison.Warnings, $comparison.Report)
    }
    Write-Host "OutputRoot: $OutputRoot" -ForegroundColor Green
}

if (-not (Test-IsWindows)) {
    throw "test-cost ratchet is Windows-only"
}

$RepositoryRoot = Get-RepositoryRoot $PSScriptRoot
$PolicyPath = Join-Path $RepositoryRoot $PolicyRelativePath
if (-not (Test-Path -LiteralPath $PolicyPath -PathType Leaf)) {
    throw "budget policy is missing: $PolicyPath"
}

Test-CleanCheckout $RepositoryRoot
$HeadCommit = Resolve-Commit $RepositoryRoot "HEAD" "head revision"
$PolicyDocument = Get-Content -LiteralPath $PolicyPath -Raw | ConvertFrom-Json
$AnchorRevision = [string]$PolicyDocument.anchor_ref
if ([string]::IsNullOrWhiteSpace($AnchorRevision)) {
    throw "budget policy anchor_ref is empty: $PolicyPath"
}
$AnchorCommit = Resolve-Commit $RepositoryRoot $AnchorRevision "anchor revision"
$allowOverride = Test-Truthy $env:LIP_ALLOW_TEST_COST_GROWTH
Test-PolicyProtection $RepositoryRoot $HeadCommit $BaseSHA $allowOverride
$TestParallel = Get-TestParallel $Parallel
$EffectiveOutputRoot = Get-EffectiveOutputRoot $OutputRoot $RepositoryRoot
Assert-OutputRoot $EffectiveOutputRoot $RepositoryRoot

if ($Preflight) {
    Write-Host "OK: test-cost ratchet preflight" -ForegroundColor Green
    Write-Host "  head:   $HeadCommit"
    Write-Host "  anchor: $AnchorCommit ($AnchorRevision)"
    Write-Host "  parallel: $TestParallel"
    Write-Host "  output: $EffectiveOutputRoot"
    exit 0
}

New-Item -ItemType Directory -Path $EffectiveOutputRoot -Force | Out-Null
$measurementRoot = Join-Path $EffectiveOutputRoot "measurements"
$reportRoot = Join-Path $EffectiveOutputRoot "reports"
$binaryRoot = Join-Path $EffectiveOutputRoot "binaries"
New-Item -ItemType Directory -Path $measurementRoot, $reportRoot, $binaryRoot -Force | Out-Null

$runID = [Guid]::NewGuid().ToString("N")
# Keep the checkout path short: the pinned anchor contains tracked paths that
# exceed legacy Win32 MAX_PATH when nested under a long runner TEMP directory.
$anchorRoot = Join-Path (Split-Path -Parent $RepositoryRoot) ("lip-testcost-anchor-" + $runID.Substring(0, 8))
$anchorTempRoot = Join-Path $EffectiveOutputRoot ("temp-anchor-" + $runID)
$headTempRoot = Join-Path $EffectiveOutputRoot ("temp-head-" + $runID)
if (Test-Path -LiteralPath $anchorRoot) {
    throw "refusing to use existing anchor worktree path: $anchorRoot"
}
New-Item -ItemType Directory -Path $anchorTempRoot, $headTempRoot -Force | Out-Null

$testCostBinary = Join-Path $binaryRoot "lip-testcost.exe"
$anchorCreated = $false
$ratchetFailed = $false
$fatalFailure = $null
$measurements = [System.Collections.Generic.List[object]]::new()
$comparisons = [System.Collections.Generic.List[object]]::new()

try {
    # Create exactly one worktree: the detached anchor.  The current checkout
    # remains the head, so its caller-owned worktree is never removed.
    Invoke-GitChecked @("-C", $RepositoryRoot, "worktree", "add", "--detach", $anchorRoot, $AnchorCommit)
    $anchorCreated = $true
    Apply-AnchorCompatibilityPatch $RepositoryRoot $anchorRoot $AnchorCommit

    # The committed anchor predates the ratchet tool itself. Build the neutral
    # measurement wrapper once from head, then point it at each source tree.
    # The commands being measured still come from their respective trees; in
    # particular, isolated TEMP/TMP roots keep each quality-checks.ps1 copy's
    # cached lip-taskrunner.exe source-correct.
    Build-TestCostBinary "head" $RepositoryRoot $testCostBinary $headTempRoot

    # Warm in the required order before any measured target: anchor, then head.
    Warm-Tree "warm-anchor" $anchorRoot $anchorTempRoot $TestParallel
    Warm-Tree "warm-head" $RepositoryRoot $headTempRoot $TestParallel

    foreach ($target in $Targets) {
        # Each target is a complete anchor/head pair before moving to the next
        # target: test-unit pair first, then quality-checks pair.
        $anchorMeasurement = Join-Path $measurementRoot ("anchor-$target.json")
        $headMeasurement = Join-Path $measurementRoot ("head-$target.json")
        Measure-Tree $testCostBinary $target $anchorRoot $AnchorCommit $anchorMeasurement $anchorTempRoot $TestParallel
        $measurements.Add([PSCustomObject]@{ Tree = "anchor"; Target = $target; Path = $anchorMeasurement })
        Measure-Tree $testCostBinary $target $RepositoryRoot $HeadCommit $headMeasurement $headTempRoot $TestParallel
        $measurements.Add([PSCustomObject]@{ Tree = "head"; Target = $target; Path = $headMeasurement })

        $reportPath = Join-Path $reportRoot ("$target.json")
        $comparison = Compare-TreePair $testCostBinary $target $anchorMeasurement $headMeasurement $PolicyPath $reportPath $RepositoryRoot $headTempRoot $allowOverride
        $comparisons.Add($comparison)
        if (-not $comparison.Passed) {
            $ratchetFailed = $true
        }
    }

    # The orchestrator itself must not leave the caller's head dirty.  This
    # also catches quality scripts that unexpectedly mutate tracked files.
    Test-CleanCheckout $RepositoryRoot
} catch {
    $fatalFailure = $_
} finally {
    if ($anchorCreated) {
        try {
            # This is the only cleanup mutation: remove the worktree created by
            # this invocation.  OutputRoot and all logs/reports are retained.
            Invoke-GitChecked @("-C", $RepositoryRoot, "worktree", "remove", "--force", $anchorRoot)
        } catch {
            Write-Host "ERROR: failed to remove created anchor worktree $anchorRoot : $($_.Exception.Message)" -ForegroundColor Red
            if ($null -eq $fatalFailure) {
                $fatalFailure = $_
            }
        }
    }
}

Write-FinalTables $EffectiveOutputRoot $measurements $comparisons

if ($null -ne $fatalFailure) {
    Write-Host "ERROR: test-cost ratchet did not complete: $($fatalFailure.Exception.Message)" -ForegroundColor Red
    Write-Host "Artifacts retained at: $EffectiveOutputRoot" -ForegroundColor Yellow
    exit 1
}
if ($ratchetFailed) {
    Write-Host "ERROR: one or more test-cost budgets exceeded; reports and logs were retained" -ForegroundColor Red
    Write-Host "Artifacts retained at: $EffectiveOutputRoot" -ForegroundColor Yellow
    exit 1
}

Write-Host "OK: test-cost ratchet passed" -ForegroundColor Green
exit 0
