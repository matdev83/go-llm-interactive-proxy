.PHONY: help test test-fast test-unit test-race test-fuzz bench quality-checks qa vet lint vuln run hooks-install

GO ?= go
GO_TEST_FLAGS ?= -short -parallel=8 -timeout=10m

help:
	@echo "Targets:"
	@echo "  make quality-checks  - gofmt, go mod tidy (no drift), go build, go vet"
	@echo "  make test            - quality-checks then full unit tests (-short)"
	@echo "  make test-fast       - quality-checks then tests for staged packages (or all)"
	@echo "  make test-unit       - go test $(GO_TEST_FLAGS) ./..."
	@echo "  make test-race       - race scan (best-effort on Windows without strict CGO)"
	@echo "  make test-fuzz       - short fuzz smoke (FUZZTIME=500ms by default)"
	@echo "  make bench           - benchmarks for testkit JSON helpers"
	@echo "  make qa              - quality-checks + unit tests + lint + vuln (local)"
	@echo "  make lint            - golangci-lint if installed, else staticcheck"
	@echo "  make hooks-install   - git config core.hooksPath .githooks"
	@echo "  make run             - go run ./cmd/lipstd"

quality-checks:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/quality-checks.ps1
else
	@bash scripts/quality-checks.sh
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

test-race:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/race-check.ps1 -Short
else
	@bash scripts/race-check.sh --short
endif

# Short fuzz smoke (extend FUZZTIME locally, e.g. FUZZTIME=30s make test-fuzz)
FUZZTIME ?= 500ms
test-fuzz:
	$(GO) test -fuzz=FuzzJSONRoundTrip -fuzztime=$(FUZZTIME) ./internal/testkit/...

bench:
	$(GO) test -bench=. -benchmem -run=Benchmark ./internal/testkit/...

qa: quality-checks test-unit lint vuln

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
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./...; \
	else \
		echo "govulncheck not found; install: go install golang.org/x/vuln/cmd/govulncheck@latest"; \
		exit 1; \
	fi

run:
	$(GO) run ./cmd/lipstd --config ./config/config.yaml

hooks-install:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/install-hooks.ps1
else
	@bash scripts/install-hooks.sh
endif
