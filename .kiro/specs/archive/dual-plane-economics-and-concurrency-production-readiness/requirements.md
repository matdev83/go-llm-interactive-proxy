# Requirements Document

**Source context:** Post-implementation review of the merged dual-plane economics and concurrency foundation rooted in PR #128, specified by PR #130, and implemented through PRs #133, #134, #135, #141, #142, #143, #144, and #145.

## Introduction

The `dual-plane-economics-and-concurrency-production-readiness` feature completes the backend and core-architecture hardening needed before the existing metering, rating, authority, and concurrency foundation can support strict enterprise economic controls.

The current repository already contains valuable dual-plane vocabulary, fixed-window usage authority, request and attempt coordinators, metering checkpoints, public extension contracts, renewable concurrency leases, versioned snapshot metadata, durable stores, transaction-pooled PostgreSQL support, and bounded operator queries. This feature preserves those assets while correcting remaining semantic, durability, lifecycle, and extension-boundary gaps.

The work is restricted to the open-source proxy core. Proprietary customer offers, wallets, credit-account business rules, payments, invoices, tax, and commercial reporting require a separate closed enterprise specification.

## Boundary Context

- **In scope**: customer/operator evidence separation; exact final-exposure rating; provider registration and validation; durable four-boundary metering; terminal lifecycle ownership; terminal retry/recovery; executable policy generations; distributed concurrency hardening; migration, readiness, privacy, performance, and release evidence.
- **Out of scope**: proprietary pricebooks, wallets, payment collection, invoices, tax, GUI, SSO/SAML/SCIM, compression algorithms, security/content-policy engines, and provider marketplace settlement.
- **Adjacent expectations**: preserve existing usage-authority transactionality, B2BUA lineage, secure-session authority, immutable attempt derivation, routing/failover semantics, streaming-first execution, PostgreSQL pooling, and no retry after first visible output.
- **Boundary ownership**: public provider-neutral contracts belong in `pkg/lipsdk` and `pkg/lipruntime`; lifecycle orchestration belongs in `internal/core`; technical persistence belongs in `internal/infra`; proprietary commercial logic remains external.
- **Revalidation triggers**: request and attempt authority DTOs; metering boundaries; stream termination; routing and parallel races; snapshots; concurrency leases; durable schemas; readiness; cross-protocol conformance.

## Requirements

### Requirement 1: Independent Economic Evidence
**Objective:** As a platform operator, I want customer-side usage and charge evidence separated from operator-side provider liability, so transformations and routing cannot contaminate either perspective.

#### Acceptance Criteria
1.1. The proxy shall represent customer logical-request evidence and operator backend-attempt evidence as independent lifecycle objects.
1.2. When a logical request is admitted, the proxy shall derive customer input usage from the frontend-ingress request basis.
1.3. When output is released, the proxy shall derive customer output usage from client-visible canonical output.
1.4. When a backend attempt is authorized, the proxy shall derive operator input exposure from the final backend-bound request.
1.5. When a backend attempt terminates, the proxy shall retain operator usage and cost for surfaced, failed, canceled, swallowed, and parallel-losing attempts when provider work may have been incurred.
1.6. The proxy shall not copy provider-reported cost into customer-perspective evidence.
1.7. One top-level logical request shall settle customer authority once, while every committed backend attempt shall settle operator authority independently.
1.8. Where customer charge and operator cost are equal, the proxy shall still compute them independently.
1.9. The proxy shall retain sufficient safe evidence to calculate original input, transformed backend input, provider output, delivered output, routing overhead, and compression savings without retaining raw content.

### Requirement 2: Final Exposure and Rating
**Objective:** As a strict-limit implementer, I want authorization based on the final exposure submitted upstream, so later mutation cannot invalidate reservations.

#### Acceptance Criteria
2.1. Before operator rating and attempt admission, the proxy shall complete request transforms, candidate shaping, hooks, route parameters, clamps, and final provider-neutral assembly.
2.2. The operator rating request shall contain the complete backend-ingress quantity set and a conservative future-output assumption.
2.3. If a later mutation can widen exposure, the proxy shall reject it or remeasure, rerate, and reauthorize before backend work.
2.4. Every strict reservation shall retain the economic perspective, metering boundary, lifecycle scope, rating version, and rule version used at admission.
2.5. If a required rater fails or returns absent money before protected work, the proxy shall deny admission with a stable unavailable outcome.
2.6. If rating fails after client-visible output is committed, the proxy shall preserve output and retain retryable evidence.
2.7. If future output is unknown, the proxy shall apply an explicit require-limit, default, model/backend maximum, clamp, or deny policy; unknown output shall not mean zero.
2.8. The customer rater and operator rater shall be invoked independently.
2.9. The proxy shall preserve authoritative zero usage, zero rate, and zero cost independently from absent or unavailable values.

