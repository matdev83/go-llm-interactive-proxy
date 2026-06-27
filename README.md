# Go LLM Interactive Proxy

Go LLM Interactive Proxy (LIP) is a streaming-first control plane for LLM traffic. It sits between AI clients and provider backends so operators can keep client integrations stable while changing routing, provider mix, resilience behavior, observability, and extension policy at the proxy layer.

The standard distribution, `cmd/lipstd`, serves bundled HTTP frontends, routes through canonical `lipapi` requests and event streams, and wires the official backends and feature plugins through explicit registration.

## What it does

- **Multi-protocol frontends** - OpenAI Responses, legacy OpenAI-compatible chat, Anthropic Messages, and Gemini generateContent-compatible HTTP surfaces.
- **Backend flexibility** - hosted provider adapters, OpenAI-compatible/local runtimes, agent-specific backends, custom-compatible backend rows, and a no-key `localstub` backend for dogfood.
- **Canonical translation** - frontend and backend adapters translate through one protocol-neutral request model and event stream; no pairwise protocol translators.
- **Core-owned routing** - ordered failover, weighted routing, parallel races, TTFT budgets, model aliases, route diagnostics, and circuit-breaker eligibility live in the core.
- **Continuity and recovery** - B2BUA-style A-leg/B-leg lineage records recoverable pre-output attempts, while post-output failures are surfaced instead of silently retried.
- **Operator hardening** - typed config, auth/access modes, secure sessions, diagnostics secrets, pprof controls, Prometheus metrics, OpenTelemetry tracing, access logs, and resource limits.
- **Extension platform** - feature bundles use `pkg/lipsdk` facades for request shaping, tools, completion gates, workspace/state, traffic observation, auxiliary calls, and compatibility hooks.

## Standard distribution

Exact registration is code-owned by [`internal/pluginreg/standard_table.go`](internal/pluginreg/standard_table.go); mandatory distribution subset is in [`pkg/lipsdk/standard_bundle.go`](pkg/lipsdk/standard_bundle.go).

| Surface | Bundled support |
| --- | --- |
| Frontends | `openai-responses`, `openai-legacy`, `anthropic`, `gemini` |
| Hosted/provider backends | `openai-responses`, `openai-legacy`, `anthropic`, `gemini`, `bedrock`, `acp`, `openrouter`, `nvidia`, `huggingface`, `openai-codex`, `opencode-go`, `opencode-zen` |
| Local / compatible backends | `ollama`, `ollama-cloud`, `llamacpp`, `lmstudio`, `vllm`, `localstub`, custom OpenAI/Anthropic-compatible backend kinds |
| Feature plugins | no-op compatibility hooks plus reference/proof plugins for submit, parts, tools, workspace guard, traffic transcript, verifier, pre-request policy, auto-append, and Codex client compatibility |

## Quick start

Start with the no-key local stub path when you want to validate config, routing, inventory, and HTTP serving without hosted provider credentials:

```bash
go run ./cmd/lipstd check-config --config ./config/examples/dogfood-local-stub.yaml
go run ./cmd/lipstd routes --config ./config/examples/dogfood-local-stub.yaml
go run ./cmd/lipstd inventory --config ./config/examples/dogfood-local-stub.yaml
go run ./cmd/lipstd serve --config ./config/examples/dogfood-local-stub.yaml
```

For hosted providers, use [`config/config.yaml`](config/config.yaml) as the sample and provide API keys through YAML or environment variables. `pluginreg.ResolveUpstreamAPIKeysFromEnv` resolves the supported provider env vars and numbered variants once at startup; see [`internal/pluginreg/keys.go`](internal/pluginreg/keys.go) for the exact names and numbering rules.

```bash
go run ./cmd/lipstd --config ./config/config.yaml
```

`lipstd` accepts `--config` before or after the subcommand; if it appears more than once, the later value wins. See [`docs/dogfood-local.md`](docs/dogfood-local.md) for the full local dogfood flow.

## Configuration and operations

