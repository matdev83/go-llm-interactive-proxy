param(
    [Parameter(Mandatory = $true)][string]$Target
)

. "$PSScriptRoot/taskrunner.ps1"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$goTestFlags = @("-parallel=8", "-timeout=10m")
$localGoEnv = if ($env:LIP_DISABLE_VCS_STAMPING -eq "1" -and -not $env:GOFLAGS) { @("GOFLAGS=-buildvcs=false") } else { @() }

function Run-RootGoTest {
    param([string]$Label, [string[]]$TestArgs, [string[]]$Env = @(), [string]$Timeout = "15m")
    Invoke-TaskRunner -Label $Label -Cwd $root -Timeout $Timeout -Env (@($localGoEnv) + @($Env)) -Command (@("go", "test") + $TestArgs) | Out-Host
}

function Run-NestedGoTest {
    param([string]$Label, [string]$Directory, [string[]]$TestArgs, [string]$Timeout = "15m")
    Invoke-TaskRunner -Label $Label -Cwd (Join-Path $root $Directory) -Timeout $Timeout -Env (@($localGoEnv) + @("GOWORK=off")) -Command (@("go", "test") + $TestArgs + @("./...")) | Out-Host
}

function Run-RootGoTestWithMatches {
    param([string]$Label, [string]$Package, [string]$Pattern, [string[]]$TestArgs, [string[]]$Env = @(), [string]$Timeout = "15m")
    $listResult = Invoke-TaskRunner -Label "${Label}:list" -Cwd $root -Timeout $Timeout -Env (@($localGoEnv) + @($Env)) -Output capture -Command (@("go", "test", "-tags=integration", "-list", $Pattern, $Package))
    $matches = @($listResult | Where-Object { $_ -match '^Test[A-Za-z0-9_]+$' })
    if ($matches.Count -eq 0) {
        throw "$Label selector $Pattern matched zero tests in $Package"
    }
    Run-RootGoTest $Label $TestArgs $Env $Timeout
}

