# Final Ownership Census — `core-feature-ownership-full-closure` (Task 12.1)

Regeneration of the Task 1.1 enumeration against the final tree. Every
production responsibility is classified into exactly one of the five final
categories: **kernel invariant**, **generic extension mechanism**, **optional
feature implementation/policy**, **feature-specific infrastructure/composition**,
**standard-distribution registration/composition**. Zero `mixed`, `unknown`,
`temporary`, `compat-to-remove`, or `future simplification` rows.

## 1. Method

Commands run in this session against the final worktree (branch
`feat/core-feature-ownership-full-closure`, HEAD `e7016d3f`):

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
| `internal/infra/runtimebundle` | kernel invariant (generic composition root) | Production imports include only the `featurehost` facade plus `pkg/lipsdk/featurehost` — zero `internal/plugins/features/*` production edges (verified `go list -f Imports`; remaining `git grep` hits are test files only). |
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

No fix-up change was required under this SDD: there was nothing material to
fix. Accordingly this task changes no production code — the deliverable is
this census plus the tasks.md checkbox.

### 7.1 Correction: independent-review findings and fixes

The independent architecture review (Task 12.2) returned four high findings
against the census above after it was written. All four were mechanical
ownership gaps and were fixed under this SDD with RED-first regressions plus
permanent ratchets; the census claims in §§2–6 were re-verified after the
fixes by the same gates listed in §1 (archtest, runtimebundle/featurehost/
core-runtime/feature test suites, planes check, diff check, gofmt).

1. Keep-warm generation leak (Req 2.3/6.5/8.5). The candidate acquired the
   keep-warm manager (lazy start plus process-registry registration) while
   its cleanup travelled separately to final bundle construction, so a
   rejected candidate (candidate-compile failure, fault injection, handler
   composition failure) retained its manager in the process registry; the
   bundle also kept a feature-specific cleanup path beside the ledger. Fix:
   the acquired cleanup transfers into the candidate `ResourceLedger`
   immediately (rollback covers every later failure point), and the bundle
   relies on ledger ownership only. Covered by process-registry counts
   around rejected candidates, overlapping-generation counts, and a
   fault-injection rollback test.
2. Memo steering policy in core (Req 4.2/5.2/13.3). The model-visible memo
   header, overlay identity, placement/fallback selection, and memo-specific
   filtering/deactivation lived in `internal/core/runtime`. Fix: rendering,
   overlay identity, placement/fallback, and filtering moved to the
   feature-owned implementation (`interleavedthinking` steering policy)
   behind an explicit adapter (`featurehost/interleaved.go`) extending the
   `InterleavedProcessor` core port; core keeps authoritative output
   commitment and B-leg continuation sequencing. Locked by a new archtest
   ratchet forbidding the memo literal, overlay-ID constants,
   `StablePrefixFallback`, and memo-policy symbols in `internal/core`
   production code.
3. Registration-level disablement bypassed (Req 5.5/6.5/10.5). Both adapters
   matched registrations without checking outer `Registration.Enabled`, so
   outer-disabled entries still constructed resources (interleaved with inner
   enabled; keep-warm even with empty config via decoder defaults). Fix:
   outer `Enabled=false` is authoritative in both adapters (no resource
   constructed); absent entries keep their intended defaults. Covered by an
   absent/disabled/enabled/conflicting matrix for both adapters.
4. Generic runtime branched on concrete feature identity (Req 8.3/9.3/13.4).
   `runtimebundle` imported `secretguardhost`, scanned registrations for its
   `BindingID`, and threaded a feature-specific `SecretEnv` composition
   input. Fix: default environment binding construction plus
   presence/precedence handling moved into standard-distribution
   composition; `featurehost` exposes a generic `HostEnvironment`
   capability in `ProcessInput` and synthesizes the default
   `secretguardhost.Binding` only when no explicit registration is present
   (explicit wins, no duplicates). `runtimebundle` production code no longer
   imports `secretguardhost`. Locked by a production-import ratchet plus the
   extended host entry-point tests.

Net ownership effect: §§2–5 classifications are unchanged (no responsibility
changed category; the §6 transition table stays discharged). The fixes moved
policy surface out of generic trees into its owners, so generic-tree line
totals did not grow except for the explicit port/capability seams the review
required (see the implementation evidence for the measured deltas).

### 7.2 Evidence closure for the 12.2 re-review

