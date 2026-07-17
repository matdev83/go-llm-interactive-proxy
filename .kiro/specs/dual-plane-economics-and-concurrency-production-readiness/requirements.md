# Requirements Document

**Source context:** Post-implementation review of the merged dual-plane economics and concurrency foundation rooted in PR #128, specified by PR #130, and implemented through PRs #133, #134, #135, #141, #142, #143, #144, and #145.

## Introduction

The `dual-plane-economics-and-concurrency-production-readiness` feature completes the backend and core-architecture hardening needed before the existing metering, rating, authority, and concurrency foundation can support strict enterprise economic controls.

The repository already contains dual-plane vocabulary, usage authority, request and attempt coordinators, metering checkpoints, public extension contracts, renewable concurrency leases, versioned snapshot metadata, durable stores, transaction-pooled PostgreSQL support, and bounded operator queries. This feature preserves those assets while correcting remaining semantic, durability, lifecycle, and extension-boundary gaps.

The work is restricted to the open-source proxy core. Proprietary customer offers, wallets, credit-account business rules, payments, invoices, tax, and commercial reporting require a separate closed enterprise specification.

## Boundary Context

- **In scope**: customer/operator evidence separation; final-exposure rating; provider posture; durable metering; terminal lifecycle ownership; retry/recovery; runtime policy refresh; distributed concurrency hardening; migration, readiness, and release evidence.
- **Out of scope**: proprietary pricebooks, wallets, payment collection, invoices, tax, GUI, SSO/SAML/SCIM, compression algorithms, security/content-policy engines, and provider marketplace settlement.
- **Adjacent expectations**: preserve existing usage-authority transactionality, B2BUA lineage, secure-session authority, immutable attempt derivation, routing/failover, streaming-first execution, PostgreSQL pooling, and no retry after first visible output.
- **Boundary ownership**: provider-neutral contracts belong in public SDK packages; lifecycle orchestration belongs in core; technical persistence belongs in infrastructure; commercial logic remains external.
- **Revalidation triggers**: authority DTOs; metering boundaries; stream termination; routing; snapshots; concurrency leases; durable schemas; readiness; protocol conformance.

## Requirements

### Requirement 1: Independent Economic Evidence
**Objective:** As a platform operator, I want customer-side usage and charge evidence separated from operator-side provider liability, so transformations and routing cannot contaminate either perspective.

#### Acceptance Criteria
1.1. The proxy shall represent customer logical-request evidence and operator backend-attempt evidence as independent lifecycle objects.
1.2. When a logical request is admitted, the proxy shall derive customer input usage from the frontend-ingress request basis.
1.3. When output is released, the proxy shall derive customer output usage from client-visible canonical output.
1.4. When a backend attempt is authorized, the proxy shall derive operator input exposure from the final backend-bound request.
1.5. When a backend attempt terminates, the proxy shall retain operator usage and cost when provider work may have been incurred.
1.6. The proxy shall not copy provider-reported cost into customer-perspective evidence.

### Requirement 2: Final Exposure and Rating
**Objective:** As a strict-limit implementer, I want authorization based on the final exposure submitted upstream, so later mutation cannot invalidate reservations.

#### Acceptance Criteria
2.1. Before operator rating and attempt admission, the proxy shall complete request transforms, candidate shaping, hooks, route parameters, clamps, and final provider-neutral assembly.
2.2. The operator rating request shall contain backend-ingress quantities and a conservative future-output assumption.
2.3. If a later mutation can widen exposure, the proxy shall reject it or remeasure and reauthorize before backend work.
2.4. Every strict reservation shall retain the perspective, boundary, lifecycle, rating version, and rule version used at admission.
2.5. If a required rater fails before protected work, the proxy shall deny admission.
2.6. If future output is unknown, the proxy shall apply an explicit bounding policy rather than zero.

### Requirement 3: Public Provider Posture
**Objective:** As an enterprise module author, I want injected providers to retain declared identity and posture, so runtime execution is deterministic.

