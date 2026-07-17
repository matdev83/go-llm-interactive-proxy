# Implementation Plan

**Source context:** Production-readiness hardening after review of the merged dual-plane economics and concurrency implementation rooted in PR #128 and specified by PR #130.

> **TDD gate:** Every phase begins with focused failing contracts or regressions. Production changes follow only after the red evidence is reviewable. Each phase ends with focused green tests before dependent phases begin. Tasks remain within phases 1–7 of this spec; proprietary enterprise finance is excluded.

## Phase 1 — Restore Strict Dual-Plane Correctness

- [x] 1.1 Freeze customer-versus-operator terminal invariants with red tests
  - Reproduce provider usage entering customer settlement, provider cost entering frontend facts, stale preflight money after final backend counting, and missing frontend/backend correlation.
  - Cover compression, response filtering, explicit zero cost, absent cost, sequential failover, parallel losers, and auxiliary calls without provider network access.
  - **Deliverable:** executable regressions fail for the reviewed current behavior and name the independent expected planes.
  - _Requirements: 1.1–1.9, 2.1–2.9, 13.1, 13.2_
  - _Design rules: D1, D2, D3, D13, D17_
  - _Boundary: tests in core runtime, metering checkpoint, token accounting, and economics_
  - _Depends: approved requirements and design_
  - _Validation: focused `go test ./internal/core/runtime/ ./internal/core/metering/checkpoint/ ./internal/core/accounting/`_

- [x] 1.2 Implement the final-canonical customer evidence accumulator
  - Add request-owned incremental accumulation after response hooks and completion-gate resolution and before release to the frontend encoder.
  - Derive customer output solely from released canonical content; never import provider usage scopes or provider money.
  - Settle customer authority once from frontend ingress, final customer output, and the independent customer rater.
  - **Deliverable:** compression/filtering tests show customer and operator quantities diverging correctly without buffering or changing TTFT.
  - _Requirements: 1.1–1.9, 7.5, 12.4_
  - _Design rules: D1, D2, D8, D13, D14_
  - _Boundary: core runtime request lifecycle and customer accumulation_
  - _Depends: 1.1_
  - _Validation: focused customer accumulator, response-hook, gate, stream/non-stream, and settlement tests_

- [x] 1.3 Build, rate, and authorize the exact final backend exposure
  - Run side-effect-free bounded clamp preview, then construct the complete backend-ingress quantity set after transforms, hooks, route parameters, and converged clamps.
  - Persist or bind the backend-ingress fact, invoke the operator rater from those exact quantities, and attach fact/rating/rule versions to admission.
  - Recount/rerate/reauthorize any later widening or reject it before `Open`.
  - **Deliverable:** attempt money and quantities are provably derived from the same authorized call.
  - _Requirements: 2.1–2.9, 5.2, 5.6_
  - _Design rules: D2, D3, D5, D13_
  - _Boundary: core attempt-open orchestration, token preflight, metering, and economics_
  - _Depends: 1.1_
  - _Validation: focused backend-ingress, clamp, widening, rater, and attempt-authority tests_

- [x] 1.4 Correct boundary correlation, presence, and ingress evidence construction
  - Populate actual trace, frontend, request, A-leg, B-leg, attempt, backend, and model identity at legal checkpoints.
  - Use explicit usage/cost presence rather than value heuristics and preserve authoritative zero independently from absence.
  - Capture immutable calls before mutation, bind trusted scope later, and prepare deterministic ingress fact inputs.
  - **Deliverable:** safe cross-boundary correlation and presence round-trip through public facts and control-plane projections.
  - _Requirements: 1.6, 2.4, 2.9, 5.5–5.7, 12.5_
  - _Design rules: D2, D5, D6, D14_
  - _Boundary: metering/checkpoint contracts, runtime integration, control-plane projections_
  - _Depends: 1.2, 1.3_
  - _Validation: focused checkpoint, fact, JSON-presence, and control-plane tests_

