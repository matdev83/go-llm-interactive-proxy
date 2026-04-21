# Go LLM Interactive Proxy

This repository is the greenfield Go re-implementation of LLM Interactive Proxy.

The repository implements the **Go core v1** stack from `.kiro/specs/go-core-reimplementation-v1`: canonical `lipapi` contracts, core routing/B2BUA/executor, bundled frontend and backend plugins, conformance matrix, and a **runnable** standard distribution binary (`cmd/lipstd`) that serves the bundled HTTP APIs when configured.

## Current state

- **API parity (spec + matrices)** — vendor-surface claims are tracked under [.kiro/specs/llm-api-parity/](.kiro/specs/llm-api-parity/) with row-level status; the README does not assert parity beyond what those matrices mark `implemented` (see also [.kiro/specs/go-core-reimplementation-v1/refclient-spec-matrix.md](.kiro/specs/go-core-reimplementation-v1/refclient-spec-matrix.md)).
- canonical Go module and repository layout
- package boundaries aligned with `AGENTS.md` and Kiro steering
- typed runtime configuration (`config/config.yaml`)
- **`cmd/lipstd`** — loads config, validates plugin registration, then runs [`internal/stdhttp.Run`](internal/stdhttp/server.go): HTTP server with all bundled frontends, optional diagnostics (`/healthz`, `/admin/attempts` per config), and backends wired from enabled `plugins.backends` rows (see [`internal/stdhttp/wire.go`](internal/stdhttp/wire.go))
- test, vet, lint, and vuln-check entrypoints
- QA scripts, optional git hooks, and a GitHub Actions workflow aligned with the sibling `go-live-market-data-aggregator` process (trimmed for this repo: no domain-specific custom vets)
- deterministic fallback IDs/timestamps in the core runtime, frontend encoders, and ACP reference paths so tests no longer depend on wall-clock or random generators

### Resource bounds (memory / DoS hardening)

- **`lipapi.Call.Validate`** enforces maximum sizes on route selectors, IDs, messages/parts/tool counts, part payloads, extensions, and related option strings (see `pkg/lipapi/limits.go`). Oversized canonical requests fail validation before orchestration runs.
- **`lipapi.Collect`** applies `DefaultCollectLimits` when aggregating streaming events into a single `Collected` struct. Use **`CollectWithLimits`** for custom caps or **`CollectUnbounded`** only for tests/harnesses that deliberately exceed defaults.
- **`b2bua.MemoryStore`** with **TTL disabled** applies a **default maximum number of concurrent A-leg rows** (`DefaultMemoryStoreMaxLegsWithoutTTL`, currently 100k); set **`MemoryStoreOptions.MaxLegs`** explicitly (including **negative** for no cap if you truly need unbounded in-memory retention). With **TTL enabled**, max-leg count defaults to unlimited and expiry is TTL-driven.

### Routing defaults and continuity

- **Default route selector** when clients omit `X-LIP-Route` is resolved by `routing.EffectiveDefaultRouteSelector` from `routing.default_route` in YAML, then the first enabled backend plus registry default model ids (`pluginreg.DefaultWireModel`). See [`internal/core/routing/default_route.go`](internal/core/routing/default_route.go).
- **SQLite continuity** (`continuity.store: sqlite` and `continuity.sqlite_path`) persists A-leg rows and attempt lineage across process restarts (`internal/core/continuity/sqlitestore`, pure-Go driver `modernc.org/sqlite`).
- **Hook bus**: root `hooks.tool_reactor_error_policy` selects `fail_open` (default), `fail_closed`, or `swallow_event` for tool-reactor errors. Optional reference feature plugins are documented in [`internal/plugins/features/REFERENCE_PLUGINS.md`](internal/plugins/features/REFERENCE_PLUGINS.md).

## QA and local workflow

Fast checks (format, `go mod tidy` drift, build, vet) plus staged-package tests mirror the sibling repo’s `quality-checks` / `test-staged` pattern.

```bash
make quality-checks   # gofmt -l, tidy+diff guard, go build, go vet
make test             # quality-checks + go test -short -parallel=8 ./...
make test-fast        # quality-checks + tests for staged packages (or all if none staged)
make test-race        # no-op on Windows; use Linux CI or WSL for -race
make test-fuzz        # short fuzz smoke on internal/testkit (override: FUZZTIME=30s make test-fuzz)
make bench            # JSON normalize micro-benchmark in internal/testkit
make qa               # quality-checks + unit tests + golangci-lint + govulncheck (tools must be installed)
make hooks-install    # set core.hooksPath to .githooks (runs scripts/quality-gate on pre-commit when .go is staged)
```

Pre-commit runs `scripts/quality-gate` (quality checks, staged tests, `golangci-lint` if present, `govulncheck` if present). The race step is not run on Windows; use Linux CI or WSL for `go test -race`.

CI (`.github/workflows/qa.yml`) runs `make quality-checks`, unit tests, strict race on Linux, `golangci-lint-action`, and `govulncheck`.

Linter config lives in `.golangci.yml` (staticcheck, govet, revive, small correctness linters). Prefer `make lint` over ad hoc `staticcheck` so local and CI stay aligned.

## Repository layout

- `cmd/lipstd/` - standard distribution composition root scaffold
- `pkg/lipapi/` - canonical public contracts
- `pkg/lipsdk/` - stable plugin SDK contracts
- `internal/core/` - runtime, routing, stream, config, admin, capabilities
- `internal/plugins/` - bundled frontend, backend, and feature plugins
- `internal/stdhttp/` - standard distribution HTTP wiring (mount + executor build from YAML)
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
