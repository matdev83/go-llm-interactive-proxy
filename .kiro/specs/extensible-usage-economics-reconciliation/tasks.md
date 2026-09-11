# Implementation Plan

## Execution contract

This is the complete pre-OSS implementation plan for `extensible-usage-economics-reconciliation`. It is **not** permission to implement only DTOs and leave the hooks unwired. Read `requirements.md`, `design.md` and `research.md` first.

Use red -> green -> refactor for every leaf task. Run focused tests before broader gates. Keep production changes in chronological small PRs below the existing 100-modified-Go-file gate. All new monetary behavior remains disabled/unbound or shadow-only until Task 17 cutover. Do not close the execution issue when the spec-only PR merges.

`_Requirements_` are numeric acceptance-criterion IDs, not task IDs. `_Depends_` refers to leaf tasks. No hidden cross-milestone prerequisite is implied. Existing completed ownership and provider-expansion programs are baseline, not work to redo. Exact existing file anchors are in the design File Structure Plan; proposed paths are marked new there.

The only initially approved parallel implementation group is Tasks 8.1–8.4, after shared contracts and ABI are stable. Each branch owns its own family code/fixtures only; shared SDK/schema/TCK changes must be merged before that group. All other tasks run sequentially unless a later reviewed plan proves disjoint ownership. Full protocol Cartesian matrices are prohibited.

Tests in this plan are required future implementation evidence. They were not run during specification authoring. Unavailable PostgreSQL, pooler, Windows, connector toolchain or race environments must be supplied at certification; absence cannot be relabelled a passing gate.

## Tasks

- [ ] 1. Freeze the execution baseline and red regression contract

- [ ] 1.1 Re-inventory current owners and all economic producers
  - Read the five canonical spec files and current steering, record the starting commit and compare the source anchors in research.md. A rename is a mapping update, not a reason to redesign the spec.
  - Mechanically enumerate token usage, sideband/finalizer, prompt-cache/compaction, metering journal, monetary posting and report callers. Record one owner, protocol family and proposed certified/bridge/unsupported disposition for each.
  - Completion: an execution-baseline fixture covers all discovered production producers/consumers and identifies any semantic drift; stop only the affected task for spec repair if an actual contract conflict is found.
  - _Contracts: Existing architecture; File Structure Plan_
  - _Boundary: tests and architecture inventory_
  - _Depends: none_
  - _Validation: git rev-parse HEAD; go test ./internal/archtest/..._
  - _Requirements: 17.1, 15.1, 18.3_

- [ ] 1.2 Lock in financial failure cases before implementation
  - Add red tests for independent E/Q/P preservation, input/cache/reasoning double-count prevention, multimodal input/output direction and transformation boundaries, attempted-with-missing-evidence versus never-started zero, resumptions after DONE on the same A-leg, B-leg-rooted retail selection, and fixed fee once per call/submission.
  - Use the synthetic acceptance vectors in design.md; preserve existing historical-price policy tests as versioned legacy behavior rather than changing expected results indiscriminately.
  - Completion: each new regression fails for its intended semantic reason on the baseline, without production fixes in this task.
  - _Contracts: D3; Testing Strategy and Acceptance Vectors_
  - _Boundary: tests: billing and metering domain_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/billing/... ./internal/core/metering/..._
  - _Requirements: 1.1, 1.4, 1.5, 3.2, 3.3, 3.4, 6.3, 8.3, 14.3, 18.1, 18.2_

- [ ] 1.3 Capture durability, compatibility and performance baselines
  - Freeze representative V1 call/leg payloads and hashes, sideband/finalizer fixtures, old posting identity and dialect schema assumptions.
  - Measure disabled/enabled accounting allocations, terminal writes, multi-MiB traffic and current test-suite cost using repository-owned commands. Record environment and repetitions; do not invent throughput targets from a single sample.
  - Completion: reproducible baseline fixtures and commands exist; missing external DB or Windows measurements are recorded as pending execution evidence, never green.
  - _Contracts: Migration Strategy; Error Handling, Security and Performance_
  - _Boundary: tests: compatibility and performance_
  - _Depends: 1.1_
  - _Validation: make test-unit; make test-db-parity-sqlite; make test-cost on Windows_
  - _Requirements: 10.6, 11.6, 17.1, 17.2, 18.4, 18.5, 18.6_

- [ ] 1.4 Install scope and architecture guardrails
  - Add guards against new provider-name branches, raw-content economic payloads, a second monetary authority, unversioned schema/hash changes and production posting from shadow mode.
  - Keep changes below the repository Go-source gate; maintain old build behavior while new code remains unbound.
  - Completion: architecture tests express the allowed public-binding exception and preserve ordinary non-money Options without relaxing unrelated core budgets.
  - _Contracts: Boundary Commitments; C7_
  - _Boundary: tests: architecture governance_
  - _Depends: 1.2, 1.3_
  - _Validation: go test ./internal/archtest/...; make quality-checks_
  - _Requirements: 15.1, 15.3, 15.5, 17.3, 18.1_


- [ ] 2. Introduce canonical V2 evidence and exact component contracts

- [ ] 2.1 Implement bounded exact decimal and unit validation
  - Implement coefficient/scale normalization, integer meter constraints, bounded exponent parsing and checked conversion to ledger nanos; reuse established checked arithmetic where applicable.
  - Test zero/negative-zero, absent, large values, fractions, overflow, invalid units and precision limits without float conversion.
  - Completion: exact round-trip and deterministic canonical representation pass the synthetic fractional-credit and time fixtures.
  - _Contracts: D2_
  - _Boundary: SDK/public metering value objects_
  - _Depends: 1.4_
  - _Validation: go test ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/..._
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6_

- [ ] 2.2 Implement full component identity and schema relationships
  - Implement economic direction plus sorted unique qualifiers and canonical key/hash equality including unit and schema; reject duplicate keys, contradictory direction/schema combinations and duplicate dimension names.
  - Declare token aggregates, cache lifetime and multimodal input/output relationships; unknown namespaced components persist without being automatically considered billable.
  - Completion: image/audio/video input and output with different units/quality/lifetime cannot collide, and aggregate/subcomponent/transform relationships are explicit.
  - _Contracts: D2–D3_
  - _Boundary: SDK/public metering schemas_
  - _Depends: 2.1_
  - _Validation: go test ./pkg/lipsdk/metering/..._
  - _Requirements: 2.1, 2.2, 2.4, 3.1, 3.4, 3.5, 15.5_

- [ ] 2.3 Implement observation, subject and provenance contracts
  - Implement V2 observation identity, source revision, acquisition/origin, subject union, timestamps, measure presence/quality, safe evidence fields and typed charge coverage using store-scoped charge references.
  - Validate account-window/resource/B-leg distinctions, provider-versus-runtime attribution authority, resumable A-leg versus per-invocation CallID lineage, and coverage-reference scope; split different acquisition provenance rather than broaden record-wide authority.
  - Completion: local/provider/statement records coexist, aggregate-only money is legal, an A-leg can accept later calls without reopening prior B-legs, and malformed source/scope/coverage combinations are rejected.
  - _Contracts: D1–D2_
  - _Boundary: SDK/public metering evidence_
  - _Depends: 2.2_
  - _Validation: go test ./pkg/lipsdk/metering/..._
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 5.3, 5.5, 6.1, 9.1, 10.1, 16.3_

