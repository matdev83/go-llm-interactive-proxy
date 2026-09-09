# Final Ownership Census — `core-feature-ownership-full-closure` (Task 12.1)

Regeneration of the Task 1.1 enumeration against the final tree. Every
production responsibility is classified into exactly one of the five final
categories: **kernel invariant**, **generic extension mechanism**, **optional
feature implementation/policy**, **feature-specific infrastructure/composition**,
**standard-distribution registration/composition**. Zero `mixed`, `unknown`,
`temporary`, `compat-to-remove`, or `future simplification` rows.

## 1. Method

Commands run in this session against the final worktree (branch
`fix/closure-ownership-final-census`, incorporating metrics publication fix
`48ee4b19`, collector registration assertion `db773a0e`, and HEAD at this
census certification commit):

- `go list ./internal/core/... ./internal/infra/... ./internal/standardplugins/...
  ./internal/pluginreg/... ./internal/featurebundle/... ./pkg/lipruntime/...
  ./internal/plugins/features/...` — package enumeration.
- `go list -f '{{range .Imports}}{{println .}}{{end}}'` on
  `internal/infra/runtimebundle`, `pkg/lipruntime`, `internal/core/runtime`,
  `internal/core/config` — production-only import edges (excludes test-only
  references, which `go list -deps` would otherwise conflate).
- `git grep "internal/plugins/features" -- internal/core
  internal/infra/runtimebundle pkg/lipruntime` filtered to non-test files —
  production feature-import ban check.
- `Test-Path` on all 9 retired trees (5 core packages, 3 infra compose
  packages, top-level `internal/reasoningreplay`) — resurrection check.
- `go test -count=1 ./internal/archtest/... ./tools/kiro/speccheck`,
  `go test -count=1 ./internal/infra/runtimebundle/ -run
  'TestProcessFeatureOwnership|TestTransition'`, `go build ./...`,
  `go run ./scripts/generate-feature-planes.go -check`, `make arch-report`,
  `git diff --check`, `gofmt -l` — validation gates.

Baseline reference: `implementation-baseline.md` (Task 1.1 census) is
historical and untouched; this document supersedes it for the final tree.

## 2. Surviving `internal/core/*` — all 52 top-level packages justified

Cross-checked against the `internal/archtest/core_ownership.go` manifest
(Task 11.1), which holds exactly 52 entries. The admission test
`TestCoreOwnershipManifestCoversEveryTopLevelPackage` (green this session)
fails on any new top-level core package without an entry, and
`validateCoreOwnershipEntries` rejects unknown categories, empty reasons,
generic mechanisms without consumer evidence, and stale entries. The live
tree holds exactly these 52 top-level directories with production Go; all 9
retired trees from both simplification specs are absent (§6).

50 rows are `kernel invariant`; 2 (`extensions`, `hooks`) are `generic
extension mechanism` with recorded independent consumers.