- [x] 1.5 Prove dual-plane behavior across routing and transformations
  - Add composed scenarios for original input versus compressed backend input, provider output versus delivered output, retries, failover, parallel races, cancellation, and internal auxiliary calls.
  - Assert one customer settlement, one operator settlement per incurred attempt, and no retry after output.
  - **Deliverable:** Phase 1 invariant matrix is green across the canonical streaming path.
  - _Requirements: 1.1–1.9, 2.1–2.9, 13.2, 13.9_
  - _Design rules: D1, D2, D3, D13, D17_
  - _Boundary: composed core runtime and cross-protocol tests_
  - _Depends: 1.2–1.4_
  - _Validation: focused executor/failover/parallel tests plus shared frontend-checkpoint conformance_

## Phase 2 — Harden Public Authority and Rating Contracts

- [ ] 2.1 Add red public-contract and hostile-provider tests
  - Define failing tests for duplicate IDs, descriptor/provider mismatches, advisory denies, required deterministic denies, malformed holds, standalone compensation handles, invalid clamps, foreign settlement handles, malformed leases, rating perspective mismatch, currency errors, overflow, and provider panics.
  - **Deliverable:** public and coordinator contracts describe every accepted and rejected external result before implementation.
  - _Requirements: 3.1–3.9, 4.1–4.10, 13.1, 13.3_
  - _Design rules: D4, D5, D14, D17_
  - _Boundary: SDK/public contract and authority-coordinator tests_
  - _Depends: Phase 1_
  - _Validation: `go test ./pkg/lipsdk/authority/ ./pkg/lipsdk/economics/ ./internal/core/authoritycoord/`_

- [ ] 2.2 Implement descriptor-bound provider registrations and composition
  - Add request, attempt, concurrency, and rater registration types that bind descriptor, priority, and provider instance.
  - Replace production provider/descriptor parallel slices; preserve a bounded deprecation adapter only for deterministic legacy registration.
  - Carry registrations through `pkg/lipruntime`, runtimebundle, enterprise fixture, readiness, and diagnostics.
  - **Deliverable:** no production provider identity or posture is generated from list index or discarded during composition.
  - _Requirements: 3.1–3.4, 3.7–3.9, 11.6_
  - _Design rules: D4, D10, D15_
  - _Boundary: public SDK/runtime facade and composition root_
  - _Depends: 2.1_
  - _Validation: public facade, runtimebundle, architecture, and separate-module tests_

- [ ] 2.3 Correct coordinator posture and compensation semantics
  - Implement deterministic priority ordering and complete truth tables for required/advisory plus fail-open/fail-closed outcomes.
  - Normalize advisory deny to advisory evidence; never fail open deterministic required exhaustion.
  - Reject v2 non-allow results with holds and compensate compatibility-provider holds before prior-stack compensation.
  - Persist validated per-provider settlement results and retry only unfinished providers.
  - **Deliverable:** malformed or advisory providers cannot deny strict traffic incorrectly or leak reservations.
  - _Requirements: 3.3–3.8, 4.2–4.5, 7.7, 8.6_
  - _Design rules: D4, D5, D8, D9_
  - _Boundary: core request/attempt coordinators and compensation lifecycle_
  - _Depends: 2.2_
  - _Validation: coordinator posture matrix, panic isolation, provider ownership, and compensation tests_

- [ ] 2.4 Implement complete external-result validation
  - Add context-aware validation for preview decisions, decisions, reservations, settlements, leases, renewals, ratings, versions, evidence, and clamps.
  - Validate handle ownership, exactly-one amount, nonnegative ordinary values, normalized currency, generation/expiry timing, and contradictory states.
  - Quarantine or fail closed malformed required results with client-safe errors and operator-safe evidence.
  - **Deliverable:** all external extension output crosses one validated boundary before mutating runtime state.
  - _Requirements: 4.1–4.9, 10.2, 12.2, 12.5_
  - _Design rules: D5, D14_
  - _Boundary: public value objects, coordinator invoke boundary, error mapping_
  - _Depends: 2.2, 2.3_
  - _Validation: public validation tests, hostile providers, fuzz seeds, and client-safe mapping tests_

