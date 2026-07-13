# Requirements Document

## Introduction

The `dual-plane-economics-and-concurrency-foundation` feature remediates and extends the current token-accounting, usage-authority, pricing, and limiting implementation so the proxy core can safely support two independent economic perspectives:

- **customer-side usage and charge authority**, based on traffic accepted from and delivered through proxy frontend interfaces; and
- **proxy-operator usage and cost authority**, based on traffic actually submitted to and billed by backend inference providers.

The two perspectives may be configured to produce identical values, but the architecture must never assume they are identical. Compression, request shaping, caching, retries, failover, parallel racing, auxiliary model calls, subsidies, markups, and custom offers can make them diverge.

This feature also adds logical-request concurrency authority, including limits such as a maximum of five active requests for one principal across multiple proxy instances. It is a backend and core-architecture specification only. It does not implement a web GUI, payments, invoicing, or proprietary enterprise rating and wallet logic.

## Boundary Context

- **In scope**: correction of current accounting semantics; immutable metering checkpoints; explicit economic perspective, lifecycle scope, and metering basis; idempotent usage facts; public enterprise injection contracts; request- and attempt-level authority orchestration; correct routing-attempt accounting; monetary presence and arithmetic hardening; dynamic versioned authority sources; distributed concurrent-request leases; durable metering and authority evidence; runtime and control-plane integration.
- **OSS core responsibility**: protocol-neutral metering facts, lifecycle timing, generic authority ports and coordination, safe scope attribution, a reusable concurrency authority, compatibility adapters for existing usage authority, and protected backend query/evidence surfaces.
- **Closed enterprise responsibility**: customer offers and pricebooks, provider commercial agreements, markups and discounts, wallet and credit business logic, double-entry financial ledger, payment collection, invoices, tax handling, white-label portal, proprietary usage analytics, and commercial optimization policy.
- **Out of scope**: frontend UI, web administration screens, customer portal, payment processors, invoice rendering, SSO/SAML/SCIM implementation, security/content policy engines, compression algorithms themselves, and cloud marketplace packaging.
- **Revalidation triggers**: frontend decode timing, request mutation stages, backend-open timing, stream mutation and completion gates, B2BUA attempt lineage, cancellation/finalization, parallel racing, token component semantics, price calculation, usage authority store transactions, runtime composition, control-plane schemas, and extension-platform import boundaries.

## Requirements

### Requirement 1: Explicit Economic Perspectives and Lifecycle Scopes
**Objective:** As a platform owner, I want every enforceable usage or monetary decision to declare whose economics and which lifecycle object it represents, so customer charges cannot be confused with provider costs.

#### Acceptance Criteria
1.1. The proxy shall represent customer-side charge/allowance authority and proxy-operator cost/spend authority as distinct economic perspectives.
1.2. The proxy shall represent logical client requests and individual backend attempts as distinct lifecycle scopes.
1.3. Every strict quota, budget, spend-cap, rate, or concurrency rule shall declare or deterministically inherit an economic perspective and lifecycle scope before it is enforceable.
1.4. A 1:1 pass-through deployment shall be modeled as two independently computed perspectives whose values happen to match, not as one shared monetary record.
1.5. The proxy shall reject or preserve under an explicit legacy mode any rule whose perspective or lifecycle scope is ambiguous; it shall not silently reinterpret existing state.
1.6. Query and evidence records shall expose economic perspective and lifecycle scope whenever known.
1.7. Provider-local quota headers or cooldowns shall not become customer-side authority without an explicit safe mapping.
1.8. Internal auxiliary model calls shall default to operator-cost scope and shall not consume another top-level customer logical-request concurrency slot unless explicitly configured.

### Requirement 2: Immutable Metering Checkpoints
**Objective:** As a billing and cost-control integrator, I want immutable usage checkpoints at the legal proxy boundaries, so later transformations cannot rewrite the original charge or cost basis.