| Top-level package | Category | Manifest justification |
| --- | --- | --- |
| `accessmode` | kernel invariant | Base proxy execution-mode vocabulary, independent of features. |
| `accounting` | kernel invariant | Generic token accounting types and ledger contracts. |
| `admin` | kernel invariant | Minimal admin command port definitions. |
| `affinity` | kernel invariant | Backend connection affinity keys and routing selectors. |
| `auth` | kernel invariant | Client authentication, credential verification, principal mapping. |
| `authorityattribution` | kernel invariant | Attribution metadata for usage and concurrency leases. |
| `authoritycoord` | kernel invariant | Distributed lease and quota coordination across nodes. |
| `auxreq` | kernel invariant | Auxiliary request lifecycle, sub-request pipeline, cancellation. |
| `b2bua` | kernel invariant | Back-to-back user agent state machine, attempt loop, upstream I/O. |
| `billing` | kernel invariant | Billing identity, quote/exposure policy, immutable usage contracts. |
| `capabilities` | kernel invariant | Model capability bitflags consumed by routing and backends. |
| `concurrencyauthority` | kernel invariant | Concurrency lease allocation and enforcement (app/compatible/domain). |
| `config` | kernel invariant | Typed base proxy configuration; feature payloads stay opaque subtrees. Optional keep-warm/interleaved schemas removed in Task 9; `git grep` over non-test `internal/core/config/*.go` finds zero `keepwarm`, `stream_to_client`, `max_memo`, or `instructions_file` semantics (the sole `interleaved.go` feature mention is a doc pointer to the owning feature package). |
| `configreload` | kernel invariant | Dynamic configuration reload and generation swap coordination. |
| `continuation` | kernel invariant | Request continuation token domain types. |
| `continuity` | kernel invariant | Session continuity contracts and replay markers. |
| `controlplane` | kernel invariant | Control-plane state management and cluster observers. |
| `conversationprojection` | kernel invariant | Pure backend-effective projection, `never_backend` exclusion, anchors (Task 4 kernel half of the split). |
| `diag` | kernel invariant | Diagnostic telemetry, trace attributes, logging context. |
| `execbackend` | kernel invariant | Backend executor abstraction and invocation port. |
| `execctx` | kernel invariant | Execution context: deadlines, trace identity, cancellation. |
| `extensions` | generic extension mechanism | Closed standard extension planes and completion gates. Consumers: runtime, traffic, diag, testkit conformance. |
| `geoip` | kernel invariant | Client IP geolocation lookup for regional policy routing. |
| `hooks` | generic extension mechanism | Extension hook pipeline execution and phase ordering. Consumers: extensions, runtime, testkit conformance. |
| `http` | kernel invariant | HTTP protocol utilities, header normalization, status mapping. |
| `identity` | kernel invariant | Tenant, organization, user identity representation. |
| `interleavedstate` | kernel invariant | Routing-required thinker cycle state only; memo payload moved to the interleaved feature (Task 5). |
| `jsonpresence` | kernel invariant | JSON empty-vs-null presence semantics. |
| `jsonshape` | kernel invariant | Structural JSON validation for streams and frontends. |
| `leglifecycle` | kernel invariant | Upstream attempt leg lifecycle tracking and abort handling. |
| `lineage` | kernel invariant | Request/response message causal lineage tracking. |
| `localstream` | kernel invariant | In-memory canonical stream for local execution loops. |
| `metering` | kernel invariant | Raw usage event metering ports and aggregation. |
| `modelcatalog` | kernel invariant | Static model definitions, tokenizer mappings, context limits. |
| `modelregistry` | kernel invariant | Dynamic runtime model registration and lookup. |
| `modelview` | kernel invariant | Filtered model visibility views for tenants. |
| `policy` | kernel invariant | Rate limit and tier policy definitions. |
| `routeoverride` | kernel invariant | Operator A-leg routing-override rules and latest-wins state. |
| `routing` | kernel invariant | Backend selection, fallback sequences, weighted health routing. |
| `runtime` | kernel invariant | Request/attempt orchestration, output commitment, immutable generations. Concrete feature fields removed in Tasks 3–7; executor consumes only ordinary planes plus the approved narrow ports in §4. |
| `safety` | kernel invariant | Prompt safety boundary assertions and stream guard invariants. |
| `securesession` | kernel invariant | Secure-session authority: creation, verification, rotation, stores. |
| `snapshotgen` | kernel invariant | Immutable runtime state snapshot generation. |
| `state` | kernel invariant | Core state container and epoch tracking. |
| `stream` | kernel invariant | Canonical streaming abstractions, backpressure, chunk multiplexing. |
| `streamrecovery` | kernel invariant | Mid-stream disconnect recovery and idempotent retry markers. |
| `terminal` | kernel invariant | Final generation completion state evaluation. |
| `terminalwork` | kernel invariant | Post-response async terminal work pipeline. |
| `tokenaccounting` | kernel invariant | Token counting, budget enforcement, ledger, preflight. |
| `traffic` | kernel invariant | Traffic shaping and admission tokens. |
| `usageauthority` | kernel invariant | Distributed usage quota coordination and lease renewal. |
| `workspace` | kernel invariant | Workspace isolation boundary contracts. |

Production import evidence: `go list -f Imports` over
`internal/core/runtime` and `internal/core/config` shows zero
`internal/plugins/features` edges; the only `internal/core` non-test
feature-path hit is the doc comment in `config/interleaved.go`.

## 3. `internal/infra/*compose` remnants — none feature-specific

`ls internal/infra/` shows exactly one `*compose` survivor:
`internal/infra/billingcompose`. The three feature-specific compose adapters
from the baseline (`compactioncompose`, `reasoningcompose`,
`secretguardcompose`) are absent (`Test-Path` false for all three); their
content was consolidated under `internal/standardplugins/featurehost/*`
children in Tasks 2.4/3.3/10.3 (see `residual-consumer-census.md` rows 2–4).

