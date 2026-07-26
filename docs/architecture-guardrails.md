# Architecture guardrails

This document complements [ADR 0001](adr/0001-registry-driven-composition.md) and [ADR 0005](adr/0005-architecture-guardrails-and-complexity-budgets.md). It explains why we enforce structural rules and where to update the numbers.

Stage four (extension platform) adds the **legal extension pipeline**, brownfield hook-bus migration rules, privileged inventory surfaces, and reload-oriented snapshot assumptions — see [ADR 0006](adr/0006-stage-four-extension-seam-map-and-migration.md). Versioned runtime config reload (immutable generations, explicit triggers, process/generation ownership split) is owned by [ADR 0008](adr/0008-versioned-runtime-config-reload.md) and the operator guide [runtime-config-reload.md](runtime-config-reload.md).

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
| Per-file critical-file budgets for single-file gravity wells (`executor.go`, `runtimebundle/build.go`, `runtimebundle/options.go`, `stdhttp/server.go`, `standardplugins/standard_table.go`, `pluginreg/reg.go`, plus migration freezes for `runtimehost/coordinator.go`, `runtimehost/generation.go`, `runtimebundle/candidate_compile.go`, `runtimebundle/process_services.go`, `pkg/lipruntime/build.go`, and Task 2.3 thin reload facade files `pkg/lipruntime/reload.go` / `reload_aliases.go`) | same (`TestCriticalFileLineBudgets`, `TestCriticalFileMigrationHotspotFreezeBudgets`) |
| No `func init()` in `internal/pluginreg`, `internal/standardplugins`, and `cmd/lipstd` (non-test `.go` files) | same |
| `internal/infra/runtimebundle` production code must not reference `pluginreg.Default` (AST selector) | same |
| `internal/infra/runtimebundle` (except `bootstrap_plan.go` composition-root startup) and `internal/stdhttp` production code must not call `InstallStandardBundleOn` / `RegisterStandardBundle` | same |
| `runtimebundle`, `stdhttp`, `cmd/lipstd` production code must not declare package-level `*pluginreg.Registry` / `pluginreg.NewRegistry()` vars or package-level `sync.Once` | same |
| `cmd/lipstd` production code must not reference `sync.Once` and call `InstallStandardBundleOn` / `RegisterStandardBundle` in the same file | same |
| Tests must not pair `func init()` with `RegisterStandardBundle()` | same |
| Core does not import bundled plugins | [`internal/core/runtime/boundaries_test.go`](../internal/core/runtime/boundaries_test.go) (`TestCorePackagesDoNotDependOnConcretePluginPackages`) |
| Extension platform import boundaries (no vendor SDK in `pkg/lipsdk`, no `stdhttp` in core, no concrete frontends/backends in core; feature plugins may import only `internal/core/toolcallrepair` for ADR 0007) | [`internal/archtest/extension_platform_boundaries_test.go`](../internal/archtest/extension_platform_boundaries_test.go) |
| `internal/core` does not import `pkg/lipsdk/transport/...` (principal context from `pkg/lipsdk/execview`); hexagonal task 4.1 | same (`TestInternalCoreDoesNotDependOnStdhttpOrProtocolPlugins`) |
| `internal/core/runtime` has no direct `net/http` import (decode/encode stay at driving adapters; task 4.2) | same (`TestInternalCoreRuntimeDoesNotImportNetHTTP`) |
| Public contract surfaces (`pkg/lipapi`, `pkg/lipsdk`) must not depend on `internal/...`, composition roots (`pluginreg`, `runtimebundle`), `stdhttp`, or official provider SDKs (hexagonal task 2.1) | same (`TestPkgLipapiPublicContractDoesNotImportInternalOrWiring`, `TestPkgLipsdkDoesNotDependOnVendorSDKs`) |
| Hexagonal migration baseline (direct `internal/core/*` imports, classifications, `role` metadata, `retired_exceptions`, and required `retirement_trigger` for `exception` packages) and core closure must not import composition helpers; current `pluginreg` exception no longer includes continuity store opening or diag inventory assertion edges; `runtimebundle` is labeled `composition_root` role with intentionally wide import fan-out | [`testdata/architecture/hexagonal_migration_baseline.json`](../testdata/architecture/hexagonal_migration_baseline.json) (`schema_version` 3), [`internal/archtest/hexagonal_migration_baseline_test.go`](../internal/archtest/hexagonal_migration_baseline_test.go), [`internal/archtest/hexagonal_boundaries_test.go`](../internal/archtest/hexagonal_boundaries_test.go) |
| Extension runtime grouped facade and narrow seams (`RequestRuntimeSnapshot`, `CompletionGatesFromContext`, `TrafficPortBundle`; hexagonal task 5.1) | [`internal/core/extensions/doc.go`](../internal/core/extensions/doc.go), [`internal/core/extensions/facade_contract_test.go`](../internal/core/extensions/facade_contract_test.go) |
| Official feature plugins (`./internal/plugins/features/...`) must not depend on `internal/core` (SDK-only feature code; hexagonal task 5.3) | [`internal/archtest/extension_platform_boundaries_test.go`](../internal/archtest/extension_platform_boundaries_test.go) (`TestOfficialFeaturePluginsDoNotDependOnInternalCore`) |
| Diagnostics query seam for attempt reads (`diag.AttemptLoader` + `lipapi.AttemptRecord`; hexagonal task 5.4) | [`internal/core/diag/doc.go`](../internal/core/diag/doc.go), [`internal/core/diag/attempts.go`](../internal/core/diag/attempts.go), [`internal/core/diag/attempts_test.go`](../internal/core/diag/attempts_test.go) (`TestAttemptsHandler_fakeAttemptLoaderJSON`) |
| Vendor SDK import closure for full `internal/core/...` (not only `runtime`) | [`internal/archtest/openaicompat_boundaries_test.go`](../internal/archtest/openaicompat_boundaries_test.go) (`TestInternalCoreDoesNotDependOnVendorSDKs`) |
| Shared OpenAI-compatible backend adapter (`openaicompat`) must not import concrete providers; `openrouter` / `nvidia` compose it | same (`TestOpenaiCompatSharedAdapterDoesNotImportConcreteProviders`, `TestConcreteOpenAICompatProvidersImportSharedAdapter`) |