#### Acceptance Criteria
2.1. The proxy shall capture a frontend-ingress canonical request snapshot after protocol decode and validation but before submit hooks, request transforms, compression, steering, route shaping, or backend-specific mutation.
2.2. The proxy shall capture a backend-ingress canonical attempt snapshot after all request-wide transforms, attempt hooks, route parameters, clamps, and provider-neutral shaping, immediately before protected backend open.
2.3. The proxy shall capture backend-egress usage and cost evidence separately for every committed backend attempt, including surfaced, swallowed, canceled, failed, and parallel-losing attempts when evidence is available.
2.4. The proxy shall capture frontend-egress usage from the final client-visible canonical event stream after response mutation and completion gating but before protocol encoding.
2.5. Checkpoint capture shall be side-effect free with respect to routing estimates and shall not create reservations during diagnostic or eligibility-only evaluation.
2.6. Checkpoint records shall carry stable request, A-leg, B-leg, attempt, principal/scope, frontend, backend, model, and timestamp correlation when those values are known.
2.7. The proxy shall not persist raw prompts, responses, headers, credentials, or provider payloads merely to satisfy metering; quantities and safe metadata shall be sufficient by default.
2.8. Every request shall use one immutable frontend-ingress snapshot even if later retries or route changes create multiple backend attempts.

### Requirement 3: Idempotent Metering Facts and Quantity Semantics
**Objective:** As an operator, I want usage evidence to have precise identity and correction semantics, so repeated, cumulative, partial, and authoritative reports cannot be double-counted.

#### Acceptance Criteria
3.1. Every persisted metering fact shall have a stable idempotency key derived from safe lifecycle and source identity, not from raw content.
3.2. A fact shall declare whether it is an additive delta, cumulative snapshot, correction, authoritative replacement, reservation estimate, or unavailable marker.
3.3. A correction or authoritative replacement shall identify the fact or fact stream it supersedes and shall apply only the required delta to aggregate state.
3.4. Replaying the same fact across retries, restarts, or proxy instances shall not double-count any quantity or monetary amount.
3.5. Multiple same-authority but non-identical cumulative provider reports shall be resolved by explicit sequence/correction semantics rather than first-arrival selection or unconditional summation.
3.6. Token component semantics shall define cache-read and cache-write tokens as input subcomponents and reasoning tokens as an output subcomponent unless a provider adapter explicitly declares different semantics.
3.7. When total tokens are absent, inferred totals shall not double-count cache or reasoning subcomponents already included in input or output totals.
3.8. Explicit zero values shall remain distinguishable from omitted values for every token component and monetary amount.
3.9. The quantity model shall be extensible beyond current token fields to support future billable components without adding a new top-level struct field for every provider innovation.

### Requirement 4: Customer-Side Usage and Charge Basis
**Objective:** As an inference service operator, I want customer usage and charge authority to use frontend-visible traffic, so transparent proxy transformations do not unintentionally change what the customer is billed for.

#### Acceptance Criteria
4.1. Customer input usage shall be derived from the immutable frontend-ingress request basis selected by the customer policy.
4.2. Customer output usage shall be derived from the final frontend-egress content actually delivered or legally committed to the client.
4.3. In-flight compression shall not reduce customer input usage unless the selected customer offer explicitly defines a compressed or proxy-adjusted billing basis.
4.4. Parallel losers, swallowed attempts, internal retries, and verifier/auxiliary calls shall not create additional customer charge usage unless the customer offer explicitly bills those operations.
4.5. Customer usage quotas and request-rate rules scoped to a logical request shall be evaluated once per top-level request, not once per backend B-leg.
4.6. Customer monetary authorization shall be independent of provider price catalogs and may use a closed enterprise rating implementation.
4.7. The core shall preserve enough transformation lineage to calculate customer-visible usage, backend usage, and compression savings without retaining raw content.
4.8. Customer-side strict decisions shall use safe frontend error mappings and shall not expose pricebook internals, balances, or privileged rule details.

### Requirement 5: Proxy-Operator Usage and Cost Basis
**Objective:** As the proxy operator, I want provider liability tracked per committed backend attempt, so routing overhead and transformed traffic are reflected in real cost control.