#### Acceptance Criteria
3.1. Every injected authority shall have a stable public descriptor.
3.2. The composition root shall reject empty or duplicate provider IDs and incompatible stage declarations.
3.3. Request and attempt authorities shall run in deterministic priority order.
3.4. Advisory providers shall not deny traffic.
3.5. Fail-open posture shall apply to infrastructure failures rather than deterministic required-authority exhaustion.
3.6. The public runtime facade shall preserve provider posture into production composition.

### Requirement 4: External Result and Money Validation
**Objective:** As a core maintainer, I want external results validated, so malformed extensions cannot corrupt authority state or money.

#### Acceptance Criteria
4.1. The proxy shall validate external decisions, reservations, settlements, ratings, and leases before applying them.
4.2. Reservation handles shall be non-empty and unique within a provider decision.
4.3. Ordinary quantities and money shall be nonnegative.
4.4. Present money shall require normalized currency and checked arithmetic.
4.5. Explicit zero rate and zero cost shall remain distinct from absence.
4.6. Mixed currencies shall not be combined without explicit conversion.
4.7. The static reference catalog shall reject invalid configured rates.

### Requirement 5: Four-Boundary Durable Metering
**Objective:** As an auditor, I want durable facts at every economic boundary, so a request can be reconstructed after restart without raw content.

#### Acceptance Criteria
5.1. The proxy shall persist frontend-ingress and frontend-egress facts for metered logical requests.
5.2. The proxy shall persist backend-ingress and backend-egress facts for committed backend attempts.
5.3. Frontend facts shall use customer perspective and logical-request lifecycle; backend facts shall use operator perspective and attempt lifecycle.
5.4. Facts shall carry safe lifecycle, scope, route, source, authority, presence, and version correlation where known.
5.5. Default fact persistence shall not require raw content or credentials.
5.6. The journal shall remain a technical metering journal rather than a customer financial ledger.

### Requirement 6: Fact Identity and Correction
**Objective:** As a reconciler, I want idempotent fact identity and correction rules, so retries and restarts cannot double-count evidence.

#### Acceptance Criteria
6.1. Every source event shall produce a stable identity.
6.2. Replaying the same fact shall be a no-op, while conflicting content for the same identity shall be rejected.
6.3. Correction and authoritative-replacement facts shall identify superseded facts.
6.4. Aggregation shall apply delta, cumulative, correction, and replacement semantics deterministically.
6.5. Corrections shall append history rather than mutate prior facts.

### Requirement 7: Single Terminal Owner
**Objective:** As a runtime maintainer, I want one synchronized owner for finalization, so concurrent exits cannot double-settle or leak state.

#### Acceptance Criteria
7.1. `Recv`, `Close`, cancellation, error, timeout, and panic paths shall compete for one terminalization claim.
7.2. Only the terminal owner shall finalize usage, persist facts, settle or release authority, and release concurrency occupancy.
7.3. Concurrent callers shall observe the owner result without re-running effects.
7.4. Terminal state shall distinguish open, terminalizing, pending, settled, released, and failed outcomes.
7.5. If output is committed, terminal failure shall not trigger retry or replacement.
7.6. Cleanup shall use fresh bounded contexts independent of client cancellation.

### Requirement 8: Durable Terminal Recovery
**Objective:** As an operator, I want terminal side effects recoverable after request exit or restart, so facts, holds, and leases are not silently lost.

#### Acceptance Criteria
8.1. Required fact append, settlement, release, compensation, and correction work shall have durable retry representation when not completed inline.
8.2. Every work record shall have stable identity, lifecycle correlation, state, retry metadata, and bounded error classification.
8.3. The runtime shall not mark a hold released before the owning operation succeeds or retry work is recorded.
8.4. A bounded processor shall claim due work safely across multiple instances.
8.5. Replaying work after timeout, crash, or restart shall not duplicate completed effects.
8.6. Permanently malformed work shall become operator-visible rather than disappear.
8.7. Startup and shutdown shall explicitly own processor lifecycle.

