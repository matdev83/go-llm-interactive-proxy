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
- **Canonical reload contract** - Explicit SIGHUP/management-API reload through `pkg/lipsdk/configreload` (no watcher; DTOs never carry paths, credentials, or raw YAML). This is the **one reload contract** beside **one process runtime / ProcessServices**, **one generation runtime**, and **one private-field host** (`runtimebundle.BuildHost` / `Host.Close`). `check-config` validates without publishing a generation. Operator contract: [`docs/runtime-config-reload.md`](docs/runtime-config-reload.md).
- **Accounting and dual-plane economics** - Optional metering journal and authority stores. Post-turn billing rates sealed usage records; the runtime never enriches stream-time prices. Rollout: [`docs/dual-plane-rollout.md`](docs/dual-plane-rollout.md). Billing injection: [`docs/billing-host-composition.md`](docs/billing-host-composition.md). Feature gates: [`docs/release-gates.md`](docs/release-gates.md).
- **Public production facade** - `pkg/lipruntime` builds the standard distribution without importing `internal/`. Public `lipruntime.Options` uses `RequestRegistrations` / `AttemptRegistrations` / `ConcurrencyRegistration` only. Supported methods: `Build`, `ExecutorView`, `Ready`, `Capabilities`, `MeteringQuerier`, `ReadinessReport`, `RefreshSnapshots`, `Reload`, `ReloadStatus`, `ReloadControl`, `Close`. Field map: [`docs/legacy-options-migration.md`](docs/legacy-options-migration.md).

## Standard distribution

Hybrid backends ([ADR 0008](docs/adr/0008-hybrid-backend-connector-plugins.md)): essential kinds are code-owned by [`internal/standardplugins`](internal/standardplugins) (`EssentialBackendBundle` / tables in `standard_table.go`); optional connectors are executable plugins under `connectors/` via closed manifests. Mandatory distribution subset is in [`pkg/lipsdk/standard_bundle.go`](pkg/lipsdk/standard_bundle.go).

| Surface | Bundled support |
| --- | --- |
| Frontends | `openai-responses`, `openai-legacy`, `anthropic`, `gemini` |
| Hosted/provider backends | Built-in: `openai-responses`, `openai-legacy`, `anthropic`, `gemini`, `bedrock`. External plugins: `acp` family, `openrouter`, `nvidia`, `huggingface`, `opencode-go`/`opencode-zen` (`connectors/opencode` one artifact), `openai-codex`/`openai-codex-app-server` (`connectors/codex` one artifact) |
| Local / compatible backends | External: `ollama`, `ollama-cloud`, `llamacpp`, `lmstudio`, `vllm`, `local-stub`. Built-in: custom OpenAI/Anthropic-compatible kinds — see [`docs/custom-compatible-backends.md`](docs/custom-compatible-backends.md) |
| Local-agent / experimental | External `cursorcliacp` connector; experimental external `cursorsdk` connector (Node `bridge-node` over `@cursor/sdk` 1.0.23) discovered via closed manifest — see [`docs/cursor-sdk-backend.md`](docs/cursor-sdk-backend.md) |
| Feature plugins | no-op compatibility hooks plus reference/proof plugins for submit, parts, tools, workspace guard, traffic transcript, verifier, pre-request policy, auto-append, and Codex client compatibility; standard distro also default-enables canonical `tool-call-repair` (ADR 0007; opt out with `enabled: false`) |

## Quick start

Start with the no-key local stub path when you want to validate config, routing, inventory, and HTTP serving without hosted provider credentials. `local-stub` is an external connector (`connectors/localstub`); stage it before using the example config:

```bash
make package-full PACKAGE_DEST=.golip-plugins
go run ./cmd/lipstd check-config --config ./config/examples/dogfood-local-stub.yaml
go run ./cmd/lipstd routes --config ./config/examples/dogfood-local-stub.yaml
go run ./cmd/lipstd inventory --config ./config/examples/dogfood-local-stub.yaml
go run ./cmd/lipstd inspect --config ./config/examples/dogfood-local-stub.yaml
go run ./cmd/lipstd doctor --config ./config/examples/dogfood-local-stub.yaml --instance dogfood-local
go run ./cmd/lipstd serve --config ./config/examples/dogfood-local-stub.yaml
```

`inspect` reports built-in/discovered/configured plugin states without launching processes. `doctor --instance <id>` may launch only that configured backend instance for secure-channel checks (never all discovered plugins; no connector credentials after peer/channel failure). Optional `plugins.backend_discovery` configures trusted discovery roots (`enabled`, `paths`, `strict`, `development_mode`).

