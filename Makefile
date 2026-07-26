.PHONY: help test test-fast test-unit test-precommit-extra test-postgres-migrations test-authority-postgres test-authority-postgres-direct test-authority-postgres-pooled qa-tests test-race test-fuzz test-reasoning-e2e-soak parity-checks parity-acp-plugin parity-cursorcliacp-plugin parity-cli-acp-plugins parity-openrouter-plugin parity-hosted-compatible-plugins parity-ollama-plugins parity-opencode-plugins parity-codex-plugins parity-local-compatible-plugins test-local-compatible-plugin-modules release-gates bench pgo-profile pgo-build quality-checks regex-hotpath-check arch-report qa vet lint vuln run hooks-install backend-plugin-module-checks backend-plugin-absence-checks backend-plugin-security-checks backend-plugin-cross-platform-qa backend-plugin-release-gates-static backend-plugin-release-gates package-minimal package-full package-plugin-smoke docs-check knowledge-check example-config-check backend-plugin-example-check kiro-spec-check isolated-root-qa installed-plugin-smoke test-cursor-sdk-live test-cursor-sdk-live-bridge test-cursor-sdk-platform test-cursor-sdk-comparison-report

GO ?= go
GO_TEST_FLAGS ?= -parallel=8 -timeout=10m

help:
	@echo "Targets:"
	@echo "  make quality-checks  - gofmt, go mod tidy (no drift), go build, go vet, guard scripts, archtest; mod verify in CI or with LIP_VERIFY_MODULE_CACHE=1"
	@echo "  make regex-hotpath-check - forbid regexp.MustCompile in frontends/runtime (see scripts/)"
	@echo "  make test            - quality-checks, full unit tests, and conformance parity checks"
	@echo "  make test-fast       - quality-checks then tests for staged packages (or all)"
	@echo "  make test-unit       - go test $(GO_TEST_FLAGS) ./... (excludes //go:build precommit tests)"
	@echo "  make test-postgres-migrations - apply and verify dual-plane PostgreSQL migrations"
	@echo "  make test-authority-postgres-direct - direct PostgreSQL runtime proof"
	@echo "  make test-authority-postgres-pooled - transaction-pooled runtime proof"
	@echo "  make test-authority-postgres - aggregate direct + pooled proof"
	@echo "  make test-precommit-extra - hygiene + executor matrices (-tags=precommit; also in pre-commit hook + CI)"
	@echo "  make test-race       - race scan (skipped on Windows; macOS/Linux: scripts/race-check.sh)"
	@echo "  make test-fuzz       - short fuzz smoke (FUZZTIME=500ms locally; nightly CI uses 6s per target in .github/workflows/race-fuzz-nightly.yml)"
	@echo "  make test-reasoning-e2e-soak - opt-in reasoning preservation full-HTTP soak (sets LIP_REASONING_E2E_SOAK=1; not a PR/default gate; see docs/reasoning-output-preservation.md)"
	@echo "  make test-cursor-sdk-live     - opt-in live Cursor SDK Node scenarios (CURSOR_SDK_LIVE=1 + CURSOR_API_KEY)"
	@echo "  make test-cursor-sdk-live-bridge - opt-in Go→Node live bridge lifecycle (-tags=cursorsdk_live_bridge; CURSOR_SDK_LIVE=1 + key)"
	@echo "  make test-cursor-sdk-platform - current-OS bridge platform smoke (fake bridge; no API key)"
	@echo "  make test-cursor-sdk-comparison-report - ACP vs SDK matrix report (synthetic/blocked offline; no credentials)"
	@echo "  make parity-checks   - conformance package tests only (-tags=precommit,integration; FE×BE matrix + parity suites; see docs/conformance-matrix-evidence.md)"
	@echo "  make release-gates   - conformance package + all critical fuzz targets (race is separate: test-race / CI; see docs/release-gates.md)"
	@echo "  make bench           - benchmarks (testkit, stream, core runtime/routing/diag/toolcallrepair, frontend encoders)"
	@echo "  make pgo-profile     - collect default.pgo from core benches (move under cmd/lipstd before build)"
	@echo "  make pgo-build       - build cmd/lipstd (uses cmd/lipstd/default.pgo when present)"
	@echo "  make qa              - quality-checks + one full test pass (-tags=precommit,integration) + lint + vuln + release-gates-static"
	@echo "  make lint            - golangci-lint if installed, else staticcheck"
	@echo "  make hooks-install   - git config core.hooksPath .githooks (pre-commit: secrets + quality gate)"
	@echo "  make kiro-spec-check SPEC=<name> - validate a Kiro spec (e.g. SPEC=cursor-sdk-backend)"
	@echo "  make isolated-root-qa - GOWORK=off QA on a temp root copy without connectors/support/Node/artifacts"
	@echo "  make installed-plugin-smoke - one lipstd binary; install release artifacts; same-binary inspect/doctor/invoke"
	@echo "  make docs-check      - backend-plugin authoring/operator/example documentation tests"
	@echo "  make knowledge-check - EchoesVault index/pages + steering/ADR hybrid consistency"
	@echo "  make example-config-check - operator/example YAML + config/examples bootstrap inspect"
	@echo "  make backend-plugin-security-checks - executable-plugin threat-model adversarial + bounded fuzz"
	@echo "  make backend-plugin-cross-platform-qa - connector platform matrix compile/package + native lifecycle gates"
	@echo "  make backend-plugin-release-gates-static - release report/traceability/arch wiring (used by make qa)"
	@echo "  make backend-plugin-release-gates - full connector/support module matrix + root release suites"
	@echo "  make run             - go run ./cmd/lipstd"

