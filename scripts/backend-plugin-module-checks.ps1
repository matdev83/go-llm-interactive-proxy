# Dynamic multi-module checks for connectors/* and connector-support/*.
# Avoids recursive make: this script is invoked by Make once.
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $Root
$env:GOWORK = "off"

Write-Host "== root go list/build/module graph =="
& go list ./... | Out-Null
& go build -o NUL ./cmd/lipstd
$modsAll = & go list -m all
if ($modsAll | Select-String -Pattern "connectors/|connector-support/") {
  Write-Error "root go list -m all must not contain connector modules"
}

Write-Host "== root go test ./... =="
& go test ./...
if ($LASTEXITCODE -ne 0) {
  Write-Error "root GOWORK=off go test ./... failed"
}

Write-Host "== discover modules =="
$discovered = & go run ./tools/backendplugin/discover_modules -root .
Write-Host ("discovered: " + ($discovered -join " "))

foreach ($mod in @($discovered)) {
  if (-not $mod) { continue }
  Write-Host "== module $mod =="
  Push-Location $mod
  try {
    & go list ./... | Out-Null
    & go test ./...
    if (Test-Path cmd) {
      Get-ChildItem cmd -Directory | ForEach-Object {
        & go build -o NUL ("./cmd/" + $_.Name)
      }
    }
    $imports = & go list -f "{{.ImportPath}} {{.Imports}} {{.TestImports}} {{.XTestImports}}" ./...
    if ($imports | Select-String -Pattern "go-llm-interactive-proxy/internal/") {
      Write-Error "$mod imports root internal/"
    }
  } finally {
    Pop-Location
  }
}

Write-Host "== synthetic connector discovery =="
$syn = Join-Path $Root "connectors\_synthetic_ci_probe"
New-Item -ItemType Directory -Force -Path $syn | Out-Null
Set-Content -Path (Join-Path $syn "go.mod") -Value @"
module github.com/matdev83/go-llm-interactive-proxy/connectors/_synthetic_ci_probe

go 1.26.5
"@
try {
  $found = & go run ./tools/backendplugin/discover_modules -root .
  if (-not ($found | Where-Object { $_ -eq "connectors/_synthetic_ci_probe" })) {
    Write-Error "synthetic connector not discovered"
  }
} finally {
  Remove-Item -Recurse -Force $syn -ErrorAction SilentlyContinue
}

Write-Host "== root build with connectors/ absent (temp copy) =="
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("golip-root-no-connectors-" + [guid]::NewGuid().ToString("n"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
  $robolog = Join-Path $tmp "robocopy.log"
  & robocopy $Root $tmp /E /NFL /NDL /NJH /NJS /nc /ns /np `
    /XD .git connectors connector-support .golip-package-staging .golip-plugins node_modules `
    /XF *.exe | Out-File $robolog
  if ($LASTEXITCODE -ge 8) {
    Write-Error "robocopy failed with exit $LASTEXITCODE"
  }
  Push-Location $tmp
  try {
    $env:GOWORK = "off"
    & go build -o NUL ./cmd/lipstd
    if ($LASTEXITCODE -ne 0) { Write-Error "lipstd build failed without connectors/" }
  } finally {
    Pop-Location
  }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

Write-Host "== acp mirror byte-identical files =="
$RootAcp = Join-Path $Root "internal/plugins/backends/acp"
$ConnAcp = Join-Path $Root "connector-support/acp"
$MirrorFiles = @(
  "acp_protocol.go", "call_extract_test.go", "call_extract.go", "cancel.go", "client.go",
  "connector_config_test.go", "doc.go", "handshake.go", "history_transcript_test.go", "history_transcript.go",
  "invoke_test.go", "invoke.go", "main_test.go", "model_index_test.go", "model_index.go",
  "process_identity_test.go", "process_identity_unix_test.go", "process_identity_unix.go",
  "process_identity_windows.go", "process_identity.go", "rpc_error_test.go", "rpc_error.go",
  "rpc_id.go", "rpc.go", "runtime_pool_claim_test.go", "runtime_pool_ensure_test.go", "runtime_pool_ensure.go",
  "runtime_pool_test.go", "runtime_pool.go", "server_handler.go", "server_request_test.go",
  "session_update_test.go", "session.go", "session_update.go", "stderr_sanitize_test.go", "stderr_sanitize.go",
  "subprocess_protocol.go", "subprocess_spec_test.go", "tool_sink.go", "tool_summary_test.go", "tool_summary.go",
  "transport_stdio_os_unix.go", "transport_stdio_os_windows.go", "transport_stdio_os.go", "transport_stdio.go",
  "workspace_test.go", "workspace.go"
)
foreach ($f in $MirrorFiles) {
  $a = Get-FileHash (Join-Path $RootAcp $f)
  $b = Get-FileHash (Join-Path $ConnAcp $f)
  if ($a.Hash -ne $b.Hash) {
    Write-Error "acp mirror drift (expected byte-identical): $f"
  }
}

Write-Host "OK backend-plugin-module-checks"
