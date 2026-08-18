# Implementation Plan

## Phase 1 — Freeze the Canonical Identity and Registry Contracts

- [ ] 1. Implement replay-stable whole-message identity with RED-first contract tests
  - Add failing tests for legacy `lipapi.Message` vs `ItemKindMessage` equivalence, role sensitivity, CRLF normalization, metadata/ID/status/phase exclusion, multipart ordering, and deterministic JSON normalization.
  - Add failing tests rejecting non-message items and proving duplicate semantic messages intentionally share one identity.
  - Implement the typed v1 semantic projection + SHA-256 identity in `internal/core/nonforwardable` without provider/frontend imports.
  - Add frontend round-trip fixtures for representative canonical message forms so decoded replay produces the same identity.
  - _Boundary:_ `internal/core/nonforwardable` owns identity; `pkg/lipapi` remains unchanged and supplies canonical values only.
  - _Depends:_ none.
  - _Validation:_ focused identity tests pass; no new wire/canonical fields; package import boundary check passes.
  - _Requirements:_ 1.1-1.10, 9.7-9.8, 11.1, 11.7

- [ ] 2. Define the focused A-leg registry ports and RED common store contract
  - Add core `Identity`, `Tag`, `Snapshot`, `Reader`, `Tagger`, `Store`, capacity/reason validation, and typed failure contracts.
  - Write one reusable failing semantic suite covering empty snapshot, tag/snapshot, idempotency, atomic batch, 4096-cap overflow, reason bounds, unknown A-leg, and delete/recreate behavior.
  - Prove the base `b2bua.Store` and public continuity interfaces remain unchanged with drift/compile fixtures.
  - Add request-local guard/registrar tests proving a successful registration becomes visible immediately only after store commit.
  - _Boundary:_ focused optional continuity capability; no persistence implementation yet.
  - _Depends:_ 1.
  - _Validation:_ RED suite compiles/fails for missing adapters; public/base Store drift tests remain green.
  - _Requirements:_ 2.1-2.4, 2.10-2.12, 3.5, 10.5, 11.1-11.2

- [ ] 3. Implement MemoryStore non-forwardable persistence
  - Add per-A-leg bounded tag state to `b2bua.MemoryStore` under the existing lock/liveness/eviction lifecycle.
  - Implement atomic/idempotent Tag and bounded Snapshot semantics and satisfy the common store suite.
  - Add deterministic deletion/recreation and concurrent Tag/Snapshot barrier tests, including targeted `-race` coverage.
  - Verify A-leg eviction/deletion removes tag state and no newly recreated A-leg inherits it.
  - _Boundary:_ `internal/core/b2bua` is a driven adapter implementing the optional core contract; base Store remains unchanged.
  - _Depends:_ 2.
  - _Validation:_ common memory-store contract and race tests pass.
  - _Requirements:_ 2.3-2.5, 2.9-2.11, 10.4-10.5, 11.2, 11.8

- [ ] 4. Implement Bun SQLite/PostgreSQL durable tag storage
  - Add a forward-only migration for an A-leg-owned non-forwardable tag table with identity version/digest primary key, bounded reason, timestamp, and cascade cleanup.
  - Implement transactional Tag with A-leg validation/locking, unique-new capacity calculation, all-or-nothing insert, and normal A-leg liveness semantics.
  - Implement bounded Snapshot and satisfy the common store contract for SQLite and PostgreSQL.
  - Add restart/shared-store visibility and delete/recreate concurrency tests proving no orphan/inherited tags.
  - _Boundary:_ `internal/core/continuity/bunstore` owns SQL/schema; core package stays persistence-agnostic.
  - _Depends:_ 2.
  - _Validation:_ SQLite + PostgreSQL focused suites pass, including restart and transactional-capacity cases.
  - _Requirements:_ 2.6-2.11, 8.6, 10.2, 11.2, 11.8

## Phase 2 — Build the Pure Backend Projection and Enforcement Core

- [ ] 5. Implement the RED-first whole-message projector
  - Add failing tests for legacy Instructions/Messages removal, item-message removal, retained-order/field preservation, no input mutation, and multiple tagged identities.
  - Add failing item-authority tests proving in-call references to removed message IDs are removed while unrelated items/references are retained.
  - Define and test stable no-forwardable-content / invalid-dependency failure behavior so old assistant/system history is never sent as an unintended continuation after the driving local tail is removed.
  - Implement pure clone/filter/reference-cleanup/`Call.Validate` projection using semantic identities and an immutable tag set.
  - _Boundary:_ `internal/core/nonforwardable` only; no Executor/store/provider behavior in the projector.
  - _Depends:_ 1-2.
  - _Validation:_ projector property/table tests pass for legacy and item authority.
  - _Requirements:_ 4.3-4.8, 8.7, 10.10, 11.1