- [ ] 2.5 Complete checked money, explicit rate presence, and rounding
  - Require explicit presence for base and optional rates, range-check decimal-to-integer conversion, and implement declared rounding policies with checked rational arithmetic.
  - Reject negative money, empty present currency, mixed-currency aggregation, invalid rate lines, and perspective/version mismatch.
  - Use `UsagePresence` rather than nonzero values when constructing rating quantities.
  - **Deliverable:** reference and injected raters share one precise monetary contract, including authoritative zero.
  - _Requirements: 2.5–2.9, 4.3, 4.6–4.10_
  - _Design rules: D3, D5, D15_
  - _Boundary: public economics contracts and OSS reference rater_
  - _Depends: 2.1, 2.4_
  - _Validation: focused money/rating/catalog tests plus overflow, rounding, zero/absence, and mixed-currency tables_

## Phase 3 — Complete the Durable Four-Boundary Metering Journal

- [ ] 3.1 Add red identity, ingress, correction, and isolation store contracts
  - Define deterministic replay tests across process restart, duplicate/conflicting identity tests, store-ID isolation, signed correction rules, same-stream target checks, cycle rejection, and full customer/operator stream reconstruction.
  - Run the same contracts against memory, SQLite, direct PostgreSQL, and transaction-pooled PostgreSQL where supported.
  - **Deliverable:** journal deficiencies fail before schema or producer changes.
  - _Requirements: 5.1–5.8, 6.1–6.9, 11.3–11.5, 13.1, 13.6_
  - _Design rules: D2, D6, D7, D12, D17_
  - _Boundary: metering domain/store contract tests_
  - _Depends: Phase 2_
  - _Validation: focused aggregate, reconcile, memory/SQLite/PostgreSQL journal tests_

- [ ] 3.2 Implement deterministic event identity and strict fact validation
  - Add identity version, lifecycle ID, boundary, source event kind, source ID, revision, and stable sequence semantics.
  - Validate perspective/boundary/lifecycle combinations, quantity/money presence, nonnegative ordinary facts, signed corrections, currency, versions, and duplicate semantics.
  - **Deliverable:** the same economic event produces the same key after retry/restart, and conflicting facts fail integrity checks.
  - _Requirements: 5.5–5.7, 6.1–6.4_
  - _Design rules: D2, D5, D6, D14_
  - _Boundary: public metering contracts and metering domain policy_
  - _Depends: 3.1_
  - _Validation: public fact/quantity tests, identity property tests, and fuzzing_

- [ ] 3.3 Persist customer-request and operator-attempt ingress facts
  - Persist trusted-scope frontend ingress before request authority and final backend ingress before rating/attempt authority.
  - Use one customer stream per logical request and one operator stream per attempt.
  - Pass durable fact references into rating and reservation inputs; fail closed when strict required ingress evidence cannot persist.
  - **Deliverable:** restart reconstruction contains original frontend input and final backend input for every metered lifecycle.
  - _Requirements: 2.4, 5.1, 5.2, 5.5–5.8_
  - _Design rules: D2, D3, D6, D9, D14_
  - _Boundary: core runtime checkpoint/fact producers and metering recorder_
  - _Depends: 1.4, 3.2_
  - _Validation: runtime ingress persistence, scope binding, rating-reference, and failure-posture tests_

- [ ] 3.4 Implement metering schema V2 and deterministic correction aggregation
  - Add composite store-scoped identity, identity/revision columns, supersession relation, bounded indexes, and additive direct/admin migrations.
  - Update append conflict resolution and queries to include `store_id`.
  - Apply cumulative, correction, and authoritative replacement semantics without erasing unrelated components or immutable history.
  - **Deliverable:** direct and pooled stores produce identical deterministic aggregates under replay and correction.
  - _Requirements: 6.2–6.9, 11.3–11.5_
  - _Design rules: D6, D7, D12_
  - _Boundary: metering journal driven adapters and migration helpers_
  - _Depends: 3.1–3.3_
  - _Validation: migration, append-race, correction, restart, direct PostgreSQL, and pooled PostgreSQL tests_