- [ ] 2.4 Implement valuation and line-item contracts
  - Implement immutable E/Q/P/S/R valuation DTOs, input references, line detail, snapshot/qualifier hashes, exact/rounded amounts and completeness. Define the public provider-neutral `Rater`/`Quoter` interfaces and their versioned input/output DTOs; statement-import and reconciliation DTOs share the same public observation/valuation identities without accepting provider-shaped raw payloads.
  - Keep reported aggregate cost separate from inferred line costs; support native currency and optional referenced reporting conversion without an implicit FX rate.
  - Completion: serialization preserves every required unit/cost pair, distinguishes locally derived Q from provider-reported P, and an external module can compile a typed custom rater/quoter using only public packages.
  - _Contracts: D4_
  - _Boundary: SDK/public economics_
  - _Depends: 2.3_
  - _Validation: go test ./pkg/lipsdk/economics/..._
  - _Requirements: 1.2, 1.6, 2.5, 2.6, 6.6, 7.1, 7.2, 7.6, 15.2_

- [ ] 2.5 Implement explicit V1 reader and one-way projection adapters
  - Decode V1 facts/usage without changing historic hashes or guessing lost provenance; map proven fields to V2 with legacy markers.
  - Provide lossless integer-token projections for existing protocol/nonfinancial authority consumers and reject required nonrepresentable measures.
  - Completion: V1 projections cannot be re-imported as independent observations; compatibility tests prove absent-versus-zero and historical replay.
  - _Contracts: D2; Migration Strategy_
  - _Boundary: SDK compatibility and nonfinancial projection_
  - _Depends: 2.4_
  - _Validation: go test ./pkg/lipsdk/metering/... ./pkg/lipsdk/authority/... ./internal/core/metering/..._
  - _Requirements: 10.6, 11.6, 15.6, 17.2, 18.3_


- [ ] 3. Implement source-isolated normalization and reduction

- [ ] 3.1 Normalize token inclusion and disjoint charging partitions
  - Implement inclusive-input and separate-cache mappings, lifetime distinctions, reasoning-in-output semantics and unknown partition handling.
  - Add the 11.6 synthetic charge input partition fixture and inconsistent-negative residual fixtures; never infer missing operands as zero.
  - Completion: normalizations preserve original source fields and return partial when intersections or partitions are unknown.
  - _Contracts: D3_
  - _Boundary: metering domain normalization_
  - _Depends: 2.5_
  - _Validation: go test ./internal/core/metering/..._
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6_

- [ ] 3.2 Reduce source streams with complete meter keys
  - Upgrade the existing reducer owner to reduce V2 keys/decimals within source/subject/charge scope; use the V1 adapter for historical inputs.
  - Implement delta, present-field cumulative replacement, gauge and correction semantics; absence must not erase known unrelated components.
  - Completion: mixed-unit/source streams cannot merge and chunk/order fixtures reduce deterministically.
  - _Contracts: D1–D3_
  - _Boundary: metering domain reducer_
  - _Depends: 3.1_
  - _Validation: go test ./internal/core/metering/aggregate/..._
  - _Requirements: 2.4, 9.2, 10.2, 10.3, 10.6_

- [ ] 3.3 Enforce semantic replay, corrections and provenance integrity
  - Use source event/revision identity for replay and full payload equality for conflicts, including duplicate identities within one batch.
  - Validate supersession target scope, acyclic revision relationships, typed charge-coverage graph integrity and sequence ordering; preserve old evidence. Reject cross-store charge references, inclusive/additive contradictions, coverage cycles and unresolved ambiguous overlaps rather than selecting a payable graph by arrival order.
  - Completion: repeated frames are no-ops, changed payload under one identity conflicts, and legitimate corrections do not duplicate a charge.
  - _Contracts: D2; C1; C5_
  - _Boundary: metering domain identity and correction_
  - _Depends: 3.2_
  - _Validation: go test ./internal/core/metering/... ./pkg/lipsdk/metering/..._
  - _Requirements: 1.4, 3.6, 5.2, 5.5, 10.1, 10.2, 10.3_

- [ ] 3.4 Certify reusable schema and normalization TCK
  - Create a bounded family TCK for exact values, input/cache partition, multimodal direction/unit/quality, aggregate coverage, unknown fields, gauges and presence.
  - Add synthetic image-input, audio-output/video-output and non-token resource meters with new schema/qualifiers without editing reducer switches.
  - Completion: family adapters can supply text/image/audio/video fixtures to the shared contract without creating frontend-by-backend combinations or coercing media into text tokens.
  - _Contracts: C2; Testing Strategy_
  - _Boundary: testkit contracts_
  - _Depends: 3.3_
  - _Validation: go test ./internal/core/metering/... ./pkg/lipsdk/metering/..._
  - _Requirements: 3.1, 5.6, 15.5, 18.1, 18.2, 18.3_


- [ ] 4. Extend durable evidence and component storage

- [ ] 4.1 Add versioned metering envelopes and component projections
  - Add additive migrations for V2 metadata and generic component rows in the existing journal family; keep old canonical rows unchanged.
  - Implement transactional canonical append plus derived projection with store-scoped source/revision uniqueness and FK integrity.
  - Completion: arbitrary component quantity/charge detail can be stored and queried in SQLite and PostgreSQL without per-component DDL.
  - _Contracts: D5_
  - _Boundary: driven adapter: metering journal SQL_
  - _Depends: 3.4_
  - _Validation: make test-db-parity-sqlite; make test-db-parity-postgres-direct_
  - _Requirements: 11.1, 11.2, 11.3, 11.4, 11.5, 11.6_

- [ ] 4.2 Persist immutable valuations and reconciliation results
  - Add valuation envelopes and line projections plus versioned comparison records; define indexed subject/basis/input-hash queries.
  - Persist pre-round and rounded amounts exactly, retaining explicit money presence and aggregate-only claims.
  - Completion: database round-trips reproduce the complete E/Q/P/S/R distinction and line-level costs.
  - _Contracts: D4–D5_
  - _Boundary: driven adapter: billing SQL_
  - _Depends: 4.1_
  - _Validation: go test ./internal/infra/billingstore/...; make test-db-parity_
  - _Requirements: 1.1, 1.2, 1.6, 2.6, 7.1, 11.1, 11.2, 11.3, 11.4_

- [ ] 4.3 Add transactional append composition and rebuild support
  - Expose an infra-only transaction writer so evidence, closure and work use one local transaction in monetary mode.
  - Implement projection-version detection/rebuild and stable bounded pagination; do not assume separate database handles participate atomically.
  - Completion: crash injection between writes yields all-or-nothing records, and rebuilt projections match canonical data.
  - _Contracts: D5; C6_
  - _Boundary: driven adapter transaction composition_
  - _Depends: 4.2_
  - _Validation: make test-db-parity; go test ./internal/infra/metering/journalstore/... ./internal/infra/billingstore/..._
  - _Requirements: 10.5, 11.2, 11.4, 11.5, 14.4, 18.4_

- [ ] 4.4 Certify durable replay and topology isolation
  - Test same-key same-payload replay, conflicts, revisions, tenant isolation and old payload/hash preservation on both engines.
  - Exercise transaction-pooler constraints using existing repository topology targets; do not introduce session-pinned state.
  - Completion: persistence contracts are registered with the canonical dbparity catalog and fail closed when a required topology is unavailable.
  - _Contracts: D5; Migration Strategy_
  - _Boundary: tests: database parity and restart_
  - _Depends: 4.3_
  - _Validation: make test-db-parity; make quality-checks_
  - _Requirements: 10.1, 10.6, 11.4, 11.6, 17.2, 18.4_