Circuit breaker behavior (what counts as failure, recovery) is documented in [`routing-health-circuit-breaker.md`](routing-health-circuit-breaker.md).

Run `go test ./internal/archtest/...` and full `go test ./...` (also invoked from `make quality-checks` / CI).

**Architecture metrics report:** `make arch-report` prints a deterministic Markdown snapshot of non-test lines per package, hotspot file sizes, runtime-convergence package budgets, Requirement 11.5 five-surface net shrinkage (baseline `efe4624909cea318c7211d5cb3734059d3210802`), remaining compatibility exceptions, affected-surface fan-in/out, direct internal import fan-out/fan-in, exported symbol counts for `pkg/lipapi` / `pkg/lipsdk` / `pkg/lipruntime`, hexagonal baseline classifications, and deleted production symbol inventory. The Req 11.5 shrinkage section is enforced: the command exits non-zero while the five-surface delta is worse than `-800`. Other tables remain advisory. CI publishes the report as an artifact from [`.github/workflows/qa.yml`](../.github/workflows/qa.yml). Machine-checked helpers live in [`internal/archtest/shrinkage.go`](../internal/archtest/shrinkage.go) (`TestShrinkage_NetReductionMeetsRequirement115`).

**Scope caveats:** AST checks match import-local names (`pluginreg.Default` / `sync.Once`, not renamed imports). `standardplugins.DefaultWireModel` and other `Default*` identifiers are allowed. Package-level `sync.Once` is forbidden in the three wiring roots even when unrelated to plugins, to keep lazy singleton registration from creeping back in. In-function `sync.Once` elsewhere (for example `stdhttp` shutdown coordination) is allowed; `cmd/lipstd` additionally forbids combining `sync.Once` with standard-bundle install calls in one file. The guardrail `TestCompositionLayersDoNotRegisterStandardBundle` exempts `internal/infra/runtimebundle/bootstrap_plan.go` as the single composition-root startup path allowed to call `InstallStandardBundleOn`; all other `runtimebundle` and `stdhttp` production files remain forbidden.

## Updating budgets

When a deliberate feature requires a larger core or composition layer, raise the limits in `guardrails_test.go` and record the rationale in ADR 0005 or a short note in the PR.

The current `internal/core` ceiling includes prior usage-authority / model-registry / tool-call-repair / secrets-guard / jsonshape growth plus dual-plane-economics production-readiness Phase 1 and its follow-up (customer evidence accumulator, final-backend clamp preview, correlation/presence ingress facts, control-plane metering projection, operator usage retention on incurred loser/swallowed paths, and `metering/plane` pure dual-plane helpers) plus dual-plane Phase 2.3 authority-coordinator posture/compensation and settlement concurrency state plus reasoning-output-preservation (`RunCandidateAttemptTransformStage`, `RunFinalStreamObservationStage`, race-safe observation lifecycle, recv/gate/central-emit wiring, and content-safe stage telemetry / generic-port inventory posture) plus early Recv parent-cancel remediation (nil-inner `OutcomeCancelled` + swallowed release) plus dual-plane Phase 3 durable metering journal (ingress checkpoint producers, control-plane metering usage bridge/projection, restart reconstruction) plus dual-plane Phase 4.1–4.5 terminal ownership and terminal-work (stream terminal session, owner CAS, WorkRecord/idempotent intent, claim-lease transitions, processor app with owned tick/renew, durable settle/release recovery, query/metrics/readiness) plus dual-plane Phase 5 executable generations (contribution compile/publish/bind/lifetime, generation-owned coordinators/limiter/rater binding, and control-plane executable readiness) plus dual-plane Phase 6 atomic lease-set concurrency (AcquireSet/RenewSet/ReleaseSet, heartbeat fail-closed, durable ReleaseLeaseSet, QuerySets readiness). Accidental duplication was removed before raising the budget. Post-merge measured non-test total is 64452; the cap is 64550 (~98 lines headroom). Prefer decomposing further core growth rather than absorbing it by another budget increase.