### Requirement 3: Public Provider Posture
**Objective:** As an enterprise module author, I want injected providers to retain their declared identity and posture, so the runtime executes them deterministically.

#### Acceptance Criteria
3.1. Every injected request, attempt, or concurrency authority shall have a stable public descriptor.
3.2. The composition root shall reject empty or duplicate provider IDs and incompatible stage/interface declarations.
3.3. Request authorities shall run in deterministic concurrency, credit, quota/budget/rate, and advisory order.
3.4. Attempt authorities shall run in deterministic hard-spend, quota/rate, and advisory order.
3.5. Where a provider is advisory, its denial shall become advisory evidence and shall not deny traffic.
3.6. Where a provider is fail-open, only infrastructure or execution failure may degrade open; deterministic exhaustion from a required authority shall not fail open.
3.7. The public runtime facade shall preserve provider IDs, strength, failure behavior, and ordering into production composition.
3.8. Usage, traffic, and policy observers shall remain observation-only.
3.9. The runtime shall not invent externally visible provider IDs from provider-list indexes when an explicit stable descriptor is required.

### Requirement 4: External Result and Money Validation
**Objective:** As a core maintainer, I want all external results treated as untrusted, so malformed extensions cannot corrupt authority state or money.

#### Acceptance Criteria
4.1. The proxy shall validate every external decision, reservation, clamp, settlement, rating result, lease decision, and renewal before applying it.
4.2. Reservation handles shall be non-empty, unique within one provider decision, and owned by the returning provider.
4.3. A reservation shall contain exactly one present quantity or money amount and shall not contain a negative ordinary amount.
4.4. A deny or advisory response that claims holds shall be rejected or compensated before coordinator processing continues.
4.5. A settlement shall reference only submitted handles and shall use valid settlement state.
4.6. Present money shall require normalized currency and checked arithmetic.
4.7. A rating result shall match the requested perspective and shall identify its rater and immutable version.
4.8. Explicit zero rate and zero cost shall remain distinct from absence.
4.9. Mixed currencies shall not be combined without an explicit versioned conversion.
4.10. The static reference catalog shall reject invalid or overflowing configured rates.

### Requirement 5: Four-Boundary Durable Metering
**Objective:** As an auditor, I want durable facts at every economic boundary, so a request can be reconstructed after restart without raw content.

#### Acceptance Criteria
5.1. The proxy shall persist one frontend-ingress fact for every metered logical request.
5.2. The proxy shall persist one backend-ingress fact for every committed backend attempt.
5.3. The proxy shall persist backend-egress facts for every attempt with available usage or cost evidence.
5.4. The proxy shall persist frontend-egress facts for delivered or legally committed client-visible usage.
5.5. Frontend facts shall use the customer perspective and logical-request lifecycle; backend facts shall use the operator perspective and backend-attempt lifecycle.
5.6. Facts shall carry safe request, A-leg, B-leg, attempt, frontend, backend, model, scope, source, authority, presence, and version correlation where known.
5.7. Default fact persistence shall not require raw prompts, responses, credentials, headers, or provider payloads.
5.8. The journal shall remain a technical metering journal rather than a customer financial ledger.

### Requirement 6: Fact Identity and Correction
**Objective:** As a reconciler, I want deterministic fact identity and correction rules, so retries and restarts cannot double-count or silently alter evidence.

#### Acceptance Criteria
6.1. Every source event shall produce a deterministic identity stable across retries and restarts.
6.2. Journal uniqueness and lookup shall be scoped by logical store identity.
6.3. Replaying the same semantic fact shall be a no-op, while a conflicting payload for the same identity shall be rejected.
6.4. Ordinary non-correction facts shall not contain negative quantities or money.
6.5. Correction and authoritative-replacement facts shall identify superseded facts.
6.6. A superseded fact shall exist in the same store and stream.
6.7. The journal shall reject cyclic supersession.
6.8. Aggregation shall apply delta, cumulative, correction, and replacement semantics deterministically.
6.9. Corrections shall append immutable history rather than mutate prior facts.

