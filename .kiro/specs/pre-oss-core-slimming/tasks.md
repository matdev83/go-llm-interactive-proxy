# Implementation Plan

> Execute in order unless a task is marked `(P)`. This is a release-bounded migration, not permission for a general core cleanup. Every production change starts with characterization/RED coverage. Preserve current `main` behavior unless the requirements explicitly change the v1 dynamic-plane compatibility contract.

- [ ] 1. Freeze the corrected baseline and characterize the release-bound ownership seams
- [ ] 1.1 Capture the exact post-extension-correction baseline
  - Record the implementation base SHA, current standard plane count/IDs, generated-output check result, current `internal/core` non-test line count, current `internal/infra/runtimebundle` tree line count, and current direct-import census for `internal/plugins/features/*` from core/runtimebundle/standardplugins.
  - Record current package/file inventories for `internal/core/toolcallrepair`, `internal/core/secretguard`, `internal/core/compactiondetect`, `internal/plugins/features/toolcallrepair`, and `internal/plugins/features/secretguard` so mechanical moves cannot silently omit production or tests.
  - Run the existing extension-plane seam benchmarks once and retain raw `ns/op`, `B/op`, `allocs/op` as this spec's performance baseline; do not reuse the older Wave-0 numbers when a newer corrected baseline exists.
  - Done means the implementation agent has one durable baseline table/file in test/evidence scope and can name every production/test path to be moved or retained before editing implementation.
  - _Requirements: 6.1, 7.5, 9.1, 9.2, 10.5_
  - _Boundary: Verification / architecture baseline_
  - _Validation: go run ./scripts/generate-feature-planes.go -check; make arch-report; targeted extension benchmarks_

- [ ] 1.2 (P) Characterize ungenerated-plane fallback behavior and mutation boundaries
  - Add tests using a test-local arbitrary `Plane[[]T]` that currently enters fallback storage; cover contribute, Get, Freeze, Clone, ToContributions, ordinary replay, candidate replay, explicit-empty slice, and fail-before-mutate behavior.
  - Add an adversarial case made by copying a canonical standard plane and changing its ID; the final contract must reject it rather than writing the canonical generated field under a false ID.
  - Keep standard generated planes as positive controls, including ordered, scalar reduce, exclusive identity, typed-nil, and request-materialized examples.
  - Done means tests precisely distinguish the compatibility behavior intentionally removed by Requirement 1 from standard-plane semantics that must survive.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.7_
  - _Boundary: Public Feature SDK_
  - _Validation: go test -count=1 ./pkg/lipsdk/feature ./internal/featurebundle_

- [ ] 1.3 (P) Characterize tool-call-repair ownership and behavior before move
  - Run/capture the complete current `internal/core/toolcallrepair` test/fuzz/benchmark inventory and standard feature factory tests; identify all non-test imports of the core package.
  - Add or strengthen one standard factory integration test proving the YAML config maps to finalizer ID/order/max-args/schema limits/on-unrepairable behavior and both contributed tool planes.
  - Add a disabled/absent-feature control proving no repair finalizer is created.
  - Done means the old implementation location is a replaceable oracle and no behavior depends on package path identity.
  - _Requirements: 2.1, 2.2, 2.5, 2.6_
  - _Boundary: Tool-call Repair Feature_
  - _Validation: go test -count=1 ./internal/core/toolcallrepair ./internal/plugins/features/toolcallrepair ./internal/standardplugins -run 'Tool.*Repair|tool.*repair|Finalizer'_

