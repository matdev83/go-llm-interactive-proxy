# Specification bundle index (core invariants)

This page lists **named scenario registries** in the Go core: stable **SB-** IDs, Go entrypoints, primary packages, and the **precommit** test that keeps each doc aligned with `*_test.go` sources.

| Area | Package | Go entrypoint | Doc | Precommit check |
|------|---------|---------------|-----|-----------------|
| Executor / routing & failover | `internal/core/runtime` | `SpecBundleOrchestrationScenarios` | [spec-bundle-orchestration-scenarios.md](spec-bundle-orchestration-scenarios.md) | `go test -tags=precommit ./internal/core/runtime/...` |
| Continuity / B2BUA store | `internal/core/b2bua` | `SpecBundleContinuityScenarios` | [spec-bundle-continuity-scenarios.md](spec-bundle-continuity-scenarios.md) | `go test -tags=precommit ./internal/core/b2bua/...` |
| Route selector & planner | `internal/core/routing` | `SpecBundleRoutingScenarios` | [spec-bundle-routing-scenarios.md](spec-bundle-routing-scenarios.md) | `go test -tags=precommit ./internal/core/routing/...` |
| Hook bus (submit, parts, tool reactors) | `internal/core/hooks` | `SpecBundleHookScenarios` | [spec-bundle-hook-scenarios.md](spec-bundle-hook-scenarios.md) | `go test -tags=precommit ./internal/core/hooks/...` |
| Proxy identity (A-leg/B-leg, #147) | `internal/core/identity` | `SpecBundleIdentityScenarios` | [spec-bundle-identity-scenarios.md](spec-bundle-identity-scenarios.md) | `go test -tags=precommit ./internal/core/identity/...` |

Runtime config reload is **not** an `SB-*` orchestration registry row; it is a process-host feature with its own spec and operator contract: [runtime-config-reload.md](runtime-config-reload.md), [ADR 0008](adr/0008-versioned-runtime-config-reload.md), `.kiro/specs/versioned-runtime-reloadable-proxy-configuration/`. Focused evidence commands are listed under [release-gates.md](release-gates.md#versioned-runtime-config-reload).

For FE×BE matrix traceability and integration build tags, see [conformance-matrix-evidence.md](conformance-matrix-evidence.md). For migration goldens and parity file ownership, see [conformance-golden-coverage.md](conformance-golden-coverage.md). Feature-plugin reasoning preservation (issue #157) is documented in [reasoning-output-preservation.md](reasoning-output-preservation.md) with Phase 5 `TestPhase5_*` evidence and [conformance-golden-coverage.md](conformance-golden-coverage.md#reasoning-output-preservation-issue-157); it is not a core `SB-*` orchestration registry row because matching/storage remain feature-owned.

## Maintenance

When you add a **new, non-obvious** core invariant test (especially under `internal/core/runtime`, `internal/core/routing`, `internal/core/b2bua`, `internal/core/hooks`, or `internal/core/identity`), add or extend the matching **SB-\*** row in the scenario doc listed above so `go test -tags=precommit` keeps docs and tests aligned. Risk-based triage for where to invest default-suite tests lives in [testing-coverage-priorities.md](testing-coverage-priorities.md); do **not** add index rows for thin SDK-only smoke tests unless they encode a named core invariant you want precommit to guard.