| Package | Category | Evidence |
| --- | --- | --- |
| `internal/infra/billingcompose` | kernel invariant (generic infrastructure adapter) | `go list` production import set is stdlib plus `internal/core/billing` (domain contracts), `internal/core/runtime` (generic composition-root identity bundle only), `pkg/lipapi`, `pkg/lipsdk/scope`. Zero imports of `keepwarm` or any feature package. Sole consumer is `internal/infra/runtimebundle`. Per `residual-consumer-census.md` row 5. |
| `internal/infra/compactiondetect` | optional feature implementation/policy, featurehost-owned | Concrete compaction detector. Constructor invoked only from `featurehost/process.go`; `ProcessServices` holds no detector field. Transition row `CompactionDetector`, Task 3.3. |
| `internal/infra/conversationview` (+`sdkadapter`, `storecontract`) | feature-specific infrastructure/composition | Steering/state services, persistence adapters, SDK adapters outside core (Task 4.2/4.3). Core consumes only the immutable `conversationprojection` snapshot. Store owned by `featurehost.Runtime.conversationStore`. |
| `internal/infra/auxiliary` | kernel invariant (genuinely shared generic infrastructure) | Zero feature imports; operates on generic `auxreq.SchedulerConfig` bounds shared by the process background scheduler and generation auxiliary execution. Per `residual-consumer-census.md` row 4b. Borrowed by featurehost via `ProcessInput`, never closed by `Runtime.Close`. |

## 4. Standard distribution, registries, public facade, feature trees

| Package(s) | Category | Evidence |
| --- | --- | --- |
| `internal/standardplugins` (+`contrib`), `internal/standardplugins/featurehost` (+`compaction`, `reasoning`, `secretguard`, `sessionpolicy` children), `internal/standardplugins/legacyfeatureconfig` | standard-distribution registration/composition | The single composition layer permitted to know concrete standard features (Req 8.1). `legacyfeatureconfig` is the bounded one-way legacy-YAML normalizer (Task 9.3); it decodes no new semantics. Budgeted separately (Task 11.3). |
| `internal/pluginreg`, `internal/featurebundle` | generic extension mechanism | Static backend/feature registries and the closed `FeatureBundle` typed carrier; no concrete standard-feature knowledge. |
| `internal/infra/runtimebundle` | kernel invariant (generic composition root) | Production imports include only the `featurehost` facade plus `pkg/lipsdk/featurehost` — zero `internal/plugins/features/*` production edges (verified `go list -f Imports`; remaining `git grep` hits are test files only). Featurehost-owned metrics swaps arrive via opaque `CorePorts.MetricsSwap func()` registered into candidate `PhasePublish`, executing only post-publication. |
| `pkg/lipruntime` | kernel invariant / generic extension mechanism (public facade) | Production imports are `internal/infra/runtimebundle`, `internal/stdhttp`, and `pkg/lipsdk/*` only — zero `internal/plugins/features/*` production edges. `Options.ReasoningCompression` is gone (no non-test hit); the sole feature seam is `FeatureHostRegistrations []featurehost.Registration`. |
| `internal/plugins/features/*` (compactioncontinuity + subpackages, interleavedthinking + state, keepwarm, reasoningpreservation + reasoningreplay, secretguard + engine, toolcallrepair, agentloopguard, reference `ref*` fixtures) | optional feature implementation/policy | Leaves with respect to core/runtime: `go list -deps` over the three migrated features shows zero `internal/core` / `runtimebundle` edges; `TestProductionClosureEdgesHold` and the recursive feature import-boundary tests enforce this permanently. Reasoning replay lives under its owning feature (`reasoningpreservation/reasoningreplay`); top-level `internal/reasoningreplay` is absent. |
| `pkg/lipsdk/featurehost`, `pkg/lipsdk/reasoninghost`, `pkg/lipsdk/secretguardhost` | generic extension mechanism (public SDK host-binding contracts) | Feature-neutral registration envelope plus narrowly scoped typed capability contracts; generic runtime forwards without type-switching. Per `residual-consumer-census.md` row 6. |

## 5. Req 13.4 — generic aggregates contain no concrete feature fields

- `ProcessServices` (`internal/infra/runtimebundle/process_services_types.go`):
  generic process resources plus exactly one standard-feature handle,
  `StandardFeatures *featurehost.Runtime`. No `KeepwarmPolicy`,
  `KeepwarmRegistry`, `TerminalDecisionPolicy`, `CompactionDetector`,
  `BranchCoordinator`, `CompactionParentPort`, or `ConversationStore` fields.
- `ProcessServicesInput`: `Cfg`, `Log`, `Opts`, `Tracing`, discovered-plugin
  ownership resources, and borrowed `BackgroundAux` only.