- [ ] 1.4 (P) Characterize secret-guard source, matcher, audit, and security invariants
  - Pin single-user catalog discovery, include/exclude behavior, known-prefix behavior, min-secret length, matcher options, entry/category inventory, and matcher resolver results.
  - Pin multi-user **zero environment calls** with a panic/counting environment fake; pin disabled **zero environment calls** separately.
  - Pin runtime composition: feature uniqueness, access mode, action, audit policy, observer chaining/default slog observer, catalog inventory, redaction, typed-nil behavior, and failure text/classification currently relied on by tests.
  - Done means package relocation or access-mode type translation cannot weaken a security invariant without a focused RED failure.
  - _Requirements: 3.1, 3.2, 3.3, 3.6, 3.7, 6.2, 9.3_
  - _Boundary: Secret Guard Feature / Composition_
  - _Validation: go test -count=1 ./internal/core/secretguard ./internal/plugins/features/secretguard ./internal/infra/runtimebundle -run 'Secret|secret|Matcher|Catalog'_

- [ ] 1.5 (P) Characterize compaction detector port semantics and lifetime
  - Enumerate the exact detector methods called by core runtime (`RequestOpened`, `PreviewResponse`, `ResponseReleased`) and assert that inputs/outputs can be represented with existing `lipapi` and `pkg/lipsdk/compaction` contracts.
  - Add fake-detector runtime tests for nil/no-op, request-open ordering, pure preview-before-preserver, response-release commit-after-preserver, panic isolation, and exact correlation fields.
  - Prove the current concrete detector has no `Close`, goroutine, external I/O, or generation-owned resource; if this premise is false, stop implementation and repair the spec before moving it.
  - Done means dependency inversion can be mechanical and `ProcessServices` remains the single lifetime owner.
  - _Requirements: 4.1, 4.2, 4.4, 4.5, 6.3, 9.4_
  - _Boundary: Core Runtime Detector Port / Process Ownership_
  - _Validation: go test -count=1 ./internal/core/compactiondetect ./internal/core/runtime -run 'Compaction|compaction'_

- [ ] 2. Close issue #554 on generated standard planes only
- [ ] 2.1 Add the stable unsupported-plane error and generated binding marker
  - Add one errors.Is-compatible SDK sentinel for a plane lacking a canonical generated binding; use the final repository-consistent name everywhere (`ErrUngeneratedPlane` is the design default).
  - Extend the generated access binding with a deterministic unexported manifest marker (ID/ordinal/token) sufficient to reject a wholly arbitrary `Plane[T]` and a copied standard plane whose ID is changed.
  - Validate the marker before nil policy, validation, combine, identity extraction, or any candidate mutation.
  - Do not use reflection, a runtime map of plane IDs, pointer identity, `init()`, or mutable registration to decide support.
  - Done means the RED tests from 1.2 fail closed with contributor/plane attribution and standard generated planes still pass.
  - _Depends: 1.2_
  - _Requirements: 1.1, 1.2, 1.6, 1.7_
  - _Boundary: Public Feature SDK / Generated Binding_
  - _Validation: go test -count=1 ./pkg/lipsdk/feature_

- [ ] 2.2 Remove arbitrary value/identity storage from `ContributionSet`
  - Delete `values map[string]any` and arbitrary-plane identity fallback from production contribution state; retain only generated typed storage and metadata demonstrably required by generated attribution/diagnostics.
  - Delete reflection-based arbitrary value clone/combine logic from the production contribution path; do not replace it with another erased container.
  - Preserve fail-before-mutate by continuing staged/generated clone semantics for every standard combination rule.
  - Preserve explicit-empty vs nil semantics through generated typed storage tests.
  - Done means no valid public contribution can enter an erased value store and all standard-plane contribution tests remain green.
  - _Depends: 2.1_
  - _Requirements: 1.1, 1.2, 1.4, 1.5_
  - _Boundary: Public Feature SDK / Candidate State_
  - _Validation: go test -count=1 ./pkg/lipsdk/feature ./internal/featurebundle_

