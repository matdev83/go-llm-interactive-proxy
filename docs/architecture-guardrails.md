# Architecture guardrails

This document complements [ADR 0001](adr/0001-registry-driven-composition.md) and [ADR 0005](adr/0005-architecture-guardrails-and-complexity-budgets.md). It explains why we enforce structural rules and where to update the numbers.

Stage four (extension platform) adds the **legal extension pipeline**, brownfield hook-bus migration rules, privileged inventory surfaces, and reload-oriented snapshot assumptions — see [ADR 0006](adr/0006-stage-four-extension-seam-map-and-migration.md).

**Authoring** — stage choice, facades, privileged inventory fields, hook→bundle migration, and the feature→seam mapping for new work: [extension-platform-authoring.md](extension-platform-authoring.md).

## Goals

- Keep `internal/core` free of concrete plugin implementations.
- Avoid hidden composition (`init()`-driven registration in the standard bundle path).
- Keep **composition roots** owning a concrete `*pluginreg.Registry`: create it (`NewRegistry`), install the standard bundle on that instance (`InstallStandardBundleOn`), validate, then pass it into `runtimebundle.Build` / `stdhttp` / mounting APIs. Wiring layers must not grow alternate global registries, lazy `sync.Once` singletons for registration, or implicit dependence on `pluginreg.Default`.
- Cap growth of the orchestration layers so the codebase does not drift into an oversized “god core”.

## Automated checks

| Check | Location |
| --- | --- |
| Non-test line budgets for key trees | [`internal/archtest/guardrails_test.go`](../internal/archtest/guardrails_test.go) |
| No `func init()` in `internal/pluginreg` and `cmd/lipstd` (non-test `.go` files) | same |
| `internal/infra/runtimebundle` production code must not reference `pluginreg.Default` (AST selector) | same |
| `internal/infra/runtimebundle` and `internal/stdhttp` production code must not call `InstallStandardBundleOn` / `RegisterStandardBundle` | same |
| `runtimebundle`, `stdhttp`, `cmd/lipstd` production code must not declare package-level `*pluginreg.Registry` / `pluginreg.NewRegistry()` vars or package-level `sync.Once` | same |
| `cmd/lipstd` production code must not reference `sync.Once` and call `InstallStandardBundleOn` / `RegisterStandardBundle` in the same file | same |
| Tests must not pair `func init()` with `RegisterStandardBundle()` | same |
| Core does not import bundled plugins | [`internal/core/runtime/boundaries_test.go`](../internal/core/runtime/boundaries_test.go) (`TestCorePackagesDoNotDependOnConcretePluginPackages`) |
| Extension platform import boundaries (no vendor SDK in `pkg/lipsdk`, no `stdhttp` in core, no concrete frontends/backends in core) | [`internal/archtest/extension_platform_boundaries_test.go`](../internal/archtest/extension_platform_boundaries_test.go) |
| `internal/core` does not import `pkg/lipsdk/transport/...` (principal context from `pkg/lipsdk/execview`); hexagonal task 4.1 | same (`TestInternalCoreDoesNotDependOnStdhttpOrProtocolPlugins`) |
| `internal/core/runtime` has no direct `net/http` import (decode/encode stay at driving adapters; task 4.2) | same (`TestInternalCoreRuntimeDoesNotImportNetHTTP`) |
| Public contract surfaces (`pkg/lipapi`, `pkg/lipsdk`) must not depend on `internal/...`, composition roots (`pluginreg`, `runtimebundle`), `stdhttp`, or official provider SDKs (hexagonal task 2.1) | same (`TestPkgLipapiPublicContractDoesNotImportInternalOrWiring`, `TestPkgLipsdkDoesNotDependOnVendorSDKs`) |
| Hexagonal migration baseline (direct `internal/core/*` imports, classifications, `retired_exceptions`, and required `retirement_trigger` for `exception` packages) and core closure must not import composition helpers; current `pluginreg` exception no longer includes continuity store opening or diag inventory assertion edges | [`testdata/architecture/hexagonal_migration_baseline.json`](../testdata/architecture/hexagonal_migration_baseline.json) (`schema_version` 2), [`internal/archtest/hexagonal_migration_baseline_test.go`](../internal/archtest/hexagonal_migration_baseline_test.go), [`internal/archtest/hexagonal_boundaries_test.go`](../internal/archtest/hexagonal_boundaries_test.go) |
| Extension runtime grouped facade and narrow seams (`RequestRuntimeSnapshot`, `CompletionGatesFromContext`, `TrafficPortBundle`; hexagonal task 5.1) | [`internal/core/extensions/doc.go`](../internal/core/extensions/doc.go), [`internal/core/extensions/facade_contract_test.go`](../internal/core/extensions/facade_contract_test.go) |
| Official feature plugins (`./internal/plugins/features/...`) must not depend on `internal/core` (SDK-only feature code; hexagonal task 5.3) | [`internal/archtest/extension_platform_boundaries_test.go`](../internal/archtest/extension_platform_boundaries_test.go) (`TestOfficialFeaturePluginsDoNotDependOnInternalCore`) |
| Diagnostics query seam for attempt reads (`diag.AttemptLoader` + `lipapi.AttemptRecord`; hexagonal task 5.4) | [`internal/core/diag/doc.go`](../internal/core/diag/doc.go), [`internal/core/diag/attempts.go`](../internal/core/diag/attempts.go), [`internal/core/diag/attempts_test.go`](../internal/core/diag/attempts_test.go) (`TestAttemptsHandler_fakeAttemptLoaderJSON`) |

Circuit breaker behavior (what counts as failure, recovery) is documented in [`routing-health-circuit-breaker.md`](routing-health-circuit-breaker.md).

Run `go test ./internal/archtest/...` and full `go test ./...` (also invoked from `make quality-checks` / CI).

**Scope caveats:** AST checks match import-local names (`pluginreg.Default` / `sync.Once`, not renamed imports). `pluginreg.DefaultWireModel` and other `pluginreg.Default*` identifiers are allowed. Package-level `sync.Once` is forbidden in the three wiring roots even when unrelated to plugins, to keep lazy singleton registration from creeping back in. In-function `sync.Once` elsewhere (for example `stdhttp` shutdown coordination) is allowed; `cmd/lipstd` additionally forbids combining `sync.Once` with standard-bundle install calls in one file.

## Updating budgets

When a deliberate feature requires a larger core or composition layer, raise the limits in `guardrails_test.go` and record the rationale in ADR 0005 or a short note in the PR.
