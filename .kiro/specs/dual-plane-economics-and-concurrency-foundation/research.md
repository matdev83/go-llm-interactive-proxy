# Current-State Review and Gap Analysis: Dual-Plane Economics and Concurrent Request Authority

Generated: 2026-07-13T19:36:44+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Reviewed ref: `main` at `8ed0bd79ca5dcdc7463a1adfc0ba051434ad93d0`
- Primary recent implementation: PR #128, `usage-quota-rate-budget-authority`
- Requirements source: `.kiro/specs/dual-plane-economics-and-concurrency-foundation/requirements.md`
- Review mode: static source and contract review through GitHub; no independent local test execution was available during the review.
- Scope: backend/core only. GUI, payments, invoice rendering, SSO provisioning, and proprietary enterprise business logic are excluded.

## Executive Assessment

The recent accounting-authority work is not a throwaway implementation. PR #128 introduced a technically meaningful fixed-window authority kernel with:

- safe scope-attributed rule matching;
- strict and advisory quota, rate, budget, and spend-cap rules;
- atomic multi-rule reservation sets;
- settlement, release, overage, and authoritative correction;
- idempotency across retries and instances;
- memory, SQLite, and PostgreSQL backing;
- PostgreSQL row locking and conflict handling;
- bounded live-state and decision queries;
- policy/control-plane evidence;
- explicit token-versus-money authority separation;
- lifecycle cleanup coverage for retries, cancellation, and parallel racing.

That work should be retained.

The current implementation is nevertheless not a sufficient foundation for the proposed inference-provider and enterprise billing MVP because it conflates or under-specifies several distinct concerns:

1. customer-visible usage and customer charge;
2. provider-billable usage and operator cost;
3. technical metering facts;
4. rating policy;
5. strict authority/reservation;
6. financial balances and journals;
7. request occupancy/concurrency.

The proper remediation is an architectural alignment and staged refactor, not a destructive rewrite.

## Reviewed Assets

### Usage and accounting vocabulary

- `pkg/lipapi/token_accounting.go`
- `pkg/lipapi/events.go`
- `internal/core/tokenaccounting/domain/reconcile.go`
- `internal/core/tokenaccounting/streamusage/reconstructor.go`
- `internal/core/tokenaccounting/ledger/ledger.go`
- `internal/infra/tokenaccounting/ledgerstore/*`

### Cost estimation

- `internal/core/accounting/accounting.go`
- accounting pricing configuration and validation
- provider usage/cost normalization, including `internal/plugins/backends/openaiusage/usage.go`

### Usage authority

- `internal/core/usageauthority/domain/*`
- `internal/core/usageauthority/app/*`
- `internal/infra/usageauthority/authoritystore/*`
- `internal/infra/usageauthority/configsource/*`
- `internal/infra/usageauthority/evidencesink/*`
- `internal/core/runtime/accounting_authority.go`
- `internal/core/runtime/authority_lifecycle.go`
- `internal/core/runtime/executor_settlement.go`
- open, retry, cancellation, and parallel-race paths

### Composition and open-core seams

- `internal/core/runtime/executor_config.go`
- `internal/infra/runtimebundle/build_executor.go`
- `internal/infra/runtimebundle/usage_authority.go`
- `docs/enterprise-extension-boundaries.md`
- `docs/extension-platform-authoring.md`
- `pkg/lipsdk/usage`
- `pkg/lipsdk/traffic`
- `pkg/lipsdk/controlplane`

### Existing concurrency mechanisms

- frontend decode QoS limiter under `internal/plugins/frontends/decodeqos`
- route attempt budget under runtime routing
- A-leg/B-leg lifecycle coordination

None of these currently implements distributed per-principal active logical-request leases.

## Current Strengths to Preserve

### 1. Atomic reservation-set semantics

The usage-authority application contract passes complete per-rule reservation, settlement, and release descriptor sets to the state store as one logical mutation. This is the correct direction for multiple simultaneously matching strict rules and avoids partial successful admission.

Preserve:

- complete-set atomicity;
- typed deterministic capacity outcomes;
- idempotent replay;
- compensation on later failure;
- evidence for both successful and denied mutations.

### 2. Durable concurrency control

The PostgreSQL implementation locks relevant reservation and limit rows, re-reads missing-row state after coordination locks, detects lost updates, and uses unique source identities. The implementation also includes dedicated integration and differential-state tests.