- [ ] 5. Attach canonical evidence to B2BUA terminal ownership

- [ ] 5.1 Implement call, attempt, provider charge and workload lineage
  - Carry trusted CallID/ALeg/BLeg/AttemptSeq and provider account/request/charge identities without conflating them. Treat A-leg as resumable continuity, BillingCallID as one invocation/grouping scope, and B-leg as the root for every request-scoped inference usage observation/charge.
  - Resolve store-scoped charge references and explicit parent-inclusive/additive-child coverage across the attributable graph. Reject cycles, contradictory/ambiguous overlap and non-conserved shared allocations before the graph becomes eligible for rating; keep genuine resource/account-period economics at their native subject until explicit allocation.
  - Completion: retries, losers, failed work and auxiliary charges have stable distinct owners, one economic leaf per actual charge, no inclusive parent can be rolled up together with the child amount it already covers, and later calls on the same A-leg cannot mutate earlier economic records.
  - _Contracts: D1; C1_
  - _Boundary: core lifecycle and billing identity_
  - _Depends: 4.4_
  - _Validation: go test ./internal/core/billing/... ./internal/core/runtime/..._
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 5.5, 10.4_

- [ ] 5.2 Replace destructive terminal evidence selection with capture
  - Extend the existing attempt-owned accumulator/journal and terminal handoff to retain source-separated observations and references instead of merging authority/cost into one event; allow bounded V2 observations to become durable as acquired without direct stream-time money mutation.
  - Reuse attempt snapshot/terminal claim ownership and drain final evidence on all exit paths; late callbacks cannot bind to a later current B-leg. Treat terminal/DONE as a B-leg/call checkpoint only, never A-leg/session finality.
  - Completion: stream/finalizer/sideband disagreement survives closure, a same-A-leg resume creates fresh CallID/B-legs, and no new monetary receive-loop operation appears.
  - _Contracts: C1; C6_
  - _Boundary: core/runtime terminal wiring_
  - _Depends: 5.1_
  - _Validation: go test ./internal/core/runtime/... ./internal/archtest/..._
  - _Requirements: 1.1, 1.3, 1.4, 5.2, 10.4, 14.1, 15.1_

- [ ] 5.3 Make strict terminal durability and recovery explicit
  - Bind the transactional envelope writer through the existing TerminalUsageSink lifecycle, preserving separate optional observation mode.
  - On append failure, preserve recoverable intent/incomplete accounting health and block new strict work when durability is unavailable; never retry inference after output.
  - Completion: shutdown, crash and canceled-context tests preserve closure identity and expose missing evidence honestly.
  - _Contracts: C6_
  - _Boundary: core lifecycle plus durable handoff_
  - _Depends: 5.2_
  - _Validation: go test ./internal/core/runtime/... ./internal/infra/billingstore/..._
  - _Requirements: 10.5, 10.6, 11.2, 14.1, 14.4, 16.5_

- [ ] 5.4 Certify all-leg COGS attribution independent of retail selection
  - Exercise winner, retry, canceled/failed attempt, parallel loser, swallowed failure, compaction and maintenance lineage, including multiple BillingCallIDs on one resumed A-leg.
  - Check inclusive parent/child dedupe, partial known subtotal, BYOK payer and resource allocation conservation; prove retail inference selection is independent from all-leg supplier COGS.
  - Completion: known 3+5+2 supplier costs roll up to 10 regardless of retail selection, while the default surfaced-only retail policy can rate only the winning B-leg; an unknown extra cost makes the operator subtotal partial.
  - _Contracts: D1; C4; Acceptance Vectors_
  - _Boundary: tests: B2BUA and auxiliary contracts_
  - _Depends: 5.3_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/billing/..._
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 18.2, 18.3_


- [ ] 6. Capture independent local measurements at actual boundaries

- [ ] 6.1 Measure the final upstream representation
  - Attach provider-neutral measurement summaries after adapter payload construction and rewrites, before upstream byte commitment, covering text plus media/document properties after resize, transcode, frame/rate or other provider-bound transformation.
  - Distinguish prepared/attempted/accepted status; preserve canonical estimates when exact provider token/media metering is unsupported.
  - Completion: an adapter-added field or image/audio/video transformation changes the proper local modelled B-leg input evidence and is not invisible to a claimed exact count.
  - _Contracts: C1 hook map_
  - _Boundary: backend attachment plus core neutral checkpoint_
  - _Depends: 5.4_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/metering/... ./internal/plugins/backends/..._
  - _Requirements: 4.1, 4.3, 4.4, 14.5_

- [ ] 6.2 Measure provider output before customer transforms
  - Capture provider-side text and generated-media measurement separately from frontend-delivered output; keep tokenizer/media meter/version/coverage and pre/post transform identity.
  - Implement chunk-invariant text counting plus bounded media duration/frame/size accounting using supported incremental measurement or existing bounded reconstruction; do not sum arbitrary independently tokenized chunks or replace provider-side duration/quality with downstream-transcoded values.
  - Completion: text chunking and image/audio/video transform fixtures produce stable independent B-leg evidence without retaining unbounded raw output.
  - _Contracts: C1; Error Handling, Security and Performance_
  - _Boundary: core metering and boundary accumulator_
  - _Depends: 6.1_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/metering/..._
  - _Requirements: 4.2, 4.3, 4.5, 16.1, 18.5_

- [ ] 6.3 Represent unobservable local components honestly
  - Handle cache disposition, hidden reasoning, provider tools and real compute as unavailable or method-labelled estimates where independently unknown.
  - Keep provider count API evidence provider-derived and prohibit local/remote cloning in conversion helpers.
  - Completion: no provider cache counter can produce a falsely independent matching local observation.
  - _Contracts: D1; C1; C4_
  - _Boundary: metering domain provenance_
  - _Depends: 6.2_
  - _Validation: go test ./internal/core/metering/... ./internal/core/runtime/..._
  - _Requirements: 1.5, 3.5, 4.3, 4.4, 12.3_

- [ ] 6.4 Preserve large-payload and no-money behavior
  - Integrate required evidence capability into canonical/fast-path eligibility; fall back before upstream commitment when a lane cannot prove bounded capture.
  - Test accounting-disabled zero additional I/O/allocations and independent optional observation mode; avoid new payload-sized clones.
  - Completion: affected #532/#503 contracts remain correct whether that implementation lands before or after this work.
  - _Contracts: C1; Performance; Migration Strategy_
  - _Boundary: core/runtime and performance tests_
  - _Depends: 6.3_
  - _Validation: go test ./internal/core/runtime/... ./internal/archtest/...; make parity-checks_
  - _Requirements: 4.6, 14.1, 15.3, 18.3, 18.5_


- [ ] 7. Extend the executable connector ABI without silent loss

- [ ] 7.1 Define and generate negotiated V2 economic payloads
  - Add new versioned sideband/finalization messages and capability flag without reusing field numbers or changing V1 wire meaning.
  - Generate transport code with the repository generator; define size and presence validation in public DTO conversions.
  - Completion: code generation is reproducible and both old and new fixture payloads decode with explicit capabilities.
  - _Contracts: C2_
  - _Boundary: SDK/public backend-plugin ABI_
  - _Depends: 6.4_
  - _Validation: go test ./pkg/lipsdk/backendplugin/...; make quality-checks_
  - _Requirements: 5.1, 5.4, 5.5, 15.2, 18.3_

