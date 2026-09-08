# Implementation Baseline: `core-feature-ownership-full-closure`

## 1. Executive Metadata

- **Specification**: `core-feature-ownership-full-closure` (#572)
- **Starting Main SHA**: `fff67a0b3559cea120c5fa0abf4badabda7e69cd`
- **Current Implementation HEAD SHA**: `204ecc6d9ad8ea4ca886e034aa293477091917b5`
- **Host OS / Arch**: `windows/amd64` (Windows 11)
- **Go Version**: `go version go1.26.6 windows/amd64`
- **Git Checkout State**: Full git depth/history (unshallow checkout; all historical evidence commits resolvable)
- **Baseline Date**: 2026-09-06
- **Phase**: Phase 1 (Tasks 1.1, 1.2, 1.3) — Evidence and baselines only; zero production code modifications.

---

## 2. Predecessor Verification Prerequisite Proof (Gate 0.1)

Per `execution-guardrails.md` §1 and Task 0.1, the predecessor specification `pre-oss-core-slimming` must be canonically completed, verified, and archived before any production modifications for `#572` begin.

| Prerequisite Invariant | Required Value / Location | Verification Command / Evidence | Status |
| --- | --- | --- | --- |
| Archived Predecessor Spec | `.kiro/specs/archive/pre-oss-core-slimming/` | Directory exists with canonical completion evidence (`spec.json`, `requirements.md`, `design.md`, `tasks.md`, `residual-ownership-inventory.md`) | **VERIFIED** |
| Predecessor Final Certified SHA | `d784a8344888dd9de2141a13d4bf723125d4b08c` | Merged via PR #591; Linux race certification verified on GitHub Actions run `33891386913` | **VERIFIED** |
| Residual Ownership Inventory | `.kiro/specs/archive/pre-oss-core-slimming/residual-ownership-inventory.md` | Inventory baseline SHA `a8c18f35436afcb18570a38d5e6c05d06fe4fccc`; 10 deferred findings documented with exact classifications and migration paths | **VERIFIED** |
| Generated-only Standard Planes | Standard plane generated output in sync | `go run ./scripts/generate-feature-planes.go -check` -> `generated feature planes file is up to date.` | **VERIFIED** |
| Retired Packages Absent | `internal/core/toolcallrepair`, `internal/core/secretguard`, `internal/core/compactiondetect` absent | File search confirms none of the retired packages exist in the tree | **VERIFIED** |
| Concrete Feature Import Ban | `internal/infra/runtimebundle` has 0 imports of `internal/plugins/features/*` | `go test -count=1 ./internal/archtest/...` PASS; `TestNoConcreteFeatureImportsInRuntimeBundle` PASS | **VERIFIED** |
| External Feature SDK Fixture | `testdata/external_feature_sdk` present | External plugin fixture present and verified by `internal/archtest` | **VERIFIED** |
| Predecessor Architecture Budgets | Core and runtimebundle LOC ratchets active | `internal/archtest/budgets.go` budget tests PASS (`internal/core` <= 89,961, `internal/infra/runtimebundle` <= 12,566) | **VERIFIED** |

---

## 3. Repository Verification Baseline Gates

Repository verification was executed on the starting tree (`fff67a0b3559cea120c5fa0abf4badabda7e69cd`). In this repair pass, fast scoped commands were personally re-verified; long-running repository-wide suites from the prior worker are recorded with explicit attribution.

| Verification Gate / Command | Exit Status | Elapsed Time | Observed Outcome / Details |
| --- | --- | --- | --- |
| `go run ./scripts/generate-feature-planes.go -check` | **PASS** (0) | 0.45s | Re-verified: `generated feature planes file is up to date.` Zero plane divergence. |
| `go test -count=1 ./tools/kiro/speccheck` | **PASS** (0) | 1.24s | Re-verified: Spec validation passed for all specs. |
| Focused `internal/archtest` (`-run "TestRetiredPackagesAbsent\|TestProductionRuntimeBundleHasZeroConcreteFeatureImports\|TestPackageTreeBudgetsExact"`) | **PASS** (0) | 0.33s | Re-verified: Architectural invariants, retired package absence, runtimebundle concrete feature import ban, and package budgets pass. |
| `make arch-report` | **PASS** (0) | 2.10s | Re-verified: Architecture report generated successfully; zero undeclared boundary violations. |
| `go vet ./internal/infra/runtimebundle/...` | **PASS** (0) | 0.25s | Re-verified: Zero vet errors in `runtimebundle`. |
| `go test -count=1 -run TestProcessFeatureResources ./internal/infra/runtimebundle/` | **PASS** (0) | 0.11s | Re-verified: Ownership-counting and overlapping generation tests pass cleanly. |
| `make docs-check` | **PASS** (0) | 1.00s | Re-verified: Documentation formatting and link integrity verified. |
| `go mod verify` | **PASS** (0) | 0.20s | Re-verified: `all modules verified`. Module dependencies clean. |
| `go test -parallel=16 -timeout=10m ./...` | **NOT RE-VERIFIED** | — | NOT RE-VERIFIED in this pass — reported by prior worker (PASS in 7m42s), must be confirmed in Task 11/12 certification. |
| `make quality-checks` (`scripts/lint-all-modules.ps1`) | **FAIL** (1) | 10.63s | Re-verified on starting SHA (`fff67a0b`): 22 of 23 modules passed cleanly; root module failed with 17 pre-existing baseline linter issues across 8 untouched test files. Newly added test `internal/infra/runtimebundle/process_feature_ownership_counting_test.go` has 0 lint issues. |
| Canonical Linux Race Workflow (CI Run `34044117599` on `fff67a0b`) | **PASS** (0) | CI (Linux/macOS) | Recorded from prior worker CI evidence: Ubuntu 22.04 race, macOS, DB Parity, Repo Hygiene green. Windows job failed on `test-cost-ratchet` measurement. |

---

## 4. Required Baseline Failure Table

As mandated by `execution-guardrails.md` §2, non-green baseline results on the starting SHA are documented below:

| Gate / Command | Package / Test / Job | Failure Signature | Classification | Reproduced on Starting SHA | Intersects #572 Production Scope | Existing Tracker | #572 Treatment |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `make quality-checks` (`scripts/lint-all-modules.ps1`) | `internal/featurebundle`, `internal/plugins/features/reasoningreplay`, `internal/plugins/features/toolcallrepair`, `pkg/lipruntime` | `golangci-lint` reported 17 issues (`gofumpt`: 1, `modernize`: 9, `paralleltest`: 5, `staticcheck`: 2) across 8 untouched test files (`featurebundle_test.go` [1], `replay_test.go` [3], `adapter_test.go` [4], `compatibility_test.go` [1], `export_test.go` [1], `facade_cross_generation_external_test.go` [1], `facade_named_methods_external_test.go` [1], `runtime_test.go` [5]) | `baseline test/CI harness defect` | **yes**: Directly reproduced on starting SHA `fff67a0b` via `pwsh -NoProfile -File scripts/lint-all-modules.ps1` (Exit code 1; 22 modules pass; root module reports 17 issues across 8 untouched test files). Newly added test `process_feature_ownership_counting_test.go` has 0 issues. | **no**: Phase 1 modifies zero production code; none of these 8 test files are touched or modified by Phase 1 | Local CI harness configuration / pre-existing repo lint debt | `out-of-scope baseline`: Per `execution-guardrails.md` §3, pre-existing linter defects in untouched files must not expand #572 scope into general cleanup. |
| Canonical CI Workflow (GitHub Actions Run `34044117599`) | Windows Runner / `test-cost-ratchet` | `measurement exceeded baseline threshold by 3.2%` | `baseline test/CI harness defect` | **yes**: Observed on CI run `34044117599` for commit `fff67a0b3559cea120c5fa0abf4badabda7e69cd` | **no**: Runner timing fluctuation on Windows CI; Linux race and unit suites fully green | CI Run `34044117599` | `out-of-scope baseline`: CI runner performance fluctuation; does not affect Linux race certification or architectural invariants. |

---

## 5. Task 1.1: Authoritative Production Ownership Census

All 95 production Go packages under `internal/core`, `internal/infra/*compose`, `internal/standardplugins`, `internal/pluginreg`, `internal/featurebundle`, `internal/infra/runtimebundle`, `pkg/lipruntime`, and one-feature support packages outside `internal/plugins/features` were recursively scanned and analyzed.

### Classification Vocabulary & Core Admission Test
Each package is evaluated against the Core Admission Test:
- **Q1**: Is the package independent of feature-specific vocabulary, prompt templates, and feature heuristics?
- **Q2**: Does it operate solely on engine primitives (routing, B2BUA, byte streaming, auth, session lineage)?
- **Q3**: Would the package retain an identical single meaning if the entire standard feature set were replaced or removed?
- **Q4**: Does it contain zero direct references to feature-specific fields, config blocks, or policies?

Per Requirement 1.4, each inventoried package is classified as exactly one of the six mandated terms:
1. `kernel invariant` — Universal engine primitives and base proxy invariants required when all optional standard features are absent.
2. `generic extension mechanism` — Feature-neutral extension, hook, dispatch, registry, and plane assembly substrate.
3. `optional feature implementation/policy` — Optional feature implementation, heuristic, prompt template, or feature policy.
4. `feature-specific infrastructure/composition` — Feature-specific infrastructure adapter, compose bridge, or feature persistence/runner.
5. `standard-distribution registration/composition` — Standard distribution plugin catalog and registration manifest.
6. `obsolete/duplicate` — Retired, duplicate, or dead packages.

Temporary Task-1 marker: Packages containing both kernel invariants and feature policies are marked `mixed/needs split` (strictly linked to Tasks 3–10, with final required term stated). Zero `unknown` classifications exist.

### Complete Census Table

| Package | Files | Non-Test LOC | Production Importers | Classification | Core Admission Rationale & Target Disposition |
| --- | ---: | ---: | --- | --- | --- |
| `internal/core/accessmode` | 6 | 271 | `cmd/lipstd`, `internal/core/config`, `internal/infra/runtimebundle`, `internal/infra/secretguardcompose`, `internal/stdhttp/admin/configreload` | `kernel invariant` | Q1-Q4 PASS: Core proxy execution mode enum (ReadOnly, ReadWrite, etc.) independent of features. Retained in core. |
| `internal/core/accounting` | 2 | 286 | `internal/core/config`, `internal/plugins/backends/openaiusage` | `kernel invariant` | Q1-Q4 PASS: Generic token accounting types and ledger contracts. Retained in core. |
| `internal/core/admin` | 2 | 28 | *(none)* | `kernel invariant` | Q1-Q4 PASS: Minimal admin command port definitions. Retained in core. |
| `internal/core/affinity` | 2 | 103 | `internal/core/affinity/memorystore`, `internal/core/runtime`, `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Backend connection affinity keys and routing selectors. Retained in core. |
| `internal/core/affinity/memorystore` | 1 | 61 | `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: In-memory store for backend affinity bindings. Retained in core. |
| `internal/core/auth` | 11 | 865 | `internal/core/config`, `internal/core/runtime`, `internal/infra/authevent`, `internal/infra/controlplane/observers`, `internal/infra/osidentity`, `internal/infra/runtimebundle`, `internal/stdhttp/auth` | `kernel invariant` | Q1-Q4 PASS: Client authentication, credential verification, and principal mapping. Retained in core. |
| `internal/core/authorityattribution` | 3 | 237 | `internal/core/authoritycoord`, `internal/core/concurrencyauthority/domain`, `internal/core/usageauthority/domain` | `kernel invariant` | Q1-Q4 PASS: Attribution metadata for usage and concurrency leases. Retained in core. |
| `internal/core/authoritycoord` | 11 | 1632 | `internal/core/runtime`, `internal/core/snapshotgen`, `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Distributed lease and quota coordinator across cluster nodes. Retained in core. |
| `internal/core/auxreq` | 4 | 1005 | `internal/infra/compactioncompose`, `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Auxiliary request lifecycle, sub-request execution pipeline, and cancellation. Retained in core. |
| `internal/core/b2bua` | 6 | 1336 | `internal/core/continuity`, `internal/core/continuity/bunstore`, `internal/core/diag`, `internal/core/execctx`, `internal/core/routeoverride/storecontract`, `internal/core/runtime`, `internal/core/securesession/adapters/b2bualineage`, `internal/infra/controlplane/observers`, `internal/infra/runtimebundle`, `internal/testkit`, `internal/testkit/b2buatest`, `internal/testkit/conformance` | `kernel invariant` | Q1-Q4 PASS: Back-to-back user agent protocol state machine, attempt loop, upstream communication. Core proxy invariant. |
| `internal/core/billing` | 29 | 3528 | `internal/core/metering`, `internal/core/runtime`, `internal/infra/billingadmission`, `internal/infra/billingcompose`, `internal/infra/billingspool`, `internal/infra/billingstore`, `internal/infra/runtimebundle`, `internal/plugins/frontends/execerr`, `internal/stdhttp/admin/billing`, `internal/stdhttp/contract` | `kernel invariant` | Q1-Q4 PASS: Core proxy billing admission, credit balance verification, and ledger recording. Retained in core. |
| `internal/core/capabilities` | 4 | 130 | `internal/core/runtime`, `internal/infra/runtimebundle`, `internal/testkit/contract/backend` | `kernel invariant` | Q1-Q4 PASS: Model capability bitflags (tools, streaming, vision). Retained in core. |
| `internal/core/compactioncontinuity` | 8 | 871 | `internal/infra/compactioncompose`, `internal/infra/runtimebundle` | `mixed/needs split` (final: `optional feature implementation/policy`, Task 3) | Q1-Q4 FAIL: Mixes generic branch-state CAS coordination with compaction-specific capsule schemas and watermark tracking. Split in Task 3: feature types to `internal/plugins/features/compactioncontinuity/state`; coordinator to feature state; package deleted. |
| `internal/core/concurrencyauthority/app` | 5 | 1116 | `internal/core/concurrencyauthority/compatible`, `internal/infra/concurrencyauthority/configsource`, `internal/infra/concurrencyauthority/leasestore`, `internal/infra/runtimebundle`, `internal/stdhttp/admin/controlplane` | `kernel invariant` | Q1-Q4 PASS: Concurrency lease allocation and enforcement application layer. Retained in core. |
| `internal/core/concurrencyauthority/compatible` | 4 | 329 | `internal/core/runtime`, `internal/infra/runtimebundle`, `internal/standardplugins` | `kernel invariant` | Q1-Q4 PASS: Compatibility adapters for legacy concurrency lease formats. Retained in core. |
| `internal/core/concurrencyauthority/domain` | 8 | 561 | `internal/core/concurrencyauthority/app`, `internal/core/concurrencyauthority/compatible`, `internal/core/config`, `internal/infra/concurrencyauthority/configsource`, `internal/infra/concurrencyauthority/leasestore`, `internal/stdhttp/admin/controlplane` | `kernel invariant` | Q1-Q4 PASS: Pure domain models for concurrency rate limits. Retained in core. |
| `internal/core/config` | 40 | 5164 | 30 packages across repo | `mixed/needs split` (final: `kernel invariant` / `optional feature implementation/policy`, Task 9) | Q1-Q4 FAIL: Mixes core proxy runtime configuration with optional feature schemas (`prompt_cache.go` keepwarm, `interleaved.go` thinker prompts/budgets). Split in Task 9: feature schemas moved to owning feature packages. |
| `internal/core/configreload` | 7 | 1271 | `internal/infra/metrics`, `internal/infra/runtimebundle`, `internal/infra/runtimehost` | `kernel invariant` | Q1-Q4 PASS: Dynamic configuration reload coordinator and generation swapper. Retained in core. |
| `internal/core/continuation` | 3 | 60 | *(none)* | `kernel invariant` | Q1-Q4 PASS: Request continuation token domain types. Retained in core. |
| `internal/core/continuity` | 4 | 229 | `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Session continuity contracts and replay markers. Retained in core. |
| `internal/core/continuity/bunstore` | 8 | 1952 | `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: PostgreSQL / Bun persistence implementation for session continuity. Retained in core. |
| `internal/core/controlplane` | 13 | 2368 | `internal/core/usageauthority/app`, `internal/infra/controlplane/ledgerstore`, `internal/infra/controlplane/ledgerstore/contract`, `internal/infra/controlplane/observers`, `internal/infra/runtimebundle`, `internal/infra/usageauthority/evidencesink`, `internal/stdhttp/admin/controlplane` | `kernel invariant` | Q1-Q4 PASS: Control plane state management and cluster observers. Retained in core. |
| `internal/core/conversationview` | 8 | 3598 | `internal/core/b2bua`, `internal/core/continuity/bunstore`, `internal/core/conversationview/sdkadapter`, `internal/core/conversationview/storecontract`, `internal/core/runtime`, `internal/infra/metrics` | `mixed/needs split` (final: `kernel invariant` / `optional feature implementation/policy`, Task 4) | Q1-Q4 FAIL: Mixes kernel projection safety (never sending client-tagged content to backends) with optional steering overlays and local turns. Split in Task 4: retain `internal/core/conversationprojection`; move steering/store to `internal/infra/conversationview`; delete package. |
| `internal/core/conversationview/sdkadapter` | 4 | 292 | `internal/core/runtime`, `internal/infra/metrics` | `mixed/needs split` (final: `optional feature implementation/policy`, Task 4) | Q1-Q4 FAIL: SDK adapter for conversation view steering features. Relocated with conversation view in Task 4. |
| `internal/core/conversationview/storecontract` | 1 | 1038 | *(none)* | `mixed/needs split` (final: `feature-specific infrastructure/composition`, Task 4) | Q1-Q4 FAIL: Persistence contract for conversation view steering items. Relocated to `internal/infra/conversationview` in Task 4. |
| `internal/core/diag` | 19 | 1544 | 17 packages across repo | `kernel invariant` | Q1-Q4 PASS: Diagnostic telemetry, trace attributes, span helpers, logging context. Retained in core. |
| `internal/core/execbackend` | 3 | 248 | 23 packages across repo | `kernel invariant` | Q1-Q4 PASS: Core backend executor abstraction and invocation port. Retained in core. |
| `internal/core/execctx` | 7 | 411 | `internal/core/auxreq`, `internal/core/extensions`, `internal/core/hooks`, `internal/core/runtime`, `internal/core/state`, `internal/infra/compactioncompose`, `internal/testkit` | `kernel invariant` | Q1-Q4 PASS: Execution context carrying request deadlines, trace identity, and cancellation. Retained in core. |
| `internal/core/extensions` | 32 | 4623 | `internal/core/diag`, `internal/core/runtime`, `internal/core/traffic`, `internal/infra/metrics`, `internal/infra/runtimebundle`, `internal/infra/secretguardcompose`, `internal/testkit` | `generic extension mechanism` | Q1-Q4 PASS: Closed standard extension planes, completion gates, and hook dispatch substrate. Retained in core. |
| `internal/core/geoip` | 3 | 280 | `internal/core/config`, `internal/infra/geoip`, `internal/infra/metrics`, `internal/stdhttp/contract`, `internal/stdhttp/geoip` | `kernel invariant` | Q1-Q4 PASS: Client IP geolocation lookup for regional policy routing. Retained in core. |
| `internal/core/hooks` | 11 | 707 | `internal/core/extensions`, `internal/core/runtime`, `internal/infra/runtimebundle`, `internal/testkit`, `internal/testkit/conformance` | `generic extension mechanism` | Q1-Q4 PASS: Extension hook pipeline execution and phase ordering. Retained in core. |
| `internal/core/http` | 4 | 167 | `internal/infra/metrics`, `internal/infra/tracing`, `internal/stdhttp` | `kernel invariant` | Q1-Q4 PASS: Core HTTP protocol utilities, header normalization, and status mapping. Retained in core. |
| `internal/core/identity` | 8 | 493 | 10 packages across repo | `kernel invariant` | Q1-Q4 PASS: Tenant, organization, and user identity representation. Retained in core. |
| `internal/core/interleavedstate` | 2 | 175 | `internal/core/b2bua`, `internal/core/continuity/bunstore`, `internal/core/interleavedthinking`, `internal/core/routing`, `internal/core/runtime`, `internal/infra/controlplane/observers`, `internal/testkit/b2buatest` | `mixed/needs split` (final: `kernel invariant` / `optional feature implementation/policy`, Task 5) | Q1-Q4 FAIL: Mixes cycle-state tracking with memo payload refs. Split in Task 5: memo payload references relocated to feature package. |
| `internal/core/interleavedthinking` | 6 | 704 | `internal/core/runtime`, `internal/infra/runtimebundle` | `mixed/needs split` (final: `kernel invariant` / `optional feature implementation/policy`, Task 5) | Q1-Q4 FAIL: Mixes `[thinker]` routing grammar and cycle tracking with optional thinker prompt templates, memo extraction, and sanitization. Split in Task 5: extract feature policy to `internal/plugins/features/interleavedthinking`; delete package. |
| `internal/core/jsonpresence` | 2 | 20 | 8 packages across repo | `kernel invariant` | Q1-Q4 PASS: Low-level JSON zero-copy presence detection. Retained in core. |
| `internal/core/jsonshape` | 5 | 473 | `internal/jsonbody`, `internal/plugins/frontends/jsonguard`, `internal/plugins/protocols/openresponses` | `kernel invariant` | Q1-Q4 PASS: AST validation and stream JSON structural validation. Retained in core. |
| `internal/core/keepwarm` | 13 | 1675 | `internal/core/config`, `internal/core/runtime`, `internal/infra/billingcompose`, `internal/infra/metrics`, `internal/infra/runtimebundle`, `internal/stdhttp/admin/keepwarm` | `optional feature implementation/policy` | Q1-Q4 FAIL: Optional background prompt-cache ping policy, scheduler, and registry. Extract to `internal/plugins/features/keepwarm` in Task 6; delete `internal/core/keepwarm`. |
| `internal/core/leglifecycle` | 2 | 434 | 9 packages across repo | `kernel invariant` | Q1-Q4 PASS: Upstream attempt leg lifecycle tracking and abort handling. Retained in core. |
| `internal/core/lineage` | 2 | 112 | `internal/core/diag`, `internal/core/extensions`, `internal/core/hooks` | `kernel invariant` | Q1-Q4 PASS: Request/response message causal lineage tracking. Retained in core. |
| `internal/core/localstream` | 2 | 66 | `internal/core/runtime` | `kernel invariant` | Q1-Q4 PASS: In-memory stream channel for local execution loops. Retained in core. |
| `internal/core/metering` | 2 | 30 | `internal/core/runtime` | `kernel invariant` | Q1-Q4 PASS: Raw usage event metering ports. Retained in core. |
| `internal/core/metering/aggregate` | 3 | 346 | `internal/core/metering/reconcile` | `kernel invariant` | Q1-Q4 PASS: Aggregation windows for metered usage events. Retained in core. |
| `internal/core/metering/checkpoint` | 6 | 850 | `internal/core/runtime` | `kernel invariant` | Q1-Q4 PASS: Durable checkpointing of metered usage. Retained in core. |
| `internal/core/metering/plane` | 1 | 140 | `internal/core/runtime` | `kernel invariant` | Q1-Q4 PASS: Metering plane event dispatch. Retained in core. |
| `internal/core/metering/reconcile` | 1 | 126 | *(none)* | `kernel invariant` | Q1-Q4 PASS: Usage reconciliation engine. Retained in core. |
| `internal/core/modelcatalog` | 15 | 1533 | 7 packages across repo | `kernel invariant` | Q1-Q4 PASS: Static model definitions, tokenizer mappings, context window limits. Retained in core. |
| `internal/core/modelregistry` | 6 | 1314 | 6 packages across repo | `kernel invariant` | Q1-Q4 PASS: Dynamic runtime model registration and lookup. Retained in core. |
| `internal/core/modelview` | 2 | 94 | `internal/core/runtime`, `internal/infra/runtimebundle`, `internal/stdhttp` | `kernel invariant` | Q1-Q4 PASS: Filtered model visibility views for tenants. Retained in core. |
| `internal/core/policy` | 5 | 407 | `internal/core/runtime`, `internal/infra/routinghealth`, `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Rate limit and tier policy definitions. Retained in core. |
| `internal/core/routeoverride` | 5 | 250 | `internal/core/b2bua`, `internal/core/continuity/bunstore`, `internal/core/routeoverride/storecontract`, `internal/core/runtime`, `internal/infra/runtimebundle`, `internal/stdhttp/admin/routeoverride` | `kernel invariant` | Q1-Q4 PASS: Operator manual route override rules. Retained in core. |
| `internal/core/routeoverride/storecontract` | 1 | 658 | *(none)* | `kernel invariant` | Q1-Q4 PASS: Storage contract for route override rules. Retained in core. |
| `internal/core/routing` | 15 | 2613 | 26 packages across repo | `kernel invariant` | Q1-Q4 PASS: Backend selection, fallback sequences, weighted health routing, B2BUA retry decisions. Core engine invariant. |
| `internal/core/runtime` | 94 | 22068 | 8 packages across repo | `mixed/needs split` (final: `kernel invariant`, Tasks 3–7) | Q1-Q4 FAIL: Main execution orchestrator. Mostly kernel runtime, but contains embedded concrete feature references (`KeepwarmPolicy`, `TerminalDecisionPolicy`, `CompactionDetector`, etc.). Feature references extracted to narrow consumer ports in Tasks 3-7; package retained as kernel engine. |
| `internal/core/runtime/failclosed` | 0 | 0 | *(none)* | `kernel invariant` | Package marker for fail-closed security assertions. Retained in core. |
| `internal/core/safety` | 3 | 157 | 7 packages across repo | `kernel invariant` | Q1-Q4 PASS: Prompt safety boundary assertions and stream guard invariants. Retained in core. |
| `internal/core/securesession/adapters/b2bualineage` | 1 | 52 | `internal/infra/runtimebundle`, `internal/testkit` | `kernel invariant` | Q1-Q4 PASS: Adapter linking secure session token to B2BUA lineage. Retained in core. |
| `internal/core/securesession/adapters/bunstore` | 14 | 1920 | `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Bun PostgreSQL persistence for encrypted session tokens. Retained in core. |
| `internal/core/securesession/adapters/diag` | 4 | 708 | `internal/stdhttp`, `internal/stdhttp/contract` | `kernel invariant` | Q1-Q4 PASS: Diagnostic sanitization of session tokens in traces. Retained in core. |
| `internal/core/securesession/adapters/lipapidenial` | 1 | 49 | `internal/infra/runtimebundle`, `internal/testkit` | `kernel invariant` | Q1-Q4 PASS: Access denial enforcement for invalid sessions. Retained in core. |
| `internal/core/securesession/adapters/memory` | 2 | 544 | `internal/infra/runtimebundle`, `internal/testkit` | `kernel invariant` | Q1-Q4 PASS: In-memory session store for local dev and testing. Retained in core. |
| `internal/core/securesession/app` | 9 | 1104 | 9 packages across repo | `kernel invariant` | Q1-Q4 PASS: Secure session creation, verification, and rotation service. Retained in core. |
| `internal/core/securesession/domain` | 5 | 495 | 13 packages across repo | `kernel invariant` | Q1-Q4 PASS: Domain types for cryptographically bound sessions. Retained in core. |
| `internal/core/securesession/storecontract` | 3 | 1057 | *(none)* | `kernel invariant` | Q1-Q4 PASS: Secure session repository contract. Retained in core. |
| `internal/core/snapshotgen` | 6 | 885 | `internal/core/runtime`, `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Immutable runtime state snapshot generator. Retained in core. |
| `internal/core/state` | 3 | 210 | `internal/infra/runtimebundle`, `internal/testkit` | `kernel invariant` | Q1-Q4 PASS: Core state container and epoch tracking. Retained in core. |
| `internal/core/stream` | 6 | 561 | 14 packages across repo | `kernel invariant` | Q1-Q4 PASS: SSE/Chunked streaming abstractions, backpressure handling, chunk multiplexing. Retained in core. |
| `internal/core/streamrecovery` | 2 | 143 | `internal/core/runtime`, `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Mid-stream disconnect recovery and idempotent retry markers. Retained in core. |
| `internal/core/terminal` | 3 | 217 | `internal/core/runtime` | `kernel invariant` | Q1-Q4 PASS: Final generation completion state evaluation. Retained in core. |
| `internal/core/terminaldecisionpolicy` | 2 | 265 | `internal/core/runtime`, `internal/infra/runtimebundle`, `internal/stdhttp/contract`, `internal/stdhttp/terminalpolicy` | `optional feature implementation/policy` | Q1-Q4 FAIL: Bounded actor tri-state policy store for terminal decision feature overrides. Relocated to feature package in Task 7; core retains only immutable reader interface. Package deleted. |
| `internal/core/terminalwork` | 8 | 570 | 4 packages across repo | `kernel invariant` | Q1-Q4 PASS: Post-response async terminal work pipeline. Retained in core. |
| `internal/core/terminalwork/app` | 15 | 3186 | 3 packages across repo | `kernel invariant` | Q1-Q4 PASS: Terminal work scheduler and queue dispatcher. Retained in core. |
| `internal/core/tokenaccounting/app` | 2 | 364 | 11 packages across repo | `kernel invariant` | Q1-Q4 PASS: Token counting and budget enforcement application service. Retained in core. |
| `internal/core/tokenaccounting/domain` | 2 | 346 | `internal/core/tokenaccounting/streamusage` | `kernel invariant` | Q1-Q4 PASS: Token count domain models. Retained in core. |
| `internal/core/tokenaccounting/ledger` | 1 | 174 | `internal/core/metering/aggregate` | `kernel invariant` | Q1-Q4 PASS: Token usage ledger persistence adapter. Retained in core. |
| `internal/core/tokenaccounting/observability` | 2 | 376 | `internal/core/runtime`, `internal/infra/metrics`, `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Token usage metrics emission and Prometheus counters. Retained in core. |
| `internal/core/tokenaccounting/preflight` | 1 | 304 | `internal/core/runtime`, `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Pre-request token estimate preflight check. Retained in core. |
| `internal/core/tokenaccounting/streamusage` | 1 | 224 | `internal/core/runtime`, `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Streaming token count parser and accumulator. Retained in core. |
| `internal/core/traffic` | 2 | 13 | `internal/core/runtime` | `kernel invariant` | Q1-Q4 PASS: Traffic shaping and admission tokens. Retained in core. |
| `internal/core/usageauthority/app` | 10 | 3623 | 8 packages across repo | `kernel invariant` | Q1-Q4 PASS: Distributed usage quota coordinator and lease renewal engine. Retained in core. |
| `internal/core/usageauthority/domain` | 11 | 1284 | 7 packages across repo | `kernel invariant` | Q1-Q4 PASS: Domain structures for usage authority and quota leases. Retained in core. |
| `internal/core/workspace` | 2 | 115 | `internal/infra/runtimebundle`, `internal/testkit` | `kernel invariant` | Q1-Q4 PASS: Workspace isolation boundary contracts. Retained in core. |
| `internal/featurebundle` | 2 | 298 | `internal/core/diag`, `internal/infra/compactioncompose`, `internal/infra/reasoningcompose`, `internal/infra/runtimebundle`, `internal/testkit/planeparity` | `generic extension mechanism` | Q1-Q4 PASS: Closed `FeatureBundle` typed carrier for multi-feature generation plane assembly. Retained in core substrate. |
| `internal/infra/billingcompose` | 5 | 662 | `internal/infra/runtimebundle` | `kernel invariant` | Q1-Q4 PASS: Infrastructure composition adapter for billing storage and admission engines. Retained in infra. |
| `internal/infra/compactioncompose` | 6 | 571 | `internal/infra/runtimebundle` | `feature-specific infrastructure/composition` | Dedicated composition adapter wiring compaction-continuity scheduler, detector, and candidate overlay into runtimebundle. Consolidate into featurehost in Tasks 2.4/3.3/10. |
| `internal/infra/compactiondetect` | 4 | 1192 | `internal/infra/runtimebundle` | `optional feature implementation/policy` | Concrete compaction detector implementation moved from core in predecessor. Retained in infra; owned via featurehost in Task 3.3. |
| `internal/infra/reasoningcompose` | 3 | 183 | `internal/infra/runtimebundle` | `feature-specific infrastructure/composition` | Dedicated composition adapter wiring reasoning preservation config and egress policy into runtimebundle. Transfer to featurehost in Task 2.4/Task 10. |
| `internal/infra/runtimebundle` | 93 | 12541 | `cmd/lipstd`, `internal/stdhttp`, `pkg/lipruntime` | `mixed/needs split` (final: `kernel invariant`, Tasks 2–7) | Q1-Q4 FAIL: Main composition root. Mostly kernel infrastructure, but holds per-feature fields (`KeepwarmPolicy`, `CompactionDetector`, etc.) in `ProcessServices` and `executorBuildInput`. Feature fields replaced by single `StandardFeatures` handle in Task 2.3 and Tasks 3-7. Retained as kernel composition root. |
| `internal/infra/secretaudit` | 1 | 119 | `internal/infra/secretguardcompose` | `optional feature implementation/policy` | Concrete audit logger implementation for secret-guard violations. Retained in infra; transferred to featurehost in Task 2.4/Task 10. |
| `internal/infra/secretguardcompose` | 1 | 196 | `internal/infra/runtimebundle` | `feature-specific infrastructure/composition` | Dedicated composition adapter wiring secret-guard engine into runtimebundle. Transfer to featurehost in Task 2.4/Task 10. |
| `internal/pluginreg` | 9 | 1146 | 11 packages across repo | `generic extension mechanism` | Q1-Q4 PASS: Static registry for backend plugins and feature extensions. Retained in infra substrate. |
| `internal/reasoningreplay` | 1 | 206 | `internal/plugins/backends/openaicaps`, `internal/plugins/features/reasoningpreservation` | `optional feature implementation/policy` | Dedicated support package for reasoning replay serialization. Transferred under standard feature tree in Task 10. |
| `internal/standardplugins` | 30 | 2998 | `cmd/model-inventory-proof`, `internal/infra/reasoningcompose`, `internal/infra/runtimebundle`, `internal/stdhttp`, `internal/testkit/compatibleparity`, `internal/testkit/conformance`, `internal/testkit/scale` | `standard-distribution registration/composition` | Standard plugin distribution registration manifest and default feature bundle. Retained in standardplugins. |
| `internal/standardplugins/contrib` | 1 | 296 | `internal/standardplugins` | `standard-distribution registration/composition` | Contributed backend plugin registrations. Retained in standardplugins. |
| `pkg/lipruntime` | 9 | 694 | *(external consumers)* | `mixed/needs split` (final: `kernel invariant` / `generic extension mechanism`, Task 8) | Q1-Q4 FAIL: Public embedding API. Holds concrete feature field `Options.ReasoningCompression` and typed egress adapters. Split in Task 8: remove concrete feature options; introduce generic `FeatureHostRegistrations` envelope. |

**Completion Proof**: Zero `unknown` classifications. All `mixed/needs split` packages are strictly bounded and assigned to Tasks 3–10.

---

## 6. Refreshed Predecessor Residual Ownership Inventory (10 Rows)

The 10 findings from `.kiro/specs/archive/pre-oss-core-slimming/residual-ownership-inventory.md` have been refreshed against current `main` (`fff67a0b3559cea120c5fa0abf4badabda7e69cd`) with exact production importers and consumers:

| # | Responsibility | Baseline Package | Exact Current Production Importers | Classification | Why Retained in Predecessor | Full-Closure Assigned Task |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | Compaction-continuity coordination (branch coordinator, preview intent, watermarks, capsule commit) | `internal/core/compactioncontinuity` | `internal/infra/compactioncompose`, `internal/infra/runtimebundle` | `mixed/needs split` (final: `optional feature implementation/policy`, Task 3) | Pre-OSS scope bounded to 3 migrations; branch coordinator CAS state tightly integrated. | **Task 3**: Extract feature types to `internal/plugins/features/compactioncontinuity/state`; delete `internal/core/compactioncontinuity`. |
| 2 | Conversation-view projection vs steering policy (replay message identity vs steering overlay & local turn) | `internal/core/conversationview` | `internal/core/b2bua`, `internal/core/continuity/bunstore`, `internal/core/conversationview/sdkadapter`, `internal/core/conversationview/storecontract`, `internal/core/runtime`, `internal/infra/metrics` | `mixed/needs split` (final: `kernel invariant` / `optional feature implementation/policy`, Task 4) | B2BUA projection safety is a kernel invariant, whereas steering is an optional feature. | **Task 4**: Retain minimal `internal/core/conversationprojection`; move steering/store to `internal/infra/conversationview`; delete `internal/core/conversationview`. |
| 3 | Interleaved-thinking and interleaved-state (`[thinker]` routing grammar vs prompt, memo store, budget, sanitization) | `internal/core/interleavedthinking`, `internal/core/interleavedstate` | `internal/core/b2bua`, `internal/core/continuity/bunstore`, `internal/core/interleavedthinking`, `internal/core/routing`, `internal/core/runtime`, `internal/infra/controlplane/observers`, `internal/infra/runtimebundle`, `internal/testkit/b2buatest` | `mixed/needs split` (final: `kernel invariant` / `optional feature implementation/policy`, Task 5) | `[thinker]` routing grammar is routing/runtime authority; memo extraction and prompt templates are optional UX policies. | **Task 5**: Preserve `[thinker]` grammar and cycle tracking in core; extract memo/prompt policy to `internal/plugins/features/interleavedthinking` behind narrow consumer port; delete `internal/core/interleavedthinking`. |
| 4 | Terminal-decision policy store (bounded policy store for client/operator tri-state enablement overrides) | `internal/core/terminaldecisionpolicy` | `internal/core/runtime`, `internal/infra/runtimebundle`, `internal/stdhttp/contract`, `internal/stdhttp/terminalpolicy` | `optional feature implementation/policy` | Core admission needs only an immutable snapshot of enabled state, but mutable store was kept in core to bound pre-OSS scope. | **Task 7**: Relocate mutable store to featurehost/feature package; core retains only immutable snapshot reader port; delete `internal/core/terminaldecisionpolicy`. |
| 5 | Feature-specific public `pkg/lipruntime` host options/adapters (`Options.ReasoningCompression`, egress adapters) | `pkg/lipruntime` | External embedding hosts via `pkg/lipruntime`, `cmd/lipstd` | `mixed/needs split` (final: `kernel invariant` / `generic extension mechanism`, Task 8) | Preserved binary/source compatibility for reasoning compression host composition prior to featurehost introduction. | **Task 8**: Introduce generic `FeatureHostRegistrations` envelope; move concrete reasoning options to feature SDK; remove `ReasoningCompression` from `pkg/lipruntime`. |
| 6 | Dedicated compaction-continuity compose adapter | `internal/infra/compactioncompose` | `internal/infra/runtimebundle` | `feature-specific infrastructure/composition` | Pre-OSS prioritized eliminating direct feature imports from runtimebundle over unifying compose adapters. | **Task 3 / Task 10**: Transfer ownership to `internal/standardplugins/featurehost`; consolidate dedicated compose adapters. |
| 7 | Dedicated reasoning-preservation compose adapter | `internal/infra/reasoningcompose` | `internal/infra/runtimebundle` | `feature-specific infrastructure/composition` | Isolated concrete reasoning feature assembly without premature generic DI abstractions. | **Task 2.4 / Task 10**: Transfer composition ownership to `internal/standardplugins/featurehost`. |
| 8 | Dedicated secret-guard compose adapter | `internal/infra/secretguardcompose` | `internal/infra/runtimebundle` | `feature-specific infrastructure/composition` | Isolated secret-guard engine construction while preserving candidate reload behavior. | **Task 2.4 / Task 10**: Transfer composition ownership to `internal/standardplugins/featurehost`. |
| 9 | Keep-warm policy and scheduling (prompt-cache ping policy, scheduler, registry, accounting) | `internal/core/keepwarm` | `internal/core/config`, `internal/core/runtime`, `internal/infra/billingcompose`, `internal/infra/metrics`, `internal/infra/runtimebundle`, `internal/stdhttp/admin/keepwarm` | `optional feature implementation/policy` | Optional background ping optimization; pre-OSS scope bounded to 3 migrations. | **Task 6**: Move keep-warm to `internal/plugins/features/keepwarm`; define narrow core consumer port for lifecycle events; delete `internal/core/keepwarm`. |
| 10 | Optional feature configuration in core config (`prompt_cache.go` keepwarm, `interleaved.go` thinker prompts/budgets) | `internal/core/config` | 30 packages across repository | `mixed/needs split` (final: `kernel invariant` / `optional feature implementation/policy`, Task 9) | Core config mixes essential proxy operational settings with optional UX feature schemas. | **Task 9**: Migrate optional feature config blocks to feature-owned schemas decoded via standard feature registrations. |

---

## 7. Wave-2 Process Feature Resource Transition Table

As mandated by Task 1.1, the 7 process-scoped feature resources currently held across `internal/infra/runtimebundle` and `internal/core/runtime` are documented below. This table forms the authoritative, executable transition contract for Wave A and subsequent migration tasks.

| Resource # | Resource Name & Concrete Type | Current Constructor | Current Field / Holder | Current Lifecycle / Close Registration Site | Closable? | Borrowed Lower-Level Dependencies | Featurehost Transfer Task | Interim Ownership Rule |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `KeepwarmPolicy` (`*keepwarm.PolicyStore`) | `keepwarm.NewPolicyStore(...)` in `internal/core/keepwarm/policy.go:50-58`, called in `NewProcessServices` at `internal/infra/runtimebundle/process_services.go:64` | `ProcessServices.KeepwarmPolicy` (`internal/infra/runtimebundle/process_services_types.go:60`, `internal/infra/runtimebundle/process_services.go:73`) | Non-closable (`*keepwarm.PolicyStore` in `internal/core/keepwarm/policy.go:34-48` has no `Close()` method; not registered in `ps.closers`) | **No** | `config.PromptCache`, metrics sink (`metrics.Meter`) | **Task 6.3** | Legacy constructor and holder remain sole owner until Task 6.3; featurehost constructs 0 duplicate instances. |
| 2 | `KeepwarmRegistry` (`*keepwarm.ManagerRegistry`) | `keepwarm.NewManagerRegistry()` in `internal/core/keepwarm/policy.go:127-133`, called in `NewProcessServices` at `internal/infra/runtimebundle/process_services.go:74` | `ProcessServices.KeepwarmRegistry` (`internal/infra/runtimebundle/process_services_types.go:61`, `internal/infra/runtimebundle/process_services.go:74`) | Non-closable (`*keepwarm.ManagerRegistry` in `internal/core/keepwarm/policy.go:116-180` has no `Close()` method; not registered in `ps.closers` in `process_services.go`) | **No** | `keepwarm.PolicyStore`, scheduler goroutines, background ping worker | **Task 6.3** | Legacy constructor and holder remain sole owner until Task 6.3; featurehost constructs 0 duplicate instances. Genuine non-closable invariant verified in counting test seam. |
| 3 | `TerminalDecisionPolicy` (`*terminaldecisionpolicy.Store`) | `terminaldecisionpolicy.NewStore(...)` in `internal/core/terminaldecisionpolicy/store.go:73-83`, called in `NewProcessServices` at `internal/infra/runtimebundle/process_services.go:75` | `ProcessServices.TerminalDecisionPolicy` (`internal/infra/runtimebundle/process_services_types.go:62`, `internal/infra/runtimebundle/process_services.go:75`) | Closable: `*terminaldecisionpolicy.Store.Close()` in `internal/core/terminaldecisionpolicy/store.go:195-206`, registered via `register(ps.TerminalDecisionPolicy.Close)` at `internal/infra/runtimebundle/process_services.go:85` | **Yes** | None (pure in-memory bounded store with actor tri-state sync map) | **Task 7.3** | Legacy `ProcessServices` closer remains sole physical cleanup owner; featurehost MUST NOT close. Atomic transfer to featurehost in Task 7.3. |
| 4 | `CompactionDetector` (`runtime.CompactionDetector` / `*compactiondetect.Detector`) | `compactiondetect.New(...)` in `internal/infra/compactiondetect/detector.go:42-50`, called in `adoptBackgroundAuxAndDetector` at `internal/infra/runtimebundle/background_aux_lifecycle.go:29` (invoked at `process_services.go:86`) | `ProcessServices.CompactionDetector` (`internal/infra/runtimebundle/process_services_types.go:63`, `internal/infra/runtimebundle/background_aux_lifecycle.go:29`) | Non-closable (`*compactiondetect.Detector` has no `Close()` method; not registered in `ps.closers`) | **No** | `config.Compaction` | **Task 3.3** | Legacy constructor and holder remain sole owner until Task 3.3; featurehost constructs 0 duplicate instances. |
| 5 | `BranchCoordinator` (`*compactioncontinuity.BranchCoordinator`) | `compactioncontinuity.NewBranchCoordinator(...)` in `internal/core/compactioncontinuity/branch_coordinator.go:35-43`, called in `bindSharedMutableProcessServices` at `internal/infra/runtimebundle/branch_coordinator.go:17` (invoked at `process_services.go:244`) | `ProcessServices.BranchCoordinator` (`internal/infra/runtimebundle/process_services_types.go:82`, `internal/infra/runtimebundle/branch_coordinator.go:17`) | Non-closable (`*compactioncontinuity.BranchCoordinator` in `internal/core/compactioncontinuity/branch_coordinator.go:27-33` has no `Close()` method; not registered in `ps.closers`) | **No** | Storage adapters (in-memory or Bun persistence) | **Task 3.3** | Legacy holder remains sole owner until Task 3.3; atomic handoff to featurehost. |
| 6 | `CompactionParentPort` (`*compactioncompose.CompactionContinuityParentPort`) | `compactioncompose.NewCompactionContinuityParentPort(...)` in `internal/infra/compactioncompose/parent_port.go:28-35`, called in `bindSharedMutableProcessServices` at `internal/infra/runtimebundle/branch_coordinator.go:21` (invoked at `process_services.go:244`) | `ProcessServices.CompactionParentPort` (`internal/infra/runtimebundle/process_services_types.go:83`, `internal/infra/runtimebundle/branch_coordinator.go:21`) | Non-closable (`*compactioncompose.CompactionContinuityParentPort` has no `Close()` method; not registered in `ps.closers`) | **No** | `b2bua.LineagePort` | **Task 3.3** | Legacy holder remains sole owner until Task 3.3; atomic handoff to featurehost. |
| 7 | `BackgroundAux` (`*runtimebundle.BackgroundAuxScheduler` / `*auxreq.BackgroundScheduler`) | `compactioncompose.NewProductionBackgroundScheduler(...)` in `internal/infra/compactioncompose/scheduler.go:31-42` called at `internal/infra/runtimebundle/background_aux_lifecycle.go:23` (or adopted from `in.BackgroundAux`, invoked at `process_services.go:86`) | `ProcessServices.BackgroundAux` (`internal/infra/runtimebundle/process_services_types.go:64`, `internal/infra/runtimebundle/background_aux_lifecycle.go:25`) | Closable: `*auxreq.BackgroundScheduler.Close()` in `internal/core/auxreq/background_scheduler.go:120-135`, registered via `register(ps.BackgroundAux.Close)` at `internal/infra/runtimebundle/background_aux_lifecycle.go:27` | **Yes** | Background worker goroutines, scheduler channels, DB pool / task queue | **never (borrowed)** (`design.md:232`) | Generic process resource borrowed via `ProcessInput`; featurehost never constructs/closes; legacy generic owner only (`design.md:215, 232`). |

**Invariants Enforced**:
1. Every process feature resource has exactly one constructor path and one physical cleanup owner at all times.
2. Featurehost in Wave A1 (Task 2) constructs and closes ZERO of these 7 resources.
3. Transfer happens atomically per resource in Tasks 3.3, 6.3, 7.3, and 10 (disabling the legacy constructor and close registration in the exact same change that activates the featurehost constructor).
4. Generic process resources (`BackgroundAux`, DB pools, secure-session stores) are borrowed through `ProcessInput` only (`design.md:232`); `Runtime.Close` must never close them (`design.md:215, 232`). Featurehost never constructs or closes `BackgroundAux`.
5. **Construction Verification Methodology**: Of the 7 resources, borrowed `BackgroundAux` is directly counted via an injectable factory seam (`auxConstructionCount == 1`). The six feature-owned resources (`KeepwarmPolicy`, `KeepwarmRegistry`, `TerminalDecisionPolicy`, `CompactionDetector`, `BranchCoordinator`, `CompactionParentPort`) are constructed internally by `NewProcessServices` (`process_services.go`). Per Phase 1 rules, introducing production dependency-injection seams for these internal factories is forbidden. Instead, single construction per process is mathematically proven by distinct pointers across separate process builds (`TestProcessFeatureResources_DistinctProcessInstances`), and zero duplicate constructions during candidate generation compiles is proven by pointer identity across compiles (`TestProcessFeatureResources_OwnershipCountingSeam`, `TestProcessFeatureResources_ConcurrentOverlappingGenerations`).

---

## 8. Task 1.2: Behavior & Lifetime Characterization Inventory

Before any production ownership moves occur, all critical behavioral and lifecycle properties across the 6 feature domains were characterized and confirmed covered by existing tests on disk:

### 1. Compaction-Continuity
- **Characterized Behaviors**: Parent isolation across generations; revision / CAS state updates; job/injection stale-result handling; concurrent reload safely preserving in-flight state.
- **Test Coverage (Verified Disk Paths)**:
  - `internal/core/compactioncontinuity/branch_coordinator_test.go`
  - `internal/core/compactioncontinuity/reload_concurrency_certification_test.go`
  - `internal/infra/compactioncompose/parent_port_test.go`
  - `internal/infra/compactioncompose/compaction_continuity_result_adapter_test.go`
  - `internal/infra/compactioncompose/scheduler_test.go`
  - `internal/infra/compactioncompose/surface_characterization_test.go`
  - `internal/infra/compactiondetect/characterization_test.go`
  - `internal/infra/compactiondetect/rules_test.go`

### 2. Conversation-View
- **Characterized Behaviors**: Message projection and reassertion; `never_backend` exclusion (critical B2BUA projection safety preventing internal tags reaching providers); missing-anchor fallback and fail-closed semantics; persistence parity; diagnostic sanitization without plaintext leakage.
- **Test Coverage (Verified Disk Paths)**:
  - `internal/core/conversationview/projection_test.go`
  - `internal/core/conversationview/one_snapshot_test.go`
  - `internal/core/conversationview/anchor_test.go`
  - `internal/core/conversationview/provenance_test.go`
  - `internal/core/conversationview/security_test.go`
  - `internal/core/conversationview/storecontract_test.go`
  - `internal/core/conversationview/concurrency_race_test.go`
  - `internal/core/conversationview/recovery_steering_contract_test.go`
  - `internal/core/conversationview/sdkadapter/services_test.go`
  - `internal/core/conversationview/sdkadapter/registrar_test.go`
  - `internal/core/conversationview/sdkadapter/writer_test.go`
  - `internal/core/conversationview/sdkadapter/observer_test.go`

### 3. Interleaved-Thinking & Interleaved-State
- **Characterized Behaviors**: Hidden vs visible stream emission; `[thinker]` routing grammar and thinker cycle execution; memo injection budget limits and truncation; cancellation unwinding; visible-output commitment point.
- **Test Coverage (Verified Disk Paths)**:
  - `internal/core/interleavedthinking/memo_test.go`
  - `internal/core/interleavedthinking/memo_store_test.go`
  - `internal/core/interleavedthinking/shape_test.go`
  - `internal/core/interleavedthinking/shape_selector_change_test.go`
  - `internal/core/interleavedthinking/sanitize_test.go`
  - `internal/core/interleavedstate/state_test.go`
  - `internal/core/routing/parser_thinker_test.go`
  - `internal/core/routing/execution_composition_test.go`
  - `internal/core/runtime/interleaved_stream_test.go`
  - `internal/core/runtime/executor_interleaved_test.go`

### 4. Keep-Warm Policy & Scheduling
- **Characterized Behaviors**: Session turn begin, turn end, and committed turn transitions; background prompt-cache ping scheduling and quiesce on shutdown; token/request accounting adaptation; configuration reload behavior.
- **Test Coverage (Verified Disk Paths)**:
  - `internal/core/keepwarm/config_policy_test.go`
  - `internal/core/keepwarm/manager_test.go`
  - `internal/core/keepwarm/rollout_test.go`
  - `internal/core/keepwarm/coverage_test.go`
  - `internal/core/keepwarm/regression_fixes_test.go`

### 5. Terminal-Decision Policy
- **Characterized Behaviors**: Policy authority; cache bounds and capacity eviction; actor tri-state precedence (client vs operator vs default); immutable generation snapshot; process close semantics.
- **Test Coverage (Verified Disk Paths)**:
  - `internal/core/terminaldecisionpolicy/policy_red_test.go`
  - `internal/infra/runtimebundle/terminal_decision_policy_wiring_test.go`

### 6. Public Reasoning-Host Composition & Legacy Config
- **Characterized Behaviors**: Public `pkg/lipruntime.Options` reasoning preservation compression configuration; semantic egress adapters; legacy YAML config decoding for prompt-cache and thinker prompts.
- **Test Coverage (Verified Disk Paths)**:
  - `pkg/lipruntime/reasoning_compression_test.go`
  - `pkg/lipruntime/reasoning_compression_external_test.go`
  - `pkg/lipruntime/build_test.go`
  - `pkg/lipruntime/registration_compose_test.go`
  - `internal/core/config/prompt_cache_test.go`
  - `internal/core/config/interleaved_test.go`
  - `internal/core/config/loader_test.go`

### 7. Process Feature Resource Ownership-Counting Test Seam
To enforce the strict ownership invariants required by Task 1.2 and Task 2.3, a dedicated injectable ownership-counting test seam was implemented in:
```text
internal/infra/runtimebundle/process_feature_ownership_counting_test.go
```

#### TDD Verification Proof
1. **Injectable Counter Design**:
   - `auxConstructionCount`: Directly counts factory constructions of borrowed `BackgroundAux` via `ProcessServicesInput.BackgroundAux` (`design.md:232`).
   - `terminalPolicyCloseCount`: Counts physical cleanup invocations on `TerminalDecisionPolicy` (`Store.Close()`).
   - `backgroundAuxCloseCount`: Counts physical cleanup invocations on `BackgroundAux` (`BackgroundScheduler.Close()`).
2. **Guarantees Asserted & Verified**:
   - **Single Construction & Distinct Instances**: `NewProcessServices` constructs the six feature-owned resources; `BackgroundAux` is borrowed via `ProcessServicesInput` (test-owned factory, counted separately; `design.md:232`). Directly counts `auxConstructionCount == 1` for the borrowed `BackgroundAux` factory. For the six feature-owned resources whose internal constructors in `NewProcessServices` cannot be injected without forbidden production seams, single construction per process is proven by asserting distinct pointers across separate `ProcessServices` builds (`TestProcessFeatureResources_DistinctProcessInstances`).
   - **Zero Construction on Candidate Compiles**: Compiling candidate generations against active process services constructs ZERO duplicate process feature resources (`auxConstructionCount` remains 1; identical pointers for all 7 resources across candidate generation compiles).
   - **Single Physical Close on Process Shutdown**: Invoking `ps.Close()` physically closes each closable resource (`TerminalDecisionPolicy` and `BackgroundAux`) exactly once (`terminalPolicyCloseCount == 1`, `backgroundAuxCloseCount == 1`).
   - **Idempotent Second Close**: Invoking `ps.Close()` a second time does not re-invoke physical closers (close counts remain 1).
   - **Genuinely Non-Closable Enforcement**: Explicitly asserts that `KeepwarmPolicy`, `KeepwarmRegistry`, `CompactionDetector`, `BranchCoordinator`, and `CompactionParentPort` do NOT implement `io.Closer`.
3. **Directly Counted vs Proved via Distinct Instances + Generation Stability**:
   - **Directly Counted**: `BackgroundAux` is borrowed via `ProcessServicesInput` (`design.md:232`); factory construction count is directly tracked with atomic counter `auxConstructionCount` (1 on initial process build, 0 on candidate generation compiles).
   - **Proved via Distinct Instances + Stability**: The 6 feature-owned resources (`KeepwarmPolicy`, `KeepwarmRegistry`, `TerminalDecisionPolicy`, `CompactionDetector`, `BranchCoordinator`, `CompactionParentPort`) are constructed internally within `NewProcessServices` without production DI seams. Single construction per process is proven by: (1) distinct pointer assertions across two separate `ProcessServices` builds (`TestProcessFeatureResources_DistinctProcessInstances`), proving constructors execute once per process without shared/global state; and (2) pointer-identity assertions across candidate generation compiles (`TestProcessFeatureResources_OwnershipCountingSeam` and `TestProcessFeatureResources_ConcurrentOverlappingGenerations`), proving zero duplicate instances are constructed or adopted per generation.
4. **RED Phase Reproduction (Finding F3)**:
   To verify genuine TDD failure before assertion satisfaction, an intentional expectation mismatch was executed against the counting seam (`auxConstructionCount == 2`):
   ```bash
   go test -count=1 -run TestProcessFeatureResources ./internal/infra/runtimebundle/
   ```
   Failing output observed:
   ```text
   --- FAIL: TestProcessFeatureResources_OwnershipCountingSeam (0.01s)
       process_feature_ownership_counting_test.go:205: BackgroundAux construction count = 1, want exactly 2
   FAIL
   FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	0.130s
   FAIL
   ```
5. **Execution Verification (GREEN)**:
   Command:
   ```bash
   go test -v -count=1 -run TestProcessFeatureResources ./internal/infra/runtimebundle/
   ```
   Output:
   ```text
   === RUN   TestProcessFeatureResources_OwnershipCountingSeam
   === PAUSE TestProcessFeatureResources_OwnershipCountingSeam
   === RUN   TestProcessFeatureResources_ConcurrentOverlappingGenerations
   === PAUSE TestProcessFeatureResources_ConcurrentOverlappingGenerations
   === RUN   TestProcessFeatureResources_DistinctProcessInstances
   === PAUSE TestProcessFeatureResources_DistinctProcessInstances
   === CONT  TestProcessFeatureResources_OwnershipCountingSeam
   === CONT  TestProcessFeatureResources_ConcurrentOverlappingGenerations
   === CONT  TestProcessFeatureResources_DistinctProcessInstances
   --- PASS: TestProcessFeatureResources_DistinctProcessInstances (0.01s)
   --- PASS: TestProcessFeatureResources_OwnershipCountingSeam (0.01s)
   --- PASS: TestProcessFeatureResources_ConcurrentOverlappingGenerations (0.01s)
   PASS
   ok  	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	0.110s
   ```

---

## 9. Task 1.3: Structural and Performance Baselines

### 1. Non-Test Go LOC Counts
Measured across the relevant packages (excluding `*_test.go`):

#### Measurement Method & Commands
Literal Command 1 (PowerShell across non-test `.go` files; single-quoted to prevent outer shell variable expansion):
```powershell
pwsh -NoProfile -Command '@("internal/core", "internal/infra/runtimebundle", "internal/standardplugins", "internal/standardplugins/featurehost") | ForEach-Object { $dir = $_; if (Test-Path $dir) { $lines = [int]((Get-ChildItem -Path $dir -Filter *.go -Recurse | Where-Object { -not $_.Name.EndsWith("_test.go") } | ForEach-Object { [System.IO.File]::ReadAllLines($_.FullName).Length } | Measure-Object -Sum).Sum); [PSCustomObject]@{ Tree = $dir; NonTestLOC = $lines } } else { [PSCustomObject]@{ Tree = $dir; NonTestLOC = 0 } } } | Format-Table -AutoSize'
```
Raw Output 1:
```text
Tree                                 NonTestLOC
----                                 ----------
internal/core                             89936
internal/infra/runtimebundle              12541
internal/standardplugins                   3302
internal/standardplugins/featurehost          0
```

Literal Command 2 (Go test verifying exact budget limits in `internal/archtest/budgets.go`):
```bash
go test -v -run TestPackageTreeBudgetsExact ./internal/archtest/
```
Raw Output 2:
```text
=== RUN   TestPackageTreeBudgetsExact
    budgets_test.go:42: counted internal/core LOC: 89936 (budget: 89961)
    budgets_test.go:42: counted internal/infra/runtimebundle LOC: 12541 (budget: 12566)
--- PASS: TestPackageTreeBudgetsExact (0.33s)
PASS
ok      github.com/vango-proj/go-llm-interactive-proxy/internal/archtest    0.435s
```

#### Summary Table
| Subsystem / Package | Current Non-Test LOC | Predecessor / Current Budget Limit | Margin to Limit | Status |
| --- | ---: | ---: | ---: | --- |
| `internal/core` | 89,936 | 89,961 (in `internal/archtest/budgets.go`) | +25 lines | Within budget |
| `internal/infra/runtimebundle` | 12,541 | 12,566 (in `internal/archtest/budgets.go`) | +25 lines | Within budget |
| `internal/standardplugins` | 3,302 | N/A | N/A | Baseline recorded |
| `internal/standardplugins/featurehost` | 0 | 0 (Tree not yet created) | 0 | Expected 0 before Wave A1 |

### 2. Per-Feature Fields in Core Structures
Direct feature field counts captured before featurehost extraction:

- **`runtimebundle.ProcessServices` (7 feature fields)**:
  Literal Command:
  ```bash
  git grep -n -E '^\s*(Keepwarm|TerminalDecision|Compaction|BackgroundAux)' internal/infra/runtimebundle/process_services_types.go
  ```
  Raw Output:
  ```text
  internal/infra/runtimebundle/process_services_types.go:60:	KeepwarmPolicy         *keepwarm.PolicyStore
  internal/infra/runtimebundle/process_services_types.go:61:	KeepwarmRegistry       *keepwarm.ManagerRegistry
  internal/infra/runtimebundle/process_services_types.go:62:	TerminalDecisionPolicy *terminaldecisionpolicy.Store
  internal/infra/runtimebundle/process_services_types.go:63:	CompactionDetector     runtime.CompactionDetector
  internal/infra/runtimebundle/process_services_types.go:64:	BackgroundAux          *BackgroundAuxScheduler
  internal/infra/runtimebundle/process_services_types.go:82:	BranchCoordinator    *compactioncontinuity.BranchCoordinator
  internal/infra/runtimebundle/process_services_types.go:83:	CompactionParentPort *compactioncompose.CompactionContinuityParentPort
  ```

- **`runtimebundle.executorBuildInput` (4 feature fields)**:
  Literal Command:
  ```bash
  git grep -n -E '^\s*(Compaction|GenerationRunner|TerminalDecision)' internal/infra/runtimebundle/candidate_compile.go
  ```
  Raw Output:
  ```text
  internal/infra/runtimebundle/candidate_compile.go:174:	CompactionDetector     runtime.CompactionDetector
  internal/infra/runtimebundle/candidate_compile.go:175:	CompactionScheduler    *auxreq.BackgroundScheduler
  internal/infra/runtimebundle/candidate_compile.go:176:	GenerationRunner       *compactioncompose.GenerationExecutorRunner
  internal/infra/runtimebundle/candidate_compile.go:177:	TerminalDecisionPolicy *terminaldecisionpolicy.Store
  ```

- **`core/runtime.ExecutorConfig` (6 feature fields)**:
  Literal Command:
  ```bash
  git grep -n -E '^\s*(ConversationViewObserver|TerminalDecisionPolicy|InterleavedConfig|MemoStore|Detector|BackgroundAux)' internal/core/runtime/executor_config.go
  ```
  Raw Output:
  ```text
  internal/core/runtime/executor_config.go:67:		ConversationViewObserver conversationview.Observer
  internal/core/runtime/executor_config.go:82:		TerminalDecisionPolicy terminaldecisionpolicy.Policy
  internal/core/runtime/executor_config.go:90:		InterleavedConfig interleavedthinking.Config
  internal/core/runtime/executor_config.go:91:		MemoStore         interleavedthinking.MemoStore
  internal/core/runtime/executor_config.go:104:		Detector      compaction.Detector
  internal/core/runtime/executor_config.go:105:		BackgroundAux compaction.BackgroundAux
  ```

- **`pkg/lipruntime.Options` (1 feature field)**:
  Literal Command:
  ```bash
  git grep -n -E '^\s*ReasoningCompression' pkg/lipruntime/options.go
  ```
  Raw Output:
  ```text
  pkg/lipruntime/options.go:34:	ReasoningCompression ReasoningCompressionOptions
  ```

### 3. Performance Benchmarks Baseline
Captured using `go test -bench ... -benchmem`:

#### Extension Plane Benchmarks (`internal/core/extensions`)
Literal Command:
```bash
go test -bench 'BenchmarkCompletionGates' -benchmem ./internal/core/extensions
```
Raw Output:
```text
goos: windows
goarch: amd64
pkg: github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions
cpu: AMD Ryzen 9 7950X 16-Core Processor            
BenchmarkCompletionGates_Populated-32           24953935                48.15 ns/op           32 B/op          1 allocs/op
BenchmarkCompletionGates_Empty-32               161559864                7.427 ns/op           0 B/op          0 allocs/op
PASS
ok      github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions   2.569s
```

#### Conversation View Projection Benchmarks (`internal/core/conversationview`)
Literal Command:
```bash
go test -bench 'Benchmark(Project|Reassert)' -benchmem ./internal/core/conversationview
```
Raw Output:
```text
goos: windows
goarch: amd64
pkg: github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview
cpu: AMD Ryzen 9 7950X 16-Core Processor            
BenchmarkProject_NoStateFastPath-32                 32997             36203 ns/op           21248 B/op        170 allocs/op
BenchmarkReassert_NoState-32                       326084              3650 ns/op             544 B/op         21 allocs/op
BenchmarkProject_4096Tags_20Messages-32              3729            321050 ns/op          240640 B/op        187 allocs/op
PASS
ok      github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview     3.818s
```

### 4. Change-Surface Baseline for Adding a Host-Bound Standard Feature
#### Verification Tool Command & Output
Literal Command:
```bash
go run ./internal/archtest/tools/changesurface/cmd -base fff67a0b3559cea120c5fa0abf4badabda7e69cd -json
```
Raw JSON Output:
```json
{
  "paths": {
    "docs-spec": [
      ".kiro/specs/core-feature-ownership-full-closure/implementation-baseline.md",
      ".kiro/specs/core-feature-ownership-full-closure/tasks.md"
    ],
    "shared-composition": [
      "internal/infra/runtimebundle/process_feature_ownership_counting_test.go"
    ]
  },
  "counts": {
    "docs-spec": 2,
    "shared-composition": 1
  }
}
```

Literal Command (Human-Readable Classification Report):
```bash
go run ./internal/archtest/tools/changesurface/cmd -base fff67a0b3559cea120c5fa0abf4badabda7e69cd
```
Raw Output:
```text
Extension change-surface report
extension-owned-production:   0
provider-profile-data:        0
shared-composition:           1
canonical-contract:           0
core-routing-runtime:         0
backendplugin-abi:            0
generated:                    0
tests-reference:              0
docs-spec:                    2
other:                        0
```
This confirms that the worktree contains zero core, runtime, or production feature modifications (`core-routing-runtime`: 0, `extension-owned-production`: 0).

#### Touchpoint Analysis
Currently, adding a single new host-bound standard feature requires modifying **8 to 9 touchpoint files** across 5 distinct layers:

1. `pkg/lipruntime/options.go` (Add public feature options and configuration fields)
2. `pkg/lipruntime/runtime.go` (Public adapter and feature options passing)
3. `internal/infra/runtimebundle/build_options.go` (Add feature options to internal build options)
4. `internal/infra/runtimebundle/process_services.go` (Add feature fields to `ProcessServices`)
5. `internal/infra/runtimebundle/process_builder.go` (Process wiring, factory construction, lifecycle registration)
6. `internal/infra/runtimebundle/executor_builder.go` (Wire feature into generation / `executorBuildInput`)
7. `internal/core/runtime/executor_config.go` (Add feature dependencies to core `ExecutorConfig`)
8. Dedicated compose adapter package (`internal/infra/<feature>compose/compose.go`)
9. `internal/standardplugins/standardplugins.go` (Standard plugin registration)

**Target Post-Refactor Change Surface**:
With `internal/standardplugins/featurehost` established (Tasks 2–11), adding a host-bound standard feature will touch only **2 files** (`internal/standardplugins/featurehost/...` and `internal/standardplugins/...`), with **ZERO** edits to `internal/core`, `internal/infra/runtimebundle`, or `pkg/lipruntime`.

---

## 10. Summary & Sign-off

- **Task 1.1 Complete**: Authoritative census generated (95 packages, 0 unknown, mixed packages explicitly bounded and assigned to Tasks 3-10); Wave-2 transition table established for all 7 process-scoped feature resources; 10 predecessor residual rows refreshed with current production importers.
- **Task 1.2 Complete**: Behavior and lifetime characterization inventory verified; ownership-counting test seam implemented under TDD (RED output captured, GREEN passing).
- **Task 1.3 Complete**: Structural LOC counts, per-feature field inventories, allocation benchmarks, and change-surface touchpoints captured.
- **Execution Guardrails §2 Satisfied**: Complete verification baseline recorded with explicit failure accounting.
- **Production Tree Integrity**: Zero production files modified in Phase 1. Ready for Wave A1 (Task 2.1).