Preserve:

- row-targeted transaction boundaries;
- unique constraints;
- denial replay;
- missing-row creation handling;
- authoritative correction and restart behavior.

The broad process-level mutex should be removed later for scalability, but the database invariants are valuable.

### 3. Independent token and monetary authority

The runtime and store distinguish token authority from cost authority and preserve explicit authoritative zero cost. This is necessary and should become a broader rule: authority is independent per quantity/component and per economic perspective.

### 4. Safe scope attribution

Authority dimensions already include safe principal, credential, tenant, organization, workspace, project, department, cost center, backend, model, route, and policy labels, while excluding raw bearer/API/OAuth material.

This is sufficient as the identity substrate for:

- customer allowances;
- operator/provider limits;
- concurrent-request rules;
- future enterprise grouping and reporting.

### 5. Streaming and lineage awareness

The implementation correctly recognizes:

- logical request versus B-leg correlation;
- output commitment;
- no transparent retry after output;
- cancellation settlement;
- parallel loser cleanup;
- late authoritative reconciliation.

The missing part is economic interpretation, not the absence of lifecycle hooks.

### 6. Architecture guardrails

The usage-authority domain/app packages are kept free of SQL, HTTP, provider SDK, Bun, and concrete plugin dependencies. This boundary should remain.

## Critical Findings

## F-01 — A single selected billable amount is insufficient

**Severity:** P0 architecture blocker

The canonical usage vocabulary already contains multiple planes:

- provider-billable;
- client-visible;
- proxy-billable.

However, token reconciliation selects one `BillablePlane` according to one billing policy, and usage-authority admission receives one `Spend` amount. The runtime exposes one usage-authority service.

This is insufficient because customer charge and operator cost can differ due to:

- compression;
- prompt/steering append;
- routing transforms;
- cache economics;
- retries/failover;
- parallel racing;
- verifier or auxiliary calls;
- customer markup/discount/subsidy;
- provider-specific commercial agreements.

**Required remediation:**

- preserve independent metering facts;
- declare economic perspective separately from metering boundary and authority provenance;
- run logical-request/customer and backend-attempt/operator authority independently;
- treat pass-through billing as two independent calculations that happen to match.

## F-02 — Current metering placement is incompatible with transparent compression

**Severity:** P0 correctness blocker

The runtime baseline used for local stream reconstruction is created after request transforms and pre-request handlers. A compression transform placed there would cause the so-called client-visible input usage to reflect compressed content rather than the original frontend input.

If compression is instead applied through later attempt/request-part hooks, current authority admission may happen before the final mutation, causing operator exposure to use the original rather than actual backend request.

**Required remediation:**

Capture immutable typed checkpoints at:

1. frontend ingress, before mutation;
2. final backend ingress, after all mutation and immediately before backend open;
3. backend egress, per attempt;
4. frontend egress, after final response mutation/gating.

## F-03 — Unknown future output may reserve zero exposure

**Severity:** P0 monetary control blocker

When `max_output_tokens` is absent, preflight output exposure can remain zero. The current cost estimator then reserves input cost plus zero future output cost. Later settlement may record overage, but that does not prevent a prepaid or postpaid-credit account from being exceeded under concurrent traffic.

**Required remediation:**

Policy must explicitly select one of:

- require a client limit;
- apply a configured plan cap;
- reserve backend/model maximum;
- reserve a configured default allowance;
- deny when exposure cannot be bounded.

Zero must never be the implicit unknown-output estimate.

## F-04 — Final backend mutation can occur after monetary authorization

**Severity:** P0 correctness blocker

Request-part hooks and route parameter shaping can change the final backend-bound call after current authority admission. Reapplying a previously calculated output clamp prevents one form of widening but does not remeasure arbitrary content/tool/steering changes.

**Required remediation:**

Move operator exposure measurement and attempt authority to the final backend-ingress checkpoint. Any mutation after authorization must be prohibited from widening exposure or must trigger deterministic remeasurement and reauthorization.

## F-05 — Parallel losers release operator liability

**Severity:** P0 cost-accounting blocker

Current parallel loser cleanup releases authority reservations after cancellation. That is reasonable for customer surfaced usage, but providers may charge for losing or swallowed attempts.

**Required remediation:**

