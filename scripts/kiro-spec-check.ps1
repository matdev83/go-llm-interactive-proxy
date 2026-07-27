param(
    [Parameter(Mandatory = $true)]
    [string]$Spec
)

$ErrorActionPreference = "Stop"
$env:KIRO_SPEC = $Spec
& go test -parallel=8 -timeout=10m ./tools/kiro/speccheck/ -run "^TestKiroSpec$" -count=1
exit $LASTEXITCODE
