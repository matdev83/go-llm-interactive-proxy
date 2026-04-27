.PHONY: help test test-fast test-unit test-precommit-extra qa-tests test-race test-fuzz parity-checks release-gates bench quality-checks regex-hotpath-check qa vet lint vuln run hooks-install

GO ?= go
GO_TEST_FLAGS ?= -parallel=8 -timeout=10m

help:
	@echo "Targets:"
	@echo "  make quality-checks  - gofmt, go mod tidy (no drift), go build, go vet, guard scripts, archtest; mod verify in CI or with LIP_VERIFY_MODULE_CACHE=1"
	@echo "  make regex-hotpath-check - forbid regexp.MustCompile in frontends/runtime (see scripts/)"
	@echo "  make test            - quality-checks then full unit tests"
	@echo "  make test-fast       - quality-checks then tests for staged packages (or all)"
	@echo "  make test-unit       - go test $(GO_TEST_FLAGS) ./... (excludes //go:build precommit tests)"
	@echo "  make test-precommit-extra - hygiene + executor matrices (-tags=precommit; also in pre-commit hook + CI)"
	@echo "  make test-race       - race scan (skipped on Windows; macOS/Linux: scripts/race-check.sh)"
	@echo "  make test-fuzz       - short fuzz smoke (FUZZTIME=500ms locally; CI uses 6s per target in .github/workflows/qa.yml)"
	@echo "  make parity-checks   - conformance package tests only (API parity suites + matrix; see .kiro/specs/llm-api-parity/)"
	@echo "  make release-gates   - conformance package + all critical fuzz targets (race is separate: test-race / CI; see docs/release-gates.md)"
	@echo "  make bench           - benchmarks (testkit, stream, core runtime/routing/diag, frontend encoders)"
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

regex-hotpath-check:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/regex-hotpath-check.ps1
else
	@bash scripts/regex-hotpath-check.sh
endif

test: quality-checks test-unit

test-fast: quality-checks
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-staged.ps1
else
	@bash scripts/test-staged.sh
endif

test-unit:
	$(GO) test $(GO_TEST_FLAGS) ./...

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
test-fuzz:
	@echo "Fuzz smoke (FUZZTIME=$(FUZZTIME)) one target per line"
	$(GO) test -fuzz=FuzzJSONRoundTrip$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/testkit
	$(GO) test -fuzz=FuzzParseSnapshot$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/infra/modelcatalog/modelsdev
	$(GO) test -fuzz=FuzzParseSelector$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/routing
	$(GO) test -fuzz=FuzzParseSelectorFromBytes$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/routing
	$(GO) test -fuzz=FuzzDecodeCreateRequest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/openairesponses
	$(GO) test -fuzz=FuzzDecodeMessageRequest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/anthropic
	$(GO) test -fuzz=FuzzDecodeGenerateContentRequest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/gemini
	$(GO) test -fuzz=FuzzDecodeChatRequest$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/openailegacy
	$(GO) test -fuzz=FuzzWriteNonStreamJSON_toolArguments$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/anthropic
	$(GO) test -fuzz=FuzzBuildGenerateContentResponse_toolJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/frontends/gemini
	$(GO) test -fuzz=FuzzCallValidateJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipapi
	$(GO) test -fuzz=FuzzMergeRouteQueryGenerationOptions$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipapi
	$(GO) test -fuzz=FuzzCollectWithLimitsProgram$$ -fuzztime=$(FUZZTIME) -run=^$$ ./pkg/lipapi
	$(GO) test -fuzz=FuzzStableCallIdentity$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/diag
	$(GO) test -fuzz=FuzzParamsForCall$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openairesponses
	$(GO) test -fuzz=FuzzHandleResponseStreamUnion$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openairesponses
	$(GO) test -fuzz=FuzzBuildToolsParametersJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openairesponses
	$(GO) test -fuzz=FuzzHandleMessageStreamEventUnion$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/anthropic
	$(GO) test -fuzz=FuzzToolInputSchemaParametersJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/anthropic
	$(GO) test -fuzz=FuzzHandleChatCompletionChunk$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openailegacy
	$(GO) test -fuzz=FuzzBuildChatToolsParametersJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/openailegacy
	$(GO) test -fuzz=FuzzHandleGenerateContentResponse$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/gemini
	$(GO) test -fuzz=FuzzBuildToolsParametersJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/gemini
	$(GO) test -fuzz=FuzzMessageToContentToolResultJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/gemini
	$(GO) test -fuzz=FuzzAssistantPartsToContentBlocksJSON$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/bedrock
	$(GO) test -fuzz=FuzzParseNDJSONLine$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/acp
	$(GO) test -fuzz=FuzzMapSessionUpdateToEvents$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/acp
	$(GO) test -fuzz=FuzzMergeHandshakeProfileExtensions$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/plugins/backends/acp
	$(GO) test -fuzz=FuzzHookMutationValidators$$ -fuzztime=$(FUZZTIME) -run=^$$ ./internal/core/hooks

parity-checks:
	$(GO) test ./internal/testkit/conformance/...

release-gates:
	$(GO) test ./internal/testkit/conformance/...
	@$(MAKE) test-fuzz

bench:
	$(GO) test -bench=. -benchmem -run=Benchmark ./internal/testkit/... ./internal/core/stream/... \
		./internal/core/securesession/... \
		./internal/core/runtime/... ./internal/core/routing/... ./internal/core/diag/... \
		./internal/plugins/frontends/openailegacy/... \
		./internal/plugins/frontends/gemini/... \
		./internal/plugins/frontends/openairesponses/... \
		./internal/plugins/frontends/anthropic/...

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
