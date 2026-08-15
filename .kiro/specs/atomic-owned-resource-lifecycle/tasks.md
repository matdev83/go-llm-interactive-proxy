# Implementation Plan

Implementation is TDD-first. The plan deliberately limits work to the two validated high-ROI ownership seams and finishes with a simplification gate.

## 1. Freeze Ownership Behavior With RED Tests

- [ ] 1.1 Define RED process-owner contract tests
  - Pin successful acquire-plus-release registration before the acquired value can be used by the next construction step.
  - Pin reverse-order rollback after a later injected constructor failure.
  - Pin aggregate cleanup errors while all remaining releases are still attempted.
  - Pin normal `ProcessServices` shutdown idempotency and no double release after successful construction.
  - _Requirements: 2.1-2.7, 6.1_
  - _Boundary: runtime composition process lifetime only_
  - _Depends: none_
  - _Validation: focused runtimebundle ownership/process-services tests_

- [ ] 1.2 (P) Characterize selected process-builder cleanup ordering
  - Record the current resource close order for the control-plane, authority, persistence, accounting, metering, and terminal-work construction graph.
  - Add fault-injection cases after representative successful acquisitions and before later fallible steps.
  - Prove plugin host/artifact/staging and database pool special ordering remains unchanged.
  - Prove current cleanup error aggregation continues across failures.
  - _Requirements: 3.1-3.6, 5.5, 6.1_
  - _Boundary: ProcessServices construction graph_
  - _Depends: none_
  - _Validation: focused constructor failure/ownership lifecycle tests_

- [ ] 1.3 (P) Define RED generation-loop lifetime tests
  - Prove a newly created composition-owned loop performs no application work before lifecycle ownership is established.
  - Prove rollback and quiesce both cancel and join the loop before lifecycle completion.
  - Prove already-closing/already-quiesced ownership cannot leak or deadlock a new loop.
  - Run leak/race-focused cases with repeated concurrent retirement.
  - _Requirements: 4.1-4.7, 6.2-6.3_
  - _Boundary: generation composition lifecycle only_
  - _Depends: none_
  - _Validation: focused runtimebundle race/goleak lifecycle tests_

## 2. Add the Minimal Private Ownership Primitives

- [ ] 2.1 Implement the private process resource owner and acquire helper
  - Add an append-only process release owner that unwinds through the existing reverse-disposal/error semantics.
  - Add one acquire-plus-release helper that registers ownership before returning a successful value.
  - Keep `ProcessServices.Close` / host shutdown as the only process shutdown coordinator.
  - Expose no lookup, provisioning, lazy service, reflection, global registry, or public API.
  - _Requirements: 1.1-1.6, 2.1-2.7_
  - _Boundary: runtimebundle private composition infrastructure_
  - _Depends: 1.1_
  - _Design: Process Resource Owner; Process Acquisition Helper_

- [ ] 2.2 (P) Implement the narrow ledger-backed cancel+join loop helper
  - Create the loop in a blocked state, establish its existing `ResourceLedger` cancel+join ownership, then release application work.
  - Preserve the caller-selected existing cleanup phase and ledger error semantics.
  - Handle immediate cleanup/closing ownership without starting unowned work.
  - Add no scheduler, worker pool, restart policy, supervision tree, or generic async error channel.
  - _Requirements: 1.2-1.6, 4.1-4.6, 5.1_
  - _Boundary: runtimebundle generation-owned background loops_
  - _Depends: 1.3_
  - _Design: Generation Loop Helper_

## 3. Migrate Only the Proven High-ROI Call Sites

- [ ] 3.1 Migrate selected process builders from closer propagation to local ownership
  - Change the selected process-owned builders to accept the private process owner and register acquired releases before later fallible construction.
  - Return runtime values/errors without caller-visible closer lists for the migrated paths.
  - Remove the corresponding `NewProcessServices` closer aggregation/adaptation plumbing as each path moves.
  - Leave special pool and plugin staging/artifact teardown explicit where that preserves clearer ordering.
  - _Requirements: 3.1-3.7, 5.5-5.6, 6.5-6.7_
  - _Boundary: ProcessServices and selected process-owned builder families_
  - _Depends: 1.2, 2.1_
  - _Design: Migrated Process Builders; Migration Plan_

- [ ] 3.2 Migrate the model-registry refresh loop to structured generation ownership
  - Replace the manual derived-context/cancel/wait-group lifecycle with the private loop helper.
  - Preserve refresh cadence, model-registry behavior, generation phase, and shutdown ordering exactly.
  - Prove candidate rollback and superseded-generation quiesce both terminate the refresh loop.
  - Do not migrate unrelated goroutines unless characterization proves the identical cancel+join lifetime shape.
  - _Requirements: 4.1-4.7, 5.1, 5.4-5.5, 6.2-6.3_
  - _Boundary: generation-owned model-registry refresh lifecycle_
  - _Depends: 2.2_
  - _Design: Generation-Owned Loop; Initial migration_

- [ ] 3.3 Lock unchanged backend and generation semantics
  - Retain characterization tests proving backend cleanup remains generation-owned and transfers to the existing ledger.
  - Prove backend plugin factory/ABI contracts and connector process supervision did not change.
  - Prove generation publication, pinning, retention, drain, and retirement behavior remains unchanged.
  - Prove request-path routing/stream/accounting/session outputs are unaffected by the composition refactor.
  - _Requirements: 5.1-5.6_
  - _Boundary: unchanged lifecycle contracts; regression-only_
  - _Depends: 3.1, 3.2_
  - _Validation: existing backend/reload/generation/runtime regression suites_

## 4. Ratchet Scope and Prove the Refactor Pays for Itself

- [ ] 4.1 Add architecture/source-shape gates against ownership abstraction creep
  - Fail if the new ownership primitive becomes exported or moves outside runtime composition.
  - Fail if it grows keyed lookup or service-locator concepts such as `Get`, `Resolve`, or `Provide`.
  - Fail if the selected migrated process builders reintroduce caller-visible closer-list ownership.
  - Confirm no public config/API/plugin/canonical contract or external dependency was added.
  - _Requirements: 1.3-1.6, 2.5-2.6, 3.5, 5.2, 6.4-6.5_
  - _Boundary: architecture tests and runtime composition_
  - _Depends: 3.1_
  - _Validation: focused archtest/source-shape tests_

- [ ] 4.2 Run lifecycle safety, race, leak, and repository quality gates
  - Run focused process rollback/close and generation-loop tests including fault injection.
  - Run targeted race/leak coverage for repeated generation quiesce/retirement and loop teardown.
  - Run repository quality/architecture checks and default unit tests.
  - Compare existing generation acquire/dispatch/request benchmarks or equivalent hot-path baselines and confirm no material regression.
  - _Requirements: 6.1-6.5, 6.8-6.9_
  - _Boundary: verification only_
  - _Depends: 3.1, 3.2, 3.3, 4.1_
  - _Validation: `make quality-checks`; `make test-unit`; targeted `go test -race`; existing lifecycle benchmarks_

- [ ] 4.3 Perform final simplification review
  - Delete superseded caller-side closer aggregation, adapters, and duplicated lifecycle wrappers made unnecessary by the migration.
  - Verify acquisition-to-release ownership is easier to trace in every migrated path than in the baseline.
  - Revert or simplify any migrated call site that adds more lifecycle concepts than it removes.
  - Confirm the final diff remains limited to the frozen high-ROI composition ownership scope.
  - _Requirements: 1.6, 3.4-3.7, 6.6-6.7_
  - _Boundary: final runtimebundle refactor scope_
  - _Depends: 4.2_
  - _Validation: final diff/architecture review_