### Requirement 7: Single Terminal Owner
**Objective:** As a runtime maintainer, I want one synchronized owner for request and attempt finalization, so concurrent stream exits cannot double-settle or leak state.

#### Acceptance Criteria
7.1. `Recv`, `Close`, cancellation, error, timeout, and panic paths shall compete for one atomic terminalization claim.
7.2. Only the terminalization owner shall finalize usage, persist terminal facts, settle or release authority, and stop or release concurrency occupancy.
7.3. Concurrent terminal callers shall await or observe the owner result without re-running terminal effects.
7.4. Terminal state shall distinguish open, terminalizing, pending, settled, release-pending, released, and permanently failed outcomes.
7.5. If output is committed, terminal failure shall not trigger transparent retry or replacement.
7.6. Terminal cleanup shall use fresh bounded contexts independent of client cancellation.
7.7. The terminal owner shall preserve already completed provider settlements when another provider remains unfinished.
7.8. Terminal state transitions shall be queryable through safe operator evidence.

### Requirement 8: Durable Terminal Recovery
**Objective:** As an operator, I want required terminal side effects recoverable after request exit or process restart, so facts, holds, and leases are not silently lost.

#### Acceptance Criteria
8.1. Required post-admission fact append, settlement, release, compensation, and authoritative-correction work shall have durable idempotent representation when not completed inline.
8.2. Every terminal work item shall have a stable source key, work kind, lifecycle correlation, provider identity where applicable, bound versions, state, retry metadata, and bounded error classification.
8.3. The runtime shall not mark a reservation or lease released until the owning operation succeeds or durable retry work is accepted.
8.4. A bounded processor shall claim due work safely across multiple proxy instances.
8.5. Replaying work after timeout, ambiguous commit, worker crash, or process restart shall not duplicate completed effects.
8.6. Independent provider actions shall complete independently so one failed provider does not repeat another provider’s successful settlement.
8.7. Permanently malformed work shall be quarantined with operator-visible evidence rather than retried forever or discarded.
8.8. Startup and shutdown shall explicitly own processor lifecycle and drain/cancel behavior.
8.9. The proxy shall expose pending, retrying, oldest-age, completed, and quarantined work through bounded queries and metrics.

### Requirement 9: Executable Immutable Generations
**Objective:** As an operator, I want runtime policy refresh to change actual enforcement for new requests while preserving in-flight consistency.

#### Acceptance Criteria
9.1. A published runtime generation shall contain the actual immutable request authorities, attempt authorities, concurrency authority, raters, and policy data used by admission.
9.2. The composition root shall validate a complete required generation before publication.
9.3. Each logical request shall bind one generation before request authority admission.
9.4. Each backend attempt shall use the request-bound generation unless an explicitly designed attempt-time policy says otherwise.
9.5. Settlement, release, retry work, and correction shall retain the provider and version identities bound at admission.
9.6. A failed refresh shall leave the prior executable generation active and shall expose degraded or unavailable readiness.
9.7. A successful refresh shall affect new admissions without mutating in-flight requests.
9.8. Old generations or compatible provider resolution shall remain available until their live and pending terminal work is complete.
9.9. Version evidence shall identify the executable object that made the decision rather than a metadata-only label.

### Requirement 10: Strict Distributed Concurrency
**Objective:** As an administrator, I want maximum-active-request limits to remain correct under contention, renewal failure, crash, and multiple instances.

#### Acceptance Criteria
10.1. A concurrency rule shall require `0 < renew_before < lease_ttl` and bounded practical timing values.
10.2. External lease admissions and renewals shall return valid lease IDs, generations, expiries, TTL, and posture.
10.3. Where multiple strict concurrency rules match, acquisition shall be atomic for the complete lease set.
10.4. One logical request shall occupy one lease set across retries and parallel backend attempts.
10.5. Lease-set acquisition and replay shall be idempotent across proxy instances.
10.6. A strict request shall not continue beyond loss of proven occupancy merely because the last lease TTL expired.
10.7. On fail-closed renewal failure, the proxy shall cancel and terminalize before expiry or preserve a durable uncertain-but-occupied state that cannot be reclaimed as free capacity.
10.8. Lease-set release and rollback failures shall remain retryable and queryable.
10.9. Expiry cleanup shall be bounded and shall not erase unresolved terminal evidence.
10.10. A five-slot rule shall allow at most five active logical requests across multiple instances and shall recover capacity after release or proven expiry.