- customer lifecycle: one top-level logical request;
- operator lifecycle: one per committed B-leg;
- cancel loser, finalize billing if possible, settle incurred cost/usage, then release residual reservation;
- when exact cost is unavailable, apply explicit estimate/unavailable posture rather than unconditional release.

## F-06 — Customer request quotas/rates are currently evaluated per B-leg

**Severity:** P0 semantic blocker

Authority admission is integrated in the attempt-open path. A principal-level request-count quota or customer input-token quota can therefore be evaluated/reserved separately for retries and parallel legs.

The existing release logic may later return capacity, but temporary denials, evidence, and race behavior remain semantically wrong.

**Required remediation:**

Split rule execution by lifecycle scope:

- logical-request stage for customer request count, customer rate, customer token/charge allowances, and concurrency;
- backend-attempt stage for provider/operator spend, credential limits, and attempt-level rate/quota rules.

## F-07 — Current pricing is a reference estimator, not a financial rating engine

**Severity:** P0 for strict production billing; P1 for OSS reference use

Current price catalog limitations:

- one backend/model key;
- one catalog currency;
- static configuration;
- no offer/plan identity;
- no effective dates;
- no customer/operator distinction;
- no context tiers;
- no peak/off-peak policy;
- no fixed fees or minimums;
- no immutable price line binding;
- zero price used as both a valid rate and an absent/fallback signal in some paths;
- unchecked `int64` multiplication/addition.

**Required remediation:**

- public rating contract;
- explicit price/cost presence;
- checked money arithmetic;
- deterministic rounding;
- version/effective-time binding;
- simple static operator rater retained as OSS reference;
- proprietary customer/provider commercial logic implemented externally.

## F-08 — Token total inference can double-count subcomponents

**Severity:** P0 accounting correctness

Current code can infer total tokens by summing input, output, cache-read, cache-write, and reasoning counters. In common provider semantics, cache counters are input subcomponents and reasoning is an output subcomponent, so this can double-count.

**Required remediation:**

Define a component schema. Default:

```text
input includes cache-read and cache-write
output includes reasoning
 total = input + output
```

Separate components remain available for differential rating without being added twice.

## F-09 — Fact identity does not fully model cumulative and correction semantics

**Severity:** P0 for provider reconciliation

The existing token ledger appends rows, and reconciliation often selects a best candidate by authority. It does not provide a universal durable fact identity/sequence model for:

- additive deltas;
- cumulative snapshots;
- same-authority updates;
- corrections;
- authoritative replacements;
- supersession.

**Required remediation:**

Introduce an idempotent metering journal with fact kind, stream ID, sequence, source key, and supersession/correction identity.

## F-10 — Public enterprise composition seam is incomplete

**Severity:** P0 open-core blocker

The runtime authority interface and DTOs depend on `internal/core/usageauthority/app`. Production construction creates the OSS authority internally; only a testing-only store override exists. A separately versioned closed Go module cannot legally import these internal packages.

Existing public usage observers are insufficient for strict authority because they are post-facto/fail-open and do not carry complete plane/source/authority/presence/fact identity.

**Required remediation:**

Add public packages for:

- metering facts/recorders;
- economics/rating;
- request and attempt authority providers;
- concurrency leases and evidence;
- public production runtime composition.

Add a separate-module compile/integration proof.

## F-11 — Control-plane usage vocabulary erases business basis

**Severity:** P1 reporting and audit blocker

Control-plane usage currently distinguishes broadly between observed and accounting-authoritative evidence, but this is not equivalent to:

- customer versus operator perspective;
- frontend versus backend boundary;
- logical request versus attempt lifecycle;
- evidence provenance.

**Required remediation:**

Expose those classifications as independent fields. Preserve legacy projections for compatibility.

## F-12 — Token ledger is not a financial ledger

**Severity:** P1 architecture clarification

The current token ledger is a per-request/attempt/plane token record. It lacks financial debit/credit/hold semantics, account identity, rating version, unique financial source IDs, transfers, refunds, and invoice linkage.

**Decision:**

- retain as compatibility/technical usage view;
- add a new technical metering journal;
- keep customer financial journal/wallet entirely in the closed enterprise module.

## F-13 — Rules and pricing are not runtime-dynamic

**Severity:** P1 MVP gap

The config rule source is immutable for the process lifetime. Runtime updates without restart are required for enterprise allowance, provider pricing, and account policy management.

**Required remediation:**

Versioned immutable snapshot sources with atomic publication and per-request binding. Static YAML remains one adapter.

