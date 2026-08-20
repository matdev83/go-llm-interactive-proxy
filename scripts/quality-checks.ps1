# quality-checks.ps1
# Fast quality checks before tests. Order: fastest to slowest, fail-fast.

$ErrorActionPreference = "Stop"
. "$PSScriptRoot/taskrunner.ps1"
$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

function Invoke-QualityChild {
    param([string]$Label, [string[]]$Command, [string[]]$Env = @(), [string]$Timeout = "2m")
    Invoke-TaskRunner -Label "quality-checks:$Label" -Cwd $RepositoryRoot -Timeout $Timeout -Env $Env -Command $Command | Out-Host
}

function Test-UnderNestedGoModule {
    param([string]$NormalizedPath)

    $dir = Split-Path -Parent $NormalizedPath
    if ([string]::IsNullOrWhiteSpace($dir) -or $dir -eq '.') {
        return $false
    }

    while (-not [string]::IsNullOrWhiteSpace($dir) -and $dir -ne '.') {
        if (Test-Path -LiteralPath (Join-Path $dir 'go.mod')) {
            return $true
        }
        $parent = Split-Path -Parent $dir
        if ([string]::IsNullOrWhiteSpace($parent) -or $parent -eq $dir) {
            break
        }
        $dir = $parent -replace '\\', '/'
    }

    return $false
}

function Test-AgentSkillPath {
    param([string]$NormalizedPath)

    return $NormalizedPath -match '^\.(agents|codex|cursor|kiro|opencode|pi)/skills/'
}

function Get-QualityPackages {
    $stagedGoFiles = @(git diff --cached --name-only --diff-filter=ACMRD 2>$null | Where-Object { $_ -match '\.go$' })
    if (-not $stagedGoFiles -or $stagedGoFiles.Count -eq 0) {
        return @("./...")
    }

    $forceFull = $false
    $packageSet = [System.Collections.Generic.HashSet[string]]::new()

    foreach ($file in $stagedGoFiles) {
        $normalized = $file -replace '\\', '/'
        if (Test-AgentSkillPath $normalized) {
            continue
        }
        $dir = Split-Path -Parent $normalized
        if ([string]::IsNullOrWhiteSpace($dir) -or $dir -eq '.') {
            $forceFull = $true
            break
        }
        if (Test-UnderNestedGoModule $normalized) {
            continue
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
$unformatted = @(Invoke-TaskRunner -Label "quality-checks:gofmt" -Cwd $RepositoryRoot -Timeout "2m" -Output capture -Command @("gofmt", "-l", ".") 2>$null |
    Where-Object { $_ -and -not (Test-AgentSkillPath ($_ -replace '\\', '/')) })
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
$null = Invoke-QualityChild "go-mod-tidy" @("go", "mod", "tidy") -Timeout "3m"
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
$shouldVerifyModuleCache = $false
if ($env:LIP_VERIFY_MODULE_CACHE -match '^(?i:1|true|yes|on)$') {
    $shouldVerifyModuleCache = $true
} elseif ($env:CI -match '^(?i:1|true|yes|on)$') {
    $shouldVerifyModuleCache = $true
}

if ($shouldVerifyModuleCache) {
    Write-Host "Verifying module checksums..." -ForegroundColor DarkGray
    go mod verify
    if ($LASTEXITCODE -ne 0) {
        Write-Host "ERROR: go mod verify failed (checksum mismatch or corrupt module cache)" -ForegroundColor Red
        exit $LASTEXITCODE
    }
} else {
    Write-Host "Skipping module cache verification locally (set LIP_VERIFY_MODULE_CACHE=1 to enable)." -ForegroundColor DarkGray
}
Write-Host "OK: Module check passed" -ForegroundColor Green
Write-Host ""

if ($env:LIP_SKIP_GO_COMPILE_CHECKS -eq "1") {
    Write-Host "Skipping standalone build/vet: the following go test target owns compilation and curated vet checks." -ForegroundColor DarkGray
} else {
    Write-Host "[3/7] Checking build..." -ForegroundColor Yellow
    $null = Invoke-QualityChild "build" (@("go", "build") + $qualityPackages)
    Write-Host "OK: Build check passed" -ForegroundColor Green
    Write-Host ""

    Write-Host "[4/7] Running go vet..." -ForegroundColor Yellow
    $null = Invoke-QualityChild "vet" (@("go", "vet") + $qualityPackages)
    Write-Host "OK: Vet check passed" -ForegroundColor Green
    Write-Host ""
}

Write-Host "[5-7/7] Running independent guardrails in parallel..." -ForegroundColor Yellow
$guardJobs = @(
    @{ Label = "adhoc-goroutines"; Command = @("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "$PSScriptRoot/check-adhoc-goroutines.ps1") },
    @{ Label = "regex-hotpath"; Command = @("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "$PSScriptRoot/regex-hotpath-check.ps1") }
)
if ($env:LIP_SKIP_ARCHTEST -ne "1") {
    # Match the test-unit flags (make GO_TEST_FLAGS) so the standalone
    # quality-checks archtest run shares Go's build/test cache with
    # subsequent `make test`/`make qa` executions (see #291).
    $guardJobs += @{ Label = "archtest"; Command = @("go", "test", "-parallel=8", "-timeout=10m", "./internal/archtest/...") }
}
$runnerBinary = Get-TaskRunnerBinary
$jobResults = @()
foreach ($guard in $guardJobs) {
    $jobResults += Start-Job -ScriptBlock {
        param($runnerScript, $runnerPath, $repoRoot, $label, $command)
        $env:LIP_TASKRUNNER_NO_CLEANUP = "1"
        . $runnerScript
        $script:TaskRunnerBinary = $runnerPath
        Invoke-TaskRunner -Label "quality-checks:$label" -Cwd $repoRoot -Timeout "5m" -Command $command | Out-Host
    } -ArgumentList (Join-Path $PSScriptRoot "taskrunner.ps1"), $runnerBinary, $RepositoryRoot, $guard.Label, $guard.Command
}

$guardFailure = $false
foreach ($job in $jobResults) {
    Wait-Job $job | Out-Null
}
foreach ($job in $jobResults) {
    $jobOutput = @(Receive-Job $job -ErrorAction SilentlyContinue)
    $jobOutput | ForEach-Object { Write-Host $_ }
    if ($job.State -ne "Completed" -or @($job.ChildJobs | Where-Object { $_.State -ne "Completed" }).Count -gt 0) {
        $guardFailure = $true
    }
    Remove-Job $job -Force -ErrorAction SilentlyContinue
}
if ($guardFailure) {
    throw "one or more parallel quality guardrails failed"
}
Write-Host ""

Write-Host "=== All Quality Checks Passed ===" -ForegroundColor Green
exit 0
