# One-off / maintainer: create native fuzz seed corpus files under each package's testdata/fuzz/.
# Safe to re-run (overwrites same seed filenames only).
Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

function Write-PackedUtf8([string] $relPath, [string] $json) {
    $full = Join-Path $root $relPath
    New-Item -ItemType Directory -Force -Path (Split-Path $full) | Out-Null
    $body = [System.Text.Encoding]::UTF8.GetBytes($json)
    $packed = New-Object byte[] ($body.Length + 1)
    $packed[0] = 0
    [Array]::Copy($body, 0, $packed, 1, $body.Length)
    [IO.File]::WriteAllBytes($full, $packed)
}

Write-PackedUtf8 "internal\plugins\frontends\openairesponses\testdata\fuzz\FuzzDecodeCreateRequest\seed_packed" '{"model":"gpt-4o-mini","input":"x"}'
Write-PackedUtf8 "internal\plugins\frontends\anthropic\testdata\fuzz\FuzzDecodeMessageRequest\seed_packed" '{"model":"claude-3-5-haiku-20241022","max_tokens":10,"messages":[{"role":"user","content":"x"}]}'
Write-PackedUtf8 "internal\plugins\frontends\gemini\testdata\fuzz\FuzzDecodeGenerateContentRequest\seed_packed" '{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}'
Write-PackedUtf8 "internal\plugins\frontends\openailegacy\testdata\fuzz\FuzzDecodeChatRequest\seed_packed" '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}]}'

$hooksDir = Join-Path $root "internal\core\hooks\testdata\fuzz\FuzzHookMutationValidators"
New-Item -ItemType Directory -Force -Path $hooksDir | Out-Null
# b=98 -> 98%2==0 (call branch), x -> text "x"
[IO.File]::WriteAllBytes((Join-Path $hooksDir "seed_call_branch"), [byte[]](98, 120))
# odd first byte -> event branch
[IO.File]::WriteAllBytes((Join-Path $hooksDir "seed_event_branch"), [byte[]](99, 0))

Write-Host "fuzz corpus bytes written under internal/.../testdata/fuzz/"