function Run-Parity {
    switch ($Target) {
        "parity-acp-plugin" {
            Run-NestedGoTest "parity-acp-plugin:connector-support/acp:test" "connector-support/acp" (@($goTestFlags) + @("-run", "KillProcessTree_|ProcessTree_CrossCompile|PID|Pool|Cancel|Open_|MapSession|Scripted"))
            Run-NestedGoTest "parity-acp-plugin:connectors/acp:test" "connectors/acp" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_"))
        }
        "parity-cursorcliacp-plugin" {
            Run-NestedGoTest "parity-cursorcliacp-plugin:connector-support/acp:test" "connector-support/acp" (@($goTestFlags) + @("-run", "KillProcessTree_|ProcessTree_CrossCompile"))
            Run-NestedGoTest "parity-cursorcliacp-plugin:connectors/cursorcliacp:test" "connectors/cursorcliacp" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_"))
        }
        "parity-cli-acp-plugins" {
            foreach ($module in @("connectors/geminicliacp", "connectors/agycliacp", "connectors/cursorcliacp")) {
                Run-NestedGoTest "parity-cli-acp-plugins:$module:test" $module (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_"))
            }
        }
        "parity-openrouter-plugin" {
            Run-NestedGoTest "parity-openrouter-plugin:connector-support/openaicompat:test" "connector-support/openaicompat" $goTestFlags
            Run-NestedGoTest "parity-openrouter-plugin:connectors/openrouter:test" "connectors/openrouter" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestBilling_"))
            Run-RootGoTest "parity-openrouter-plugin:internal/archtest:test" (@($goTestFlags) + @("-run", "OpenRouter|Phase7_"))
            Run-RootGoTest "parity-openrouter-plugin:internal/infra/runtimebundle:test" (@($goTestFlags) + @("-run", "TestPhase7_OpenRouter"))
        }
        "parity-hosted-compatible-plugins" {
            foreach ($module in @("connectors/nvidia", "connectors/huggingface")) {
                Run-NestedGoTest "parity-hosted-compatible-plugins:$module:test" $module (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_"))
            }
            Run-RootGoTest "parity-hosted-compatible-plugins:internal/archtest:test" (@($goTestFlags) + @("-run", "Phase7_"))
        }
        "parity-ollama-plugins" { Run-NestedGoTest "parity-ollama-plugins:connectors/ollama:test" "connectors/ollama" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) }
        "parity-opencode-plugins" {
            Run-NestedGoTest "parity-opencode-plugins:connectors/opencode:test" "connectors/opencode" (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_"))
            Run-RootGoTest "parity-opencode-plugins:internal/archtest:test" (@($goTestFlags) + @("-run", "OpenCode|Phase8_"))
        }
        "parity-codex-plugins" {
            Run-NestedGoTest "parity-codex-plugins:connectors/codex:test" "connectors/codex" $goTestFlags
            Run-RootGoTest "parity-codex-plugins:internal/archtest:test" (@($goTestFlags) + @("./internal/archtest", "-run", "Codex|Phase8_.*Codex|TestCodex_"))
            Run-RootGoTest "parity-codex-plugins:internal/infra/runtimebundle:test" (@($goTestFlags) + @("./internal/infra/runtimebundle", "-run", "TestPhase8_Codex"))
        }
        "test-local-compatible-plugin-modules" { Run-LocalCompatibleModules }
        "parity-local-compatible-plugins" {
            Run-LocalCompatibleModules
            Run-RootGoTest "parity-local-compatible-plugins:internal/archtest:test" (@($goTestFlags) + @("./internal/archtest", "-run", "Phase7_"))
        }
    }
}

function Run-NestedGoTestsInParallel {
    param(
        [Parameter(Mandatory = $true)][array]$Items,
        [string]$Timeout = "15m"
    )
    $runnerBinary = Get-TaskRunnerBinary
    $concurrency = [Math]::Max(2, [Math]::Min(6, $Items.Count))

    $sessionState = [System.Management.Automation.Runspaces.InitialSessionState]::CreateDefault()
    $pool = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspacePool(1, $concurrency, $sessionState, $Host)
    $pool.Open()

    $tasks = [System.Collections.Generic.List[PSObject]]::new()
    try {
        foreach ($item in $Items) {
            $label = $item.Label
            $dir = Join-Path $root $item.Directory
            $cmdArgs = @("go", "test") + $item.TestArgs + @("./...")

            $ps = [System.Management.Automation.PowerShell]::Create()
            $ps.RunspacePool = $pool
            [void]$ps.AddScript({
                param($runnerBinary, $label, $cwd, $timeout, $envList, $cmdArgs)
                $runnerArgs = @(
                    "--label", $label,
                    "--cwd", $cwd,
                    "--timeout", $timeout,
                    "--output", "capture"
                )
                foreach ($e in $envList) {
                    $runnerArgs += @("--env", $e)
                }
                $runnerArgs += "--"
                $runnerArgs += $cmdArgs

                $output = @(& $runnerBinary @runnerArgs 2>&1)
                $exitCode = $LASTEXITCODE
                return @{
                    Label = $label
                    ExitCode = $exitCode
                    Output = $output
                }
            }).AddArgument($runnerBinary).AddArgument($label).AddArgument($dir).AddArgument($Timeout).AddArgument((@($localGoEnv) + @("GOWORK=off"))).AddArgument($cmdArgs)

            $asyncResult = $ps.BeginInvoke()
            $tasks.Add([PSCustomObject]@{
                PowerShell = $ps
                AsyncResult = $asyncResult
                Label = $label
            })
        }

        $failed = $false
        foreach ($task in $tasks) {
            $res = $task.PowerShell.EndInvoke($task.AsyncResult)
            $task.PowerShell.Dispose()
            if ($res) {
                $exitCode = $res[0].ExitCode
                $output = $res[0].Output
                if ($output) { $output | ForEach-Object { Write-Host $_ } }
                if ($exitCode -ne 0) {
                    $failed = $true
                }
            }
        }
        if ($failed) {
            throw "one or more nested module tests failed"
        }
    }
    finally {
        $pool.Close()
        $pool.Dispose()
    }
}

function Run-LocalCompatibleModules {
    $modules = @(
        @{ Label = "test-local-compatible-plugin-modules:connectors/llamacpp:test"; Directory = "connectors/llamacpp"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) },
        @{ Label = "test-local-compatible-plugin-modules:connectors/lmstudio:test"; Directory = "connectors/lmstudio"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) },
        @{ Label = "test-local-compatible-plugin-modules:connectors/vllm:test"; Directory = "connectors/vllm"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) }
    )
    Run-NestedGoTestsInParallel $modules
}