- [ ] 6. Implement the request-local enforcement guard and trusted registrar
  - Add `pkg/lipsdk/nonforwardable` producer-facing Reason/Registrar contracts with whole-message-only documentation and no query/remove/client authority.
  - Implement the runtime-bound registrar over authoritative A-leg Store + request-local tag set, with TagMessage/TagItem validation and commit-before-local-view update.
  - Add tests for store errors, unsupported items, capacity failure, current-turn additions, and absence of plaintext in persistence/diagnostic values.
  - Add package/API fixtures proving the contract imports canonical types only and exposes no raw Store/ALeg mutation surface to clients.
  - _Boundary:_ SDK exposes trusted producer capability; core implementation remains server/A-leg authoritative.
  - _Depends:_ 1-4.
  - _Validation:_ registrar/security/API tests pass.
  - _Requirements:_ 1.7-1.9, 3.1-3.6, 9.3-9.8, 10.6

## Phase 3 — Add the Generic Local-Turn Extension Platform

- [ ] 7. Freeze and implement the two-phase `localturn` SDK contract
  - Write RED SDK tests for deterministic Handler ordering, Match/claimed index shape, bounded reason/reply text, and first-claim ownership.
  - Add `pkg/lipsdk/localturn` Handler/Match/Reply/Meta/Services contracts with `Match` and `Handle` separated and documented post-claim fail-closed semantics.
  - Add optional `LocalTurnHandlers` to FeatureBundle schema-v1 validation/empty logic, merge/sort machinery, request runtime snapshot accessors, and public-surface/drift fixtures.
  - Update feature-bundle tests proving additive v1 compatibility, nil rejection, deterministic merged ordering, and no contribution behavior.
  - _Boundary:_ public plugin contract only; no command grammar, command state, or runtime side effects.
  - _Depends:_ 6.
  - _Validation:_ SDK/FeatureBundle contract tests pass; schema remains v1.
  - _Requirements:_ 6.1, 6.3-6.9, 10.7, 11.1, 11.6

- [ ] 8. Implement the safe local-turn runner and canonical local reply stream
  - Write RED extension-runner tests for pass, pre-claim fail-open/fail-closed panic/error isolation, first claim, invalid claimed indexes, and no later handler execution after claim.
  - Implement runner plumbing that receives a defensive pristine ingress clone and returns a typed claim without embedding persistence or command semantics in the generic extension runner.
  - Write RED/then GREEN local-stream tests for canonical response/message/text/finished order, no UsageDelta/backend identity, cancellation, Close, and zero-goroutine finite behavior.
  - Add a runtime helper that derives the exact assistant message used for reply identity from `Reply.Text`, so tag and emitted text cannot drift.
  - _Boundary:_ `internal/core/extensions` owns safe handler invocation; runtime/local stream owns output construction; handlers never return arbitrary EventStreams.
  - _Depends:_ 7.
  - _Validation:_ runner/local-stream unit tests pass; no provider/frontend imports.
  - _Requirements:_ 6.4-6.9, 7.1-7.6, 11.6

## Phase 4 — Wire the Two Runtime Enforcement Boundaries

- [ ] 9. Integrate local-turn claim/tag/handle into secure request preparation
  - Add RED runtime tests proving local-turn matching sees a pristine canonical ingress view after secure A-leg/secret/submit acceptance and before backend-oriented transforms.
  - Refactor preparation result minimally to express `backend_prepared` versus `local_handled` without duplicating Executor/frontends; preserve client session/resume/CTP behavior on both outcomes.
  - On claim, validate normalized source indexes, commit source tags before `Handle`, commit reply tag before returning local stream, and make all post-claim failures non-fallback failures.
  - Prove local handled turns create no keepwarm real-turn work, billing call/credit authorization, route plan, B-leg, PTB, provider call, or provider usage; release any pre-acquired request authority deterministically.
  - _Boundary:_ `internal/core/runtime` owns orchestration/order; local handlers stay application plugins.
  - _Depends:_ 3-4, 7-8.
  - _Validation:_ fake-handler runtime integration tests show tag-before-handle/release and zero inference work.
  - _Requirements:_ 3.1-3.7, 6.2-6.13, 7.1-7.6, 9.5, 11.3, 11.6

- [ ] 10. Integrate one early tag snapshot and backend-effective history projection
  - Add RED tests where CTP contains tagged historical messages but request/pre-request hooks, route/context sizing, billing authorization inputs, and prepared baseline observe only the filtered projection.
  - Load one bounded authoritative tag Snapshot per normal logical turn after local-turn pass and carry the request-local guard in `preparedRequest`.
  - Project the accepted work call before backend-oriented request/pre-request transforms and continue all downstream route/billing/capability work from that filtered call.
  - Add store-unavailable/no-forwardable-content/invalid-projection tests proving failure occurs before route planning/backend open and never treats lookup failure as empty state.
  - _Boundary:_ runtime chooses sequencing; projector owns filtering semantics.
  - _Depends:_ 5-6, 9.
  - _Validation:_ one-snapshot counters/fakes and effective-call assertions pass.
  - _Requirements:_ 4.1-4.10, 8.1, 9.1, 9.4, 10.1-10.3, 11.3