- [ ] 3.5 Add compatibility projections, bounded queries, and restart reconstruction
  - Preserve legacy token-ledger/control-plane views where representable and mark historical rows without ingress facts explicitly incomplete.
  - Add indexed filters for perspective, boundary, lifecycle, correlation, time, source, authority, and identity version.
  - Reconstruct customer usage, operator usage/cost, routing overhead, and compression-savings inputs from facts alone.
  - **Deliverable:** operators can query complete V2 streams without silent legacy reinterpretation or unbounded scans.
  - _Requirements: 1.9, 5.8, 6.8, 11.2–11.4, 12.5–12.7_
  - _Design rules: D6, D7, D14, D15, D16_
  - _Boundary: metering reconciliation, compatibility projections, control-plane query seams_
  - _Depends: 3.4_
  - _Validation: bounded query, legacy compatibility, restart reconstruction, and economics-report input tests_

## Phase 4 — Centralize Terminal Ownership and Durable Economic Recovery

- [ ] 4.1 Define red terminal and terminal-work state-machine contracts
  - Model request and attempt terminal states plus commands for normal finish, partial/error, cancellation, close, timeout, gate replacement, parallel loser, frontend encoder failure, pre-backend denial, and panic.
  - Model one durable action per fact/provider/lease operation, stable source keys, retries, claims, completion, and quarantine.
  - Add interleaving/model tests before changing stream behavior or schemas.
  - **Deliverable:** every terminal exit and action transition has an executable red contract.
  - _Requirements: 7.1–7.8, 8.1–8.9, 13.1, 13.4, 13.8_
  - _Design rules: D8, D9, D13, D17_
  - _Boundary: terminal domain/application contracts and tests_
  - _Depends: Phase 3_
  - _Validation: focused state-machine/model tests and concurrent terminal race reproducer_

- [ ] 4.2 Implement one request/stream terminal owner
  - Add one CAS-owned terminal result per logical request and per attempt; delegate existing lifecycle settlement/release to the owner.
  - Make `Recv`, `Close`, cancellation, errors, encoder failure, and panic paths signal or await terminalization rather than execute competing accounting.
  - Snapshot accumulators once and preserve per-provider partial completion and no-retry-after-output.
  - **Deliverable:** concurrent terminal callers cannot race on event slices, duplicate facts, settle/release twice, or leak lease state.
  - _Requirements: 7.1–7.8, 13.4, 13.7_
  - _Design rules: D8, D13, D17_
  - _Boundary: core runtime streaming, request/attempt lifecycle, and error handling_
  - _Depends: 4.1_
  - _Validation: focused `Recv`/`Close` race, cancel, encoder, gate, failover, parallel, and panic tests under `-race`_

- [ ] 4.3 Implement terminal-work domain and durable stores
  - Add versioned per-action work identity/payload/state, durable intent, provider/fact/lease correlation, claim leases, retry schedule, and quarantine.
  - Implement memory, SQLite, direct PostgreSQL, and transaction-pool-safe PostgreSQL stores with additive migrations and bounded queries.
  - **Deliverable:** required terminal intent survives process exit and is idempotent under replay or ambiguous commit.
  - _Requirements: 8.1–8.5, 8.7, 8.9, 11.3–11.5_
  - _Design rules: D6, D9, D12, D14_
  - _Boundary: terminal-work domain/app plus driven store adapters_
  - _Depends: 4.1, 3.4_
  - _Validation: store contracts, migration, claim contention, ambiguous commit, restart, and pooled PostgreSQL tests_

- [ ] 4.4 Implement the bounded terminal-work processor and provider router
  - Create durable intent before required external/separately durable effects, invoke with the same idempotency key, and mark completion independently per action/provider.
  - Resolve stable provider IDs, bound global/per-provider concurrency, renew claims, back off retries, quarantine permanent invalid work, and own startup/shutdown.
  - **Deliverable:** facts, settlements, releases, compensation, lease release, and corrections recover without repeating completed providers.
  - _Requirements: 8.1–8.9, 9.5, 12.8_
  - _Design rules: D4, D5, D9, D10, D12, D14_
  - _Boundary: terminal-work application processor, provider router, runtimebundle ownership_
  - _Depends: 2.2–2.4, 4.3_
  - _Validation: fault injection for timeout, panic, outage, crash, partial completion, restart, missing provider, and shutdown_