- [ ] 7.2 Bridge host drains and finalization to V2 observations
  - Update host sessions/adapters to preserve sideband measure/charge detail, subject, provider identity and revision.
  - Drain on open failures, receive, normal terminal, cancel and close; do not encode host-only economics as canonical client content.
  - Completion: duplicate frame/finalizer reports remain one provider charge with retained source relationships.
  - _Contracts: C1–C2_
  - _Boundary: driven adapter: executable connector host_
  - _Depends: 7.1_
  - _Validation: go test ./internal/infra/backendplugins/... ./pkg/lipsdk/backendplugin/..._
  - _Requirements: 5.1, 5.2, 5.3, 10.4, 16.4_

- [ ] 7.3 Enforce compatibility and evidence-capability policy
  - Implement explicit lossless V1 bridge and partial coverage reporting for old connectors; strict unsupported offers fail before execution.
  - Test unknown component/schema handling, unsupported money detail, maximum sizes and unexpected field types.
  - Completion: old/new host/connector combinations cannot silently claim full V2 coverage.
  - _Contracts: C2_
  - _Boundary: SDK negotiation and host validation_
  - _Depends: 7.2_
  - _Validation: go test ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/..._
  - _Requirements: 5.4, 5.6, 14.5, 17.2, 18.3_

- [ ] 7.4 Certify the canonical sideband family contract
  - Extend the shared connector conformance TCK with presence, fractions, multimodal direction/unit/quality, charge coverage, gauges, late revisions and secret-safe errors.
  - Use synthetic image/audio/video and other non-token provider fixtures to prove root executor/schema independence.
  - Completion: one reusable transport certification suite covers required V2 invariants without provider Cartesian tests.
  - _Contracts: C2; Testing Strategy_
  - _Boundary: testkit connector contracts_
  - _Depends: 7.3_
  - _Validation: go test ./pkg/lipsdk/backendplugin/conformance/...; make parity-checks_
  - _Requirements: 15.5, 16.1, 18.1, 18.2, 18.3_


- [ ] 8. Migrate real upstream economic evidence producers

- [ ] 8.1 (P) Certify Anthropic-family cache and output evidence
  - Map input, cache read, cache creation and lifetime-specific creation fields using the documented family schema; retain safe original lexemes.
  - Cover streaming present-field updates, output/reasoning inclusion and server-side usage where actually surfaced; absent details remain absent.
  - Completion: shared normalization TCK plus real-family fixtures pass; changes stay inside the Anthropic family boundary.
  - _Contracts: C2; S01_
  - _Boundary: backend plugin: Anthropic family_
  - _Depends: 7.4_
  - _Validation: go test ./internal/plugins/backends/anthropic/..._
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 5.1, 5.2, 5.3_

- [ ] 8.2 (P) Certify OpenAI and OpenResponses-family evidence
  - Map cached/reasoning subsets plus supported image/audio/video/document input or generated-media economics exposed by the current OpenAI/OpenResponses family, provider monetary aggregates when genuinely present, request IDs and returned service context.
  - Preserve interrupted streams lacking final usage and use provider cost only as P when supplied; retain input/output media direction and test host-only isolation.
  - Completion: the OpenAI/OpenResponses family fixtures distinguish E, Q, P, multimodal direction, and missing final evidence without text-token coercion.
  - _Contracts: C2; S02; S07_
  - _Boundary: backend plugin: OpenAI/OpenResponses families_
  - _Depends: 7.4_
  - _Validation: go test ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/openresponsescompat/..._
  - _Requirements: 1.1, 3.1, 3.4, 5.1, 5.2, 5.3, 7.4_

- [ ] 8.3 (P) Certify Gemini-family modality and cache evidence
  - Map available text/image/audio/video modality, direction, cache, reasoning/total and grounded-tool usage with versioned units/quality qualifiers; do not invent usage from a published price sheet.
  - Route actual resource storage evidence through interval scope, preserving unavailable input when the response does not contain it.
  - Completion: known multimodal/cache fields round-trip with input/output separation and non-observed storage charges are not falsely labelled provider-reported.
  - _Contracts: C2; S03_
  - _Boundary: backend plugin: Gemini family_
  - _Depends: 7.4_
  - _Validation: go test ./internal/plugins/backends/protocols/geminigenerate/..._
  - _Requirements: 2.2, 2.4, 3.1, 3.4, 3.5, 5.1, 6.5_

- [ ] 8.4 (P) Certify Codex request usage and account-window snapshots
  - Map request token evidence separately from primary/secondary/named-window utilization and credit snapshots using actual available fields.
  - Preserve resets, pool/account identities and gauge semantics; a response-associated gauge is not a request debit.
  - Completion: connector-local tests cover concurrent/out-of-order windows, multiple pools and missing request cost without fabricating a debit.
  - _Contracts: C2; S06_
  - _Boundary: executable connector: Codex_
  - _Depends: 7.4_
  - _Validation: Run connector-local go test ./... in connectors/codex; run the root connector conformance gate_
  - _Requirements: 5.1, 5.2, 9.1, 9.2, 9.3, 9.4, 9.5_

- [ ] 8.5 Close the complete producer inventory including auxiliary paths
  - Apply the Task 1 disposition criteria to remaining existing hosted/compatibility/executable producers. Use central lossless bridges where valid; implement remaining family mappings in separate bounded PRs.
  - Migrate keep-warm, prompt-cache renewal, compaction and nested agent evidence through the same contract, keeping additive/inclusive parent coverage explicit.
  - Completion: every inventoried producer is certified, losslessly bridged, or explicitly negotiated as unsupported advanced evidence; no unclassified producer remains.
  - _Contracts: C1–C2; Task 1 inventory_
  - _Boundary: backend/profile/feature-owned producer integration_
  - _Depends: 8.1, 8.2, 8.3, 8.4_
  - _Validation: make parity-checks; focused module tests for every changed connector; go test ./internal/standardplugins/featurehost/..._
  - _Requirements: 5.1, 5.4, 5.5, 5.6, 6.2, 15.1, 15.5, 17.1, 18.3_


- [ ] 9. Implement reproducible component rating and snapshot binding

- [ ] 9.1 Adapt existing tariff sources into immutable rule snapshots
  - Use the existing catalog and snapshot resolver as inputs; store immutable rule material/hash and effective qualifiers.
  - Map old scalar policies into named legacy semantics; do not crawl pricing sites or introduce a new tariff database.
  - Completion: replay resolves the originally accepted rate even after configuration reload or catalog refresh.
  - _Contracts: D4; C3_
  - _Boundary: billing composition and economics snapshots_
  - _Depends: 8.5_
  - _Validation: go test ./internal/infra/billingcompose/... ./pkg/lipsdk/economics/..._
  - _Requirements: 7.1, 7.4, 7.5, 7.6, 17.2_

- [ ] 9.2 Implement exact unit rates, fixed fees and rounding rules
  - Implement line-level linear price across text and multimodal direction/unit keys, fixed fee at declared scope, block rounding and minimums with exact intermediate arithmetic.
  - Validate the resolved charge-coverage graph before rating. Reject overlapping additive aggregate/subcomponent rules unless an explicit surcharge applies, reject inclusive-parent plus covered-child double counting, and require conserved explicit allocation for shared ownership; persist pre-round and rounded values.
  - Completion: synthetic cached-token, fixed-once and fractional unit vectors produce their exact expected values.
  - _Contracts: D3–D4; C3_
  - _Boundary: billing domain reference rating_
  - _Depends: 9.1_
  - _Validation: go test ./internal/core/billing/... ./pkg/lipsdk/economics/..._
  - _Requirements: 2.6, 3.4, 7.3, 8.2_