quality-checks:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/quality-checks.ps1
else
	@bash scripts/quality-checks.sh
endif

# Advisory architecture metrics report (non-failing). Run on demand to spot
# hotspot/line/import drift.
arch-report:
	@$(GO) run ./scripts/arch-report.go

regex-hotpath-check:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/regex-hotpath-check.ps1
else
	@bash scripts/regex-hotpath-check.sh
endif

test: quality-checks test-unit parity-checks

test-fast: quality-checks
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-staged.ps1
else
	@bash scripts/test-staged.sh
endif

test-unit:
	$(GO) test $(GO_TEST_FLAGS) ./...

# PostgreSQL is the required proof surface for cross-instance authority
# semantics. The test helper fails instead of skipping when this target is
# invoked without a configured DSN.
test-authority-postgres-direct:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('LIP_REQUIRE_POSTGRES','1','Process'); if ([Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_ADMIN_DSN','Process')) { [Environment]::SetEnvironmentVariable('LIP_TEST_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_ADMIN_DSN','Process'),'Process') }; & '$(GO)' test $(GO_TEST_FLAGS) -tags=integration -skip 'Pooled' ./internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/terminalwork/workstore"
else
	@LIP_REQUIRE_POSTGRES=1 LIP_TEST_POSTGRES_DSN="$${LIP_TEST_POSTGRES_ADMIN_DSN:-$$LIP_TEST_POSTGRES_DSN}" $(GO) test $(GO_TEST_FLAGS) -tags=integration -skip 'Pooled' ./internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/terminalwork/workstore
endif

test-postgres-migrations:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "if (-not [Environment]::GetEnvironmentVariable('LIP_MIGRATION_POSTGRES_DSN','Process')) { [Environment]::SetEnvironmentVariable('LIP_MIGRATION_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_ADMIN_DSN','Process'),'Process') }; if (-not [Environment]::GetEnvironmentVariable('LIP_MIGRATION_POSTGRES_DSN','Process')) { [Environment]::SetEnvironmentVariable('LIP_MIGRATION_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_DSN','Process'),'Process') }; & '$(GO)' run ./cmd/lipstd migrate --components usage-authority,concurrency,metering"
else
	@LIP_MIGRATION_POSTGRES_DSN="$${LIP_MIGRATION_POSTGRES_DSN:-$${LIP_TEST_POSTGRES_ADMIN_DSN:-$$LIP_TEST_POSTGRES_DSN}}" $(GO) run ./cmd/lipstd migrate --components usage-authority,concurrency,metering
endif

# Transaction-pooled runtime proof. Requires LIP_TEST_POSTGRES_DSN to be an
# actual transaction-pooler endpoint and explicit topology attestation.
# Uses normal parallelism (-parallel=8 by default); do not force -parallel=1.
test-authority-postgres-pooled:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('LIP_REQUIRE_POSTGRES_POOLER','1','Process'); if ([Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_RUNTIME_IS_POOLER','Process') -ne '1') { throw 'set LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1 only when LIP_TEST_POSTGRES_DSN is a transaction-pooler endpoint' }; & '$(GO)' test $(GO_TEST_FLAGS) -tags=integration -run 'Pooled' ./internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/terminalwork/workstore ./internal/infra/runtimebundle"
else
	@test "$${LIP_TEST_POSTGRES_RUNTIME_IS_POOLER:-}" = "1" || { echo "set LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1 only when LIP_TEST_POSTGRES_DSN is a transaction-pooler endpoint" >&2; exit 1; }
	@LIP_REQUIRE_POSTGRES_POOLER=1 $(GO) test $(GO_TEST_FLAGS) -tags=integration -run 'Pooled' ./internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/terminalwork/workstore ./internal/infra/runtimebundle
endif

test-authority-postgres: test-postgres-migrations test-authority-postgres-direct test-authority-postgres-pooled

test-precommit-extra:
	$(GO) test $(GO_TEST_FLAGS) -tags=precommit ./internal/qa/... ./internal/core/runtime/...

test-race:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/race-check.ps1
else
	@bash scripts/race-check.sh
endif

# Short fuzz smoke (extend FUZZTIME locally, e.g. FUZZTIME=30s make test-fuzz)
FUZZTIME ?= 500ms
# Route each fuzz invocation through a wrapper that tolerates the Go fuzz
# engine's spurious "context deadline exceeded" at -fuzztime expiry
# (golang/go#75804, Go 1.25-1.26.x). On Windows `go test` runs directly: local
# fuzz smoke is short and a rare flake is cheap to re-run, and bash may be absent.
ifeq ($(OS),Windows_NT)
FUZZ_WRAPPER := $(GO) test
else
FUZZ_WRAPPER := bash scripts/fuzz-run.sh
endif
# Opt-in reasoning-preservation full HTTP soak (1000×100 default). Not part of
# make test / test-unit / qa / PR gates. Overrides: LIP_REASONING_E2E_SEEDS,
# LIP_REASONING_E2E_TURNS, LIP_REASONING_E2E_WORKERS. Single-seed replay:
# LIP_REASONING_E2E_MODE + LIP_REASONING_E2E_SEED.
REASONING_E2E_SOAK_TIMEOUT ?= 6h
test-reasoning-e2e-soak:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('LIP_REASONING_E2E_SOAK','1','Process'); & '$(GO)' test -parallel=8 -timeout=$(REASONING_E2E_SOAK_TIMEOUT) -tags=precommit -run '^TestReasoningPreservationHTTP_Soak$$' -count=1 ./internal/stdhttp/"
else
	@LIP_REASONING_E2E_SOAK=1 $(GO) test -parallel=8 -timeout=$(REASONING_E2E_SOAK_TIMEOUT) -tags=precommit -run '^TestReasoningPreservationHTTP_Soak$$' -count=1 ./internal/stdhttp/
endif

test-fuzz:
	@echo "Fuzz smoke (FUZZTIME=$(FUZZTIME)) one target per line"
	$(FUZZ_WRAPPER) -fuzz=FuzzJSONRoundTrip$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/testkit
	$(FUZZ_WRAPPER) -fuzz=FuzzParseSnapshot$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/infra/modelcatalog/modelsdev
	$(FUZZ_WRAPPER) -fuzz=FuzzParseSelector$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/routing
	$(FUZZ_WRAPPER) -fuzz=FuzzParseSelectorFromBytes$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/routing
	$(FUZZ_WRAPPER) -fuzz=FuzzDecodeCreateRequest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/openairesponses
	$(FUZZ_WRAPPER) -fuzz=FuzzDecodeMessageRequest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/anthropic
	$(FUZZ_WRAPPER) -fuzz=FuzzDecodeGenerateContentRequest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/gemini
	$(FUZZ_WRAPPER) -fuzz=FuzzDecodeChatRequest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/openailegacy
	$(FUZZ_WRAPPER) -fuzz=FuzzWriteNonStreamJSON_toolArguments$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/anthropic
	$(FUZZ_WRAPPER) -fuzz=FuzzBuildGenerateContentResponse_toolJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/gemini
	$(FUZZ_WRAPPER) -fuzz=FuzzCallValidateJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipapi
	$(FUZZ_WRAPPER) -fuzz=FuzzMergeRouteQueryGenerationOptions$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipapi
	$(FUZZ_WRAPPER) -fuzz=FuzzCollectWithLimitsProgram$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipapi
	$(FUZZ_WRAPPER) -fuzz=FuzzStableCallIdentity$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/diag
	$(FUZZ_WRAPPER) -fuzz=FuzzParamsForCall$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openairesponses
	$(FUZZ_WRAPPER) -fuzz=FuzzHandleResponseStreamUnion$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openairesponses
	$(FUZZ_WRAPPER) -fuzz=FuzzBuildToolsParametersJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openairesponses
	$(FUZZ_WRAPPER) -fuzz=FuzzHandleMessageStreamEventUnion$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/protocols/anthropicmessages
	$(FUZZ_WRAPPER) -fuzz=FuzzToolInputSchemaParametersJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/protocols/anthropicmessages
	$(FUZZ_WRAPPER) -fuzz=FuzzHandleChatCompletionChunk$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openailegacy
	$(FUZZ_WRAPPER) -fuzz=FuzzBuildChatToolsParametersJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openailegacy
	$(FUZZ_WRAPPER) -fuzz=FuzzHandleGenerateContentResponse$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/protocols/geminigenerate
	$(FUZZ_WRAPPER) -fuzz=FuzzBuildToolsParametersJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/protocols/geminigenerate
	$(FUZZ_WRAPPER) -fuzz=FuzzMessageToContentToolResultJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/protocols/geminigenerate
	$(FUZZ_WRAPPER) -fuzz=FuzzAssistantPartsToContentBlocksJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/bedrock
	cd connector-support/acp && GOWORK=off $(FUZZ_WRAPPER) -fuzz=FuzzParseNDJSONLine$$ -fuzztime=$(FUZZTIME) -run=^$$ .
	cd connector-support/acp && GOWORK=off $(FUZZ_WRAPPER) -fuzz=FuzzMapSessionUpdateToEvents$$ -fuzztime=$(FUZZTIME) -run=^$$ .
	cd connector-support/acp && GOWORK=off $(FUZZ_WRAPPER) -fuzz=FuzzMergeHandshakeProfileExtensions$$ -fuzztime=$(FUZZTIME) -run=^$$ .
	$(FUZZ_WRAPPER) -fuzz=FuzzDecodeLine$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/cursorsdk/protocol
	$(FUZZ_WRAPPER) -fuzz=FuzzMapBridgeEvent$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/cursorsdk
	$(FUZZ_WRAPPER) -fuzz=FuzzHookMutationValidators$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/hooks
	$(FUZZ_WRAPPER) -fuzz=FuzzManifest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/infra/backendplugins/manifest
	$(FUZZ_WRAPPER) -fuzz=FuzzServerFrame$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipsdk/backendplugin
	$(FUZZ_WRAPPER) -fuzz=FuzzAcceptClientUserAgent$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/identity
	$(FUZZ_WRAPPER) -fuzz=FuzzAcceptClientAppURL$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/identity
	$(FUZZ_WRAPPER) -fuzz=FuzzAcceptClientAppTitle$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/identity
	$(FUZZ_WRAPPER) -fuzz=FuzzValidateIdentityYAML$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/identity
	$(FUZZ_WRAPPER) -fuzz=FuzzCaptureClientUserAgent$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/identitywire

	$(FUZZ_WRAPPER) -fuzz=FuzzCompleteJSONSuffix$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/toolcallrepair
	$(FUZZ_WRAPPER) -fuzz=FuzzSchemaPreScanCompile$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/toolcallrepair
	$(FUZZ_WRAPPER) -fuzz=FuzzEngineRepair$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/toolcallrepair
	$(FUZZ_WRAPPER) -fuzz=FuzzComputeAnchor$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/features/reasoningpreservation
	$(FUZZ_WRAPPER) -fuzz=FuzzDecodeConfig$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/features/reasoningpreservation
	# Dual-plane Phase 7.2 state-machine / renew / work / owner / money / fact fuzz
	$(FUZZ_WRAPPER) -fuzz=FuzzLeaseSet_OccupiesCapacity$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/concurrencyauthority/domain
	$(FUZZ_WRAPPER) -fuzz=FuzzIsAmbiguousRenewError$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/concurrencyauthority/app
	$(FUZZ_WRAPPER) -fuzz=FuzzWorkItem_TransitionSequence$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/terminalwork
	$(FUZZ_WRAPPER) -fuzz=FuzzOwner_CommandSequences$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/terminal
	$(FUZZ_WRAPPER) -fuzz=FuzzParseDecimalToNano$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipsdk/economics
	$(FUZZ_WRAPPER) -fuzz=FuzzPhase32_SourceEventKey_DelimiterSafety$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipsdk/metering
	$(FUZZ_WRAPPER) -fuzz=FuzzPhase32_MoneyPresentCurrency$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipsdk/metering

test-cursor-sdk-live:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-cursor-sdk-live.ps1
else
	@bash scripts/test-cursor-sdk-live.sh
endif

test-cursor-sdk-live-bridge:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-cursor-sdk-live-bridge.ps1
else
	@bash scripts/test-cursor-sdk-live-bridge.sh
endif

test-cursor-sdk-platform:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-cursor-sdk-platform.ps1
else
	@bash scripts/test-cursor-sdk-platform.sh
endif

test-cursor-sdk-comparison-report:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-cursor-sdk-comparison-report.ps1
else
	@bash scripts/test-cursor-sdk-comparison-report.sh
endif

parity-checks:
	$(GO) test $(GO_TEST_FLAGS) -tags=precommit,integration ./internal/testkit/conformance/...

# Phase 6 ACP external connector parity (testemu/scripted ACP; not live Cursor/Gemini/Agy CLIs).
parity-acp-plugin:
	cd connector-support/acp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'KillProcessTree_|ProcessTree_CrossCompile|PID|Pool|Cancel|Open_|MapSession|Scripted' ./...
	cd connectors/acp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_' ./...
	$(GO) test $(GO_TEST_FLAGS) -run 'TestExternalParity_ProfileFixture|TestIntegration_refbackend' ./internal/plugins/backends/acp

parity-cursorcliacp-plugin:
	cd connector-support/acp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'KillProcessTree_|ProcessTree_CrossCompile' ./...
	cd connectors/cursorcliacp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_' ./...

parity-cli-acp-plugins:
	cd connectors/geminicliacp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_' ./...
	cd connectors/agycliacp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_' ./...
	cd connectors/cursorcliacp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_' ./...

# Phase 7 OpenAI-compatible external connectors (deterministic emulators; not live providers).
parity-openrouter-plugin:
	cd connector-support/openaicompat && GOWORK=off $(GO) test $(GO_TEST_FLAGS) ./...
	cd connectors/openrouter && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestBilling_' ./...
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'OpenRouter|Phase7_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/runtimebundle -run 'TestPhase7_OpenRouter'

parity-hosted-compatible-plugins:
	cd connectors/nvidia && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	cd connectors/huggingface && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'Phase7_'

parity-ollama-plugins:
	cd connectors/ollama && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...

parity-opencode-plugins:
	cd connectors/opencode && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'OpenCode|Phase8_'

parity-codex-plugins:
	cd connectors/codex && GOWORK=off $(GO) test $(GO_TEST_FLAGS) ./...
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'Codex|Phase8_.*Codex|TestCodex_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/runtimebundle -run 'TestPhase8_Codex'

test-local-compatible-plugin-modules:
	cd connectors/llamacpp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	cd connectors/lmstudio && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...
	cd connectors/vllm && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'TestParity_|TestDescribe_|TestConfigure_|TestInventory_' ./...

parity-local-compatible-plugins: test-local-compatible-plugin-modules
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest -run 'Phase7_'

backend-plugin-absence-checks:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/backend-plugin-absence-checks.ps1
else
	@bash scripts/backend-plugin-absence-checks.sh
endif

release-gates:
	$(GO) test $(GO_TEST_FLAGS) -tags=integration ./internal/testkit/conformance/...
	@$(MAKE) test-fuzz

bench:
	$(GO) test -bench=. -benchmem -run=Benchmark ./internal/testkit/... ./internal/core/stream/... \
		./internal/core/securesession/... \
		./internal/core/runtime/... ./internal/core/routing/... ./internal/core/diag/... \
		./internal/core/toolcallrepair/... \
		./internal/infra/concurrencyauthority/leasestore/... \
		./internal/infra/metering/journalstore/... \
		./internal/infra/usageauthority/authoritystore/... \
		./internal/plugins/frontends/openailegacy/... \
		./internal/plugins/frontends/gemini/... \
		./internal/plugins/frontends/openairesponses/... \
		./internal/plugins/frontends/anthropic/...

# Collect a CPU profile suitable for placing as cmd/lipstd/default.pgo, then rebuild with PGO.
# Requires a representative bench/workload; do not commit synthetic profiles by default.
pgo-profile:
	$(GO) test -cpuprofile=default.pgo -bench=. -benchtime=3s -run=^$$ ./internal/core/runtime/... ./internal/core/stream/...
	@echo "Move default.pgo next to the main package (e.g. cmd/lipstd/default.pgo) before building."

pgo-build:
	$(GO) build -o bin/lipstd ./cmd/lipstd

# Single test invocation matches CI (go test -tags=precommit,integration ./...) and avoids compiling twice.
# Static release-gate wiring only (no recursive full backend-plugin-release-gates / module matrix).
qa: quality-checks qa-tests lint vuln backend-plugin-release-gates-static

qa-tests:
	$(GO) test $(GO_TEST_FLAGS) -tags=precommit,integration ./...

vet:
	$(GO) vet ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	elif command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "Install golangci-lint (preferred) or staticcheck: https://golangci-lint.run/"; \
		exit 1; \
	fi

vuln:
	$(GO) tool govulncheck ./...

run:
	$(GO) run ./cmd/lipstd --config ./config/config.yaml

hooks-install:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/install-hooks.ps1
else
	@bash scripts/install-hooks.sh
endif

# Phase 5: structural connector module discovery + GOWORK=off isolation (no recursive make).
backend-plugin-module-checks:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/backend-plugin-module-checks.ps1
else
	@bash scripts/backend-plugin-module-checks.sh
endif

PACKAGE_DEST ?= $(CURDIR)/.golip-package-staging

package-minimal:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/package-plugins.ps1 -Profile minimal -Dest "$(PACKAGE_DEST)/minimal"
else
	@bash scripts/package-plugins.sh minimal "$(PACKAGE_DEST)/minimal"
endif

package-full:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/package-plugins.ps1 -Profile full -Dest "$(PACKAGE_DEST)/full"
else
	@bash scripts/package-plugins.sh full "$(PACKAGE_DEST)/full"
endif

package-plugin-smoke: package-minimal package-full
	$(GO) test $(GO_TEST_FLAGS) ./tools/backendplugin/ -run 'TestPackage_|TestDiscoverModules_'

docs-check:
	$(GO) test $(GO_TEST_FLAGS) ./docs/backend-plugins/ -run 'TestDocs|TestExample|TestOperator|TestExampleConfig|TestThreat'

# Phase 9.3: executable-plugin threat model adversarial suite + bounded fuzz.
# Pair with `make test-fuzz` and `make test-race` (Windows race is skip-only).
backend-plugin-security-checks:
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/backendplugins/...
	$(GO) test $(GO_TEST_FLAGS) ./pkg/lipsdk/backendplugin/...
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/diagredact/...
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/runtimebundle/ -run 'TestBuild_localOnly|TestBuild_unknownBackendCredential|TestBuild_oauthUser|TestBuild_unsupportedBackend|TestBuild_staticBackend|TestBuild_noneBackend|TestBuild_strictAuthoritative'
	$(GO) test $(GO_TEST_FLAGS) ./docs/backend-plugins/ -run 'TestThreat|TestOperator_|TestDocs_'
	$(FUZZ_WRAPPER) -fuzz=FuzzManifest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/infra/backendplugins/manifest
	$(FUZZ_WRAPPER) -fuzz=FuzzServerFrame$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipsdk/backendplugin

# Phase 9.4: structural connector/support discovery, claimed GOOS/GOARCH compile matrix,
# host secure-profile false-claim rejection, package matrix match, native lifecycle/IPC gates.
# Emits machine-readable unsupported pairs to .golip-crossplatform-matrix.json (gitignored).
backend-plugin-cross-platform-qa:
	$(GO) run ./tools/backendplugin/crossplatform_qa -root . -out .golip-crossplatform-matrix.json -skip-native
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/backendplugins/... -run 'TestAdversarial_|TestActivate_|TestStream_|TestDigest|TestManifest|TestDiscover|TestShutdown|TestReap|TestPeer|TestChannel|TestExact|TestUpgrade|TestRollback|TestUninstall|TestConfig|TestSecrecy|TestUnauthorized|TestProtected|TestLaunch|TestKill|TestCancel'
	$(GO) test $(GO_TEST_FLAGS) ./pkg/lipsdk/backendplugin/... -run 'Test'
	cd connector-support/acp && GOWORK=off $(GO) test $(GO_TEST_FLAGS) -run 'KillProcessTree_|ProcessTree_CrossCompile|Cancel' ./...
	$(MAKE) package-plugin-smoke
	$(GO) test $(GO_TEST_FLAGS) ./tools/backendplugin/ -run 'TestCrossPlatformQA_|TestPackage_|TestDiscoverModules_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest/ -run 'TestBackendPluginCrossPlatform_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/backendplugins/processhost/ -run 'TestHostSecureProfiles_'

# Phase 9.5 (fast): structural discovery + deterministic release report/traceability + wiring tests.
# Integrated into `make qa` without re-running the full module matrix or nested `make qa`.
backend-plugin-release-gates-static:
	$(GO) run ./tools/backendplugin/release_gates -root . -out .golip-release-gates-report.json -mode=static
	$(GO) test $(GO_TEST_FLAGS) ./tools/backendplugin/ -run 'TestReleaseGates_'
	$(GO) test $(GO_TEST_FLAGS) ./tools/backendplugin/release_gates/ -run 'TestParseRequirementIDs_|TestListMatchingTests_|TestValidateSelectors_'
	$(GO) test $(GO_TEST_FLAGS) ./internal/archtest/ -run 'TestBackendPluginReleaseGates_'

# Phase 9.5 (full local): orchestrated by release_gates -mode=full (module matrix + root package
# gates + package/security/absence/isolated/installed smoke + race honesty). Avoids fragile
# Makefile -run filters; selectors are validated via go test -list inside the tool.
backend-plugin-release-gates: backend-plugin-release-gates-static
	$(GO) run ./tools/backendplugin/release_gates -root . -out .golip-release-gates-report.json -mode=full

# EchoesVault index/pages + steering/ADR hybrid consistency (Phase 9.1).
knowledge-check:
	$(GO) test $(GO_TEST_FLAGS) ./docs/knowledge/ -run 'TestKnowledge_'

# Operator guides + every config/examples YAML via bootstrap inspect (Phase 9.2).
example-config-check: docs-check
	$(GO) test $(GO_TEST_FLAGS) ./internal/infra/runtimebundle/ -run 'TestConfigExamples_passBootstrapInspect'

# Validate Kiro specification artifacts. SPEC is required (e.g. cursor-sdk-backend).
kiro-spec-check:
ifndef SPEC
	$(error SPEC is required, e.g. make kiro-spec-check SPEC=cursor-sdk-backend)
endif
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/kiro-spec-check.ps1 -Spec "$(SPEC)"
else
	@bash scripts/kiro-spec-check.sh "$(SPEC)"
endif

backend-plugin-example-check: docs-check
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/backend-plugin-example-check.ps1
else
	@bash scripts/backend-plugin-example-check.sh
endif

# Phase 8.5: isolated root module QA (no connectors/connector-support in the copied tree).
isolated-root-qa:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/isolated-root-qa.ps1
else
	@bash scripts/isolated-root-qa.sh
endif

# Phase 8.5: unchanged lipstd binary gains optional kinds solely via installed artifacts.
installed-plugin-smoke:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/installed-plugin-smoke.ps1
else
	@bash scripts/installed-plugin-smoke.sh
endif