- Executor inputs (`internal/core/runtime/executor_config.go`): ordinary
  planes plus the minimal approved consumer-owned ports —
  `PromptCacheMaintenance`, `ConversationViewReader`/`ConversationViewTagger`/
  `ConversationViewObserver`/`SteeringWriterFactory`, `TerminalPolicyReader`,
  `InterleavedProcessor` (`Processor`), `CompactionDetector` (`Detector`) —
  all narrow interfaces over core/SDK DTOs, never concrete feature types.
  Secret Guard execution posture crosses strictly through the binder-only
  `secret_guard_execution` extension plane (closed set of 26 planes after the
  reviewed exception); `ExtensionsOptions` contains no Secret Guard exception fields.
- `pkg/lipruntime.Options`: generic production registrations (authority,
  economics, metering, traffic, usage, policy observers) plus the single
  `FeatureHostRegistrations []featurehost.Registration` aggregate. No
  per-feature option family; no `map[string]any` dependency bag in
  `pkg/lipruntime`, `featurehost`, or `runtimebundle` process composition
  (grep clean, non-test files).

Enforced by `TestGenericAggregatesContainNoPerFeatureFields` (with negative
fixtures), `TestLipruntime_PublicTypes_NoInternalImportsInSignatures`,
`TestBuild_CompressionEnabled_StockZeroFailsClosed` /
`TestBuild_CompressionDisabled_StockZeroSucceeds` /
`TestBuild_CompressionEnabled_WithPublicPolicyAndResolver_Succeeds`, and
`TestOptions_NonMoneyArchitecture_NoBillingFields` — all green in the
`./internal/archtest/...` and `./pkg/lipruntime/...` runs this session.

## 6. Wave-2 transition table discharge

Final state of the 8-row table
(`process_feature_ownership_transition_table_test.go:36-125`), updated from
the baseline §7 to final constructors under `featurehost/process.go`:

| Resource | Final constructor / holder | Physical cleanup owner | Status |
| --- | --- | --- | --- |
| `KeepwarmPolicy` | `keepwarm.NewPolicyStore` at `featurehost/process.go:136` → `Runtime.keepwarmPolicy` | non-closable, featurehost-owned (Task 6.3) | migrated |
| `KeepwarmRegistry` | `keepwarm.NewManagerRegistry` at `featurehost/process.go:141` → `Runtime.keepwarmRegistry` | non-closable, featurehost-owned (Task 6.3) | migrated |
| `TerminalDecisionPolicy` | `sessionpolicy.NewStore` at `featurehost/process.go:147` → `Runtime.terminalPolicy` | `r.registerCloser(policyStore.Close)` at `featurehost/process.go:149`, nested under the single `StandardFeatures.Close` (Task 7.3) | migrated |
| `CompactionDetector` | `compactiondetect.New` at `featurehost/process.go:59` → `Runtime.compactionDetector` | non-closable, featurehost-owned (Task 3.3) | migrated |
| `BranchCoordinator` | `state.NewBranchCoordinator` at `featurehost/process.go:61` → `Runtime.branchCoordinator` | non-closable, featurehost-owned (Task 3.3) | migrated |
| `CompactionParentPort` | `compaction.NewParentPort` at `featurehost/process.go:67` → `Runtime.compactionParentPort` | non-closable, featurehost-owned (Task 3.3) | migrated |
| `ConversationStore` | `featurehost.NewProcess` at `featurehost/process.go:85` → `Runtime.conversationStore` | non-closable, featurehost-owned (Task 4.3) | migrated |
| `BackgroundAux` | `auxiliary.NewProductionBackgroundScheduler` at `background_aux_lifecycle.go:23` → `ProcessServices.BackgroundAux` | `register(ps.BackgroundAux.Close)`; generic owner only, never transferred | intentionally borrowed generic |

Zero legacy, unassigned, or dual-owner rows. Passing tests run this session:

- `TestProcessFeatureOwnership_TransitionTableIntegrity` — row count/shape,
  no empty ownership rules.
- `TestProcessFeatureOwnership_SingleConstructorValidation` — production
  wiring validates; single closer for the closable policy store.
- `TestProcessFeatureOwnership_DualConstructorWiringRejected` — dual
  legacy+featurehost wiring fails with `ErrDualConstructorWiring`.
- `TestProcessFeatureOwnership_MissingBorrowedResourceRejected` —
  `BackgroundAux` remains the sole borrowed generic scheduler.
- `TestProcessFeatureResources_OwnershipCountingSeam` — one construction,
  zero generation-time duplicates, one physical close.

## 7. Residual-debt verdict: zero

No material simplification item remains:

1. No `mixed`/`unknown`/`temporary`/`compat-to-remove`/`future
   simplification` row exists in §§2–6 above; every responsibility has one
   final owner.