- [ ] 9.3 Implement context/tier conditions and scope-safe pricing
  - Compile conditional qualifiers and all-units/graduated tiers into immutable deterministic rule sets with conflict validation.
  - Distinguish whole-context threshold selection from uncached billable quantity; period-wide minimums/tiers require a period valuation or explicit custom rater.
  - Completion: context boundary, region/tier changes and missing qualifiers cannot select an unrelated rate silently.
  - _Contracts: C3_
  - _Boundary: billing domain snapshot compilation_
  - _Depends: 9.2_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingcompose/..._
  - _Requirements: 7.1, 7.3, 7.4, 7.5_

- [ ] 9.4 Produce independent expected and provider-quantity valuations
  - Always calculate E and Q independently when their inputs exist, even when P is supplied; leave unavailable local partitions incomplete.
  - Preserve provider-reported aggregate or component charges and derived local charges as different bases; freeze context changes rather than retroactively editing E.
  - Completion: E/Q/P input refs and costs survive durable round-trip without fallback overwriting.
  - _Contracts: D4; C3–C4_
  - _Boundary: billing domain and post-turn valuation orchestration_
  - _Depends: 9.3_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingcompose/... ./internal/infra/billingstore/..._
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 4.3, 7.2, 7.4, 7.5_

- [ ] 9.5 Certify generic non-token and unsupported-rate behavior
  - Use image input/output, audio input/output, video input/output, document/page, request, submission, time, storage-product and credit fixtures plus a new namespaced synthetic meter.
  - Test modality/direction-specific rates, rate missing, currency mismatch, explicit free rate, unsupported precision and incomplete quantity; no case defaults to zero cost.
  - Completion: adding a media component or unit changes only its schema/rule/adapter fixture, not executor or SQL schema, and unlike modalities/directions never collapse into one rated line.
  - _Contracts: C3; Acceptance Vectors_
  - _Boundary: tests: economics reference contract_
  - _Depends: 9.4_
  - _Validation: go test ./internal/core/billing/... ./pkg/lipsdk/economics/..._
  - _Requirements: 2.2, 2.3, 2.5, 7.5, 7.6, 15.5, 18.1, 18.2_


- [ ] 10. Implement independent customer policies and submission charging

- [ ] 10.1 Separate retail basis and attempt-scope selection
  - Implement request-scoped retail inference usage over an explicit B-leg selection policy (for example surfaced/winner only, selected attempts, or explicitly all attempts), with explicit cost-pass-through as a separate commercial basis. Customer-boundary quantities may feed separately declared proxy/service charges but are not a competing inference-usage source.
  - Keep supplier COGS all-leg attribution independent; migrate existing retail offers with their previous scope instead of silently applying the new B-leg-rooted default.
  - Completion: provider rate/cost readiness is absent from independent retail quantity rating, internal retries are customer-billable only when the frozen retail policy says so, and all request-scoped inference quantity lines reference B-leg observations.
  - _Contracts: C3_
  - _Boundary: billing domain customer policy_
  - _Depends: 9.5_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingcompose/..._
  - _Requirements: 6.4, 7.2, 8.1, 8.2, 8.6_

- [ ] 10.2 Establish trusted submission identity and continuation rules
  - Bind submission identity from authenticated current-turn/continuation authority or a supported harness adapter; reject untrusted overrides.
  - Test tool continuation, historical replay, new follow-up after DONE on the same A-leg, local command and transport retry. Unsupported attribution is explicit, not last-role heuristic.
  - Completion: one prompt plus multiple tool continuations contributes one submission fee, while a genuine resumed/new submission can create a new BillingCallID and fee without reopening prior B-leg usage.
  - _Contracts: C1; C3_
  - _Boundary: frontend authority adapter and neutral identity contract_
  - _Depends: 10.1_
  - _Validation: go test ./internal/core/runtime/... ./internal/plugins/frontends/..._
  - _Requirements: 6.1, 8.3, 8.4, 16.3_

- [ ] 10.3 Apply fees once at their declared customer scope
  - Move call/submission fixed-fee evaluation outside the B-leg loop and retain explicit failure/local-command/race-loser rules; fixed commercial fees remain distinct from inference usage.
  - Persist text/cache/media B-leg retail lines and optional customer-boundary proxy-service lines separately from supplier line items.
  - Completion: customer fixed fees do not multiply by number of selected B-legs, media direction remains visible, and summary charge equals its rounded detail lines.
  - _Contracts: C3; D4_
  - _Boundary: billing domain retail line rating_
  - _Depends: 10.2_
  - _Validation: go test ./internal/core/billing/..._
  - _Requirements: 2.6, 8.1, 8.2, 8.3, 8.4_

- [ ] 10.4 Implement customer credit operations and explicit included allowance
  - Add customer-owned unit balances/operations keyed by account/pool/period and integrate final credit debits with existing exposure/settlement authority.
  - For money plus included-credit plans, use pre-reserved entitlement or atomically evaluated settlement balance and a safe monetary bound; reject stale read-then-spend.
  - Completion: concurrent requests cannot spend the same included credits twice and supplier gauges cannot mutate customer entitlement.
  - _Contracts: D5; C3_
  - _Boundary: billing domain and driven unit-ledger adapter_
  - _Depends: 10.3_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingstore/...; make test-db-parity_
  - _Requirements: 2.5, 8.5, 9.6, 11.1, 14.2, 14.4_

- [ ] 10.5 Certify independent customer settlement under supplier lag
  - Run customer completion while provider-cost/statement queues are unavailable; preserve normal independent settlement.
  - Test explicit cost-pass-through provisional/pending policy with bounded state and separately permitted late adjustment.
  - Completion: supplier backlog cannot accidentally hold customer admission locks or force independent retail rating to wait.
  - _Contracts: C3; C5–C6_
  - _Boundary: tests: billing isolation_
  - _Depends: 10.4_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingcompose/... ./internal/infra/billingstore/..._
  - _Requirements: 8.6, 13.4, 14.6, 18.2_


- [ ] 11. Implement account-window and allocation economics

- [ ] 11.1 Persist and query provider allowance windows as gauges
  - Bind provider account, named pool, window/reset epoch and timestamps into account-window subjects; support partial fields and multiple windows.
  - Expose observation history without summing percentages or assigning them as per-request debit.
  - Completion: concurrent, out-of-order and reset fixtures remain correct.
  - _Contracts: D1; C2_
  - _Boundary: metering domain and journal query_
  - _Depends: 10.5_
  - _Validation: go test ./internal/core/metering/... ./internal/infra/metering/journalstore/..._
  - _Requirements: 9.1, 9.2, 9.3, 11.1_

- [ ] 11.2 Distinguish genuine request debits and local subscription allocation
  - Support additive provider credits only when a real request-scoped debit is supplied; otherwise keep the request association informational.
  - Implement optional explicit allocation records with original period/resource ownership, method/version and conserved weights.
  - Completion: amortized cost, actual upstream money and account gauge observations cannot be confused.
  - _Contracts: D1; D4; C2_
  - _Boundary: economics allocation and metering semantics_
  - _Depends: 11.1_
  - _Validation: go test ./internal/core/billing/... ./internal/core/metering/..._
  - _Requirements: 2.5, 6.5, 9.3, 9.4, 9.5_

