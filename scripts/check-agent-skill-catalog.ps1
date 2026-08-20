[CmdletBinding()]
param(
    [string]$RepositoryRoot
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($RepositoryRoot)) {
    $RepositoryRoot = Split-Path -Parent $PSScriptRoot
}
$repositoryRootPath = [System.IO.Path]::GetFullPath($RepositoryRoot)
$catalogRoot = Join-Path $repositoryRootPath '.agents\skills'
$manifestPath = Join-Path $repositoryRootPath '.agents\catalog.json'
$errors = [System.Collections.Generic.List[string]]::new()

if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    Write-Error "Catalog manifest is missing: $manifestPath"
    exit 1
}

if (-not (Test-Path -LiteralPath $catalogRoot -PathType Container)) {
    Write-Error "Catalog directory is missing: $catalogRoot"
    exit 1
}

try {
    $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
}
catch {
    Write-Error "Catalog manifest is invalid JSON: $($_.Exception.Message)"
    exit 1
}

if ($manifest.version -ne 1) {
    $errors.Add("unsupported catalog version: $($manifest.version)")
}

$declaredNames = [System.Collections.Generic.List[string]]::new()
foreach ($group in $manifest.groups.PSObject.Properties) {
    foreach ($name in $group.Value) {
        $declaredNames.Add([string]$name)
    }
}

$duplicateDeclarations = $declaredNames | Group-Object | Where-Object Count -gt 1
foreach ($duplicate in $duplicateDeclarations) {
    $errors.Add("duplicate catalog declaration: $($duplicate.Name)")
}

$declared = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
foreach ($name in $declaredNames) {
    [void]$declared.Add($name)
}

$actualNames = Get-ChildItem -LiteralPath $catalogRoot -Directory |
    Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName 'SKILL.md') -PathType Leaf } |
    Select-Object -ExpandProperty Name

foreach ($name in $declared) {
    if ($actualNames -notcontains $name) {
        $errors.Add("declared skill is missing: $name")
    }
}

foreach ($name in $actualNames) {
    if (-not $declared.Contains($name)) {
        $errors.Add("undeclared skill directory: $name")
    }
}

$allowedFrontmatter = [System.Collections.Generic.HashSet[string]]::new(
    [string[]]@('name', 'description', 'license', 'metadata'),
    [System.StringComparer]::Ordinal
)

foreach ($name in $actualNames) {
    if ($name -notmatch '^[a-z0-9]+(?:-[a-z0-9]+)*$' -or $name.Length -gt 64) {
        $errors.Add("invalid skill directory name: $name")
        continue
    }

    $skillPath = Join-Path (Join-Path $catalogRoot $name) 'SKILL.md'
    $lines = Get-Content -LiteralPath $skillPath
    if ($lines.Count -lt 4 -or $lines[0] -ne '---') {
        $errors.Add("missing YAML frontmatter: $name")
        continue
    }

    $closingMarker = -1
    for ($index = 1; $index -lt $lines.Count; $index++) {
        if ($lines[$index] -eq '---') {
            $closingMarker = $index
            break
        }
    }

    if ($closingMarker -lt 2) {
        $errors.Add("unterminated YAML frontmatter: $name")
        continue
    }

    $fields = @{}
    for ($index = 1; $index -lt $closingMarker; $index++) {
        if ($lines[$index] -match '^([a-z][a-z0-9-]*):(?:\s*(.*))?$') {
            $field = $Matches[1]
            $value = $Matches[2]
            $fields[$field] = [pscustomobject]@{ Index = $index; Value = $value }
            if (-not $allowedFrontmatter.Contains($field)) {
                $errors.Add("unsupported frontmatter field '$field' in $name")
            }
        }
    }

    if (-not $fields.ContainsKey('name')) {
        $errors.Add("missing frontmatter name: $name")
    }
    else {
        $frontmatterName = $fields.name.Value.Trim('"', "'")
        if ($frontmatterName -cne $name) {
            $errors.Add("frontmatter name '$frontmatterName' does not match directory '$name'")
        }
    }

    if (-not $fields.ContainsKey('description')) {
        $errors.Add("missing frontmatter description: $name")
    }
    else {
        $description = $fields.description.Value.Trim()
        $hasFoldedBody = $description -match '^[>|][+-]?$' -and
            ($fields.description.Index + 1) -lt $closingMarker -and
            $lines[$fields.description.Index + 1] -match '^\s+\S'
        if ([string]::IsNullOrWhiteSpace($description) -or
            (($description -match '^[>|][+-]?$') -and -not $hasFoldedBody)) {
            $errors.Add("empty frontmatter description: $name")
        }
    }
}

$legacyRoots = @(
    '.codex\skills',
    '.cursor\skills',
    '.kiro\skills',
    '.opencode\skills',
    '.pi\skills'
)

foreach ($relativeRoot in $legacyRoots) {
    $legacyRoot = Join-Path $repositoryRootPath $relativeRoot
    if (-not (Test-Path -LiteralPath $legacyRoot -PathType Container)) {
        continue
    }

    foreach ($name in $declared) {
        $legacySkill = Join-Path (Join-Path $legacyRoot $name) 'SKILL.md'
        if (Test-Path -LiteralPath $legacySkill -PathType Leaf) {
            $errors.Add("legacy duplicate for '$name': $legacySkill")
        }
    }
}

if ($errors.Count -gt 0) {
    foreach ($message in $errors) {
        [Console]::Error.WriteLine("ERROR: $message")
    }
    exit 1
}

"PASS: $($declared.Count) canonical agent skills; no legacy duplicates"