### Requirement 9: Immutable Runtime Generations
**Objective:** As an operator, I want policy refresh to change enforcement for new requests while preserving in-flight consistency.

#### Acceptance Criteria
9.1. A runtime generation shall contain immutable authority and rating policy material.
9.2. The composition root shall validate a complete required generation before publication.
9.3. Each logical request shall bind one generation before authority admission.
9.4. Settlement and release shall retain versions bound at admission.
9.5. A failed refresh shall leave the prior generation active and expose degraded readiness.
9.6. A successful refresh shall affect new admissions without mutating in-flight requests.
9.7. Evidence shall identify the generation version used for a decision.

### Requirement 10: Strict Distributed Concurrency
**Objective:** As an administrator, I want active-request limits correct under contention, renewal failure, crash, and multiple instances.

#### Acceptance Criteria
10.1. A concurrency rule shall require positive lease TTL and renew-before values.
10.2. External lease results shall carry valid identity, generation, and expiry.
10.3. Where multiple strict rules match, acquisition shall not leave partial occupancy after denial.
10.4. One logical request shall occupy one concurrency lifecycle across retries and parallel attempts.
10.5. Admission, renewal, and release shall be idempotent across instances.
10.6. Renewal failure shall follow configured fail-open or fail-closed posture.
10.7. Release and rollback failures shall remain queryable.
10.8. A five-slot rule shall allow at most five active logical requests across multiple instances.

### Requirement 11: Compatibility and Open-Core Boundary
**Objective:** As a maintainer, I want additive migration and public-only enterprise seams, so hardening ships without a rewrite or proprietary leakage.

#### Acceptance Criteria
11.1. Existing usage-authority transactionality and bounded-query guarantees shall be preserved.
11.2. Existing deployments shall remain functional when new capabilities are disabled.
11.3. Durable schema changes shall use additive migrations and explicit verification.
11.4. PostgreSQL runtime operations shall remain transaction-pool compatible.
11.5. A separate enterprise module shall implement providers and raters through public packages only.
11.6. Proprietary commercial logic shall remain outside this repository.
11.7. Rollback shall not strand admitted requests or pending terminal work.

### Requirement 12: Readiness, Privacy, and Operations
**Objective:** As an operator, I want accurate readiness and safe observability, so protected traffic is served only when required controls are trustworthy.

#### Acceptance Criteria
12.1. Readiness shall report metering, authority, rating, policy refresh, terminal recovery, concurrency, and stores independently.
12.2. Aggregate readiness shall fail closed when a required pre-work control cannot enforce.
12.3. Post-output pending work shall remain visible without altering committed output.
12.4. Metrics and logs shall use bounded labels and exclude raw content, credentials, and balances.
12.5. Privileged queries shall distinguish facts, reservations, leases, pending work, and proprietary financial projections.
12.6. Queries shall be bounded, indexed, paginated, and reject too-broad filters.
12.7. Memory and SQLite shall not be reported as distributed strict enforcement.

### Requirement 13: TDD and Release Certification
**Objective:** As a maintainer, I want executable contracts and fault evidence before rollout, so combined behavior is proven rather than inferred.

#### Acceptance Criteria
13.1. Each phase shall begin with failing contract or regression tests before production behavior changes.
13.2. Tests shall cover transformations, filtering, retries, failover, parallel losers, and authoritative zero/absence.
13.3. Tests shall cover malformed providers, posture, compensation, currency, overflow, and rounding.
13.4. Tests shall cover concurrent `Recv`/`Close`, cancellation, crash, restart, and partial completion.
13.5. Tests shall cover policy refresh changing new-request behavior while old requests retain prior behavior.
13.6. Tests shall cover direct and transaction-pooled PostgreSQL with multiple instances.
13.7. Race tests shall cover terminalization, workers, stores, generations, and heartbeats.
13.8. Cross-protocol tests shall prove equivalent frontend-boundary customer semantics.
13.9. Release validation shall include focused tests, quality checks, default tests, parity, Linux race, PostgreSQL gates, enterprise-module compilation, and full QA.