- [ ] 4.5 Integrate truthful live state, queries, readiness, and fault recovery
  - Mark holds/leases complete only after successful action or accepted durable intent; retain pending/quarantine state instead of ignoring errors.
  - Add bounded terminal-work queries, backlog/oldest-age metrics, readiness degradation, and operator-safe error codes.
  - Prove post-output failures preserve output and eventually converge after restart.
  - **Deliverable:** no terminal failure disappears when the live request object is gone.
  - _Requirements: 7.4, 7.7, 7.8, 8.3, 8.7–8.9, 12.1–12.8_
  - _Design rules: D8, D9, D14_
  - _Boundary: runtime integration, control-plane query/readiness, metrics_
  - _Depends: 4.2–4.4_
  - _Validation: runtime restart/failure tests, query bounds, readiness reports, metrics, and privacy assertions_

## Phase 5 — Publish Executable Immutable Generations

- [ ] 5.1 Add red generation behavior and lifetime tests
  - Prove current metadata-only refresh cannot be accepted as behavioral enforcement.
  - Define tests for five-to-two concurrency refresh, rating change, failed refresh, in-flight binding, settlement versions, pending-work provider resolution, and incompatible provider removal.
  - **Deliverable:** executable generation and lifetime expectations fail before new composition exists.
  - _Requirements: 9.1–9.9, 13.1, 13.5_
  - _Design rules: D4, D9, D10, D17_
  - _Boundary: snapshot/generation, runtime, runtimebundle, and public-facade tests_
  - _Depends: Phase 4_
  - _Validation: focused snapshot generation and end-to-end behavior tests_

- [ ] 5.2 Add public executable generation contributions and static compilation
  - Add public generation contribution/source contracts carrying descriptor-bound authorities and customer/operator raters.
  - Compile static YAML authority, concurrency, and reference rating configuration into the same immutable contribution shape.
  - Validate all registrations, versions, readiness, and required components before publication.
  - **Deliverable:** static and external policy use one public provider-neutral generation path.
  - _Requirements: 3.1–3.7, 9.1, 9.2, 11.6_
  - _Design rules: D4, D5, D10, D15_
  - _Boundary: public economics/authority/runtime contracts and composition compiler_
  - _Depends: 2.2–2.5, 5.1_
  - _Validation: public source/registration tests, static compiler tests, and separate enterprise-module fixture_

- [ ] 5.3 Build, validate, publish, and bind executable generations
  - Construct immutable request/attempt coordinators, concurrency registration, and raters in each generation.
  - Publish only complete required generations; retain the previous executable generation on refresh failure.
  - Bind one generation after trusted scope and before request authority, and use it for attempts and terminal work.
  - **Deliverable:** changing a source changes actual new-request enforcement without mutating in-flight requests.
  - _Requirements: 9.1–9.7, 12.1, 12.2_
  - _Design rules: D4, D5, D10, D13_
  - _Boundary: snapshotgen domain, core runtime binding, runtimebundle publication_
  - _Depends: 5.2_
  - _Validation: five-to-two, rating-change, failure-preservation, request/attempt/settlement binding tests_

- [ ] 5.4 Preserve old-generation and pending-handle compatibility
  - Keep live generation references reachable and route pending work through stable provider IDs.
  - Require same-ID providers to settle historical handles; use a new ID for incompatible replacement and block removal while pending work references the old ID.
  - Expose unresolved provider-generation references in readiness and queries.
  - **Deliverable:** refresh/restart does not strand live reservations or terminal work.
  - _Requirements: 8.2, 8.6, 9.5, 9.8, 9.9, 11.8, 12.1_
  - _Design rules: D4, D9, D10, D14_
  - _Boundary: generation lifetime, provider router, terminal-work readiness_
  - _Depends: 4.4, 5.3_
  - _Validation: old-provider drain, incompatible replacement, restart, missing provider, and readiness tests_

- [ ] 5.5 Migrate snapshot APIs and expose accurate generation readiness
  - Deprecate metadata-only policy publication as an enforcement path while preserving additive compatibility views.
  - Report executable generation ID/version/state separately from source fetch state and terminal provider-resolution state.
  - Update public facade/docs/examples without exposing internal coordinator types.
  - **Deliverable:** evidence names the evaluator objects that made decisions, not unrelated metadata labels.
  - _Requirements: 9.6–9.9, 11.2, 11.6, 12.1–12.3_
  - _Design rules: D10, D14, D15, D16_
  - _Boundary: public runtime facade, control-plane readiness, compatibility/docs_
  - _Depends: 5.3, 5.4_
  - _Validation: public API compatibility, readiness, generation evidence, architecture, and docs/config tests_