The re-review accepted the §7.1 fixes as correct but rejected the evidence as
not durable (RED outputs not captured verbatim), flagged two untested
regression combinations, and asked for the all-adapter enablement handling
behind §§2–6 to be independently established. This section closes all three
points. RED was reproduced in a detached throwaway worktree at the parent of
the fix commit (new test files copied over unchanged; files that reference
new production symbols were demonstrated with minimal parent-API-only
throwaways or pre-fix source excerpts instead of forcing compilation). The
throwaway worktree was removed after capture. No production code changed
under this section: the two suggested regressions were already handled by
the §7.1 fixes, so the deliverable is two additional regression tests plus
this evidence.

#### H1–H4 RED (pre-fix outputs, verbatim)

Command (throwaway worktree at parent commit):
`go test -count=1 -run
'TestCompileGeneration_RejectedCandidateQuiescesKeepwarmManager|TestCompileGeneration_OverlappingGenerationsKeepwarmRegistryCounts'
./internal/infra/runtimebundle/`

```text
--- FAIL: TestCompileGeneration_RejectedCandidateQuiescesKeepwarmManager (0.01s)
    keepwarm_generation_ledger_test.go:74: rejected candidate retained keepwarm manager: registry len=1 want 0
--- FAIL: TestCompileGeneration_OverlappingGenerationsKeepwarmRegistryCounts (0.01s)
    keepwarm_generation_ledger_test.go:110: after rejected candidate registry len=2 want 1
FAIL
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	0.056s
FAIL
```

Command: `go test -count=1 -run 'TestCoreHasZeroMemoSteeringPolicy' -v
./internal/archtest/`

```text
    interleaved_steering_ownership_test.go:70: internal/core production holds memo steering policy (16):
        internal/core/runtime/interleaved_steering.go: memo literal [Session Steering Guidance]
        internal/core/runtime/interleaved_steering.go: memo literal interleaved-thinking-memo
        internal/core/runtime/interleaved_steering.go: memo literal interleaved_thinking_memo
        internal/core/runtime/interleaved_steering.go: memo symbol SessionSteeringGuidanceHeader
        internal/core/runtime/interleaved_steering.go: memo symbol SessionSteeringGuidanceHeader
        internal/core/runtime/interleaved_steering.go: memo symbol StablePrefixFallback
        internal/core/runtime/interleaved_steering.go: memo symbol interleavedMemoOverlayID
        internal/core/runtime/interleaved_steering.go: memo symbol interleavedMemoOverlayID
        internal/core/runtime/interleaved_steering.go: memo symbol interleavedMemoOverlayID
        internal/core/runtime/interleaved_steering.go: memo symbol interleavedMemoOverlayID
        internal/core/runtime/interleaved_steering.go: memo symbol interleavedMemoSteeringReason
        internal/core/runtime/interleaved_steering.go: memo symbol interleavedMemoSteeringReason
        internal/core/runtime/interleaved_steering.go: memo symbol memoSteeringPayload
        internal/core/runtime/interleaved_steering.go: memo symbol memoSteeringPayload
        internal/core/runtime/interleaved_steering.go: memo symbol stripMemoSteeringOverlay
        internal/core/runtime/interleaved_steering.go: memo symbol stripMemoSteeringOverlay
--- FAIL: TestCoreHasZeroMemoSteeringPolicy (0.31s)
FAIL
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/archtest	0.347s
FAIL
```

Command: `go test -count=1 -run
'TestCompileGeneration_InterleavedRegistrationEnabled|TestCompileGeneration_KeepwarmRegistrationEnabled'
-v ./internal/standardplugins/featurehost/`

```text
    registration_enabled_test.go:98: InterleavedProcessor present=true want false
    registration_enabled_test.go:157: KeepwarmManager present=true want false
    registration_enabled_test.go:157: KeepwarmManager present=true want false
--- FAIL: TestCompileGeneration_InterleavedRegistrationEnabled (0.00s)
    --- PASS: TestCompileGeneration_InterleavedRegistrationEnabled/outer_enabled_with_inner_enabled_constructs_processor (0.00s)
    --- PASS: TestCompileGeneration_InterleavedRegistrationEnabled/outer_disabled_with_empty_config_constructs_nothing (0.00s)
    --- PASS: TestCompileGeneration_InterleavedRegistrationEnabled/absent_entry_disables_processor_without_legacy_config (0.00s)
    --- FAIL: TestCompileGeneration_InterleavedRegistrationEnabled/outer_disabled_with_inner_enabled_constructs_nothing (0.00s)
--- FAIL: TestCompileGeneration_KeepwarmRegistrationEnabled (0.00s)
    --- PASS: TestCompileGeneration_KeepwarmRegistrationEnabled/absent_entry_applies_defaults (0.00s)
    --- PASS: TestCompileGeneration_KeepwarmRegistrationEnabled/outer_enabled_with_inner_enabled_constructs_manager (0.00s)
    --- FAIL: TestCompileGeneration_KeepwarmRegistrationEnabled/outer_disabled_with_empty_config_constructs_nothing (0.00s)
    --- FAIL: TestCompileGeneration_KeepwarmRegistrationEnabled/outer_disabled_with_inner_enabled_constructs_nothing (0.00s)
FAIL
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost	0.037s
FAIL
```

