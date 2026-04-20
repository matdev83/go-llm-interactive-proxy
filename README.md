# Go LLM Interactive Proxy

This repository is the greenfield Go re-implementation of LLM Interactive Proxy.

The bootstrap in this repository intentionally sets up package boundaries, module metadata,
tooling, and configuration scaffolding only. Runtime behavior, protocol adapters, routing,
streaming, and B2BUA orchestration remain future implementation work driven by the Kiro specs
under `.kiro/specs/`.

## Current state

- canonical Go module and repository layout
- placeholder package boundaries aligned with `AGENTS.md` and Kiro steering
- typed runtime configuration scaffold
- standard distribution composition root scaffold in `cmd/lipstd`
- test, vet, lint, and vuln-check entrypoints
- QA scripts, optional git hooks, and a GitHub Actions workflow aligned with the sibling `go-live-market-data-aggregator` process (trimmed for this repo: no domain-specific custom vets)

## QA and local workflow

Fast checks (format, `go mod tidy` drift, build, vet) plus staged-package tests mirror the sibling repo’s `quality-checks` / `test-staged` pattern.

```bash
make quality-checks   # gofmt -l, tidy+diff guard, go build, go vet
make test             # quality-checks + go test -short -parallel=8 ./...
make test-fast        # quality-checks + tests for staged packages (or all if none staged)
make test-race        # best-effort race scan (skips if CGO/CC unavailable)
make test-fuzz        # short fuzz smoke on internal/testkit (override: FUZZTIME=30s make test-fuzz)
make bench            # JSON normalize micro-benchmark in internal/testkit
make qa               # quality-checks + unit tests + golangci-lint + govulncheck (tools must be installed)
make hooks-install    # set core.hooksPath to .githooks (runs scripts/quality-gate on pre-commit when .go is staged)
```

Pre-commit runs `scripts/quality-gate` (quality checks, staged tests, race on staged paths, `golangci-lint` if present, `govulncheck` if present). On Windows, set `LIP_QA_RACE_STRICT=1` before committing if you want the race step to fail closed when the race runtime is unavailable.

CI (`.github/workflows/qa.yml`) runs `make quality-checks`, unit tests, strict race on Linux, `golangci-lint-action`, and `govulncheck`.

Linter config lives in `.golangci.yml` (staticcheck, govet, revive, small correctness linters). Prefer `make lint` over ad hoc `staticcheck` so local and CI stay aligned.

## Repository layout

- `cmd/lipstd/` - standard distribution composition root scaffold
- `pkg/lipapi/` - canonical public contracts
- `pkg/lipsdk/` - stable plugin SDK contracts
- `internal/core/` - runtime, routing, stream, config, admin, capabilities
- `internal/plugins/` - official frontend, backend, and feature plugin placeholders
- `internal/infra/` - shared infrastructure seams
- `internal/testkit/` - test support surface scaffold
- `internal/qa/` - repo hygiene tests (root markdown noise, etc.)
- `scripts/` - quality gate scripts (bash + PowerShell)
- `.githooks/` - optional git hooks
- `.github/workflows/` - CI QA pipeline
- `testdata/` - fixtures and goldens
- `.kiro/` - steering and spec artifacts

## Bootstrap commands

```bash
make test
make vet
go run ./cmd/lipstd --config ./config/config.yaml
```

Install [golangci-lint](https://golangci-lint.run/) and `govulncheck` (`go install golang.org/x/vuln/cmd/govulncheck@latest`) for the full `make qa` profile.