## Phase 6 — Finalize Strict Distributed Concurrency

- [ ] 6.1 Add red lease-set, timing, and renewal-loss contracts
  - Define store/service tests for atomic multi-rule acquire/renew/release, replay, deterministic lock order, `renew_before < lease_ttl`, external result validation, uncertain occupancy, strict cancellation before expiry, rollback failure, and auxiliary inheritance.
  - **Deliverable:** existing sequential/per-lease behavior fails the new strict contracts.
  - _Requirements: 10.1–10.10, 13.1, 13.6–13.8_
  - _Design rules: D5, D11, D12, D17_
  - _Boundary: concurrency domain/app/store contract tests_
  - _Depends: Phase 5_
  - _Validation: focused concurrency service/store/heartbeat tests and deterministic-clock state models_

- [ ] 6.2 Implement atomic lease-set acquire, renew, and release
  - Add one set identity/generation/state and complete-set commands.
  - Implement targeted atomic mutations for memory, SQLite, direct PostgreSQL, and transaction-pooled PostgreSQL with deterministic key ordering and bounded expiry cleanup.
  - Migrate legacy rows as one-member sets without reinterpreting history.
  - **Deliverable:** a multi-rule request is either fully occupied/renewed/released or unchanged.
  - _Requirements: 10.3–10.5, 10.8–10.10, 11.3–11.5_
  - _Design rules: D6, D11, D12_
  - _Boundary: concurrency domain/app and lease-store driven adapters_
  - _Depends: 6.1_
  - _Validation: store contracts, migration, contention, replay, direct/pooled PostgreSQL tests_

- [ ] 6.3 Enforce strict heartbeat and ambiguous-renewal behavior
  - Validate timing and returned set shape, renew the complete set atomically, and conservatively count ambiguous occupancy.
  - For fail-closed strict rules, cancel and terminalize early enough to avoid continuing beyond unproven expiry; keep fail-open explicit and degraded.
  - **Deliverable:** a live strict request cannot become invisible to capacity accounting after renewal failure.
  - _Requirements: 10.1, 10.2, 10.5–10.7, 12.8_
  - _Design rules: D5, D8, D11, D13_
  - _Boundary: core runtime heartbeat/terminal integration and concurrency provider_
  - _Depends: 4.2, 6.2_
  - _Validation: fake-clock renewal, ambiguous commit, partition, cancellation-before-expiry, and race tests_

- [ ] 6.4 Route lease-set release and rollback through terminal work
  - Create one durable set-release/rollback action; do not mark capacity released until the store confirms it.
  - Reconcile pending/uncertain sets after restart and expose active, uncertain, expiring, released, and failed state through bounded queries.
  - **Deliverable:** lease cleanup failure cannot be ignored or permanently leak/over-admit capacity.
  - _Requirements: 8.1–8.9, 10.7–10.9, 12.6–12.8_
  - _Design rules: D8, D9, D11, D14_
  - _Boundary: terminal-work/concurrency integration, queries, readiness_
  - _Depends: 4.3–4.5, 6.2, 6.3_
  - _Validation: release outage, restart, uncertain occupancy, bounded query, and readiness tests_

- [ ] 6.5 Prove five-slot behavior across instances and failures
  - Run high-contention admission across at least two proxy/service instances with five slots and multiple matching rules.
  - Cover process crash, database outage, pooler operation, renewal loss, terminal release failure, auxiliary calls, retries, and parallel B-legs.
  - **Deliverable:** no execution admits more than five proven top-level logical requests, and capacity recovers only through valid release/reconciliation.
  - _Requirements: 10.3–10.10, 13.6, 13.10_
  - _Design rules: D11, D12, D13_
  - _Boundary: multi-instance integration and contention benchmarks_
  - _Depends: 6.2–6.4_
  - _Validation: direct/pooled PostgreSQL multi-instance tests and five-slot benchmarks_

## Phase 7 — Certify Production Readiness