- **Config** - Runtime config is typed and loaded from YAML. [`config/config.yaml`](config/config.yaml) documents access/auth templates, server timeouts, logging, diagnostics, observability, routing, continuity, and provider rows. [`config/config.multi-instance.example.yaml`](config/config.multi-instance.example.yaml) shows multiple backend instances of the same adapter.
- **Routing** - Default selectors come from `routing.default_route` or the first enabled backend plus registry default model ids. `model_aliases` rewrite full selector strings before parsing. Route selectors support ordered failover, weights, first-request annotations, parallel `!` races, per-leg `[handicap=N]`, and global/per-leg TTFT budgets.
- **Continuity** - `continuity.store: memory` is the default. `continuity.store: sqlite` with `continuity.sqlite_path` persists A-leg rows and attempt lineage through [`internal/core/continuity/sqlitestore`](internal/core/continuity/sqlitestore). In-memory `ttl` and `max_legs` tuning does not apply to SQLite.
- **Security** - Multi-user or non-loopback deployments need explicit auth/access posture. Local API keys must be at least 16 Unicode code points after trimming. Diagnostics, pprof, metrics, model-catalog diagnostics, and secure-session summaries require a shared secret when exposed beyond loopback.
- **Observability** - Optional Prometheus metrics and OpenTelemetry tracing are configured under `observability`. Access logs use bounded-cardinality route groups by default; raw paths are opt-in.
- **HTTP clients** - The shared upstream client honors `HTTP_PROXY` / `HTTPS_PROXY` by default. Set `http_client.trust_environment_proxy: false` when process environment is not trusted.
- **Resource bounds** - `lipapi.Call.Validate`, `lipapi.Collect` limits, pending wire event caps, and B2BUA store caps protect memory and request size boundaries.

More detail: [`docs/database-persistence.md`](docs/database-persistence.md), [`docs/routing-health-circuit-breaker.md`](docs/routing-health-circuit-breaker.md), [`docs/execerr-classification.md`](docs/execerr-classification.md), [`docs/extension-platform-authoring.md`](docs/extension-platform-authoring.md), [`docs/performance-checks.md`](docs/performance-checks.md), and [`docs/release-gates.md`](docs/release-gates.md).

## Developer workflow

```bash
make quality-checks        # gofmt drift, go mod tidy drift, build, vet, guard scripts, archtest
make test                  # quality-checks + unit tests + parity-checks
make test-unit             # go test -parallel=8 -timeout=10m ./...
make test-precommit-extra  # precommit-tagged hygiene + executor matrices
make test-fast             # quality-checks + staged-package tests, or all when none staged
make parity-checks         # conformance package with -tags=precommit,integration
make test-fuzz             # short fuzz smoke over release-gate fuzz targets
make test-race             # skipped on Windows; strict race runs in CI on Linux
make bench                 # benchmark smoke for hot packages
make qa                    # quality-checks + one full tagged test pass + lint + govulncheck
make hooks-install         # install optional pre-commit hooks
```

CI (`.github/workflows/qa.yml`) runs `make quality-checks`, `go test -parallel=8 -tags=precommit,integration ./...`, fuzz smoke, strict Linux race, golangci-lint v2, and `go tool govulncheck ./...`. Linter config lives in [`.golangci.yml`](.golangci.yml).

Recoverability is defined by the specification bundle: tests, `testdata/` goldens, stable `pkg/lipapi` / `pkg/lipsdk` contracts, steering, and parity/scenario docs. Start at [`docs/spec-bundle-index.md`](docs/spec-bundle-index.md), [`docs/conformance-golden-coverage.md`](docs/conformance-golden-coverage.md), and [`docs/conformance-matrix-evidence.md`](docs/conformance-matrix-evidence.md).

## Repository layout

- `cmd/lipstd/` - standard distribution command and wiring tests.
- `pkg/lipapi/` - canonical request, event, capability, validation, and error contracts.
- `pkg/lipsdk/` - stable plugin SDK contracts and standard distribution requirements.
- `internal/core/` - runtime orchestration, routing, continuity, secure sessions, hooks/extensions, stream handling, policy, accounting, config, admin, diagnostics, and safety.
- `internal/plugins/` - bundled frontend, backend, feature, compatibility, and protocol-helper packages.
- `internal/pluginreg/` - explicit standard bundle registration and backend factory helpers.
- `internal/infra/runtimebundle/` and `internal/stdhttp/` - runtime assembly and HTTP mounting/serving.
- `internal/infra/` - logging, HTTP client tuning, metrics, tracing, DB, model catalog/registry, routing health, tokenization/accounting, and auth-event plumbing.
- `internal/refbackend/`, `internal/refclient/`, `internal/testkit/` - emulators, reference clients, fixtures, stubs, and conformance helpers for tests.
- `internal/archtest/`, `internal/qa/`, `scripts/`, `.githooks/`, `.github/workflows/` - guardrails and quality automation.
- `docs/`, `.kiro/`, `testdata/`, `config/` - operator docs, steering/spec artifacts, fixtures, and sample configs.

## Relationship to Python LIP

This repository is the Go implementation of LIP with a smaller core and explicit plugin/SDK boundaries. The sibling Python project remains useful historical context and migration reference, but Go documentation should describe only behavior implemented in this repo unless a doc explicitly says a feature is Python-era or future migration work.