- [ ] 2.3 Remove arbitrary fallback from `FrozenPlaneSet` and generated output
  - Delete arbitrary values/identity maps and map-backed Get/Clone/ToContributions/Validate/Replay/CandidateReplay branches from `FrozenPlaneSet`.
  - Modify the plane generator/emitter to stop generating map replay/validation/candidate helper functions; regenerate `plane_generated.go` instead of editing it.
  - Ensure ordinary `Get` on an ungenerated plane returns its zero value because no valid set can contain such a value; it must not search dynamic storage.
  - Keep generated request freeze, identity, binder, diagnostics, hook projection, and all-plane replay behavior unchanged.
  - Done means generated code contains no dynamic plane fallback and `-check` is deterministic.
  - _Depends: 2.2_
  - _Requirements: 1.1, 1.3, 1.4, 1.5, 9.1_
  - _Boundary: Public Feature SDK / Generator_
  - _Validation: go run ./scripts/generate-feature-planes.go && go run ./scripts/generate-feature-planes.go -check && go test -count=1 ./pkg/lipsdk/feature ./internal/archtest_

- [ ] 2.4 Tighten closed-plane architecture and public-contract tests
  - Add architecture checks that reject reintroduction of arbitrary plane `map[string]any`/reflection replay storage while allowing unrelated legitimate maps/reflection outside this contract.
  - Convert disposable dynamic-plane tests that were only scaffolding for consolidation into declaration-validation or generated-fixture tests; do not weaken real standard-plane coverage.
  - Add external-package tests using `errors.Is(err, ErrUngeneratedPlane)` and confirm the `FeatureBundle` schema version/standard plane IDs did not change.
  - Done means #554's selected policy is executable and cannot silently regress.
  - _Depends: 2.3_
  - _Requirements: 1.2, 1.6, 1.7, 7.4, 8.3_
  - _Boundary: SDK Contract / Architecture Gates_
  - _Validation: go test -count=1 ./pkg/lipsdk/feature ./internal/archtest_

- [ ] 3. Move tool-call repair implementation into the feature
- [ ] 3.1 Move the deterministic repair engine under `internal/plugins/features/toolcallrepair/repair`
  - Move the complete production implementation from `internal/core/toolcallrepair` (engine, schema/compiler/cache, catalog index, JSON completion/tail repair, finalizer, diagnostics/reason codes/helpers) into the feature-local `repair` subpackage.
  - Move the corresponding unit, fuzz, benchmark, contract, and regression tests with the implementation; preserve test names/assertions where practical so coverage is visibly continuous.
  - Update imports only; do not rewrite algorithms, reason codes, cache behavior, bounds, or concurrency semantics during the move.
  - Enforce that the new `repair` package imports only standard library, `pkg/lipapi`, `pkg/lipsdk/*`, and feature-local packages.
  - Done means the complete old oracle suite passes at the new location before the old core package is deleted.
  - _Depends: 1.3, 2.4_
  - _Requirements: 2.1, 2.4, 2.6_
  - _Boundary: Tool-call Repair Feature_
  - _Validation: go test -count=1 ./internal/plugins/features/toolcallrepair/...; targeted fuzz/bench smoke_

- [ ] 3.2 Make the feature root construct its complete bundle
  - Add `toolcallrepair.FeatureBundle(cfg)` (or the repository-consistent equivalent) that translates YAML-decoded feature config into the feature-local finalizer policy and contributes both `PlaneToolCallFinalizers` and `PlaneToolCallFinalizationMaxArgsBytes`.
  - Preserve finalizer ID, order, default/max-args equality, schema limits, and on-unrepairable mapping exactly.
  - Simplify `internal/standardplugins/features_install.go` to decode config and delegate to the feature bundle constructor; remove the core repair import and duplicate policy mapping.
  - Done means standardplugins is distribution wiring only and the feature package owns implementation construction.
  - _Depends: 3.1_
  - _Requirements: 2.1, 2.2, 2.5_
  - _Boundary: Tool-call Repair Feature / Standard Distribution_
  - _Validation: go test -count=1 ./internal/plugins/features/toolcallrepair/... ./internal/standardplugins -run 'Tool.*Repair|tool.*repair|Finalizer'_

