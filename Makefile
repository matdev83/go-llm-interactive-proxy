.PHONY: help test test-fast test-unit test-precommit-extra test-postgres-migrations test-authority-postgres test-authority-postgres-direct test-authority-postgres-pooled qa-tests test-race test-fuzz parity-checks release-gates bench pgo-profile pgo-build quality-checks regex-hotpath-check arch-report qa vet lint vuln run hooks-install

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
	@echo "  make parity-checks   - conformance package tests only (-tags=precommit,integration; FE×BE matrix + parity suites; see docs/conformance-matrix-evidence.md)"
	@echo "  make release-gates   - conformance package + all critical fuzz targets (race is separate: test-race / CI; see docs/release-gates.md)"
	@echo "  make bench           - benchmarks (testkit, stream, core runtime/routing/diag/toolcallrepair, frontend encoders)"
	@echo "  make pgo-profile     - collect default.pgo from core benches (move under cmd/lipstd before build)"
	@echo "  make pgo-build       - build cmd/lipstd (uses cmd/lipstd/default.pgo when present)"
	@echo "  make qa              - quality-checks + one full test pass (-tags=precommit,integration) + lint + vuln (local)"
	@echo "  make lint            - golangci-lint if installed, else staticcheck"
	@echo "  make hooks-install   - git config core.hooksPath .githooks (pre-commit: secrets + quality gate)"
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
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('LIP_REQUIRE_POSTGRES','1','Process'); if ([Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_ADMIN_DSN','Process')) { [Environment]::SetEnvironmentVariable('LIP_TEST_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_ADMIN_DSN','Process'),'Process') }; & '$(GO)' test $(GO_TEST_FLAGS) -tags=integration -skip '^TestPostgresPooled_' ./internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore"
else
	@LIP_REQUIRE_POSTGRES=1 LIP_TEST_POSTGRES_DSN="$${LIP_TEST_POSTGRES_ADMIN_DSN:-$$LIP_TEST_POSTGRES_DSN}" $(GO) test $(GO_TEST_FLAGS) -tags=integration -skip '^TestPostgresPooled_' ./internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore
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
	@powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('LIP_REQUIRE_POSTGRES_POOLER','1','Process'); if ([Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_RUNTIME_IS_POOLER','Process') -ne '1') { throw 'set LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1 only when LIP_TEST_POSTGRES_DSN is a transaction-pooler endpoint' }; & '$(GO)' test $(GO_TEST_FLAGS) -tags=integration -run '^TestPostgresPooled_' ./internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/runtimebundle"
else
	@test "$${LIP_TEST_POSTGRES_RUNTIME_IS_POOLER:-}" = "1" || { echo "set LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1 only when LIP_TEST_POSTGRES_DSN is a transaction-pooler endpoint" >&2; exit 1; }
	@LIP_REQUIRE_POSTGRES_POOLER=1 $(GO) test $(GO_TEST_FLAGS) -tags=integration -run '^TestPostgresPooled_' ./internal/infra/usageauthority/authoritystore ./internal/infra/concurrencyauthority/leasestore ./internal/infra/metering/journalstore ./internal/infra/runtimebundle
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
	$(FUZZ_WRAPPER) -fuzz=FuzzParseNDJSONLine$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/acp
	$(FUZZ_WRAPPER) -fuzz=FuzzMapSessionUpdateToEvents$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/acp
	$(FUZZ_WRAPPER) -fuzz=FuzzMergeHandshakeProfileExtensions$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/acp
	$(FUZZ_WRAPPER) -fuzz=FuzzHookMutationValidators$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/hooks
	$(FUZZ_WRAPPER) -fuzz=FuzzAcceptClientUserAgent$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/identity
	$(FUZZ_WRAPPER) -fuzz=FuzzAcceptClientAppURL$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/identity
	$(FUZZ_WRAPPER) -fuzz=FuzzAcceptClientAppTitle$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/identity
	$(FUZZ_WRAPPER) -fuzz=FuzzValidateIdentityYAML$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/identity
	$(FUZZ_WRAPPER) -fuzz=FuzzCaptureClientUserAgent$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/identitywire

	$(FUZZ_WRAPPER) -fuzz=FuzzCompleteJSONSuffix$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/toolcallrepair
	$(FUZZ_WRAPPER) -fuzz=FuzzSchemaPreScanCompile$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/toolcallrepair
	$(FUZZ_WRAPPER) -fuzz=FuzzEngineRepair$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/toolcallrepair

parity-checks:
	$(GO) test $(GO_TEST_FLAGS) -tags=precommit,integration ./internal/testkit/conformance/...

release-gates:
	$(GO) test $(GO_TEST_FLAGS) -tags=integration ./internal/testkit/conformance/...
	@$(MAKE) test-fuzz

bench:
	$(GO) test -bench=. -benchmem -run=Benchmark ./internal/testkit/... ./internal/core/stream/... \
		./internal/core/securesession/... \
		./internal/core/runtime/... ./internal/core/routing/... ./internal/core/diag/... \
		./internal/core/toolcallrepair/... \
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
qa: quality-checks qa-tests lint vuln

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