2. All 9 retired trees from both simplification specs are absent from disk
   (§2 intro; `Test-Path` false × 9; `TestRetiredCorePackageAbsenceCoversAllManifestDirs`
   green).
3. No per-feature field, constructor, or closer survives in generic
   composition (§5; aggregate scanner green).
4. No production edge violates the allowed dependency direction (§§2–4;
   `TestProductionClosureEdgesHold`, `TestClosureForbiddenImports_RulesPresent`,
   feature import-boundary tests green).
5. `pkg/lipruntime` exposes exactly the one approved registration aggregate
   (§5; Options tests green).
6. No `map[string]any`, service locator, reflection DI, or request-time
   resolver was introduced (`TestFeatureHost_NoResolverMethodGrowth`,
   `TestFeatureHost_NoBindingMapStorage`,
   `TestFeatureHost_ReflectOnlyTypedNilGuards` green; `map[string]any` grep
   clean in `featurehost`/`runtimebundle`/`lipruntime` production files).
7. Production metrics publication timing is hardened: keep-warm `MetricsSwap`
   is registered strictly as a `PhasePublish` action (`standard-features-metrics-publish`),
   ensuring uncommitted candidate generations cannot alter process metrics.
8. Secret Guard execution posture crosses the boundary solely via the binder-only
   `secret_guard_execution` extension plane (closed set of 26 planes after the
   sole reviewed exception); `ExtensionsOptions` contains no Secret Guard exception fields.

The production changes under this final remediation PR strictly address the publication
timing gap and SDD revalidation, preserving all zero-debt invariants across the repository.

## 8. Program closeout & Final Ownership Certification

The core simplification program opened by `pre-oss-core-slimming` and
completed by this specification is fully closed with zero residual
ownership debt:

- **Final census** (§§2–6) classifies every production responsibility with
  zero deferred rows; the Wave-2 transition table is fully discharged.
- **Production publication semantics**: Keep-warm process metrics swap executes
  only post-publication via `PhasePublish` on the candidate resource ledger
  (`standard-features-metrics-publish`). Rejected candidate publications cannot
  alter active process metrics.
- **Extension planes closed set**: Exactly 26 standard planes after the sole
  approved and reviewed exception `secret_guard_execution` (binder-only contract,
  `MultExclusive`, `NilSkip`, defensive frozen copies). Generic aggregates
  (`ExtensionsOptions`, `ProcessServices`) contain zero Secret Guard fields.
- **Measured package tree budgets**:
  - `internal/standardplugins/featurehost`: **3280** non-test lines (budget ceiling **3280**).
  - `internal/infra/runtimebundle`: **12310** non-test lines (budget ceiling **12333**).
  - Both convergence trees strictly satisfy `TestPackageTreeBudgetsExact` and
    `TestLineComplexityBudgets`.
- **Repository certification gates**: Full `go test ./...`, `go vet ./...`,
  `make quality-checks`, and release-grade `make qa` pass cleanly.

### 8.1 Verification Evidence & Targeted Linux Race

```text
Targeted Linux race (required for this lifecycle change):
  command: go test -count=1 -race ./internal/infra/runtimebundle/... ./internal/standardplugins/featurehost/... ./internal/plugins/features/keepwarm/...
  runner: ubuntu-latest via pre-oss-core-slimming-race.yml (extended package list)
  result: pending orchestrator dispatch after push — do not claim PASS without a run
```

---

## Appendix: Historical Review & Delivery Predecessors

### A.1 Independent Architecture Review Findings (Historical Task 12.2)

During historical Task 12.2 review of PR #598, four HIGH findings were identified
and remediated in-tree:
1. **Keep-warm generation leak (H1)**: Acquired manager cleanup transferred
   immediately into candidate `ResourceLedger` with rollback on compile failure.
2. **Memo steering policy in core (H2)**: Memo header, overlay identity, and
   placement policy moved to `interleavedthinking` behind the `InterleavedProcessor`
   port.
3. **Registration-level disablement bypassed (H3)**: Outer `Registration.Enabled=false`
   made authoritative in interleaved and keep-warm adapters.
4. **Generic runtime branched on concrete feature identity (H4)**: Default environment
   binding moved to `featurehost` behind generic `HostEnvironment` capability;
   `secretguardhost` import eliminated from `runtimebundle`.

### A.2 Historical Predecessor Delivery (PR #598)

PR #598 was historically merged as commit `d85fcc24` completing Wave 2. This current
certification supersedes interim follow-ups (#600, #613) on branch
`fix/closure-ownership-final-census`, certifying the publication timing fix,
26-plane contract, and ownership censuses against live tree state.