- [ ] 3.3 Delete the old core package and ratchet its absence
  - Delete `internal/core/toolcallrepair` only after all production/test imports have migrated.
  - Add a permanent architecture rule rejecting production resurrection of that package and a recursive feature-tree import boundary for `internal/plugins/features/toolcallrepair`.
  - Update docs/package maps that still identify tool repair as core-owned.
  - Done means repository search has no production import/path for the retired core package and full focused integration remains green.
  - _Depends: 3.2_
  - _Requirements: 2.3, 2.4, 7.1, 7.3_
  - _Boundary: Architecture Ownership_
  - _Validation: go test -count=1 ./internal/archtest ./internal/core/runtime ./internal/plugins/features/toolcallrepair/... ./internal/standardplugins_

- [ ] 4. Move secret-guard matching/source implementation into the feature
- [ ] 4.1 Introduce feature-local source/matcher engine contracts without core imports
  - Create `internal/plugins/features/secretguard/engine` and move the concrete catalog, Aho-Corasick matcher, known-prefix, environment inventory, matcher resolver, and source-policy implementation from `internal/core/secretguard`.
  - Replace the `internal/core/accessmode.Mode` dependency with a closed feature-local mode value; preserve single-user/multi-user semantics exactly.
  - Keep the environment reader as a construction-time port inside the feature implementation/compose boundary; never expose it through request handler services or public SDK.
  - Move all implementation tests/benchmarks first and make them green in the new package before deleting old sources.
  - Done means feature-owned code has zero `internal/core` import while all source/matcher RED tests from 1.4 pass.
  - _Depends: 1.4, 2.4_
  - _Requirements: 3.1, 3.2, 3.3, 3.5, 3.7_
  - _Boundary: Secret Guard Feature Engine_
  - _Validation: go test -count=1 ./internal/plugins/features/secretguard/..._

- [ ] 4.2 Create explicit `internal/infra/secretguardcompose` assembly
  - Move feature-specific runtime composition out of `runtimebundle/secret_guard_runtime.go` into a dedicated typed adapter.
  - Define composition-neutral adapter input/override types in `secretguardcompose` so `runtimebundle` does **not** import `secretguard/engine`; translate effective core access mode, host single-user overrides, environment, frozen guards, observer, and logger inside the adapter.
  - Move/alias internal `SecretGuardInputs`/environment option types from runtimebundle to the adapter as needed to preserve internal call-site compatibility without importing the concrete feature.
  - Preserve audit observer chaining/default slog observer, failure policy, inventory extras, action/access-mode/version, and matcher resolver exactly.
  - Do not pass `BuildOptions`, `ProcessServices`, full config, registry, or arbitrary dependency maps into the adapter.
  - Done means the adapter owns concrete feature assembly and runtimebundle only supplies explicit generic inputs/consumes output.
  - _Depends: 4.1_
  - _Requirements: 3.1, 3.2, 3.3, 3.6, 3.7, 5.1, 5.4, 5.6_
  - _Boundary: Secret Guard Composition Adapter_
  - _Validation: go test -count=1 ./internal/infra/secretguardcompose ./internal/infra/runtimebundle ./internal/plugins/features/secretguard/... -run 'Secret|secret|Matcher|Catalog'_

- [ ] 4.3 Delete the old core package and preserve security ratchets
  - Delete `internal/core/secretguard` after production options/tests have migrated to adapter/feature-owned types.
  - Extend the existing feature import-boundary test to cover the full recursive secretguard feature tree, including the new engine subpackage.
  - Add architecture absence/import rules and update steering/docs that claim catalog/matcher construction is core-owned.
  - Done means multi-user and disabled no-environment-read tests, redaction/audit tests, runtime composition tests, and architecture gates all pass without the old package.
  - _Depends: 4.2_
  - _Requirements: 3.2, 3.3, 3.4, 3.5, 3.6, 7.1, 7.3, 9.3_
  - _Boundary: Secret Guard Ownership / Security_
  - _Validation: go test -count=1 ./internal/plugins/features/secretguard/... ./internal/infra/secretguardcompose ./internal/infra/runtimebundle ./internal/core/runtime ./internal/archtest_

