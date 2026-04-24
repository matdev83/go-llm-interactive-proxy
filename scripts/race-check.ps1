# race-check.ps1 — Windows shim for `make test-race` and the pre-commit quality gate.
#
# Go race detector runs (`go test -race`) are disabled on Windows in this repository
# (ThreadSanitizer / toolchain friction). Use `bash scripts/race-check.sh` on Linux or
# macOS, or rely on CI (`.github/workflows/qa.yml` runs `race-check.sh --strict`).
#
# Parameters: `-Staged` is reserved for call-site compatibility; `-Strict` fails the script
# so CI/local scripts do not treat Windows as "race clean" when strict mode is requested.

param(
    [switch]$Staged = $false,
    [switch]$Strict = $false
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    Write-Host "ERROR: race-check.ps1 is for Windows only; use: bash scripts/race-check.sh [--staged|--strict]" -ForegroundColor Red
    exit 2
}

Write-Host "Skipping Go race detector on Windows (disabled in this repository). CI runs race checks on Linux." -ForegroundColor Yellow
if ($Strict) {
    Write-Host "ERROR: -Strict requires `bash scripts/race-check.sh --strict` (Linux/macOS) or CI." -ForegroundColor Red
    exit 1
}
exit 0