function Run-Fuzz {
    $fuzzTime = if ($env:FUZZTIME) { $env:FUZZTIME } else { "500ms" }
    $fuzz = @(
        @("FuzzJSONRoundTrip", "./internal/testkit"), @("FuzzParseSnapshot", "./internal/infra/modelcatalog/modelsdev"),
        @("FuzzParseSelector", "./internal/core/routing"), @("FuzzParseSelectorFromBytes", "./internal/core/routing"),
        @("FuzzDecodeCreateRequest", "./internal/plugins/frontends/openairesponses"), @("FuzzDecodeMessageRequest", "./internal/plugins/frontends/anthropic"),
        @("FuzzDecodeGenerateContentRequest", "./internal/plugins/frontends/gemini"), @("FuzzDecodeChatRequest", "./internal/plugins/frontends/openailegacy"),
        @("FuzzWriteNonStreamJSON_toolArguments", "./internal/plugins/frontends/anthropic"), @("FuzzBuildGenerateContentResponse_toolJSON", "./internal/plugins/frontends/gemini"),
        @("FuzzCallValidateJSON", "./pkg/lipapi"), @("FuzzSemanticExtensionValidation", "./pkg/lipapi"), @("FuzzMergeRouteQueryGenerationOptions", "./pkg/lipapi"), @("FuzzCollectWithLimitsProgram", "./pkg/lipapi"),
        @("FuzzStableCallIdentity", "./internal/core/diag"), @("FuzzParamsForCall", "./internal/plugins/backends/openairesponses"),
        @("FuzzHandleResponseStreamUnion", "./internal/plugins/backends/openairesponses"), @("FuzzBuildToolsParametersJSON", "./internal/plugins/backends/openairesponses"),
        @("FuzzHandleMessageStreamEventUnion", "./internal/plugins/backends/protocols/anthropicmessages"), @("FuzzToolInputSchemaParametersJSON", "./internal/plugins/backends/protocols/anthropicmessages"),
        @("FuzzHandleChatCompletionChunk", "./internal/plugins/backends/openailegacy"), @("FuzzBuildChatToolsParametersJSON", "./internal/plugins/backends/openailegacy"),
        @("FuzzHandleGenerateContentResponse", "./internal/plugins/backends/protocols/geminigenerate"), @("FuzzBuildToolsParametersJSON", "./internal/plugins/backends/protocols/geminigenerate"),
        @("FuzzMessageToContentToolResultJSON", "./internal/plugins/backends/protocols/geminigenerate"), @("FuzzAssistantPartsToContentBlocksJSON", "./internal/plugins/backends/bedrock"),
        @("FuzzHookMutationValidators", "./internal/core/hooks"), @("FuzzManifest", "./internal/infra/backendplugins/manifest"), @("FuzzServerFrame", "./pkg/lipsdk/backendplugin"),
        @("FuzzAcceptClientUserAgent", "./internal/core/identity"), @("FuzzAcceptClientAppURL", "./internal/core/identity"), @("FuzzAcceptClientAppTitle", "./internal/core/identity"),
        @("FuzzValidateIdentityYAML", "./internal/core/identity"), @("FuzzCaptureClientUserAgent", "./internal/plugins/frontends/identitywire"),
        @("FuzzCompleteJSONSuffix", "./internal/core/toolcallrepair"), @("FuzzSchemaPreScanCompile", "./internal/core/toolcallrepair"), @("FuzzEngineRepair", "./internal/core/toolcallrepair"),
        @("FuzzComputeAnchor", "./internal/plugins/features/reasoningpreservation"), @("FuzzDecodeConfig", "./internal/plugins/features/reasoningpreservation"),
        @("FuzzLeaseSet_OccupiesCapacity", "./internal/core/concurrencyauthority/domain"), @("FuzzIsAmbiguousRenewError", "./internal/core/concurrencyauthority/app"),
        @("FuzzWorkItem_TransitionSequence", "./internal/core/terminalwork"), @("FuzzOwner_CommandSequences", "./internal/core/terminal"), @("FuzzParseDecimalToNano", "./pkg/lipsdk/economics"),
        @("FuzzPhase32_SourceEventKey_DelimiterSafety", "./pkg/lipsdk/metering"), @("FuzzPhase32_MoneyPresentCurrency", "./pkg/lipsdk/metering"),
        @("FuzzDecodeLine", "./internal/product/protocol"), @("FuzzMapBridgeEvent", "./internal/product"),
        @("FuzzParseNDJSONLine", "./"), @("FuzzMapSessionUpdateToEvents", "./"), @("FuzzMergeHandshakeProfileExtensions", "./")
    )
    $runnerBinary = Get-TaskRunnerBinary
    $concurrency = [Math]::Max(2, [Math]::Min(8, [Environment]::ProcessorCount))

    $sessionState = [System.Management.Automation.Runspaces.InitialSessionState]::CreateDefault()
    $pool = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspacePool(1, $concurrency, $sessionState, $Host)
    $pool.Open()

    $tasks = [System.Collections.Generic.List[PSObject]]::new()
    try {
        foreach ($item in $fuzz) {
            $cwd = $root
            $env = @()
            $package = $item[1]
            if ($item[0] -in @("FuzzParseNDJSONLine", "FuzzMapSessionUpdateToEvents", "FuzzMergeHandshakeProfileExtensions")) {
                $cwd = Join-Path $root "connector-support/acp"
                $env = @("GOWORK=off")
                $package = "."
            }
            elseif ($item[0] -in @("FuzzDecodeLine", "FuzzMapBridgeEvent")) {
                $cwd = Join-Path $root "connectors/cursorsdk"
                $env = @("GOWORK=off")
            }

            $ps = [System.Management.Automation.PowerShell]::Create()
            $ps.RunspacePool = $pool
            [void]$ps.AddScript({
                param($runnerBinary, $label, $cwd, $envList, $cmdArgs)
                $runnerArgs = @(
                    "--label", $label,
                    "--cwd", $cwd,
                    "--timeout", "10m",
                    "--output", "capture"
                )
                foreach ($e in $envList) {
                    $runnerArgs += @("--env", $e)
                }
                $runnerArgs += "--"
                $runnerArgs += $cmdArgs

                $output = @(& $runnerBinary @runnerArgs 2>&1)
                $exitCode = $LASTEXITCODE
                return @{
                    Label = $label
                    ExitCode = $exitCode
                    Output = $output
                }
            }).AddArgument($runnerBinary).AddArgument("test-fuzz:$($item[0])").AddArgument($cwd).AddArgument((@($localGoEnv) + @($env))).AddArgument(@("go", "test", "-fuzz=^$($item[0])$", "-fuzztime=$fuzzTime", "-run=^$", $package))

            $asyncResult = $ps.BeginInvoke()
            $tasks.Add([PSCustomObject]@{
                PowerShell = $ps
                AsyncResult = $asyncResult
                Label = "test-fuzz:$($item[0])"
            })
        }

        $failed = $false
        foreach ($task in $tasks) {
            $res = $task.PowerShell.EndInvoke($task.AsyncResult)
            $task.PowerShell.Dispose()
            if ($res) {
                $exitCode = $res[0].ExitCode
                $output = $res[0].Output
                if ($exitCode -ne 0) {
                    Write-Host "FAILED: $($task.Label) (exit $exitCode)" -ForegroundColor Red
                    if ($output) { $output | ForEach-Object { Write-Host $_ -ForegroundColor Red } }
                    $failed = $true
                } else {
                    Write-Host "PASS: $($task.Label)" -ForegroundColor Green
                }
            }
        }
        if ($failed) {
            throw "one or more fuzz tests failed"
        }
    }
    finally {
        $pool.Close()
        $pool.Dispose()
    }
}