Merged onto the above, the experimental cursor-sdk backend follow-up is also included: `metering/plane` pure helpers wired from runtime egress, operator usage evidence helpers wired into operator settle/egress with `lastAuthorityUsage` → `seenEvents` → empty `UsageDelta` shell precedence (req 1.5 / 2.9), `AttemptClampPreviewer` `PreviewAttempt` + `SkipEvidence` with a nil-`AttemptCoordinator` fallback to the direct `UsageAuthority` adapter and a bounded runtime clamp-preview loop before freeze (V-15), and `execbackend.Backend.Close`. The cap was raised from 64550 to 64800 for this follow-up; post-merge measured non-test total is 64713 (~87 lines of headroom).

The live `internal/core` cap in `guardrails_test.go` is further raised for versioned-runtime-reload (through task 5.1 configreload trigger/result vocabulary); see the `lineBudgets` comments for the current measured total and headroom.

The `internal/infra/runtimebundle` ceiling covers dual-plane Phase 2.2–4.5 composition (descriptor-bound registrations, terminal-work ownership/readiness, RequestRegistrations→AuthorityRequestEffectProvider merge, ProcessDue metrics observer) plus reasoning-output-preservation wiring on the Build path plus Phase 5 executable generation compile/publish/readiness and Phase 5 remediation (provider-removal validation, terminal pending-drain binding) plus Phase 6 lease-set QuerySets readiness, startup uncertain-set reconcile, and settle-release pending counts plus versioned-runtime-reload task 2.3 ProcessServices / candidate compilation split plus task 2.4 process-capacity and shared mutable continuity hoist (A-leg lifecycle, affinity/health compatibility views, extension state, accounting/metering stores) plus task 3.3 complete generation compilation / GenerationBundle plus task 3.4 initial-generation bootstrap host (HandlerComposer serve mode publishing generation 1 without Built) plus task 5.1 `BackendFactoryKindCounts` for LiveFactoryKinds admission. Prefer keeping Build orchestration thin; new registration validation belongs on the public facade when practical.

This applies to both the tree-level `lineBudgets` and the per-file `criticalFileBudgets`. Any increase to a critical-file budget must include a short rationale comment next to the table entry explaining why the single-file hotspot is growing rather than being decomposed. Prefer decomposing the file over raising its budget.

### Decreasing migration hotspot freezes

Feature `runtime-architecture-convergence-and-shrinkage` adds five **exact-freeze** critical-file budgets (Task 1.2) for the measured reload/runtime gravity wells. Initial ceilings equal the Task 1.1 physical line counts at reviewed baseline SHA `efe4624909cea318c7211d5cb3734059d3210802` with **no growth headroom**. Requirement 11.3 final targets (unless the named file is removed) are:

| Surface | Final ceiling | Intermediate ratchet |
| --- | ---: | --- |
| Reload coordinator orchestration | ≤300 | Phase 6 / task 6.5 |
| Generation state object | ≤400 | Phase 7 / task 7.3 |
| Generation candidate compilation | ≤350 | Phase 3 / task 3.5 |
| Process runtime construction | ≤300 | Phase 5 / task 5.5 |
| Public runtime build/facade assembly | ≤150 | Phase 8 / task 8.1 |

Authoritative path/`Max` pairs live only in [`internal/archtest/critical_files.go`](../internal/archtest/critical_files.go) (`CriticalFileBudgets`); do not copy unstable measured line counts into this document when ratcheting.

## Core admission checklist

Before adding a new `internal/core/*` package or moving code into an existing one, consult [`docs/core-boundaries.md`](core-boundaries.md) and answer the 6-question admission checklist. Include a short justification in the PR when adding a new core package. The `TestCorePackagesHaveDocGo` archtest requires every top-level `internal/core/*` package to have a `doc.go` explaining its boundary.

## Architecture PR checklist

When reviewing broad or architecture-adjacent changes, use this checklist:

- [ ] Does this change keep provider/protocol details out of core?
- [ ] Does this change avoid new global state, `init()` registration, and lazy singleton registries?
- [ ] Does this change keep canonical streaming as the primary execution path?
- [ ] Does this change add a new core package? If yes, why does it belong in core? (See [`docs/core-boundaries.md`](core-boundaries.md).)
- [ ] Does this change widen public contracts (`pkg/lipapi` / `pkg/lipsdk`)? If yes, is it versionable and minimal?
- [ ] Does this change increase architecture budgets? If yes, is the reason documented?
- [ ] Does this change add a new control-plane evidence guard? If yes, is it regression-locked at all four layers (SDK → core → normalizer → recorder)? See [`controlplane-evidence.md`](controlplane-evidence.md) for the per-guard coverage map and Go templates.

## Enterprise extension boundaries

Enterprise features attach through stable seams (`pkg/lipsdk` facades, `BuildOptions`, control-plane ports, hook bus, secure-session authority) and must not edit `runtime.Executor` or import deep runtime internals. See [`docs/enterprise-extension-boundaries.md`](enterprise-extension-boundaries.md) for the allowed and forbidden integration points.