- [ ] 7.1 Build the cross-protocol economic conformance matrix
  - Prove equivalent frontend-ingress and final frontend-egress customer semantics for OpenAI Responses, OpenAI Chat, Anthropic Messages, Gemini, and every supported operation with legal canonical representation.
  - Cover streaming and non-streaming collection, protocol errors, cancellation, and frontend encoding failure.
  - **Deliverable:** protocol adapters cannot change customer economic boundary meaning.
  - _Requirements: 1.2, 1.3, 5.1, 5.4–5.7, 13.2, 13.9_
  - _Design rules: D2, D13, D14_
  - _Boundary: conformance harness, frontend plugins, canonical runtime tests_
  - _Depends: Phases 1–6_
  - _Validation: `make parity-checks` plus dedicated dual-plane conformance rows_

- [ ] 7.2 Add dedicated race, fuzz, state-machine, and fault-injection gates
  - Add strict race suites for terminal owners, accumulators, workers, stores, generation publication, and heartbeats.
  - Add fuzz/model targets for facts/corrections, external results, money/currency, work transitions, and lease sets.
  - Add deterministic fault campaigns for panic, timeout, malformed result, outage, ambiguous success, process crash, restart, and partial completion.
  - **Deliverable:** cross-cutting lifecycle behavior is explored beyond example tests.
  - _Requirements: 13.3, 13.4, 13.7, 13.8_
  - _Design rules: D5, D6, D7, D8, D9, D11, D17_
  - _Boundary: tests, fuzz corpus, QA/release scripts_
  - _Depends: Phases 1–6_
  - _Validation: targeted fuzz/model commands, Linux strict race, fault suites, and goleak_

- [ ] 7.3 Add performance budgets, operational metrics, and readiness alerts
  - Benchmark disabled/no-feature path, independent principals, hot identities, fact append/replay, terminal-work claims/dispatch, generation refresh, and five-slot contention.
  - Add bounded metrics and alert recommendations for authority/rating latency, terminal backlog/age, quarantine, generation staleness, lease uncertainty, renewal failures, and store contention.
  - **Deliverable:** overhead and degraded economic-control posture are measurable before rollout.
  - _Requirements: 12.1–12.9, 13.10_
  - _Design rules: D12, D14, D16_
  - _Boundary: metrics, readiness, benchmarks, operator docs_
  - _Depends: Phases 1–6_
  - _Validation: focused metrics/readiness tests and repeated benchmarks suitable for `benchstat`_

- [ ] 7.4 Complete migration, rollout, rollback, and open-core verification
  - Add direct/admin migrations, pooled runtime schema verification, identity-version compatibility, legacy projections, provider-drain checks, and rollback that continues terminal-work draining.
  - Update examples and operator documentation for `EconomicControlReady`, local versus distributed posture, migration ordering, stop conditions, and non-goals.
  - Re-run the separate enterprise module using public contracts only and verify no proprietary financial logic enters OSS.
  - **Deliverable:** deployment and rollback do not reinterpret or strand existing state.
  - _Requirements: 11.1–11.8, 12.3, 13.6_
  - _Design rules: D12, D14, D15, D16_
  - _Boundary: migrations, runtimebundle, public facade, architecture tests, docs_
  - _Depends: Phases 3–6_
  - _Validation: migration/direct/pooled gates, enterprise fixture, architecture guards, check-config, and rollout rehearsal_

- [ ] 7.5 Run the clean-environment certification matrix and archive evidence
  - Run focused phase suites, `make quality-checks`, `make test`, `make parity-checks`, Linux strict race, required PostgreSQL migration/direct/pooled gates, dedicated fuzz smoke, enterprise-module compile/run, benchmarks, and `make qa` from a clean environment.
  - Record exact commands, environment posture, failures/remediations, and unresolved limitations; do not claim commercial billing readiness.
  - **Deliverable:** all mandatory gates pass and the archived implementation spec can truthfully mark the OSS foundation `EconomicControlReady`.
  - _Requirements: 13.1–13.12_
  - _Design rules: D12–D17_
  - _Boundary: release/QA evidence and spec completion_
  - _Depends: 7.1–7.4_
  - _Validation: complete clean-environment certification matrix_