## F-14 — No per-principal active request authority exists

**Severity:** P0 missing feature

The existing frontend decode limiter is process-local and bounds only decode work. Route attempt budget bounds attempts inside one request. Neither limits active logical requests per principal across instances.

**Required semantics:**

- occupancy lease, not fixed-window rate counter;
- one lease per top-level logical request;
- retries/parallel legs share the lease;
- auxiliary requests inherit parent lease by default;
- acquire after trusted identity, before expensive transforms/backend work;
- renew long-running streams;
- release on all terminal/close/error/cancellation paths;
- expire/reclaim after crashes;
- PostgreSQL atomic reference backing;
- client-safe denial and bounded queries.

## F-15 — Process-wide durable-store mutex limits scalability

**Severity:** P1 scalability

The durable authority store wraps complete database operations in one process-wide mutex. This preserves local consistency but serializes unrelated principals and windows within one process.

**Required remediation:**

- immutable store configuration;
- narrow close/readiness synchronization;
- database row locks and unique constraints for correctness;
- optional keyed locks only for hot identities;
- independent-account concurrency benchmarks.

## F-16 — Current authority specification explicitly excludes billing

**Severity:** architectural boundary confirmation

The merged PR #128 specification explicitly excludes customer charge calculation, payment collection, provider billing settlement, and invoices. Therefore it should be described and evolved as a usage/limit authority, not marketed internally as a complete billing foundation.

This new specification supplies the missing core plumbing while continuing to keep proprietary billing business logic outside OSS.

## Findings Register

| ID | Severity | Current state | Required disposition |
| --- | --- | --- | --- |
| F-01 | P0 | one selected economic amount | refactor to independent perspectives |
| F-02 | P0 | post-transform client basis | add immutable four-boundary checkpoints |
| F-03 | P0 | absent max output can imply zero | add explicit conservative exposure policy |
| F-04 | P0 | mutation after authorization | authorize final backend-bound call |
| F-05 | P0 | losers release liability | settle operator attempt cost then release residual |
| F-06 | P0 | logical rules run per attempt | split request and attempt stages |
| F-07 | P0/P1 | static estimator only | public rating contract and arithmetic hardening |
| F-08 | P0 | possible token double-count | component inclusion schema |
| F-09 | P0 | incomplete correction semantics | idempotent metering fact journal |
| F-10 | P0 | enterprise cannot inject production authority publicly | public SDK and runtime facade |
| F-11 | P1 | control-plane basis loss | independent perspective/boundary/lifecycle fields |
| F-12 | P1 | token ledger mistaken for billing ledger | compatibility plus separate technical journal |
| F-13 | P1 | immutable process config | versioned dynamic snapshots |
| F-14 | P0 | no active-request limit | distributed renewable lease authority |
| F-15 | P1 | global store serialization | targeted synchronization and benchmarks |
| F-16 | Boundary | prior feature excludes billing | retain scope and add generic plumbing only |

## Architecture Options Considered

### Option A — Extend current single usage authority directly

**Rejected as final architecture.**

It would require one service to select one spend/usage plane and would continue to mix logical customer and per-attempt operator semantics. It also leaves the public enterprise injection problem unresolved.

Useful only as a transitional adapter.

### Option B — Rewrite accounting and authority from scratch

**Rejected.**

It would discard mature reservation/store behavior, concurrency tests, evidence integration, scope matching, and lifecycle cleanup.

### Option C — Preserve store/domain kernel and add public metering/economic/lifecycle coordination

**Selected.**

- retain fixed-window amount-per-rule store;
- add explicit facts and basis selection;
- split request and attempt authority coordination;
- adapt existing authority behind public contracts;
- add separate concurrency lease authority;
- add public enterprise composition;
- add technical metering journal;
- keep financial ledger and commercial rating closed.

## Recommended Delivery Order

### Phase 1 — Immediate correctness remediation

1. fix token component inclusion and total inference;
2. distinguish explicit zero from absence throughout cost/rating paths;
3. add checked money arithmetic;
4. prohibit zero-as-unknown output exposure;
5. add characterization tests for customer rules per B-leg and lost loser cost;
6. make final backend request remeasurement mandatory.

### Phase 2 — Public vocabulary and checkpoints