For hosted providers, use [`config/config.yaml`](config/config.yaml) as the sample and provide API keys through YAML or environment variables. `standardplugins.ResolveUpstreamAPIKeysFromEnv` resolves the supported provider env vars and numbered variants once at startup; see [`internal/standardplugins/keys.go`](internal/standardplugins/keys.go) for the exact names and numbering rules.

```bash
go run ./cmd/lipstd --config ./config/config.yaml
```

## Releases and installation

Prebuilt `lipstd` binaries for Linux, macOS, and Windows (`amd64` and `arm64`) are published through GitHub Releases when a semantic version tag (`vX.Y.Z`) is pushed. Each release includes platform archives (`.tar.gz` on Linux/macOS, `.zip` on Windows), `checksums.txt` (SHA-256), and build-provenance attestations.

After downloading an archive for your OS/architecture:

```bash
# Linux/macOS example
tar -xzf go-llm-interactive-proxy_vX.Y.Z_linux_amd64.tar.gz
./lipstd --version
./lipstd check-config --config ./config/config.yaml
```

Verify the archive checksum against `checksums.txt` before use. Connector plugins remain separate installable artifacts; see [`docs/backend-plugins/operator.md`](docs/backend-plugins/operator.md).

**License:** This repository does not currently ship an owner-approved open-source license. Public redistribution rights remain undefined until one is added.

## Repository file policy

Every tracked file must match an approved path pattern or exact entry in [`.release-files`](.release-files). The manifest uses pattern wildcards (`.kiro/**`, `.agents/**`, `docs/**`, `internal/**`, `pkg/**`) to cover specifications, agent skills, documentation, and package trees without requiring individual file enumeration for spec authors or document creators.

The manifest is enforced locally and in CI (`Repo hygiene`):

```bash
bash scripts/check-release-clean.sh          # working tree
bash scripts/check-release-clean.sh --staged   # staged index (pre-commit)
bash scripts/check-release-clean.sh --ref HEAD # specific revision
```

Install versioned Git hooks (manifest check on commit/push, plus the 100-file change-size gate):

```bash
bash scripts/setup-hooks.sh
```

Commits and PRs may not change more than 100 files. Override with `LIP_ALLOW_LARGE_CHANGE=1`, `git config lip.allowLargeChange true`, or the `allow-large-change` PR label. `scripts/check-change-size.sh` (or `scripts/check-change-size.ps1`) is the same checker the hooks and CI run.

New top-level files or new component directories must be covered by patterns or entries in `.release-files` in the same commit. CI never auto-updates the manifest.

`lipstd` accepts `--config` before or after the subcommand; if it appears more than once, the later value wins. See [`docs/dogfood-local.md`](docs/dogfood-local.md) for the full local dogfood flow. Truncated tool-call repair can be exercised with [`config/examples/dogfood-tool-call-repair.yaml`](config/examples/dogfood-tool-call-repair.yaml) (see ADR [`docs/adr/0007-canonical-tool-call-repair.md`](docs/adr/0007-canonical-tool-call-repair.md)).

## Configuration and operations