- [ ] 5. Invert and relocate the concrete compaction detector
- [ ] 5.1 Define the smallest core runtime consumer port
  - Add one repository-internal detector interface at the core runtime consumer boundary with exactly the three operations characterized in 1.5; use existing `lipapi.Call`, `lipapi.Event`, `compaction.PreservationMeta`, `compaction.Event`, and `compaction.ResponsePreview` types.
  - Change `runtime.CompactionRuntime.Detector`, response-pipeline detector storage, and safe panic wrappers to the interface; preserve nil behavior.
  - Convert request/response correlation construction from concrete detector `RequestMeta`/`ResponseMeta` types to existing `compaction.PreservationMeta` fields without changing values.
  - Do not add the detector interface to public `pkg/lipsdk`, add generic observer registries, or move exact observation-stage timing out of core.
  - Done means runtime tests pass with fakes and core runtime no longer needs concrete detector types.
  - _Depends: 1.5, 2.4_
  - _Requirements: 4.1, 4.2, 4.3, 4.5, 6.3_
  - _Boundary: Core Runtime Consumer Port_
  - _Validation: go test -count=1 ./internal/core/runtime -run 'Compaction|compaction'_

- [ ] 5.2 Move detector implementation to `internal/infra/compactiondetect`
  - Move detector state, heuristic/rule recognition, fingerprint/content-free helpers, preview logic, and tests/benchmarks from `internal/core/compactiondetect` to the infra implementation package.
  - Adapt public method metadata parameters to the consumer port using `compaction.PreservationMeta` directly or a private translation inside the implementation; do not export a second correlation contract.
  - Preserve bounds, lock scope, lazy sweep, no-background-worker behavior, panic safety at caller, and emitted event bytes/fields.
  - Done means the complete detector suite passes in the new package and it satisfies the runtime interface at compile time.
  - _Depends: 5.1_
  - _Requirements: 4.1, 4.4, 4.7, 9.4_
  - _Boundary: Compaction Detector Implementation_
  - _Validation: go test -count=1 ./internal/infra/compactiondetect ./internal/core/runtime_

- [ ] 5.3 Preserve process ownership and delete the old detector package
  - Update `runtimebundle` process-service construction to instantiate the new infra detector and store it through the runtime consumer interface; retain one process-owned instance shared by generations.
  - Do not register a detector closer or create a generation copy unless Task 1.5 disproved the no-owned-resource premise and the spec was repaired first.
  - Delete `internal/core/compactiondetect`, add architecture absence/import ratchets, and update package maps/docs.
  - Run exact Linux race coverage over the new detector and runtime integration.
  - Done means process/generation ownership is unchanged and no concrete compaction heuristic remains in core.
  - _Depends: 5.2_
  - _Requirements: 4.3, 4.4, 4.5, 4.6, 7.3, 9.4_
  - _Boundary: ProcessServices / Architecture Ownership_
  - _Validation: go test -count=1 ./internal/infra/runtimebundle ./internal/core/runtime ./internal/infra/compactiondetect ./internal/archtest; Linux go test -count=1 -race ./internal/infra/compactiondetect ./internal/core/runtime ./internal/infra/runtimebundle_