- [ ] 11.3 Keep provider telemetry separate from admission authority
  - Integrate optional quota observations through existing nonfinancial authority rules without creating a money ledger or direct customer credit debit.
  - Require explicit pool/freshness/reset policy before a gauge influences admission; preserve missing/stale snapshot status.
  - Completion: provider telemetry may inform a declared quota policy but never constitutes a payable posting.
  - _Contracts: C2; C7_
  - _Boundary: authority adapter and host configuration_
  - _Depends: 11.2_
  - _Validation: go test ./pkg/lipsdk/authority/... ./internal/core/runtime/... ./internal/archtest/..._
  - _Requirements: 9.6, 14.1, 15.1, 15.4_


- [ ] 12. Implement discrepancy reconciliation and operational classification

- [ ] 12.1 Compare compatible component quantities
  - Join local/provider evidence on full subject/component/source context and calculate signed/absolute deltas.
  - Check tokenizer/schema/partition/period/coverage compatibility first; report partial/incomparable rather than zero or matched.
  - Completion: independent estimates retain quality labels and missing cache partitions are not falsely reconciled.
  - _Contracts: C4_
  - _Boundary: billing domain reconciliation_
  - _Depends: 11.3_
  - _Validation: go test ./internal/core/billing/..._
  - _Requirements: 12.1, 12.3, 1.4, 3.5_

- [ ] 12.2 Decompose metering and monetary discrepancies
  - Implement E/Q/P comparison and exact cost-effect/residual formulas when terms exist; do not fill missing values.
  - Distinguish suspected tariff/context/classification causes from proven quantity differences, with source refs.
  - Completion: the E=1, Q=1.1, P=1.32 fixture yields 0.1, 0.22 and 0.32 respectively.
  - _Contracts: C4; Acceptance Vectors_
  - _Boundary: billing domain comparison_
  - _Depends: 12.1_
  - _Validation: go test ./internal/core/billing/..._
  - _Requirements: 7.2, 12.1, 12.2, 12.3_

- [ ] 12.3 Apply versioned tolerances and retain all discrepancy evidence
  - Implement exact absolute/relative tolerance with explicit zero denominator behavior and unit/currency-scoped policies.
  - Keep signed, absolute and within-tolerance deltas plus aggregate gross discrepancies and affected counts.
  - Completion: offsetting errors do not disappear in operational aggregates and estimated/incomparable values never become exact matches.
  - _Contracts: C4_
  - _Boundary: billing domain policy and comparison projections_
  - _Depends: 12.2_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingstore/..._
  - _Requirements: 12.3, 12.4, 12.5_

- [ ] 12.4 Select operator cost without erasing reconciliation state
  - Implement versioned operator selection policy over E/Q/P/S with explicit provisional/unknown basis and payer/currency treatment.
  - Distinguish evidence completeness, comparison status and posting state; missing attempted usage stays unknown.
  - Completion: choosing P or Q never deletes E or labels unmatched evidence reconciled.
  - _Contracts: C4–C5_
  - _Boundary: billing domain cost selection_
  - _Depends: 12.3_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingcompose/..._
  - _Requirements: 1.4, 6.3, 6.6, 12.6, 13.5_


- [ ] 13. Add generic statement ingestion and idempotent adjustments

- [ ] 13.1 Implement authenticated normalized statement ingestion
  - Accept statement/line/revision/account/period identity and exact-granularity usage/charge claims through the generic public port.
  - Validate trusted scope, evidence bounds, replay and conflicts; do not implement vendor invoice parsers or a UI.
  - Completion: normalized fixture imports persist independently of request evidence and report replay/rejection results.
  - _Contracts: C5; public StatementImporter_
  - _Boundary: billing app orchestration and public import adapter_
  - _Depends: 12.4_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingstore/..._
  - _Requirements: 13.1, 13.6, 16.2, 16.3_

- [ ] 13.2 Match statements without guessed per-request allocation
  - Match by explicit charge ID or complete compatible aggregate account/period/SKU coverage.
  - Preserve unmatched lines and many-charge aggregate coverage; reject timestamp-nearest matching and duplicate parent/child inclusion.
  - Completion: an account total stays account-scoped until an explicit exact match or separately labelled allocation exists.
  - _Contracts: C5_
  - _Boundary: billing domain statement matching_
  - _Depends: 13.1_
  - _Validation: go test ./internal/core/billing/..._
  - _Requirements: 6.5, 12.3, 13.1, 13.2_

- [ ] 13.3 Implement selected-cost heads and balanced delta corrections
  - Persist selected/posted valuation revision and compare-and-swap it in the same transaction as an adjustment operation and balanced journal.
  - Validate currency comparability before computing a monetary delta. Compute new selected minus previously posted amount only for the same native currency or the same explicit frozen FX basis; otherwise keep the correction pending/incomparable and do not insert a valuation link, journal delta or cost-head transition. Support downward corrections with debit/credit reversal rather than invalid negative gross amounts.
  - Completion: 10 USD to 8 USD posts only a -2 USD adjustment, while 10 USD to 8 EUR without frozen FX posts nothing; replay and racing revisions produce no duplicate effects.
  - _Contracts: C5; D5_
  - _Boundary: billing domain posting intent and driven SQL adapter_
  - _Depends: 13.2_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingstore/...; make test-db-parity_
  - _Requirements: 10.3, 13.3, 13.4, 13.5, 14.4_

- [ ] 13.4 Make economic workers revision-aware and independently bounded
  - Extend existing customer/provider work with revision input hashes, claims/fences and retry reasons, preserving queue separation.
  - Persist and retry pure rating/reconciliation independently of the eventual atomic financial transition; do not hold customer locks for supplier computation.
  - Completion: interrupted/replayed workers finish the same revision once and expose backlog age/incomplete evidence.
  - _Contracts: C5–C6_
  - _Boundary: post-turn app orchestration and store queues_
  - _Depends: 13.3_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingstore/..._
  - _Requirements: 10.6, 13.3, 13.5, 14.6, 16.5_

- [ ] 13.5 Certify correction and dispute-like recovery scenarios
  - Test late evidence after closure, corrected quantities, aggregate statement adjustments, duplicate imports, unmatched statements and native-currency mismatch, including a no-FX mismatch that cannot post or advance the selected-cost head.
  - Test customer no-rebill default and explicit provisional pass-through adjustment policy.
  - Completion: economic history remains immutable and pending comparison does not erase incurred COGS or customer settlement.
  - _Contracts: C5; Acceptance Vectors_
  - _Boundary: tests: corrections and recovery_
  - _Depends: 13.4_
  - _Validation: make test-db-parity; go test ./internal/core/billing/... ./internal/infra/billingstore/..._
  - _Requirements: 13.1, 13.2, 13.3, 13.4, 13.5, 13.6, 10.3, 18.2, 18.4_


- [ ] 14. Extend conservative admission to richer customer offers

- [ ] 14.1 Quote token and non-token exposure from the same policy semantics
  - Use immutable customer rules and finite candidate/work bounds for unit, fixed, minimum, credit and resource charges.
  - Unknown duration/tool count or missing required evidence capability must yield explicit strict deny or a configured enforceable finite limit, not a fabricated bound.
  - Completion: quote and settlement agree on scope/rounding/price version across representative richer offers.
  - _Contracts: C3; Exposure and financial safety_
  - _Boundary: billing admission domain and adapter_
  - _Depends: 13.5_
  - _Validation: go test ./internal/infra/billingadmission/... ./internal/core/billing/..._
  - _Requirements: 7.1, 7.3, 14.1, 14.2, 14.5_