#### Acceptance Criteria
5.1. Operator input exposure shall be derived from the final backend-ingress attempt snapshot, not from the original frontend request when the two differ.
5.2. Operator output and monetary cost shall settle from provider-authoritative evidence when present and from explicitly classified estimates when authoritative evidence is unavailable.
5.3. Every committed backend attempt shall have an independent operator-cost lifecycle, including failover attempts and parallel losers.
5.4. Canceling or losing an attempt shall release only unused exposure; any usage or provider cost already incurred shall be settled rather than erased.
5.5. A backend attempt that fails before any protected provider work begins may release its complete operator reservation.
5.6. Operator cost controls shall be able to include auxiliary, verifier, compression-model, and other internal model calls even when those calls are not customer-billable.
5.7. The proxy shall preserve customer charge, total operator cost, routing overhead, and resulting gross-margin inputs as separately queryable facts.
5.8. Provider-reported cost and locally rated provider usage shall remain distinguishable and independently authoritative.

### Requirement 6: Rating and Monetary Correctness Foundation
**Objective:** As a financial-control implementer, I want a precise rating contract and safe money arithmetic, so strict monetary decisions cannot be corrupted by missing values, overflow, or ambiguous zero prices.

#### Acceptance Criteria
6.1. The core shall expose public, provider-neutral contracts through which a closed implementation can rate customer charge exposure and operator cost exposure independently.
6.2. Every rated amount shall carry currency, price/rate source, immutable version identity, effective timestamp, authority, and presence state.
6.3. An explicitly configured zero rate shall remain distinct from an unspecified rate and shall not trigger fallback pricing.
6.4. An authoritative zero provider cost shall remain distinct from an absent provider cost and shall not be replaced by an estimate.
6.5. Monetary multiplication, addition, subtraction, conversion to output limits, and aggregation shall use checked arithmetic and return a typed overflow or unsupported result instead of wrapping.
6.6. Rating shall define deterministic rounding behavior and shall record the rounding policy or rate-line identity used.
6.7. Currency values shall not be combined unless they are equal under normalized currency identity or an explicit conversion quote and version are supplied.
6.8. The OSS static provider-cost catalog may remain a basic adapter, but it shall implement the same presence, versioning, arithmetic, and error contracts as external raters.
6.9. Context tiers, time bands, fixed fees, markups, discounts, taxes, and customer offers shall be representable through closed raters without adding those proprietary business rules to OSS core.

### Requirement 7: Bounded Preflight Exposure and Reservation
**Objective:** As a budget owner, I want preflight reservations to conservatively bound possible liability, so concurrent requests cannot exceed prepaid balances or postpaid credit limits.

#### Acceptance Criteria
7.1. A strict monetary admission shall reserve a conservative maximum exposure before protected work starts.
7.2. Absence of a requested maximum output token value shall not be interpreted as zero future output exposure.
7.3. When output exposure cannot be bounded, policy shall explicitly choose to require a client limit, apply a configured cap, reserve a backend/model maximum, reserve a plan default, or deny.
7.4. A clamp shall be applied to the final backend-bound request and shall be verified as enforceable by the selected backend before protected work begins.
7.5. Any request mutation occurring after an exposure estimate shall trigger deterministic remeasurement and reauthorization or shall be prohibited from widening exposure.
7.6. Reservations shall retain the exact metering basis, rating version, rule version, and lifecycle scope used at admission.
7.7. Concurrent admissions against the same strict balance or limit shall be atomic across proxy instances.
7.8. A deterministic lack of capacity shall deny regardless of fail-open infrastructure posture.
7.9. Settlement above reserved exposure shall record debt/overage and shall never silently convert the initial reservation into proof that the strict limit was respected.

### Requirement 8: Multi-Attempt Routing Semantics
**Objective:** As a routing operator, I want accounting behavior to remain correct under retries, failover, and parallel racing, so routing strategy does not distort customer limits or hide provider liability.