- **Config** - Runtime config is typed and loaded from YAML. [`config/config.yaml`](config/config.yaml) documents access/auth templates, server timeouts, logging, diagnostics, observability, routing, continuity, **identity** (A-leg Server / B-leg User-Agent; OpenRouter HTTP attribution for the external `openrouter` plugin is configured in that connector), and provider rows. See [`docs/proxy-identity.md`](docs/proxy-identity.md). [`config/config.multi-instance.example.yaml`](config/config.multi-instance.example.yaml) shows multiple backend instances of the same adapter.
- **Usage-record billing cutover** - Set `accounting.billing.authoritative: true` only when composition injects a Bun `BillingStore` (plus admission, identity, and rating resolvers). The YAML flag does not open a journal by itself; `lipstd` and public `lipruntime.Options` do not invent billing accounts. Injected composition mounts journal/TUR-backed reports under `accounting.billing.reports_path` (default `/admin/billing`, diagnostics-secret protected), starts the post-turn worker, and makes sealed TUR/LUR the sole monetary settlement authority. Runtime fails closed if the flag is set without `BillingAdmission`. Customer rating bills observed provider-accepted usage (input when accepted; completion tokens when generated) even on cancel/connectivity loss—OpenRouter-style cost recovery—capped by the admission hold. Stream handlers never enrich prices or write the legacy token ledger; leftover `accounting.ledger.*` YAML is accepted and is not opened. Production `accounting.authority` YAML rejects monetary `budget` / `spend_cap` / `money_nano` rules. Client-visible usage may still be projected for protocol compatibility and is never authoritative money. Composition recipe: [`docs/billing-host-composition.md`](docs/billing-host-composition.md).
- **Routing** - Default selectors come from `routing.default_route` or the first enabled backend plus registry default model ids. `model_aliases` rewrite full selector strings before parsing. Route selectors support ordered failover, weights, first-request annotations, parallel `!` races, per-leg `[handicap=N]`, global/per-leg TTFT budgets, and per-leaf query generation parameters. Route query parameters such as `?reasoning_effort=xhigh` and `?verbosity=high` are explicit routing directives: when present, they override matching per-request body/canonical generation options; absent parameters leave request values unchanged. Route-wide stickiness is opt-in with `{affinity=session}` or `{affinity=client}` (aliases `{session_sticky}`, `{client_sticky}`). Interleaved thinking (`[thinker]` on one weighted branch) is off unless `interleaved.enabled` is true; `interleaved.stream_to_client` is `hidden` (default) or `visible`. Runtime A-leg routing overrides are opt-in under `routing.override_admin` (`enabled` defaults false). When enabled, protected GET/PUT/DELETE at `path_prefix` (default `/admin/routing-overrides/{a_leg_id}`) set, replace, inspect, or clear a sticky selector for later turns on that A-leg; in-flight turns keep their snapshotted revision. The admin route is mounted inside the request-plane access-auth stack (API-key/auth middleware plus the diagnostics shared secret). `path_prefix` must be a literal ServeMux path (braces/`{id}` wildcards are rejected) and must not overlap other diagnostics or admin mounts. Disabling the HTTP surface does not clear or suspend already-persisted overrides. Non-loopback exposure requires the same diagnostics shared secret as other protected admin surfaces.
- **OpenAI Codex verbosity bumps** - The `openai-codex` backend defaults to `text.verbosity=high` for the first 5 turns of each conversation, and then again on every 10th turn by default, when no explicit per-request verbosity is set. Opt out with `early_session_verbosity_bump_disabled: true` and/or `mid_session_verbosity_bump_disabled: true`, or tune with `early_session_verbosity_bump_turns` / `mid_session_verbosity_bump_frequency`. When the mid-session bump is disabled, the cadence value is ignored. See [`docs/openai-codex-backend.md`](docs/openai-codex-backend.md#early-session-verbosity-bump) and [`docs/openai-codex-backend.md`](docs/openai-codex-backend.md#mid-session-verbosity-bump).
- **Experimental Cursor SDK** - Optional local-only `cursorsdk` connector under `connectors/cursorsdk` (not in `EssentialBackendBundle`; manifest-discovered, not root-static). Install the packaged Node `bridge-node` companion manually (exact `@cursor/sdk` 1.0.23, Node ≥ 22.13); Go-LIP never runs npm. Use explicit `cursorsdk:…` routes, separate SDK API-key billing (`CURSOR_API_KEY` / `api_key`), and sandbox/settings defaults documented in [`docs/cursor-sdk-backend.md`](docs/cursor-sdk-backend.md). Schema example: [`config/examples/cursor-sdk-experimental.yaml`](config/examples/cursor-sdk-experimental.yaml). Offline ACP-vs-SDK matrix: `make test-cursor-sdk-comparison-report`. `cursorcliacp` remains a separate external connector.
- **Continuity** - `continuity.store: memory` is the default. `continuity.store: sqlite` with `continuity.sqlite_path` persists A-leg rows and attempt lineage through [`internal/core/continuity/sqlitestore`](internal/core/continuity/sqlitestore). In-memory `ttl` and `max_legs` tuning does not apply to SQLite.
- **Security** - Multi-user or non-loopback deployments need explicit auth/access posture. Local API keys must be at least 16 Unicode code points after trimming. Diagnostics, pprof, metrics, model-catalog diagnostics, and secure-session summaries require a shared secret when exposed beyond loopback. The separate management reload API (`POST /admin/config/reload`, `GET /admin/config/status`) is disabled unless `LIP_RELOAD_MANAGEMENT_ADDRESS` supplies its startup-fixed bind (recommended loopback: `127.0.0.1:9090`), so existing starts and multiple local instances do not contend for a hidden fixed port. Its authentication is independent of data-plane cookies or local API keys: explicitly enabled single-user loopback may use documented local trust; multi-user or non-loopback requires `LIP_RELOAD_MANAGEMENT_TOKEN` (≥16 Unicode code points). When required management settings are absent, management stays disabled with a warning and ordinary data-plane serve continues. Runtime reload is explicit-trigger only (no watcher/auto-retry); see [`docs/runtime-config-reload.md`](docs/runtime-config-reload.md). On Unix, OpenAI Codex `auth.json` and managed-OAuth account files must be `0600` (group/other-readable files are now rejected at load); symlinked managed-OAuth account files are skipped. See [`docs/openai-codex-backend.md`](docs/openai-codex-backend.md#token-file-permissions). Optional **secrets guard** (`plugins.features` id `secrets-guard`, disabled by default) scans model-bound ingress for loaded secret values only, including JSON object keys and scalar tokens. It does not scan responses/egress or transformed forms; `log` leaves ingress unchanged, and JSON `redact` blocks unsupported key/scalar tokens with a normal `block` decision so quarantine still applies. Multi-user matching uses only the current request credential and safe attribution identifiers. Only one enabled `secrets-guard` feature instance is supported per deployment, and rollout is staged as disabled -> `log` -> `redact` -> `block`, one action per deployment. See [`docs/secrets-guard.md`](docs/secrets-guard.md) and [`config/examples/secrets-guard-*.yaml`](config/examples/).
- **Observability** - Optional Prometheus metrics and OpenTelemetry tracing are configured under `observability`. Access logs use bounded-cardinality route groups by default; raw paths are opt-in.
- **HTTP clients** - The shared upstream client honors `HTTP_PROXY` / `HTTPS_PROXY` by default. Set `http_client.trust_environment_proxy: false` when process environment is not trusted.
- **Backend retry posture** - The hosted `openai-responses`, `openai-legacy`, and `anthropic` backend factories default `sdk_max_retries` to **0**: retry policy above the HTTP round trip lives in pre-output credential rotation and core failover, not in SDK-transparent retries. Operators may raise `sdk_max_retries` per backend row to opt back into provider-SDK retries.
- **Resource bounds** - `lipapi.Call.Validate`, `lipapi.Collect` limits, pending wire event caps (`max_pending_wire_events`; **0 = unlimited**), B2BUA store caps, and shared frontend **decode admission** (`max_concurrent_decodes` default **32**, `max_inflight_decode_bytes` default **64 MiB**) protect memory and request size boundaries. Absolute decompressed body oversize is **413**; temporary decode admission saturation is **429** + `Retry-After: 1`. Admission runs after body ReadAll (bytes already resident) and covers protocol Decode only. Raise body and inflight budgets together for large multimodal / long-context.

More detail: [`docs/proxy-identity.md`](docs/proxy-identity.md), [`docs/runtime-config-reload.md`](docs/runtime-config-reload.md), [`docs/secrets-guard.md`](docs/secrets-guard.md), [`docs/database-persistence.md`](docs/database-persistence.md), [`docs/routing-health-circuit-breaker.md`](docs/routing-health-circuit-breaker.md), [`docs/execerr-classification.md`](docs/execerr-classification.md), [`docs/extension-platform-authoring.md`](docs/extension-platform-authoring.md), [`docs/release-gates.md`](docs/release-gates.md), and [`docs/cursor-sdk-backend.md`](docs/cursor-sdk-backend.md).

## Developer workflow

```bash
make quality-checks        # gofmt drift, go mod tidy drift, build, vet, guard scripts, archtest
make arch-report           # architecture metrics Markdown; exits non-zero if Req 11.5 net shrinkage fails
make test                  # quality-checks + unit tests + parity-checks
make test-unit             # go test -parallel=8 -timeout=10m ./...
make test-precommit-extra  # precommit-tagged hygiene + executor matrices
make test-fast             # cached guard checks + complete root test graph (safe reverse-dependency coverage)
make parity-checks         # conformance package with -tags=precommit,integration
make test-fuzz             # short fuzz smoke over release-gate fuzz targets
make test-race             # skipped on Windows; strict race runs in nightly CI on Linux
make bench                 # benchmark smoke for hot packages
make pgo-profile           # collect default.pgo from core benches (optional; move under cmd/lipstd)
make pgo-build             # build cmd/lipstd (auto-applies cmd/lipstd/default.pgo when present)
make qa                    # cached fast quality checks + tagged tests + lint + govulncheck + release-gates-static
make isolated-root-qa      # GOWORK=off QA on a temp root copy without connectors/support/Node/artifacts
make installed-plugin-smoke # one lipstd binary; install release artifacts; same-binary inspect/doctor/invoke
make docs-check knowledge-check # backend-plugin docs + steering hybrid consistency
make example-config-check  # operator/example YAML + config/examples bootstrap inspect
make backend-plugin-cross-platform-qa # connector platform matrix compile/package + native lifecycle gates
make backend-plugin-release-gates-static # release report/traceability/wiring (also via make qa)
make backend-plugin-release-gates # full connector/support module matrix + root release suites
make hooks-install         # install optional legacy pre-commit hooks (.githooks)
bash scripts/setup-hooks.sh # install manifest pre-commit/pre-push hooks (recommended)
```

Operator install/trust/diagnostics/upgrade/rollback for executable backend plugins: [`docs/backend-plugins/operator.md`](docs/backend-plugins/operator.md); threat model / trust equivalence: [`docs/backend-plugins/threat-model.md`](docs/backend-plugins/threat-model.md) (`make backend-plugin-security-checks`); cross-platform packaging/IPC matrix: `make backend-plugin-cross-platform-qa`; final release gates: `make backend-plugin-release-gates` ([ADR 0008](docs/adr/0008-hybrid-backend-connector-plugins.md)).

PR CI includes:

- **Repo hygiene** (`.github/workflows/ci.yml`) — exact `.release-files` manifest on every push/PR; cross-platform tests and `lipstd` build. Linux/macOS run `go test -race`; Windows runs `go test` because the ACP PATH-cache stress test is prohibitively slow under the Windows race runtime.
- **QA** (`.github/workflows/qa.yml`) — when `*.go` changes: quality checks, plugin gates, PostgreSQL proofs, integration tests, lint, govulncheck.
- **CodeQL**, **Go vulnerability check**, and **OpenSSF Scorecard** on `main` and PRs (where configured).

Nightly CI (`.github/workflows/race-fuzz-nightly.yml`, also `workflow_dispatch`) runs strict Linux race and Tier-1 fuzz smoke (`FUZZTIME=6s`). Locally, `make lint` prefers `go tool golangci-lint` (pinned in `go.mod` `tool`) and falls back to a PATH install. A monthly modernization workflow (`.github/workflows/modernize-monthly.yml`) re-runs the `modernize` linter suite and govulncheck. Linter config lives in [`.golangci.yml`](.golangci.yml).

Recoverability is defined by tests, `testdata/` goldens, stable `pkg/lipapi` / `pkg/lipsdk` contracts, and steering. Cross-protocol parity: `make parity-checks`.

## Repository layout

- `cmd/lipstd/` - standard distribution command and wiring tests.
- `pkg/lipapi/` - canonical request, event, capability, validation, and error contracts.
- `pkg/lipsdk/` - stable plugin SDK contracts and standard distribution requirements.
  - Compatibility note: `FrontendMountOptions` gained an optional `DecodeAdmission` field. Use **named** composite literals; unkeyed literals that previously listed every field in order will not compile.
  - Compatibility note: `pkg/lipsdk/configreload.AllResultCategories` (exported mutable var) was replaced by `func ResultCategories() []ResultCategory`, which returns a defensive copy per call; the `pkg/lipruntime.AllResultCategories` alias is now a function alias for the same accessor. Readers of the former var must call the function; external assignment is no longer possible.
- `internal/core/` - runtime orchestration, routing, continuity, secure sessions, hooks/extensions, stream handling, policy, accounting, config, admin, diagnostics, and safety.
- `internal/plugins/` - bundled frontend, essential backend, feature, and protocol-helper packages.
- `connectors/`, `connector-support/` - optional executable backend plugins and shared connector support modules (ADR 0008).
- `internal/standardplugins/` - essential/static registration tables, per-backend factory helpers, and `InstallStandardBundleOn`.
- `internal/featurebundle/` - feature merge surface (`MergeFeatureSurface` over SDK hook slices).
- `internal/pluginreg/` - explicit per-composition-root registry and discovered connector registration.
- `internal/infra/runtimebundle/` and `internal/stdhttp/` - runtime assembly (executor, hook bus, stores) and HTTP mounting/serving.
- `internal/infra/` - logging, HTTP client tuning, metrics, tracing, DB, model catalog/registry, routing health, tokenization/accounting, and auth-event plumbing.
- `internal/refbackend/`, `internal/refclient/`, `internal/testkit/` - emulators, reference clients, fixtures, stubs, and conformance helpers for tests.
- `internal/archtest/`, `internal/qa/`, `scripts/`, `.githooks/`, `.github/workflows/` - guardrails and quality automation.
- `docs/` (includes ADR 0008, `docs/knowledge` knowledge-check), `.kiro/`, `testdata/`, `config/` - operator docs, steering/spec artifacts, fixtures, and sample configs.

## Relationship to Python LIP

This repository is the Go implementation of LIP with a smaller core and explicit plugin/SDK boundaries. The sibling Python project remains useful historical context and migration reference, but Go documentation should describe only behavior implemented in this repo unless a doc explicitly says a feature is Python-era or future migration work.