H4 production-import ratchet (same ratchet body as
`TestProductionRuntimeBundleHasZeroSecretguardhostImports`, run as a
throwaway against the pre-fix tree):

```text
    zz_redcheck_sg_ratchet_test.go:37: production internal/infra/runtimebundle has forbidden secretguardhost imports (2):
        internal/infra/runtimebundle/host_build.go: imports github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguardhost
        internal/infra/runtimebundle/production_options.go: imports github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguardhost
--- FAIL: TestRedcheckProductionRuntimeBundleHasZeroSecretguardhostImports (0.28s)
FAIL
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/archtest	0.312s
FAIL
```

H4 end-to-end defaulting tests cannot compile against the parent (they use
the new `featurehost.HostEnvironment` capability and `ProcessInput`
`HostRegistrations`/`HostEnv` fields), which itself evidences the fix shape.
Copy attempts failed as expected and the copies were deleted:

```text
vet: internal\standardplugins\featurehost\hostenv_redcheck_test.go:39:84: undefined: featurehost.HostEnvironment
vet: internal\infra\runtimebundle\zz_redcheck_sg_test.go:17:6: stubSecretGuardHostEnv redeclared in this block
```

Pre-fix source excerpts (all shown deleted by
`git diff <parent> HEAD -- internal/infra/runtimebundle/host_build.go
internal/infra/runtimebundle/production_options.go`):

```text
host_build.go:25:   "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguardhost"
host_build.go:231: if in.SecretEnv != nil && !hasSecretGuardHostRegistration(prod.FeatureHostRegistrations) {
host_build.go:233:   (&secretguardhost.Binding{Environment: in.SecretEnv}).Registration(),
production_options.go:15: "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguardhost"
production_options.go:88: func hasSecretGuardHostRegistration(regs []featurehost.Registration) bool {
production_options.go:93:   if strings.TrimSpace(reg.Binding.HostBindingID()) == secretguardhost.BindingID {
```

#### Suggested regressions S1/S2 (both real, both RED pre-fix, both green post-fix)

S1 premise verified, not moot: `generation.go` carries a legacy
`ConfigInterleaved` fallback (`if !ic.Enabled && in.ConfigInterleaved.Enabled`
at the parent) that resurrects a processor independently of the canonical
registration. At the parent the registration loop ignored outer `Enabled`,
so the disabled+legacy-enabled combination built a processor (empty inner
config) or errored spuriously (inner-enabled entry tripped the
legacy/canonical conflict check). New test
`TestCompileGeneration_InterleavedDisabledRegistrationSuppressesLegacyFallback`
(`registration_enabled_test.go`) pins suppression for both inner variants.
Pre-fix output:

```text
    registration_enabled_test.go:205: InterleavedProcessor present with outer-disabled registration and enabled legacy config, want nil
    registration_enabled_test.go:202: CompileGeneration: featurehost: both legacy config.interleaved and canonical feature "interleaved-thinking" are configured
--- FAIL: TestCompileGeneration_InterleavedDisabledRegistrationSuppressesLegacyFallback (0.00s)
    --- FAIL: TestCompileGeneration_InterleavedDisabledRegistrationSuppressesLegacyFallback/outer_disabled_with_empty_config_suppresses_enabled_legacy_config (0.00s)
    --- FAIL: TestCompileGeneration_InterleavedDisabledRegistrationSuppressesLegacyFallback/outer_disabled_with_inner_enabled_suppresses_enabled_legacy_config (0.00s)
FAIL
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost	0.038s
FAIL
```