1. public metering/economics/authority DTOs;
2. immutable frontend-ingress checkpoint;
3. immutable final backend-ingress checkpoint;
4. backend-egress per-attempt accumulator;
5. frontend-egress final visible accumulator;
6. legacy event/ledger compatibility adapters.

### Phase 3 — Lifecycle split and usage-authority adapter

1. request authority coordinator;
2. attempt authority coordinator;
3. deterministic priority and compensation;
4. rule perspective/lifecycle/basis/version fields;
5. legacy compatibility validation;
6. existing store namespace migration.

### Phase 4 — Concurrent request leases

1. lease domain/state machine;
2. memory adapter;
3. PostgreSQL atomic adapter;
4. acquisition before expensive transforms;
5. renewal and release owner;
6. crash expiry/reclamation;
7. query/evidence and cross-instance tests.

### Phase 5 — Dynamic sources and external composition

1. versioned snapshots;
2. static adapters;
3. public production runtime facade;
4. separate-module external provider fixture;
5. readiness/staleness reporting.

### Phase 6 — Performance and migration hardening

1. remove global store mutex from DB operations;
2. independent/hot-key benchmarks;
3. cross-protocol conformance;
4. migration tooling and compatibility gates;
5. deprecate selected-billable authority shortcut.

## Requirement-to-Finding Map

| Requirement | Primary findings |
| --- | --- |
| 1 | F-01, F-06 |
| 2 | F-02, F-04 |
| 3 | F-08, F-09 |
| 4 | F-01, F-02, F-06 |
| 5 | F-01, F-04, F-05 |
| 6 | F-07, F-08 |
| 7 | F-03, F-04, F-07 |
| 8 | F-05, F-06, F-09 |
| 9 | F-01, F-06, F-15, F-16 |
| 10 | F-14 |
| 11 | F-13 |
| 12 | F-10, F-16 |
| 13 | F-09, F-12 |
| 14 | F-11 |
| 15 | F-03, F-05, F-14 |
| 16 | F-15 |
| 17 | all migration and boundary findings |

## Final Recommendation

Do not revert or rewrite PR #128 wholesale.

Freeze further proprietary billing feature development on top of the current single `Spend` path until the P0 correctness and lifecycle alignment tasks in this specification are complete.

Treat the existing authority as a reusable fixed-window enforcement implementation, then build the missing public metering, rating, request/attempt coordination, and concurrency lease foundations around it. This provides a clean OSS substrate while preserving the commercial moat in external pricebooks, wallets, credit policy, financial journals, payments, invoices, and analytics.

## Amendment Note — Transaction-Pooled PostgreSQL (2026-07-15)

### Evidence

Observed during dual-plane PostgreSQL release-gate runs: managed transaction-pooler endpoints do not preserve session/`search_path` isolation used by some multi-statement contract harnesses. Direct endpoints pass the same suite. Coupling migration DDL and runtime DML through one DSN, plus independent pools per store, further mismatches production PgBouncer/Neon transaction-pooling deployments. This is an operational compatibility gap for pooled production runtimes, not a reason to abandon PostgreSQL as the distributed strict reference.

### Decision

Approve an additive remediation (Requirement 18 / design amendment / tasks 13–18) with six execution phases:

1. Two-endpoint harness (`LIP_TEST_POSTGRES_ADMIN_DSN` + `LIP_TEST_POSTGRES_DSN`), pooler-safe isolation, RED pooled contracts, `make test-authority-postgres-pooled` at `-parallel=8`.
2. Typed modes with compatibility defaults; composition-owned pool registry keyed by sanitized DSN + pool config; inject handles into authority/lease/journal.
3. Split Migrate / VerifySchema / open-with-injected-handle; `lipstd migrate` via `LIP_MIGRATION_POSTGRES_DSN`; `verify_only` runtime never mutates schema.
4. Remove session-dependent runtime SQL; explicit transactions + row locks/CAS; bounded retry only for SQLSTATE 40001/40P01.
5. Load/pool-sharing proofs, infra-vs-capacity classification, bounded metrics, Linux race on registry/stores/heartbeat.
6. Local PostgreSQL + PgBouncer transaction-mode CI; separate migration/direct/pooled Make targets; docs and compatibility rollout (`direct`+`auto_migrate` default initially).

Non-negotiable: no hostname/`-pooler` inference; direct-only or `-parallel=1` cannot satisfy pooled runtime completion; migration compatibility and runtime pool compatibility remain separate gates.