- [ ] 14.2 Preserve atomic exposure and actual-incurred-cost truth
  - Keep the cheap credit screen and existing atomic exposure transition as the sole monetary admission path.
  - Record actual incurred amounts even when they exceed an estimate; preserve breach state and handle settlement under existing account policy without truncating usage.
  - Completion: concurrent admissions and canceled/overrun calls cannot bypass exposure or rewrite actual cost to the quote.
  - _Contracts: C6; D5_
  - _Boundary: billing admission/settlement and runtime attachment_
  - _Depends: 14.1_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingadmission/... ./internal/infra/billingstore/..._
  - _Requirements: 10.5, 14.1, 14.2, 14.3, 14.4_

- [ ] 14.3 Certify unsupported capability and provider-backlog isolation
  - Reject strict offers before upstream spend when selected routes lack necessary evidence, without suppressing unrelated observation-only mode.
  - Exercise supplier worker backlog while customer admission proceeds and verify no supplier-side account balance lock.
  - Completion: no stream-time rating/journal path or hidden second admission authority exists.
  - _Contracts: C6–C7_
  - _Boundary: tests: strict capability and authority isolation_
  - _Depends: 14.2_
  - _Validation: go test ./internal/core/runtime/... ./internal/infra/billingadmission/... ./internal/infra/billingstore/... ./internal/archtest/..._
  - _Requirements: 5.4, 8.6, 14.1, 14.5, 14.6, 18.3_


- [ ] 15. Expose one external billing host binding without a runtime fork

- [ ] 15.1 Publish the minimal typed external binding
  - Implement public binding identity/version, complete cheap-screen/quote-admit/terminal ports and explicit owned-resource lifecycle registration. Preserve the separately defined public provider-neutral observation/sideband and Rater/Quoter/StatementImporter/ReconciliationReader contracts; do not add a generic provider-shaped normalizer port.
  - Use only public DTOs; adapters translate to existing internal billing services. Reject typed-nil, incomplete and duplicate monetary bindings.
  - Completion: public interfaces contain no internal, SQL, concrete provider or generic service-map types.
  - _Contracts: C7_
  - _Boundary: SDK/public billing host contract_
  - _Depends: 14.3_
  - _Validation: go test ./pkg/lipsdk/... ./internal/archtest/..._
  - _Requirements: 15.1, 15.3, 15.4_

- [ ] 15.2 Integrate explicit BuildWithBilling through the existing Host
  - Factor minimal common assembly behind Build and BuildWithBilling; call one BuildHost and preserve Host/Manager cleanup ownership.
  - Keep normal Options and stock YAML startup non-money; update the relevant architecture rule for this named exception only.
  - Completion: the internal reference billing composition and external binding use the same runtime, admission and terminal paths.
  - _Contracts: C7; Boundary Commitments_
  - _Boundary: public facade and generic composition root_
  - _Depends: 15.1_
  - _Validation: go test ./pkg/lipruntime/... ./internal/infra/runtimebundle/... ./internal/archtest/..._
  - _Requirements: 14.1, 15.3, 15.4, 15.5_

- [ ] 15.3 Certify cross-generation lifecycle and external-module use
  - Build a separate module using only public packages with custom per-submission/credit rating and a synthetic non-token provider component.
  - Test candidate validation, failed publication, active-generation snapshot retention, repeated Close, borrowed resource ownership and worker shutdown.
  - Completion: no external internal-package import, duplicate cleanup or second host is needed.
  - _Contracts: C7_
  - _Boundary: tests: external module and host lifecycle_
  - _Depends: 15.2_
  - _Validation: External-module go test ./...; go test ./pkg/lipruntime/... ./internal/infra/runtimebundle/..._
  - _Requirements: 7.1, 8.1, 8.5, 15.2, 15.3, 15.4, 18.3_


- [ ] 16. Expose safe economic detail and discrepancy queries

- [ ] 16.1 Implement scoped call and A-leg economic detail queries
  - Return source-separated component evidence/charges, E/Q/P/S/R detail, known subtotal, missing IDs/counts and aggregate-only coverage.
  - Preserve existing summary projections while keeping native currencies, BYOK payer and incomplete margin explicit.
  - Completion: query results reconstruct the user-required local/upstream unit-and-cost breakdown without raw-content access.
  - _Contracts: D5; C4; operator query contract_
  - _Boundary: query seam and existing billing report adapter_
  - _Depends: 15.3_
  - _Validation: go test ./internal/infra/billingstore/... ./internal/core/billing/..._
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 11.4, 16.3, 16.4_

- [ ] 16.2 Add discrepancy, allowance and statement query surfaces
  - Expose bounded paginated reconciliation, account-window, unmatched statement and adjustment views through the protected existing reports mount.
  - Add typed public reader DTOs and explicit import authorization where the host opts into an import route; never auto-enable an unauthenticated endpoint.
  - Completion: operator queries show comparison/selection/posting status independently and no supplier economics leaks into frontend responses.
  - _Contracts: C4–C5; Query contract_
  - _Boundary: driving adapter: protected control surface_
  - _Depends: 16.1_
  - _Validation: go test ./internal/stdhttp/... ./pkg/lipsdk/controlplane/... ./internal/infra/billingstore/..._
  - _Requirements: 9.1, 12.5, 12.6, 13.2, 13.5, 16.3, 16.4_

- [ ] 16.3 Enforce evidence redaction, retention and bounded diagnostics
  - Implement economic-field allowlists, raw lexeme/path limits, sanitizer/version markers, safe hashes and authorized retention.
  - Expose low-cardinality health/backlog/conflict/partial diagnostics; include gross absolute discrepancy aggregates without request/account IDs as metric labels.
  - Completion: secret-bearing payload fixtures are excluded and retention preserves required financial/adjustment linkage.
  - _Contracts: Error Handling, Security and Performance_
  - _Boundary: security policy and observability adapters_
  - _Depends: 16.2_
  - _Validation: go test ./internal/infra/billingstore/... ./internal/core/billing/... ./internal/stdhttp/... ./internal/archtest/..._
  - _Requirements: 16.1, 16.2, 16.3, 16.4, 16.5, 16.6, 5.6, 18.5_


- [ ] 17. Execute shadow migration and fence the accounting cutover

- [ ] 17.1 Implement historical readers and migration fixtures
  - Round-trip baseline V1 records/hashes through the new storage/query path without invented breakdown or source separation.
  - Keep legacy pricing semantics and V1 writer ownership for old in-flight calls explicitly versioned.
  - Completion: old replay identity and already-posted balances remain unchanged.
  - _Contracts: Migration Strategy steps 1–3_
  - _Boundary: migration/domain compatibility_
  - _Depends: 16.3_
  - _Validation: make test-db-parity; go test ./internal/core/billing/... ./internal/infra/billingstore/..._
  - _Requirements: 11.6, 17.1, 17.2, 18.4_

- [ ] 17.2 Run V2 capture and rating in no-post shadow mode
  - Persist V2 observations/valuations/reconciliation while V1 remains the sole monetary writer.
  - Compare expected new semantics using synthetic and captured-safe fixtures, distinguishing intended defect fixes from accidental behavior drift.
  - Completion: tests prove shadow code cannot debit balances, unit accounts or provider payables.
  - _Contracts: Migration Strategy step 4_
  - _Boundary: migration orchestration and tests_
  - _Depends: 17.1_
  - _Validation: go test ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... ./internal/archtest/..._
  - _Requirements: 17.3, 17.4, 18.2_

