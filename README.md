# Go LLM Interactive Proxy

This repository is the greenfield Go re-implementation of LLM Interactive Proxy.

The repository implements the **Go core v1** stack from `.kiro/specs/go-core-reimplementation-v1`: canonical `lipapi` contracts, core routing/B2BUA/executor, bundled frontend and backend plugins, conformance matrix, and a **runnable** standard distribution binary (`cmd/lipstd`) that serves the bundled HTTP APIs when configured.

## Current state

- **API parity (spec + matrices)** — vendor-surface claims are tracked under [.kiro/specs/llm-api-parity/](.kiro/specs/llm-api-parity/) with row-level status; the README does not assert parity beyond what those matrices mark `implemented` (see also [.kiro/specs/go-core-reimplementation-v1/refclient-spec-matrix.md](.kiro/specs/go-core-reimplementation-v1/refclient-spec-matrix.md)).
- canonical Go module and repository layout
- package boundaries aligned with `AGENTS.md` and Kiro steering
- typed runtime configuration ([`config/config.yaml`](config/config.yaml)); multi-instance routing example: [`config/config.multi-instance.example.yaml`](config/config.multi-instance.example.yaml)
- **Architecture / drift** — [`docs/architecture-guardrails.md`](docs/architecture-guardrails.md), [`docs/adr/0005-architecture-guardrails-and-complexity-budgets.md`](docs/adr/0005-architecture-guardrails-and-complexity-budgets.md). Routing breaker semantics: [`docs/routing-health-circuit-breaker.md`](docs/routing-health-circuit-breaker.md). Execute-error taxonomy notes: [`docs/execerr-classification.md`](docs/execerr-classification.md). **HTTP 5xx** responses from bundled frontends use a stable generic message for internal executor/upstream failures (`internal error`); operators rely on structured server logs for detail (not backward compatible if you depended on error-body echo of raw upstream text).
- **`cmd/lipstd`** — creates a [`pluginreg.Registry`](internal/pluginreg/reg.go) with [`pluginreg.NewRegistry`](internal/pluginreg/reg.go), installs the standard bundle via [`pluginreg.InstallStandardBundleOn`](internal/pluginreg/standard_table.go) (factory wiring in [`backends_install.go`](internal/pluginreg/backends_install.go), [`frontends_install.go`](internal/pluginreg/frontends_install.go), and [`features_install.go`](internal/pluginreg/features_install.go); mandatory ids in [`lipsdk.StandardDistributionRequirements`](pkg/lipsdk/standard_bundle.go)), loads config, validates mandatory plugins against that requirements list, assembles [`runtimebundle.Built`](internal/infra/runtimebundle/built.go) via [`runtimebundle.Build`](internal/infra/runtimebundle/build.go) with an explicit registry in [`runtimebundle.BuildOptions`](internal/infra/runtimebundle/options.go), a shared upstream [`httpclient.Standard`](internal/infra/httpclient/standard.go) client (overridable for tests), then serves HTTP with [`stdhttp.RunWithRuntime`](internal/stdhttp/server.go). Optional **`routing.health.circuit_breaker`** (`enabled`, `failure_threshold`, `open_for`) wires executor candidate health; when a logger is supplied, the executor emits structured **`lip.route`** routing observations (noop observer if logging is unavailable). Bundled HTTP frontends classify execute failures with [`internal/plugins/frontends/execerr`](internal/plugins/frontends/execerr/execerr.go) (reject vs internal). [`stdhttp.Run`](internal/stdhttp/server.go) is a convenience that calls `Build` (with the provided registry) then `RunWithRuntime`.
- test, vet, lint, and vuln-check entrypoints
- QA scripts, optional git hooks, and a GitHub Actions workflow aligned with the sibling `go-live-market-data-aggregator` process (trimmed for this repo: no domain-specific custom vets)
- deterministic IDs/timestamps in frontend encoders and ACP reference paths where reproducibility matters; the **standard** server path injects a real wall clock and non-deterministic RNG for the executor (see `internal/infra/runtimebundle`)

### Resource bounds (memory / DoS hardening)

- **`lipapi.Call.Validate`** enforces maximum sizes on route selectors, IDs, messages/parts/tool counts, part payloads, extensions, and related option strings (see `pkg/lipapi/limits.go`). Oversized canonical requests fail validation before orchestration runs.
- **`lipapi.Collect`** applies `DefaultCollectLimits` when aggregating streaming events into a single `Collected` struct. Use **`CollectWithLimits`** for custom caps or **`CollectUnbounded`** only for tests/harnesses that deliberately exceed defaults.
- **`b2bua.MemoryStore`** with **TTL disabled** applies a **default maximum number of concurrent A-leg rows** (`DefaultMemoryStoreMaxLegsWithoutTTL`, currently 100k); set **`MemoryStoreOptions.MaxLegs`** to a positive value to override the cap. Negative `max_legs` is rejected. With **TTL enabled**, max-leg count defaults to unlimited and expiry is TTL-driven.