- [ ] 6. Remove direct concrete-feature knowledge from generic runtimebundle
- [ ] 6.1 Move reasoning-compression options and generation binding to `internal/infra/reasoningcompose`
  - Move the concrete reasoning-preservation config scan, prerequisite validation, egress policy lookup/selection, matcher/sanitizer requirement, service construction, bundle reconstruction, attempt-transform binder, and stream-observer binder out of runtimebundle.
  - Move `ReasoningCompressionOptions` to the adapter; preserve a type alias/translation at runtimebundle only if needed for internal/public `pkg/lipruntime` source compatibility, but the runtimebundle alias/file must not import the concrete feature package.
  - Define explicit adapter inputs for registrations, already-resolved BackgroundClient/Poller, trusted option set, and candidate surface. The adapter may import `reasoningpreservation`, `featurebundle`, and `standardplugins` companion policy as required.
  - Keep production/testing option precedence in runtimebundle; do not pass `ProcessServices`/`BuildOptions` into the adapter.
  - Done means all existing reasoning compression characterization/integration/typed-nil/rollback tests pass through the adapter and runtimebundle has no direct reasoning feature import.
  - _Depends: 2.4_
  - _Requirements: 5.1, 5.2, 5.3, 5.6, 5.7, 6.2_
  - _Boundary: Reasoning Composition Adapter_
  - _Validation: go test -count=1 ./internal/infra/reasoningcompose ./internal/infra/runtimebundle ./internal/plugins/features/reasoningpreservation ./pkg/lipruntime -run 'Reasoning|reasoning|Compression|compression'_

- [ ] 6.2 Converge runtimebundle secret-guard delegation onto `secretguardcompose`
  - Remove any residual feature/config/engine-specific helper from `runtimebundle` after Task 4.2; retain only extraction of effective config/access mode/host options/frozen planes and one adapter call.
  - Ensure runtimebundle does not reconstruct secret-guard config-to-engine policy, matcher settings, or audit chaining after the move.
  - Preserve candidate overlay/reload behavior and generic `ExtensionsOptions` shape through adapter-owned aliases/types as needed.
  - Done means no secret-guard concrete implementation import or duplicated policy remains in runtimebundle.
  - _Depends: 4.2_
  - _Requirements: 5.1, 5.2, 5.4, 5.6, 6.1, 6.2_
  - _Boundary: Generic Runtime Composition_
  - _Validation: go test -count=1 ./internal/infra/secretguardcompose ./internal/infra/runtimebundle_

- [ ] 6.3 Ratchet runtimebundle to zero concrete-feature imports
  - Add a permanent architecture rule scanning production `internal/infra/runtimebundle` imports and failing on `internal/plugins/features/*`.
  - Verify `compactioncompose` remains a dedicated adapter and do not refactor it for symmetry unless a mechanical type import changed in Task 5.
  - Search runtimebundle for feature IDs/names and classify any remaining occurrence: generic config/diagnostic string may remain only with documented reason; concrete implementation branching must move to an adapter or be reported as a blocker.
  - Done means the production import rule is zero and no feature-specific exception whitelist exists in runtimebundle.
  - _Depends: 6.1, 6.2_
  - _Requirements: 5.2, 5.5, 5.7, 7.2, 7.8_
  - _Boundary: Architecture Gates / Runtime Composition_
  - _Validation: go test -count=1 ./internal/archtest ./internal/infra/runtimebundle_

- [ ] 7. Certify OSS authoring and permanent simplification ratchets
- [ ] 7.1 Add recursive core/feature ownership architecture rules
  - Reuse existing import-rule/source-scan infrastructure to enforce core -> no concrete features, runtimebundle -> no concrete features, and the three retired core package absences.
  - Add recursive feature-tree checks for toolcallrepair and secretguard rather than checking only the root package's direct imports.
  - Add adversarial self-tests proving renamed/nested files or subpackages cannot trivially bypass the rule, without building a large semantic analyzer.
  - Done means each forbidden example fails and valid standardplugins/dedicated compose adapter imports pass.
  - _Depends: 3.3, 4.3, 5.3, 6.3_
  - _Requirements: 7.1, 7.2, 7.3, 7.7, 7.8_
  - _Boundary: Architecture Gates_
  - _Validation: go test -count=1 ./internal/archtest_