switch -Regex ($Target) {
    "^test-unit$" { Run-RootGoTest "test-unit:root" (@($goTestFlags) + @("./...")); break }
    "^qa-tests$" {
        $envOverride = @("LIP_TEST_POSTGRES_DSN=", "LIP_TEST_POSTGRES_ADMIN_DSN=", "LIP_MANAGED_POSTGRES_DSN=", "LIP_MIGRATION_POSTGRES_DSN=")
        if ($env:LIP_SKIP_QA_TESTS -match '^(?i:1|true|yes|on)$') {
            Write-Host "Skipping duplicate root tests pass (LIP_SKIP_QA_TESTS=1); running tagged precommit/integration delta packages only..." -ForegroundColor DarkGray
            Run-RootGoTest "qa-tests:precommit-delta" (@($goTestFlags) + @("-tags=precommit,integration", "./internal/qa/...", "./internal/core/runtime/...", "./internal/stdhttp/...")) $envOverride
            break
        }
        Run-RootGoTest "qa-tests:root" (@($goTestFlags) + @("-tags=precommit,integration", "./...")) $envOverride
        break
    }
    "^test-fuzz$" { Run-Fuzz; break }
    "^parity-checks$" {
        Run-RootGoTest "parity-checks:contract" (@($goTestFlags) + @("./internal/testkit/contract/..."))
        Run-RootGoTest "parity-checks:profiles" (@($goTestFlags) + @("./internal/providerprofiles/..."))
        Run-RootGoTest "parity-checks:connector-contract" (@($goTestFlags) + @("./pkg/lipsdk/backendplugin/contracttest/..."))
        Run-RootGoTest "parity-checks:conformance" (@($goTestFlags) + @("-tags=precommit,integration", "./internal/testkit/conformance/..."))
        Run-RootGoTest "parity-checks:compatible" (@($goTestFlags) + @("./internal/testkit/compatibleparity/...", "-run", "CompatibleParity"))

        $nestedSuites = @(
            @{ Label = "parity-checks:connector-support/acp"; Directory = "connector-support/acp"; TestArgs = (@($goTestFlags) + @("-run", "KillProcessTree_|ProcessTree_CrossCompile|PID|Pool|Cancel|Open_|MapSession|Scripted")) },
            @{ Label = "parity-checks:connectors/acp"; Directory = "connectors/acp"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_")) },
            @{ Label = "parity-checks:connector-support/openaicompat"; Directory = "connector-support/openaicompat"; TestArgs = $goTestFlags },
            @{ Label = "parity-checks:connectors/openrouter"; Directory = "connectors/openrouter"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestBilling_")) },
            @{ Label = "parity-checks:connectors/nvidia"; Directory = "connectors/nvidia"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) },
            @{ Label = "parity-checks:connectors/huggingface"; Directory = "connectors/huggingface"; TestArgs = (@($goTestFlags) + @("-run", "TestParity_|TestDescribe_|TestConfigure_|TestInventory_")) }
        )
        Run-NestedGoTestsInParallel $nestedSuites

        Run-RootGoTest "parity-checks:sentinel" (@($goTestFlags) + @("-tags=integration", "./internal/testkit/conformance", "-run", "^TestBoundedSentinel")); break
    }
    "^parity-|^test-local-compatible-plugin-modules$" {
        Run-Parity
        break
    }
    "^backend-plugin-security-checks$" {
        foreach ($pkg in @("./internal/infra/backendplugins/...", "./pkg/lipsdk/backendplugin/...", "./internal/infra/diagredact/...")) { Run-RootGoTest "backend-plugin-security-checks:$pkg" (@($goTestFlags) + @($pkg)) }
        Run-RootGoTest "backend-plugin-security-checks:runtimebundle" (@($goTestFlags) + @("./internal/infra/runtimebundle/", "-run", "TestBuild_localOnly|TestBuild_unknownBackendCredential|TestBuild_oauthUser|TestBuild_unsupportedBackend|TestBuild_staticBackend|TestBuild_noneBackend|TestBuild_strictAuthoritative"))
        Run-RootGoTest "backend-plugin-security-checks:docs" (@($goTestFlags) + @("./docs/backend-plugins/", "-run", "TestThreat|TestOperator_|TestDocs_"))
        break
    }
    "^backend-plugin-cross-platform-qa$" {
        $selectArgs = @()
        if ($env:CROSS_PLATFORM_SELECT) { $selectArgs = @("-select", $env:CROSS_PLATFORM_SELECT) }
        Invoke-TaskRunner -Label $Target -Cwd $root -Timeout "20m" -Env $localGoEnv -Command (@("go", "run", "./tools/backendplugin/crossplatform_qa", "-root", ".", "-out", ".golip-crossplatform-matrix.json", "-skip-native") + $selectArgs) | Out-Host
        Run-RootGoTest "backend-plugin-cross-platform-qa:backendplugins" (@($goTestFlags) + @("./internal/infra/backendplugins/...", "-run", "TestAdversarial_|TestActivate_|TestStream_|TestDigest|TestManifest|TestDiscover|TestShutdown|TestReap|TestPeer|TestChannel|TestExact|TestUpgrade|TestRollback|TestUninstall|TestConfig|TestSecrecy|TestUnauthorized|TestProtected|TestLaunch|TestKill|TestCancel"))
        Run-RootGoTest "backend-plugin-cross-platform-qa:backendplugin-sdk" (@($goTestFlags) + @("./pkg/lipsdk/backendplugin/...", "-run", "Test"))
        Run-NestedGoTest "backend-plugin-cross-platform-qa:connector-support/acp:test" "connector-support/acp" (@($goTestFlags) + @("-run", "KillProcessTree_|ProcessTree_CrossCompile|Cancel"))
        Run-RootGoTestWithMatches "backend-plugin-cross-platform-qa:tools" "./tools/backendplugin/" "TestCrossPlatformQA_|TestPackage_|TestDiscoverModules_" (@($goTestFlags) + @("-tags=integration", "./tools/backendplugin/", "-run", "TestCrossPlatformQA_|TestPackage_|TestDiscoverModules_"))
        Run-RootGoTest "backend-plugin-cross-platform-qa:archtest" (@($goTestFlags) + @("./internal/archtest/", "-run", "TestBackendPluginCrossPlatform_"))
        Run-RootGoTest "backend-plugin-cross-platform-qa:processhost" (@($goTestFlags) + @("./internal/infra/backendplugins/processhost/", "-run", "TestHostSecureProfiles_"))
        break
    }
    "^backend-plugin-release-gates-static$" {
        Invoke-TaskRunner -Label $Target -Cwd $root -Timeout "15m" -Command @("go", "run", "./tools/backendplugin/release_gates", "-root", ".", "-out", ".golip-release-gates-report.json", "-mode=static") | Out-Host
        Run-RootGoTestWithMatches "backend-plugin-release-gates-static:tools" "./tools/backendplugin/" "TestReleaseGates_" (@($goTestFlags) + @("-tags=integration", "./tools/backendplugin/", "-run", "TestReleaseGates_"))
        Run-RootGoTest "backend-plugin-release-gates-static:release-gates" (@($goTestFlags) + @("./tools/backendplugin/release_gates/", "-run", "TestParseRequirementIDs_|TestListMatchingTests_|TestValidateSelectors_"))
        Run-RootGoTest "backend-plugin-release-gates-static:archtest" (@($goTestFlags) + @("./internal/archtest/", "-run", "TestBackendPluginReleaseGates_"))
        break
    }
    "^backend-plugin-release-gates$" {
        Invoke-TaskRunner -Label $Target -Cwd $root -Timeout "120m" -Command @("go", "run", "./tools/backendplugin/release_gates", "-root", ".", "-out", ".golip-release-gates-report.json", "-mode=full") | Out-Host
        break
    }
    "^lint$" {
        if (Get-Command golangci-lint -ErrorAction SilentlyContinue) { Invoke-TaskRunner -Label "lint:golangci-lint" -Cwd $root -Timeout "10m" -Command @("golangci-lint", "run") | Out-Host }
        elseif (Get-Command staticcheck -ErrorAction SilentlyContinue) { Invoke-TaskRunner -Label "lint:staticcheck" -Cwd $root -Timeout "10m" -Command @("staticcheck", "./...") | Out-Host }
        else { throw "Install golangci-lint (preferred) or staticcheck and ensure it is on PATH." }
        break
    }
    "^vuln$" { Invoke-TaskRunner -Label "vuln:govulncheck" -Cwd $root -Timeout "10m" -Command @("go", "tool", "govulncheck", "./...") | Out-Host; break }
    default { throw "unsupported Windows task target: $Target" }
}