### Routing defaults and continuity

- **Default route selector** when clients omit `X-LIP-Route` is resolved by `routing.EffectiveDefaultRouteSelector` from `routing.default_route` in YAML, then the first enabled backend plus registry default model ids (`pluginreg.DefaultWireModel`). Backend rows use `id` as the **runtime instance id**; optional `kind` sets the bundled factory when you need multiple instances of the same adapter (`kind: openai-responses`, `id: openai-primary`). See [`internal/core/routing/default_route.go`](internal/core/routing/default_route.go).
- **SQLite continuity** (`continuity.store: sqlite` and `continuity.sqlite_path`) persists A-leg rows and attempt lineage across process restarts (`internal/core/continuity/sqlitestore`, pure-Go driver `modernc.org/sqlite`). **`continuity.ttl` / `max_legs` apply only to the in-memory store**; combining them with SQLite is rejected at config load until durable pruning exists.
- **Hook bus**: root `hooks.tool_reactor_error_policy` selects `fail_open` (default), `fail_closed`, or `swallow_event` for tool-reactor errors. Optional reference feature plugins are documented in [`internal/plugins/features/REFERENCE_PLUGINS.md`](internal/plugins/features/REFERENCE_PLUGINS.md).

## QA and local workflow

Fast checks (format, `go mod tidy` drift, build, vet, architecture guardrails in [`internal/archtest`](internal/archtest/guardrails_test.go)) plus staged-package tests mirror the sibling repo’s `quality-checks` / `test-staged` pattern. See [`docs/architecture-guardrails.md`](docs/architecture-guardrails.md).

```bash
make quality-checks   # gofmt -l, tidy+diff guard, go build, go vet
make test             # quality-checks + go test -short -parallel=8 ./...
make test-fast        # quality-checks + tests for staged packages (or all if none staged)
make test-race        # no-op on Windows; use Linux CI or WSL for -race
make test-fuzz        # short fuzz smoke on internal/testkit (override: FUZZTIME=30s make test-fuzz)
make bench            # JSON normalize micro-benchmark in internal/testkit
make qa               # quality-checks + unit tests + golangci-lint + govulncheck (via `go tool`, see go.mod)
make hooks-install    # set core.hooksPath to .githooks (runs scripts/quality-gate on pre-commit when .go is staged)
```

Pre-commit runs `scripts/quality-gate` (quality checks, staged tests, `golangci-lint` if present, `go tool govulncheck`). The race step is not run on Windows; use Linux CI or WSL for `go test -race`.

CI (`.github/workflows/qa.yml`) runs `make quality-checks`, unit tests, strict race on Linux, `golangci-lint-action`, and `go tool govulncheck`.

Linter config lives in `.golangci.yml` (staticcheck, govet, revive, small correctness linters). Prefer `make lint` over ad hoc `staticcheck` so local and CI stay aligned.

## Repository layout

- `cmd/lipstd/` - standard distribution entrypoint (registry + runtimebundle + stdhttp)
- `internal/pluginreg/` - standard bundle registration (`register_standard.go`, `*_install.go`) and registry helpers; mandatory bundled ids are defined in `pkg/lipsdk`
- `pkg/lipapi/` - canonical public contracts
- `pkg/lipsdk/` - stable plugin SDK contracts
- `internal/core/` - runtime, routing, stream, config, admin, capabilities
- `internal/plugins/` - bundled frontend, backend, and feature plugins
- `internal/stdhttp/` - standard distribution HTTP wiring (mount + `RunWithRuntime`)
- `internal/infra/runtimebundle/` - assembles executor, continuity, shared upstream HTTP, health/observer seams
- `internal/infra/` - shared infrastructure seams
- `internal/testkit/` - test support surface scaffold
- `internal/qa/` - repo hygiene tests (root markdown noise, etc.)
- `internal/archtest/` - architecture guardrail tests (budgets, forbidden patterns)
- `internal/refbackend/` - spec-shaped HTTP emulator servers for tests (`*_test.go` imports only)
- `internal/refclient/` - official-SDK reference clients for conformance/matrix tests
- `internal/plugins/stores/` - bundled persistence / continuity store plugins (intentional seam alongside backends)
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

Install [golangci-lint](https://golangci-lint.run/) for the full `make qa` profile; `govulncheck` is invoked as `go tool govulncheck` (version pinned via the `tool` line and `golang.org/x/vuln` in `go.mod`).
