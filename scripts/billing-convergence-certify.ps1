$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

function Run-Step([string]$Label, [string[]]$Arguments, [hashtable]$Environment = @{}) {
    Write-Host "`n==> $Label"
    $saved = @{}
    foreach ($key in $Environment.Keys) {
        $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
        [Environment]::SetEnvironmentVariable($key, [string]$Environment[$key], 'Process')
    }
    try {
        & $Arguments[0] $Arguments[1..($Arguments.Count - 1)]
        if ($LASTEXITCODE -ne 0) { throw "$Label failed with exit code $LASTEXITCODE" }
    }
    finally {
        foreach ($key in $Environment.Keys) {
            [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process')
        }
    }
}

Set-Location $root
Run-Step 'billing focused suites' @('go', 'test', './internal/core/billing', './internal/core/runtime', './internal/core/usageauthority/...', './internal/infra/billingstore', './internal/infra/billingspool', './internal/infra/billingcompose', './internal/infra/billingadmission', './internal/infra/runtimebundle', './internal/archtest', '-run', 'Billing|UsageAuthority|Usage|Spool|Compose|Admission|Runtime|Migration|Outbox|Wiring|Construction|Deletion|LOC|Symbol', '-count=1')
Run-Step 'predecessor billing regressions' @('go', 'test', './internal/core/billing', './internal/core/runtime', './internal/infra/billingstore', './internal/infra/runtimebundle', '-count=1')

if ($env:LIP_REQUIRE_POSTGRES -eq '1') {
    $dsn = if ($env:LIP_TEST_POSTGRES_DSN) { $env:LIP_TEST_POSTGRES_DSN } else { $env:LIP_TEST_POSTGRES_ADMIN_DSN }
    if (-not $dsn) { throw 'FAIL: LIP_REQUIRE_POSTGRES=1 but no PostgreSQL DSN is configured' }
    Run-Step 'PostgreSQL billingstore integration' @('go', 'test', '-tags=integration', './internal/infra/billingstore', '-run', 'Postgres', '-count=1') @{ LIP_REQUIRE_POSTGRES = '1'; LIP_TEST_POSTGRES_DSN = $dsn }
}
else {
    Write-Host 'SKIP: PostgreSQL billingstore integration (set LIP_REQUIRE_POSTGRES=1 to require it)'
}

Run-Step 'quality checks' @('make', 'quality-checks')
Run-Step 'unit tests' @('make', 'test-unit')
Run-Step 'documentation checks' @('make', 'docs-check')
if ((go env GOOS) -eq 'windows') {
    Write-Host 'SKIP: race certification is unavailable on Windows in this repository'
}
else {
    Run-Step 'race tests' @('make', 'test-race')
}
Run-Step 'full test suite' @('go', 'test', './...', '-count=1')
Run-Step 'diff whitespace' @('git', 'diff', '--check')
$goFiles = @((git diff --name-only --diff-filter=ACM -- '*.go') + (git ls-files --others --exclude-standard -- '*.go') | Where-Object { $_ } | Sort-Object -Unique)
$gofmt = @($goFiles | ForEach-Object { gofmt -l $_ } | Where-Object { $_ })
if ($gofmt.Count -ne 0) { throw "FAIL: changed Go files are not gofmt-clean: $($gofmt -join ', ')" }
$changedGo = $goFiles.Count
$changedFiles = @((git diff --name-only) + (git ls-files --others --exclude-standard) | Where-Object { $_ } | Sort-Object -Unique).Count
if ($changedGo -gt 100 -or $changedFiles -gt 100) { throw "FAIL: changed files exceed the 100-file checkpoint limit (Go=$changedGo total=$changedFiles)" }
Write-Host 'PASS: billing convergence certification completed; PostgreSQL/race status was reported explicitly'