- [ ] 7.2 Reset core/runtimebundle budgets downward and prove change-surface ROI
  - Re-measure final non-test `internal/core` and `internal/infra/runtimebundle` trees after migrations.
  - Set the core budget to measured final + 25 lines; do not retain deleted feature LOC as headroom. If runtimebundle shrank, ratchet its package tree to final + 25; it may not receive a budget increase solely because logic moved behind adapters.
  - Run a disposable existing-standard-plane feature probe from the post-migration tree: feature code + standard registration/test maintenance only, zero core/runtimebundle production edits. Record exact changed paths and remove the probe.
  - Done means simplification has deterministic structural evidence rather than a subjective "cleaner" claim.
  - _Depends: 7.1_
  - _Requirements: 7.5, 7.6, 9.1, 10.5_
  - _Boundary: Architecture Budgets / ROI Evidence_
  - _Validation: make arch-report; go test -count=1 ./internal/archtest/tools/changesurface/... ./internal/archtest_

- [ ] 7.3 Add an external-style OSS feature SDK fixture
  - Add a separate module under the repository's established `testdata` external-module pattern (or extend the existing external-module fixture if it is already the canonical location) that imports only exported `pkg/lipsdk`/`pkg/lipapi` contracts from the root module.
  - Implement a tiny feature using one ordered standard plane through `NewContributionSet` -> `Contribute` -> `Freeze` -> `BundleFromPlanes`, and test the resulting bundle/plane value.
  - In the same external consumer, construct an arbitrary ungenerated plane and assert contribution fails with `errors.Is(..., ErrUngeneratedPlane)`; do not import `internal` packages to prove it.
  - Wire the fixture into an existing contract/quality gate appropriate for external public source compatibility; do not add a slow independent CI matrix if a current fixture runner exists.
  - Done means the OSS feature contract is executable outside repository internals.
  - _Depends: 2.4_
  - _Requirements: 8.1, 8.2, 8.3_
  - _Boundary: Public SDK / External Consumer TCK_
  - _Validation: GOWORK=off go test ./... from the external fixture; make quality-checks_

- [ ] 7.4 Reconcile feature authoring and architecture documentation
  - Update `pkg/lipsdk/feature` godoc, `docs/extension-platform-authoring.md`, `docs/plugin-authoring.md`, `internal/plugins/features/README.md`, architecture/steering package maps, and any direct references affected by moved packages.
  - Remove stale statements that features add named `FeatureBundle` fields/slices; document the frozen PlaneSet lifecycle and closed standard manifest.
  - State the standard distribution boundary precisely: feature-owned bundle constructor/factory behavior plus explicit standard registration; no feature-specific core/runtimebundle branch.
  - State that a new plane requires an upstream manifest/platform change; do not imply arbitrary dynamic planes are supported in v1.
  - Done means docs match the external fixture and current code, not historical Stage-4 bundle shapes.
  - _Depends: 3.3, 4.3, 5.3, 6.3, 7.3_
  - _Requirements: 1.8, 8.4, 8.5_
  - _Boundary: SDK / Documentation_
  - _Validation: make docs-check; go test -count=1 ./pkg/lipsdk/feature_

- [ ] 8. Prove release-safe behavior and hand off full closure
- [ ] 8.1 Run migrated-feature and generation/reload regression gates
  - Run focused SDK/featurebundle/toolrepair/secretguard/compaction/reasoningcompose/runtimebundle/core-runtime suites from a clean tree.
  - Run feature enable/disable/removal reload tests proving old requests stay pinned and new requests receive the new/no-feature surface.
  - Re-run terminal/failover/output-commit focused tests affected by compaction observation seam changes; no routing/B2BUA/billing semantics may change.
  - Done means all named migrations are behavior-preserving except the intentional ungenerated-plane rejection.
  - _Depends: 7.4_
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 9.3_
  - _Boundary: Runtime / Generation Verification_
  - _Validation: targeted package suites plus make test_