S2 covers failure inside candidate compilation: the `discardAcquiredKeepwarm`
path (`candidate_lifecycle.go:65`, invoked from `compile_generation.go`
when `compileCandidate` itself fails, before any candidate ledger exists to
own the acquired cleanup). New test
`TestCompileGeneration_CandidateCompileFailureDiscardsAcquiredKeepwarm`
(`keepwarm_generation_ledger_test.go`) injects a fault at the `model`
boundary inside `compileCandidate` and asserts the process registry is
 clean. At the parent the error returned directly and the acquired manager
leaked. Pre-fix output:

```text
    keepwarm_generation_ledger_test.go:161: candidate-compile failure leaked keepwarm manager: registry len=1 want 0
--- FAIL: TestCompileGeneration_CandidateCompileFailureDiscardsAcquiredKeepwarm (0.01s)
FAIL
FAIL	github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle	0.066s
FAIL
```

Both tests pass against the fixed tree with no production change required
(the §7.1 fixes already implement the asserted behavior).

#### All-adapter enablement audit

Every `featurehost` adapter plus host bindings was read for outer
disablement, absent-entry, and disabled-entry handling. No adapter ignores
outer disablement; no fix was required, so no Part-C test was added (the
reasoning/secretguard/compaction outer filters are pre-existing and
unchanged by the §7.1 commit, hence not parent-RED-reproducible; mutation-run
RED was out of scope, so the table cites code lines plus the existing tests
that pin each behavior).

| Adapter | Outer `Enabled=false` | Absent entry | Pin |
|---|---|---|---|
| compaction (`compaction.go:28,57`) | skipped in both prerequisite validation and continuity binding | loop no-op, surface unchanged | `TestBindCompactionContinuity_DisabledAndNonMatchingRegistrations` (`compaction_test.go:512`) |
| conversation (`conversation.go`, `process.go:100-145`) | no registration concept: process-owned store, always on; no conversation feature ID exists under `internal/plugins/features` | N/A | N/A by design (infrastructure projection, not an optional feature) |
| interleaved (`generation.go:150-186`) | authoritative; a lone disabled entry also suppresses the legacy `ConfigInterleaved` fallback (`:169,178,186`) | legacy fallback applies, else nil processor | §7.1 matrix (`registration_enabled_test.go:50,109`) plus S1 above |
| keepwarm (`keepwarm.go:66-96`) | authoritative: `featureDisabled && !featureFound` returns nil manager/ports/cleanup (`:85-87`) | decoder defaults apply, i.e. `DefaultConfig` enabled (`:89-92`) | §7.1 matrix (`registration_enabled_test.go:109`) plus H1 ledger tests |
| reasoning (`reasoning/generation.go:33,40`) | `!reg.Enabled` skipped; double-gated on inner `cfg.Compression.Enabled` | no bindings composed | `TestReasoningCompression_BindDisabledNoOp` (`reasoning/compose_test.go:373`, inner-disabled no-op) |
| secretguard (`secretguard/compose.go` via `features/secretguard/runtime_compose.go:56-68`) | `!r.Enabled` filtered (`:59`); a lone disabled entry yields zero matches so `ComposeRuntimeConfig` returns the disabled zero value (`:35-36`) | disabled zero value; host bindings overlay only by presence | `TestSecretGuardCompose_DisabledZeroEnvironmentCalls` (`secretguard/compose_test.go:138`), `TestSecretGuardCompose_EnabledRegistrations` (`:319`), `TestSecretGuardCompose_ValidateRegistrations_RejectsDuplicates` (`:294`) |
| terminalpolicy/sessionpolicy (`sessionpolicy/store.go:223-227`, `process.go:158`, `runtime.go:92-97`) | no registration concept: process-owned store; enablement decided per query from tri-state overrides plus generation default | N/A | N/A by design (policy store infrastructure; per-request enablement) |
| host bindings (`bindings.go:35-77`) | no `Enabled` field exists (`pkg/lipsdk/featurehost/registration.go:29-31`): explicit host construction is presence-based; duplicates and unknown IDs fail (`:49-73`) | empty input yields the zero binding | N/A by design (constructor contract, not config enablement); covered by `bindings_test.go` |

#### Exact featurehost measurement

Authoritative counter `CountNonTestGoLines` (`internal/archtest/budgets.go:149`,
raw lines over recursive non-`_test.go` `.go` files) reports for
`internal/standardplugins/featurehost`: **3057** lines across 24 files.
Budget ceiling 3082 (`budgets.go:76,143`) holds with arithmetic
3082 − 3057 = 25 lines of headroom, the standard ratchet allowance. The two
regression tests added under this section are `_test.go` files and do not
enter the count.