#### Acceptance Criteria
8.1. One logical client request shall have one customer-side lifecycle regardless of the number of backend attempts.
8.2. Each committed backend attempt shall have its own operator-side lifecycle and stable attempt identity.
8.3. Parallel attempt admission shall not reserve a principal-level logical request count or customer input quota once per racing leg.
8.4. A parallel loser shall be marked non-surfaced for customer accounting while retaining operator usage and cost evidence.
8.5. Sequential failover shall not double-charge customer usage, and it shall retain operator cost for every incurred attempt.
8.6. No settlement or authority failure after first client-visible output shall trigger transparent retry or replacement of the committed attempt.
8.7. Attempt cancellation and billing finalization shall use fresh bounded cleanup contexts and shall preserve late authoritative corrections.
8.8. When exact loser cost cannot be recovered, the operator authority shall apply an explicit estimate/unavailable policy rather than unconditionally releasing the reservation.
8.9. Control-plane evidence shall correlate logical request decisions with all child attempt decisions and their surfaced status.

### Requirement 9: Existing Usage-Authority Remediation and Alignment
**Objective:** As a maintainer, I want the current usage-authority implementation retained where sound but corrected where its assumptions are wrong, so the project avoids both a destructive rewrite and continued semantic debt.

#### Acceptance Criteria
9.1. The atomic reservation, settlement, release, idempotency, authoritative correction, safe-scope matching, and bounded-query behavior of the existing authority store shall be preserved unless a replacement proves equivalent behavior.
9.2. Existing authority rules shall select a metering basis and lifecycle scope instead of consuming one undifferentiated `Request`/`Spend` amount.
9.3. Request-count and logical-request rate rules shall execute at logical request admission; backend-attempt rules shall execute at attempt admission.
9.4. Token and money authority shall remain independent, and authority shall also remain independent across economic perspectives.
9.5. The existing provider-first authority settlement shortcut shall be replaced by rule-basis selection from explicit metering facts.
9.6. Legacy rules lacking the new fields shall run only under a documented compatibility basis or shall fail validation with actionable migration errors.
9.7. Store and decision keys shall include sufficient authority namespace, perspective, lifecycle scope, and basis identity to prevent collisions.
9.8. The current process-wide durable-store serialization shall not be required for correctness of unrelated accounts or windows.
9.9. Existing PostgreSQL concurrency, replay, rollover, and correction contract tests shall remain release gates after the refactor.

### Requirement 10: Concurrent Logical Request Limits
**Objective:** As an administrator, I want to limit active requests per user or other safe scope, so one principal cannot occupy unbounded streaming or agentic capacity.

#### Acceptance Criteria
10.1. The proxy shall support strict and advisory maximum-active-logical-request rules by principal and other supported safe scope dimensions.
10.2. A rule such as `max_active_requests = 5` for one principal shall allow at most five simultaneously active top-level requests across all proxy instances using the same durable authority store.
10.3. One logical request shall consume one concurrency lease across all retries, failover attempts, and parallel backend legs.
10.4. A concurrency lease shall be acquired after trusted identity/scope resolution and before configurable expensive request transforms or backend work.
10.5. A concurrency lease shall be released on normal completion, protocol error, policy denial after acquisition, backend failure, client cancellation, stream close, and all preparation/open error paths.
10.6. Lease acquisition, renewal, release, and replay shall be idempotent.
10.7. Durable leases shall have expiry and renewal semantics so process crashes or abandoned connections cannot permanently consume capacity.
10.8. Long-running streams shall renew their leases before expiry, and a renewal failure shall follow configured fail-open/fail-closed post-admission behavior without corrupting the active count.
10.9. Expired leases shall become reclaimable through bounded inline cleanup or a bounded reconciler without scanning unbounded history.
10.10. Auxiliary requests shall not consume an additional top-level principal lease by default, but this behavior shall be configurable.
10.11. Client-safe denial shall include a stable concurrency-limit category and optional retry context without revealing other users or internal lease identifiers.
10.12. Operators shall be able to query active, expiring, expired, and released lease counts and safe lease correlation.

### Requirement 11: Dynamic Versioned Policy and Pricing Sources
**Objective:** As an enterprise operator, I want limits and economic policy changed at runtime without restarting the proxy, while every in-flight request remains internally consistent.

