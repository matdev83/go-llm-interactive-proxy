# Source index

Generated: 2026-07-07

The review was based on static inspection of the following repository files and architectural documents.

## High-level product and package map

| Path | Why it matters |
| --- | --- |
| `README.md` | Describes product shape, standard distribution, supported frontends/backends/features, runtime configuration, operations, QA workflow, and repository layout. |
| `AGENTS.md` | Defines non-negotiable architecture guardrails: small core, plugin-first features, streaming-first execution, core-owned routing/failover/B2BUA, no provider SDK imports from core, no concrete plugin imports from core, no pairwise protocol translators, explicit construction. |
| `.kiro/steering/structure.md` | Provides the strongest project-specific architectural map and pragmatic hexagonal interpretation. It explicitly classifies primary zones and names `pluginreg`, `runtimebundle`, and `stdhttp` as standard distribution assembly, not another core. |
| `docs/architecture.md` | Current-state runtime map: request flow, core-owned behavior, plugin-owned behavior, extension platform, composition/startup sequence, diagnostics, and permanent boundaries. |
| `docs/architecture-guardrails.md` | Lists automated architecture checks and scope caveats. |
| `docs/adr/0005-architecture-guardrails-and-complexity-budgets.md` | Accepts line-count budgets, no hidden registration, explicit registry ownership, and core import boundary. |

## Architecture tests and machine-readable baselines

| Path | Why it matters |
| --- | --- |
| `internal/archtest/guardrails_test.go` | Enforces non-test line budgets for `internal/core`, `internal/pluginreg`, `internal/stdhttp`, and `internal/infra/runtimebundle`; forbids hidden `init()` registration and package-level registry/sync.Once patterns in key paths. |
| `internal/core/runtime/boundaries_test.go` | Enforces that core production packages do not depend on concrete plugins or reference emulators. |
| `internal/archtest/extension_platform_boundaries_test.go` | Enforces public contract purity, no provider SDK leakage into `pkg/lipapi`/`pkg/lipsdk`, feature plugin SDK-only dependency, no `stdhttp`/protocol plugin dependency from core, and no `net/http` import in core runtime. |
| `testdata/architecture/hexagonal_migration_baseline.json` | Machine-readable migration register. Especially important because it classifies `internal/infra/runtimebundle` as `extract` and `internal/pluginreg` as an `exception` with a retirement trigger. |
| `internal/archtest/hexagonal_migration_baseline_test.go` | Locks the migration baseline against silent drift. |

## Composition and runtime hotspots

| Path | Why it matters |
| --- | --- |
| `internal/infra/runtimebundle/doc.go` | Declares the package as the standard-distribution composition root and states that it lives outside core to keep core orchestration-only. |
| `internal/infra/runtimebundle/build.go` | Main composition hotspot. Builds control plane, auth events, auth providers, metrics, HTTP client, model catalog, backends, model registry, continuity store, secure-session runtime, route defaults, executor, accounting, and extension snapshot. |
| `internal/infra/runtimebundle/options.go` | Wide `BuildOptions` bag spanning startup context, HTTP client, tracing, testing clock, control-plane override, registry, wire model, auth providers, deciders, session/workspace/traffic/usage/policy extension surfaces, and secure-session diagnostics store. |
| `internal/stdhttp/server.go` | Standard HTTP server hotspot. Mounts diagnostics/admin/metrics/model-catalog/control-plane/frontends, builds middleware stack, manages server lifecycle and shutdown, and contains convenience runtime building path. |
| `internal/stdhttp/wire.go` | Thin wrapper around `runtimebundle.BuildExecutor`; likely a candidate for deprecation/removal if no longer needed. |
| `internal/pluginreg/reg.go` | Registry implementation and contracts. Also stores backend credential/security metadata and auth error renderers. |
| `internal/pluginreg/standard_table.go` | Imports all bundled frontends, backends, and feature plugins to assemble the standard distribution table. Correct as a composition boundary, but too broad for a narrow “registry” package over time. |
| `internal/pluginreg/featurebundle.go` | Transitional hook-only to feature-bundle adapter. Useful for migration, but should not remain a permanent bridge if the target is a clean SDK feature bundle model. |
| `internal/pluginreg/frontends_install.go` | Repetitive frontend mount wiring; small but visible DRY opportunity. |
| `internal/core/runtime/executor.go` | Core orchestration hotspot. `Executor` has many fields and `Execute` coordinates validation, runtime snapshot, secure session, A-leg lifecycle, route parsing, planning/opening, failover, retry stream, accounting/recovery/interleaved wrappers. |

## Dependency surface

| Path | Why it matters |
| --- | --- |
| `go.mod` | Direct module dependencies include provider SDKs, Prometheus, OpenTelemetry, Bun, SQLite, tokenizers, and other infrastructure. This is acceptable in a single-module app if architecture tests prevent SDK leakage into core/public contracts. |