### Requirement 11: Compatibility and Open-Core Boundary
**Objective:** As a maintainer, I want additive migration and public-only enterprise seams, so hardening can ship without a rewrite or proprietary leakage.

#### Acceptance Criteria
11.1. Existing usage-authority transactional reservation, settlement, release, correction, and bounded-query guarantees shall be preserved.
11.2. Existing deployments shall remain functional when new hardening capabilities are disabled.
11.3. Durable schema changes shall use additive migrations and explicit schema verification.
11.4. Legacy facts, reservations, and leases shall use explicit compatibility namespaces or versions and shall not be silently reinterpreted.
11.5. PostgreSQL runtime operations shall remain compatible with transaction pooling and shall not apply migrations through pooled runtime connections.
11.6. A separately versioned enterprise module shall implement providers and raters through public packages only.
11.7. Proprietary offers, wallets, credit policy, payments, invoices, tax, and commercial analytics shall remain outside this repository.
11.8. Migration rollback shall not strand already admitted requests or pending terminal work.

### Requirement 12: Readiness, Privacy, and Operations
**Objective:** As an operator, I want accurate readiness and content-safe observability, so protected traffic is served only when required economic controls are trustworthy.

#### Acceptance Criteria
12.1. Readiness shall report metering, request authority, attempt authority, rating, generation, terminal recovery, concurrency, and durable stores independently.
12.2. Aggregate economic-control readiness shall fail closed when a required pre-work control cannot enforce its contract.
12.3. Aggregate readiness shall not claim payments, invoices, tax, or complete commercial billing readiness.
12.4. Post-output pending work shall degrade readiness and remain visible without altering committed output.
12.5. Metrics and logs shall use bounded-cardinality labels and shall not include raw content, credentials, balances, anchors, or arbitrary user-controlled strings.
12.6. Privileged queries shall distinguish historical facts, live reservations, active lease sets, pending terminal work, and proprietary financial projections.
12.7. Queries shall be bounded, indexed, paginated, and reject unsupported or too-broad filters.
12.8. The proxy shall expose latency, timeout, retry, contention, backlog, renewal, and validation-failure metrics by bounded stage/provider identity.
12.9. Local memory and SQLite postures shall not be reported as distributed strict enforcement.

### Requirement 13: TDD and Release Certification
**Objective:** As a maintainer, I want executable contracts and fault evidence before rollout, so the combined system is proven rather than inferred from isolated tests.

#### Acceptance Criteria
13.1. Each implementation phase shall begin with failing contract or regression tests before production behavior changes.
13.2. Tests shall cover compression, response filtering, retries, sequential failover, parallel losers, auxiliary calls, and authoritative zero/absence cases.
13.3. Tests shall cover malformed provider results, panics, advisory/required posture, compensation, mixed currency, overflow, and rounding.
13.4. Tests shall cover concurrent `Recv` and `Close`, cancellation, frontend encoding failure, process crash, ambiguous persistence, restart, and partial provider completion.
13.5. Tests shall cover generation refresh changing actual limits and ratings for new requests while old requests retain their generation.
13.6. Tests shall cover direct PostgreSQL and transaction-pooled PostgreSQL with multiple proxy instances.
13.7. Race tests shall cover stream terminalization, accumulators, workers, stores, generations, and heartbeats.
13.8. Fuzz or model-based tests shall cover facts/corrections, provider decisions, money/currency, terminal work, and lease sets.
13.9. Cross-protocol tests shall prove equivalent customer frontend-boundary semantics for all supported frontend operations.
13.10. Benchmarks shall cover disabled/no-feature overhead, independent principals, hot-account contention, terminal-work throughput, journal replay, and five-slot contention.
13.11. Release validation shall include focused tests, `make quality-checks`, `make test`, `make parity-checks`, Linux strict race, required PostgreSQL gates, dedicated fuzz smoke, enterprise-module compilation, and `make qa`.
13.12. The project shall not declare the OSS foundation production-ready until all mandatory gates pass from a clean environment.
