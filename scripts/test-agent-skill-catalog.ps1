[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$checker = Join-Path $PSScriptRoot 'check-agent-skill-catalog.ps1'

if (-not (Test-Path -LiteralPath $checker -PathType Leaf)) {
    throw "Catalog checker is missing: $checker"
}

$temporaryBase = [System.IO.Path]::GetTempPath()
$fixtureRoot = Join-Path $temporaryBase ("lip-agent-skill-catalog-test-{0}" -f [guid]::NewGuid())
$resolvedTemporaryBase = [System.IO.Path]::GetFullPath($temporaryBase)
$resolvedFixtureRoot = [System.IO.Path]::GetFullPath($fixtureRoot)

if (-not $resolvedFixtureRoot.StartsWith($resolvedTemporaryBase, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Fixture path escapes the temporary directory: $resolvedFixtureRoot"
}

function Invoke-CatalogCheck {
    param([string]$Root)

    $previousErrorActionPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $output = & pwsh -NoProfile -File $checker -RepositoryRoot $Root 2>&1
    $exitCode = $LASTEXITCODE
    $ErrorActionPreference = $previousErrorActionPreference
    return [pscustomobject]@{
        ExitCode = $exitCode
        Output   = ($output -join [Environment]::NewLine)
    }
}

function Write-Utf8NoBom {
    param([string]$Path, [string]$Content)

    [System.IO.File]::WriteAllText($Path, $Content, [System.Text.UTF8Encoding]::new($false))
}

try {
    $skillDirectory = Join-Path $fixtureRoot '.agents\skills\example-skill'
    New-Item -ItemType Directory -Path $skillDirectory -Force | Out-Null

    Write-Utf8NoBom -Path (Join-Path $fixtureRoot '.agents\catalog.json') -Content @'
{
  "version": 1,
  "groups": {
    "test": [
      "example-skill"
    ]
  }
}
'@

    Write-Utf8NoBom -Path (Join-Path $skillDirectory 'SKILL.md') -Content @'
---
name: example-skill
description: Example skill used by the catalog validator test.
---

# Example
'@

    $valid = Invoke-CatalogCheck -Root $fixtureRoot
    if ($valid.ExitCode -ne 0) {
        throw "Valid catalog was rejected:`n$($valid.Output)"
    }

    $duplicateDirectory = Join-Path $fixtureRoot '.codex\skills\example-skill'
    New-Item -ItemType Directory -Path $duplicateDirectory -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $skillDirectory 'SKILL.md') -Destination (Join-Path $duplicateDirectory 'SKILL.md')

    $duplicate = Invoke-CatalogCheck -Root $fixtureRoot
    if ($duplicate.ExitCode -eq 0 -or $duplicate.Output -notmatch 'legacy duplicate') {
        throw "Legacy duplicate was not rejected:`n$($duplicate.Output)"
    }

    Remove-Item -LiteralPath (Join-Path $fixtureRoot '.codex') -Recurse -Force

    Write-Utf8NoBom -Path (Join-Path $skillDirectory 'SKILL.md') -Content @'
---
name: example-skill
description: Example skill used by the catalog validator test.
allowed-tools: Read
---

# Example
'@

    $unsupported = Invoke-CatalogCheck -Root $fixtureRoot
    if ($unsupported.ExitCode -eq 0 -or $unsupported.Output -notmatch 'unsupported frontmatter') {
        throw "Unsupported frontmatter was not rejected:`n$($unsupported.Output)"
    }

    "PASS: agent skill catalog validator"
}
finally {
    if (Test-Path -LiteralPath $fixtureRoot) {
        Remove-Item -LiteralPath $fixtureRoot -Recurse -Force
    }
}