- [ ] 11. Add the final shared PTB/backend-open guard
  - Add a RED regression where an AttemptTransform deliberately reintroduces an already tagged message after early projection and prove the unguarded fake backend/PTB would receive it.
  - Enforce the request-local tag set on final `wireCall` after candidate shaping/transforms/adaptation and before PTB serialization/capture and `be.Open`.
  - Add shared-path tests for initial, failover/retry-before-output, parallel/race, TTFT replacement, and interleaved thinker/executor attempts proving no path bypasses the guard.
  - Add invalid-final-projection/store/enforcement tests proving no PTB/open and no retry-after-output behavior change.
  - _Boundary:_ one core candidate-open safety cut; no backend connector changes.
  - _Depends:_ 5-6, 10.
  - _Validation:_ PTB fake + backend fake never observe tagged content across all attempt modes.
  - _Requirements:_ 5.1-5.8, 9.2, 9.4, 10.9, 11.4-11.5

## Phase 5 — Certify Replay, Continuation, Reload, and Production Quality

- [ ] 12. Add cross-frontend local-reply and full-history replay contract coverage
  - Use fake generic local-turn handlers to produce local assistant replies through the shared frontend pipeline for all supported frontend families where the common contract applies.
  - Re-submit the resulting client transcript and prove canonical identity recognition removes both tagged client-origin source and proxy-origin reply before a fake backend.
  - Verify streaming and non-streaming encoders accept the canonical local stream without provider/backend-specific local-response branches.
  - Add identity round-trip cases for canonical text/multipart forms covered by each frontend decoder/encoder contract.
  - _Boundary:_ frontend tests certify generic core/SDK behavior; production frontend adapters gain no policy owner.
  - _Depends:_ 9-11.
  - _Validation:_ frontend TCK/parity suites pass with fake local handler only.
  - _Requirements:_ 1.6, 7.2-7.7, 8.1, 11.7

- [ ] 13. Certify OpenResponses continuation and immutable-generation behavior
  - Add a flow test: remote turn A -> fake local turn B -> new input C using `previous_response_id`; prove stored/materialized A-leg history contains B while backend projection is A+C.
  - Prove local input/reply continuation records need no non-forwardable-specific rewrite/schema flag for correctness.
  - Add generation reload coverage where the generation that created a local turn is replaced by one with no local-turn handler, yet the persisted A-leg tags still filter B on a later backend turn.
  - Add composition tests that reject a generation with local-turn handlers when the configured continuity implementation cannot provide required non-forwardable storage, while no-handler configurations remain compatible.
  - _Boundary:_ continuation stays frontend/A-leg owner; enforcement stays core; persistence stays process-owned across generations.
  - _Depends:_ 4, 7, 9-12.
  - _Validation:_ continuation/reload/composition integration suites pass.
  - _Requirements:_ 2.7-2.9, 8.2-8.6, 10.6-10.8, 11.7-11.8

- [ ] 14. Add observability, bounded diagnostics, and security regressions
  - Add counters/events for tag writes/idempotency/capacity, early/final filtered counts, local-turn outcomes, and enforcement failures using repository metric conventions without plaintext/digest high-cardinality labels.
  - Add traffic assertions that CTP remains truthful and PTB is always post-final-guard; local turns emit no PTB.
  - Add log/capture tests proving reason codes are bounded and message plaintext is not introduced into registry-specific diagnostics/persistence.
  - Add security tests proving client metadata/wire fields cannot create/remove tags and secret guard still precedes local handler execution.
  - _Boundary:_ observability reports policy outcomes only; it does not become enforcement authority.
  - _Depends:_ 3-4, 9-13.
  - _Validation:_ metrics/log/traffic/security suites pass with bounded cardinality.
  - _Requirements:_ 1.7, 9.1-9.8, 10.10, 11.3

- [ ] 15. Run concurrency/performance/architecture certification and update docs
  - Add targeted benchmarks/alloc checks showing one bounded snapshot per normal turn and no per-B-leg store reads; characterize worst-case 4096-tag projection without introducing a cache/watcher.
  - Run focused `go test -race` suites for memory/Bun registry, local-turn/source-tag ordering, and runtime guard paths; add deterministic barriers for concurrent Tag/Snapshot and A-leg delete/recreate cases.
  - Update SDK/plugin-authoring/architecture docs with whole-message granularity, tag-before-release, local-turn Match->tag->Handle ordering, producer guidance, and explicit interactive-command non-implementation.
  - Run repository formatting/vet/architecture/hygiene checks, `go test ./...`, SQLite/PostgreSQL focused integration, and existing frontend/backend contract suites; fix only regressions caused by this feature.
  - _Boundary:_ final certification/documentation only; do not add command/notification product behavior during polish.
  - _Depends:_ 1-14.
  - _Validation:_ full required quality gates green; spec traceability has no uncovered acceptance criterion.
  - _Requirements:_ 10.1-10.10, 11.1-11.10

## Explicit Scope Guard

The implementation described above is complete for **non-forwardable conversation content infrastructure**. It MUST NOT add any production interactive command parser/registry/handler, `!/set` behavior, model-routing mutation, quota threshold logic, quota notification generator, or asynchronous notification scheduler. Those future features consume this completed infrastructure rather than extending its enforcement core.