#### Acceptance Criteria
11.1. Authority rules, concurrency rules, and rating policies shall be obtained from immutable versioned snapshots.
11.2. One logical request and each of its reservations shall retain the snapshot versions captured at their legal admission points.
11.3. Publishing a new snapshot shall affect new admissions without mutating decisions already made for in-flight requests.
11.4. Settlement shall use the version bound to the reservation unless an explicit correction policy requires a newer authoritative source.
11.5. A static YAML-backed source shall remain available for OSS/local deployments.
11.6. A closed enterprise source shall be injectable through public contracts and may refresh from a database or management service.
11.7. Snapshot refresh failure shall expose ready, degraded, stale, unavailable, or disabled posture without silently substituting a different policy version.
11.8. Snapshot versions and fetched/effective timestamps shall be query-visible when safe.

### Requirement 12: Public Open-Core Extension and Composition Contracts
**Objective:** As the author of a closed enterprise module, I want stable public production seams, so proprietary implementations can extend the OSS core without importing Go `internal` packages or forking runtime orchestration.

#### Acceptance Criteria
12.1. Public SDK packages shall define all DTOs and interfaces required to inject metering recorders, request authorities, attempt authorities, concurrency authorities, rating providers, and evidence/query adapters.
12.2. Public contracts shall not expose `internal/core`, `internal/infra`, SQL/Bun, provider SDK, frontend HTTP handler, or executor-private types.
12.3. A public production composition facade shall allow an external Go module to construct or run the proxy with enterprise implementations while the OSS composition root remains authoritative.
12.4. Production authority or metering overrides shall not be hidden under testing-only options.
12.5. The enterprise implementation shall not need to edit `runtime.Executor`, fork `runtimebundle`, register global state, or use `init()` side effects.
12.6. Architecture tests shall compile a separate-module fixture that imports only public packages and injects reference external implementations.
12.7. Existing feature bundles and usage/traffic observers shall remain supported, but fail-open observers shall not be represented as strict admission authorities.
12.8. Public contracts shall be versioned additively and shall define compatibility behavior for unknown enum values and optional fields.
12.9. Proprietary pricebooks, wallets, credit policy, invoices, and analytics shall remain implementable entirely outside the OSS repository.

### Requirement 13: Durable Metering and Authority Journals
**Objective:** As an auditor and reconciler, I want durable idempotent technical journals, so usage and authority state can be recovered and explained after crashes or retries.

#### Acceptance Criteria
13.1. The core metering journal shall persist safe quantity facts independently from live limit counters and independently from a proprietary financial ledger.
13.2. The journal shall persist economic perspective, metering boundary, lifecycle scope, fact kind, authority, presence, source, version references, and safe correlation.
13.3. Monetary cost facts shall not be dropped merely because the existing token ledger stores only token fields.
13.4. Durable schemas shall enforce unique fact/source identity and support correction/supersession without destructive updates to historical evidence.
13.5. Live reservations and concurrency leases shall remain in authority stores optimized for atomic mutation; control-plane and metering journals shall remain append-oriented evidence.
13.6. A restart shall hydrate enough state to settle, release, renew, or reconcile in-flight durable reservations and leases without double-counting.
13.7. Orphaned reservations and unavailable settlements shall be queryable and eligible for bounded reconciliation.
13.8. The core journal shall not claim to be a customer financial ledger, invoice ledger, or payment ledger.

### Requirement 14: Operator Evidence and Query Semantics
**Objective:** As an operator, I want queryable evidence that preserves distinct dimensions, so I can diagnose customer usage, provider cost, compression savings, and active limits correctly.

#### Acceptance Criteria
14.1. Usage evidence shall expose economic perspective separately from evidence provenance and separately from metering boundary.
14.2. Existing generic `observed` versus `accounting` availability shall not erase `frontend_ingress`, `backend_ingress`, `backend_egress`, or `frontend_egress` identity.
14.3. Authority evidence shall expose logical-request versus backend-attempt scope, surfaced status, matched rule, reservation/lease identity, version references, and safe scope.
14.4. Queries shall support bounded filtering by principal, tenant, workspace, project, department, cost center, backend, model, route, perspective, boundary, lifecycle scope, rule, and time window where indexed and supported.
14.5. Queries shall distinguish historical metering totals, live reservations, active concurrency leases, remaining authority, and proprietary rated/financial projections.
14.6. Compression reporting foundations shall expose original frontend input, backend input, delivered output, provider output/cost, and calculable savings without exposing raw content.
14.7. Customer charge evidence and operator cost evidence shall never be merged into one amount without an explicit report calculation.
14.8. Unsupported or too-broad queries shall return stable bounded-query outcomes rather than silently scanning or widening.