- [ ] 17.3 Implement durable epoch, worker fencing and in-flight ownership
  - Add a durable cutover marker per configured deployment/store boundary, stop old claims and drain/classify V1 in-flight work before enabling V2 admissions.
  - Enforce one posting version per call/charge/adjustment, including workers waking after a lease/epoch change.
  - Completion: concurrency/crash tests demonstrate no dual posting across cutover.
  - _Contracts: Migration Strategy step 6_
  - _Boundary: billingstore migration/claim authority_
  - _Depends: 17.2_
  - _Validation: make test-db-parity; go test ./internal/infra/billingstore/..._
  - _Requirements: 10.6, 14.4, 17.4, 18.4_

- [ ] 17.4 Implement compatible rollback and recovery checks
  - Allow capture-only rollback before cutover; after V2 postings require a compatible reader/epoch-aware binary or quiesce strict admissions.
  - Test forward recovery, stale binary/version rejection, pending provider evidence and optional raw-retention loss.
  - Completion: no rollback path reopens an old writer against unsupported new-format financial state.
  - _Contracts: Migration Strategy step 7_
  - _Boundary: migration recovery and tests_
  - _Depends: 17.3_
  - _Validation: make test-db-parity; go test ./internal/infra/billingstore/... ./internal/infra/runtimebundle/..._
  - _Requirements: 10.5, 11.6, 17.5, 18.4_


- [ ] 18. Retire superseded live financial paths

- [ ] 18.1 Remove token-only authoritative billing conversions and selectors
  - Delete V1 live financial producers and destructive selected-event merge/fallback pathways now replaced by V2.
  - Keep only historical readers and explicit one-way protocol/nonfinancial projections; no dual maintained source of monetary truth.
  - Completion: architecture/source guards reject reintroduction of scalar-only live rating or observer/token-ledger monetary writes.
  - _Contracts: File Structure Plan; Migration Strategy step 8_
  - _Boundary: billing/runtime owner cleanup_
  - _Depends: 17.4_
  - _Validation: go test ./internal/archtest/...; make quality-checks; go test ./internal/core/billing/... ./internal/core/runtime/..._
  - _Requirements: 15.1, 15.5, 15.6, 17.6_

- [ ] 18.2 Close all consumer and producer migration dispositions
  - Recheck the Task 1 inventory against the final tree and prove each live consumer uses V2 or an explicit safe projection.
  - Remove stale configuration/conversion hooks and update public godoc/operator migration instructions inside the owning implementation changes.
  - Completion: no orphan interface, unclassified producer, unused hook or undocumented unsupported strict offer remains.
  - _Contracts: C1–C7; Migration Strategy step 8_
  - _Boundary: all owning adapters and contract documentation_
  - _Depends: 18.1_
  - _Validation: make quality-checks; make parity-checks; changed connector module tests_
  - _Requirements: 5.4, 15.2, 15.6, 17.1, 17.6, 18.1_


- [ ] 19. Run end-to-end correctness and cost certification

- [ ] 19.1 Certify the complete independent-economics lifecycle
  - Run local/provider capture through storage, E/Q/P/R rating, discrepancy, COGS/customer settlement and query using real-family fixtures.
  - Include all-leg/auxiliary, B-leg-rooted retail selection, same-A-leg resume after DONE, missing/zero, aggregate-only money, trusted submission, credits, image/audio/video input-output transformations and synthetic non-token extensibility.
  - Completion: every design acceptance vector exercised by this integrated lifecycle task has a passing named test and every requirement listed on this task has implementation evidence; migration/cutover and remaining release-wide criteria are completed by 19.2–20.1.
  - _Contracts: Testing Strategy and Acceptance Vectors_
  - _Boundary: tests: bounded integrated contracts_
  - _Depends: 18.2_
  - _Validation: make test-unit; make parity-checks_
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 11.1, 11.2, 11.3, 11.4, 11.5, 11.6, 12.1, 12.2, 12.3, 12.4, 12.5, 12.6, 13.1, 13.2, 13.3, 13.4, 13.5, 13.6, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 15.1, 15.2, 15.3, 15.4, 15.5, 15.6, 16.1, 16.2, 16.3, 16.4, 16.5, 16.6, 18.1, 18.2, 18.3_

- [ ] 19.2 Certify database, restart and lifecycle races
  - Run canonical dual-dialect/pooler contracts plus repeated terminal/DONE followed by same-A-leg resume, late evidence, cancellation, loser callbacks, adjustment races and cutover crashes.
  - Use repository race targets on a supported environment; Windows-only skips are not substitutes for scoped race evidence.
  - Completion: correctness survives restart and concurrent worker/call lifecycles without duplicate financial effects.
  - _Contracts: C6; Migration Strategy; Testing Strategy_
  - _Boundary: tests: persistence and concurrency_
  - _Depends: 19.1_
  - _Validation: make test-db-parity; make test-race; make qa_
  - _Requirements: 10.1, 10.3, 10.4, 10.5, 10.6, 11.4, 13.3, 14.4, 17.4, 17.5, 18.4, 18.6_

- [ ] 19.3 Certify enabled and disabled overhead and test cost
  - Re-run Task 1 measurements at the final SHA, including multi-MiB canonical/fast-path traffic, bounded metadata limits and terminal writes.
  - Require zero extra disabled-path allocation/I/O and bounded enabled capture; fix material regressions rather than relax existing budgets.
  - Completion: Windows make test-cost evidence and affected #394 benchmark refresh are recorded with environment and repeated measurements.
  - _Contracts: Performance; Baseline Task 1.3_
  - _Boundary: tests: performance and QA cost_
  - _Depends: 19.2_
  - _Validation: Focused go benchmarks with -benchmem; make test-cost on Windows; make quality-checks_
  - _Requirements: 4.6, 18.3, 18.5, 18.6_


- [ ] 20. Close the release gate with verified implementation evidence

- [ ] 20.1 Run full repository gates and reconcile final traceability
  - Run normal comprehensive verification and wide QA at the exact release candidate; include root and changed connector-module contracts.
  - Cross-check every numbered acceptance criterion, task dependency and producer disposition against named passing tests or explicit supported-capability behavior.
  - Completion: no silent skip, unimplemented hook, unknown financial migration state or incomplete mandatory certification remains.
  - _Contracts: Requirements Traceability; entire design_
  - _Boundary: tests: final release certification_
  - _Depends: 19.3_
  - _Validation: make quality-checks; make test; make parity-checks; make test-db-parity; make qa_
  - _Requirements: 17.1, 17.2, 17.3, 17.4, 17.5, 17.6, 18.1, 18.2, 18.3, 18.4, 18.5, 18.6_

- [ ] 20.2 Seal implementation completion and release-gate disposition
  - Update canonical spec task evidence and completed metadata only after the preceding gates pass; record exact SHA and verification outputs.
  - Confirm the work-order acceptance before permitting #398 to close; actual rebranding/split remains outside this implementation.
  - Completion: completed spec is archived under the repository rule and the execution issue has a verifiable completion record, not merely a merged partial PR.
  - _Contracts: Migration Strategy; release scheduling_
  - _Boundary: technical certification metadata and release guard_
  - _Depends: 20.1_
  - _Validation: Review completed task evidence; validate spec metadata and canonical archive location_
  - _Requirements: 17.6, 18.1, 18.6_