- [ ] 8.2 Refresh hot-path allocation and Linux race evidence
  - Re-run the complete corrected extension-plane seam benchmark suite and compare `allocs/op`, `B/op`, and fixed-cost `ns/op` to Task 1.1; allocation regression is blocking.
  - Inspect generated/request execution paths for new reflection, arbitrary map lookup, locks, or per-request goroutines.
  - Run Linux race detector over `./internal/infra/compactiondetect ./internal/core/runtime ./internal/core/extensions ./internal/infra/runtimebundle` and the migrated secretguard package if concurrent matcher behavior warrants it.
  - Record exact commit/run evidence and explicitly state that this is not #394 load/HOLD certification.
  - Done means request-path structural/allocation guarantees and detector concurrency remain sound.
  - _Depends: 8.1_
  - _Requirements: 9.1, 9.2, 9.3, 9.4_
  - _Boundary: Performance / Concurrency Verification_
  - _Validation: targeted -benchmem; Linux go test -count=1 -race ..._

- [ ] 8.3 Produce the residual ownership inventory for the full-closure SDD
  - Scan remaining `internal/core`, `pkg/lipruntime`, `internal/infra/*compose`, and standard composition for feature-adjacent responsibilities after this migration.
  - Classify each finding as kernel invariant, generic extension mechanism, concrete optional feature policy, feature-specific infrastructure/composition, or mixed/needs split; include current owner, consumers, and why it was not moved in this pre-OSS spec.
  - Include at minimum compaction-continuity coordination, conversation-view optional steering policy, interleaved-thinking/state, terminal-decision policy, feature-specific public host options/adapters, and the dedicated compose adapters created/retained here.
  - Record this as durable closeout research/evidence for the **second full-closure Kiro SDD**; do not implement the findings in this task.
  - Done means the later spec can be generated without redoing basic ownership discovery and no deferred item exists only in chat history.
  - _Depends: 8.1_
  - _Requirements: 10.1, 10.2, 10.3, 10.4_
  - _Boundary: Full-closure Handoff_

- [ ] 8.4 Run final repository gates, independent review, and merged-main certification
  - Run generated-plane check, `make quality-checks`, `make test`, `make qa`, deterministic `make arch-report`, docs checks, external fixture, and `go run ./cmd/lipstd --help` on the final implementation commit.
  - Obtain an independent architecture/code review focused on closed-plane compatibility, feature ownership, secret security, detector lifetime/dependency direction, runtimebundle import boundary, immutable generations, and accidental scope expansion.
  - Merge only after required CI succeeds; then create a clean worktree from resulting `origin/main` and rerun the final verification chain appropriate to changed surfaces.
  - Archive this spec only after merged-main verification; retain #394 as independent performance work and reference the residual inventory as input to the full-closure SDD.
  - Done means the pre-OSS simplification is release-certified with no must-fix finding and no hidden follow-up prerequisite.
  - _Depends: 8.2, 8.3_
  - _Requirements: 9.5, 10.4, 10.5_
  - _Boundary: Release Verification / Delivery_
  - _Validation: go run ./scripts/generate-feature-planes.go -check && make quality-checks && make test && make qa && make arch-report && make docs-check && go run ./cmd/lipstd --help_

## Execution Notes for Smaller Agents

- Do not redesign while executing. If a current-main fact contradicts this plan materially, stop that subtask, record the contradiction, and repair the active Kiro artifacts before continuing.
- Do not move additional core packages opportunistically. Requirement 10 is a hard scope boundary.
- Do not introduce an abstraction to make the three migrations look uniform. Reuse existing generated planes and explicit composition patterns.
- For package moves, establish RED/characterization coverage first, move implementation + tests, make the new location green, then delete the old location. Never delete the oracle first.
- `plane_generated.go` is generator output. Modify the generator/emitter, regenerate, and use `-check`.
- Keep each PR/checkpoint within the 100-Go-file gate. Split by the numbered migration groups above rather than using a blanket override.
- Preserve exact process/generation owners: `ProcessServices`, `ResourceLedger`, `runtimehost.Manager`, request/attempt owners. This spec adds no new lifetime manager.
- Treat the first client-visible content event as irreversible; no part of this cleanup changes retry/failover semantics.