### Requirement 15: Failure, Readiness, and Compensation
**Objective:** As an operator, I want predictable behavior when one of several authorities or stores fails, so availability choices do not silently weaken strict financial or concurrency controls.

#### Acceptance Criteria
15.1. Request and attempt authority providers shall declare required, advisory, fail-open, or fail-closed posture per lifecycle stage.
15.2. When multiple authorities participate in admission, the coordinator shall apply them in deterministic order and compensate prior successful reservations in reverse order if a later required authority denies or fails.
15.3. Compensation, release, settlement, renewal, and reconciliation shall each receive a fresh bounded context independent of client cancellation.
15.4. Deterministic capacity exhaustion shall never be converted to infrastructure-unavailable fail-open behavior.
15.5. A post-output settlement failure shall not alter already committed client output; it shall retain reservation/debt evidence for reconciliation.
15.6. Required customer credit or concurrency authority unavailable before protected work shall deny; advisory operator analytics may fail open when explicitly configured.
15.7. Readiness shall report each authority and journal independently and shall provide an aggregate protected-traffic posture.
15.8. Local-memory fallback shall never be reported as distributed strict enforcement.
15.9. Panics or malformed responses from injected enterprise providers shall be isolated and mapped through stable failure posture.

### Requirement 16: Performance and Scalability
**Objective:** As an inference provider, I want authority and metering overhead to scale with the affected request and account, so unrelated traffic is not serialized or scanned.

#### Acceptance Criteria
16.1. Mutations for unrelated principals, accounts, rules, windows, or leases shall be able to proceed concurrently within one proxy process.
16.2. PostgreSQL correctness shall rely on targeted row locks, unique constraints, compare-and-swap, or equivalent transactional primitives rather than one process-wide mutex around all database work.
16.3. Active-limit, active-lease, and fact-idempotency lookups shall perform bounded indexed SQL work.
16.4. Metering shall avoid serializing or retaining full raw request/response payloads when only counts and safe metadata are required.
16.5. Authority and metering work shall have configurable time budgets and shall expose latency/timeout metrics by stage and provider.
16.6. Benchmarks shall cover independent-account throughput, hot-account contention, five-slot concurrency admission, parallel routing, settlement, journal append, and correction replay.
16.7. SQLite may serialize local writers but shall report its single-node limitations; PostgreSQL shall be the distributed strict reference backing.
16.8. The no-authority/no-metering-customization path shall preserve a low-overhead default and existing streaming behavior.

### Requirement 17: Compatibility, Privacy, and Delivery Boundaries
**Objective:** As a maintainer, I want an incremental migration that protects privacy and the open-core boundary, so the remediation can ship without an uncontrolled rewrite.

#### Acceptance Criteria
17.1. Existing token accounting, frontend protocol behavior, B2BUA lineage, routing, streaming, and secure-session authority shall remain functional when the new capabilities are disabled.
17.2. Existing usage-authority data and config shall have an explicit migration or compatibility strategy; destructive reinterpretation shall not occur automatically.
17.3. The implementation shall be staged so immediate correctness fixes can land before the full architecture is enabled.
17.4. New evidence and public SDK fields shall be additive where practical, with deprecation periods for legacy selected-billable semantics.
17.5. Raw bearer tokens, API keys, OAuth tokens, resume tokens, raw headers, unredacted prompts/responses, and unsafe claims shall not enter default metering, rating, authority, lease, or query records.
17.6. Safe principal/scope values shall preserve unknown versus known-empty semantics.
17.7. The OSS core shall contain generic infrastructure and reference implementations only; proprietary commercial logic shall remain outside the repository.
17.8. This feature shall not implement web GUI, payment collection, invoice generation, tax calculation, SSO/SAML/SCIM, content-security policy, or compression algorithms.
17.9. PostgreSQL integration, race, fuzz, architecture, and cross-protocol conformance suites shall be required release gates for the completed feature.
