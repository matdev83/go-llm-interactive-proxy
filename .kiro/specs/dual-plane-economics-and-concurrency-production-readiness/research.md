# Current-State Review and Requirements Gap Analysis: Dual-Plane Economics and Concurrency Production Readiness

Generated: 2026-07-17T10:16:22+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Reviewed ref: `main` at `e02cd609033be746fd84f89cbc6f54d0ab2f5d12`
- Predecessor authority implementation: PR #128, `usage-quota-rate-budget-authority`
- Predecessor architecture specification: PR #130, `dual-plane-economics-and-concurrency-foundation`
- Implementation stack reviewed: PRs #133, #134, #135, #141, #142, #143, #144, and #145
- Requirements source: `.kiro/specs/dual-plane-economics-and-concurrency-production-readiness/requirements.md`
- Review mode: static source, contract, persistence, lifecycle, steering, and archived-spec review through the connected GitHub repository
- Scope: brownfield backend/core hardening only; this PR changes specifications only

## Executive Assessment

The merged implementation is a strong architectural advance and is not a rewrite candidate. It provides reusable public economic vocabulary, fixed-window usage authority, request and attempt coordinators, renewable concurrency leases, four legal checkpoint concepts, durable metering and authority stores, transaction-pooled PostgreSQL support, version metadata, public composition seams, bounded queries, and substantial race/integration coverage.

The implementation is not yet a safe foundation for strict customer billing or prepaid/postpaid credit because several remaining seams can misstate whose usage is being settled, lose terminal work after request exit, preserve metadata without changing executable policy, or allow strict concurrency to become unproven after renewal failure. The highest-risk gaps are cross-cutting rather than localized:

1. customer terminal evidence can still be derived from provider-oriented usage;
2. public provider descriptors are not fully bound to the registered provider instances and coordinator posture;
3. snapshot refresh publishes versions but does not necessarily replace the evaluator objects used by built-in authorities;
4. `Recv` and `Close` can compete in billing finalization;
5. release and settlement failures can be dropped once the live request disappears;
6. ingress checkpoints and terminal facts do not yet form a complete restart-safe journal;
7. multi-rule and renewal concurrency semantics need stronger set-level guarantees.

The selected direction is a staged hybrid hardening: preserve existing stores and lifecycle machinery, extend public contracts where external substitution is real, add dedicated domain/application components for terminal ownership and durable terminal work, and migrate incrementally behind additive schemas and compatibility adapters.

## Reviewed Assets

### Steering, workflow, and architecture rules

- `AGENTS.md`, `.kiro/AGENTS.md`
- `.kiro/steering/{product,structure,api-standards,routing-and-orchestration,tech,testing}.md`
- `.kiro/rules/{ears-format,gap-analysis,design-principles}.md`
- `.kiro/settings/templates/specs/{init.json,requirements.md,design.md,tasks.md}`
- latest closed full-workflow spec: `.kiro/specs/reasoning-output-preservation/*`

### Public contracts and composition

- `pkg/lipsdk/metering/*`
- `pkg/lipsdk/economics/*`
- `pkg/lipsdk/authority/*`
- `pkg/lipsdk/controlplane/*`
- `pkg/lipruntime/*`
- `testdata/enterprise_module/*`
- `internal/infra/runtimebundle/{production_options,authority_coord,snapshot_controller,readiness_report}.go`

### Runtime lifecycle and economics

- `internal/core/runtime/{accounting_authority,authority_lifecycle,authority_request,economics_rate,metering_checkpoint,metering_egress}.go`
- `internal/core/runtime/executor_open_attempt.go`
- retry/recv, close, cancellation, completion-gate, failover, and parallel-race paths
- token-accounting reconstruction and usage-presence helpers

### Authority, metering, snapshots, and concurrency

- `internal/core/authoritycoord/*`
- `internal/core/usageauthority/{domain,app}/*`
- `internal/core/metering/{checkpoint,aggregate,reconcile}/*`
- `internal/core/snapshotgen/*`
- `internal/core/concurrencyauthority/{domain,app}/*`
- durable memory/SQLite/PostgreSQL adapters and migrations under `internal/infra`
- pooled PostgreSQL registry, migration, harness, and release-gate support

### Existing release evidence

- architecture and public-enterprise compile tests
- direct and transaction-pooled PostgreSQL integration tests
- concurrency contention and renewal CAS tests
- metering append/conflict/query tests
- authority differential-state, replay, correction, and contention tests
- runtime cancellation, failover, parallel, clamp, generation, and lease tests
- `docs/release-gates.md` and `docs/dual-plane-rollout.md`

## Existing Strengths to Preserve

1. **Atomic usage-authority mutation sets.** Matching strict rule reservations, settlements, releases, and authoritative corrections have durable idempotent store contracts.
2. **Explicit economic vocabulary.** Customer/operator perspective, legal metering boundary, lifecycle scope, source, authority, presence, quantity, money, and version types already exist publicly.
3. **Final backend mutation placement.** Request hooks and route parameters run before final backend-ingress freeze and attempt authorization, with a widening assertion before `Open`.
4. **Request-versus-attempt orchestration.** Logical customer controls and operator attempt controls have separate coordinators and compensation stacks.
5. **Distributed concurrency foundation.** Durable lease identity, generation CAS, expiry, renewal, query, and PostgreSQL contention tests exist.
6. **Open-core composition.** A separately versioned module can inject public raters, request/attempt providers, concurrency providers, recorders, and snapshot sources without importing `internal` packages.
7. **Transaction-pool posture.** Direct migration and pooled runtime roles, shared pool ownership, verification-only startup, and pooler-safe DML are already designed and tested.
8. **Streaming and lineage discipline.** B2BUA attempts, output commitment, no retry after visible output, loser cancellation, and fresh cleanup contexts are established runtime concepts.

## Brownfield Requirements Gap Analysis

| ID | Severity | Current-state gap | Requirement remediation |
| --- | --- | --- | --- |
| **G-01** | P0 | Successful request settlement can receive provider-authoritative usage rather than an independently accumulated final customer stream. | Require a dedicated final-canonical customer accumulator and customer-only settlement evidence. |
| **G-02** | P0 | Provider-reported cost can be attached to a customer-perspective fact through heuristic money presence/source mapping. | Forbid provider cost in customer facts; customer money comes only from the customer rater/authority. |
| **G-03** | P0 | Attempt money may be rated before final backend-ingress quantities replace preflight quantities. | Require complete final exposure construction before operator rating and reservation. |
| **G-04** | P1 | Frontend/backend correlation fields have incomplete or misleading identities in some checkpoint paths. | Require real trace/frontend identity and consistent request/A-leg/B-leg/attempt correlation. |
| **G-05** | P0 | `ProviderDescriptors` are validated at the public facade but provider slices are composed with generated IDs and hard-coded required posture. | Bind descriptor, priority, posture, and provider instance in one registration object. |
| **G-06** | P0 | Advisory/fail-open behavior is applied mainly to provider errors; an explicit advisory `deny` can still block. | Define posture truth tables for decision and error outcomes. |
| **G-07** | P0 | Public decision validation checks only the top-level kind; malformed holds, clamps, money, stages, and contradictory states can pass inward. | Require context-aware validation for all external results. |
| **G-08** | P1 | A current provider can return an error/deny with claimed holds that are not necessarily compensated before the coordinator exits. | Reject the shape or compensate current-provider holds before applying posture. |
| **G-09** | P0 | Snapshot refresh publishes version/readiness envelopes but built-in services can continue using static rule sources. | Publish executable immutable generations containing actual authorities and raters. |
| **G-10** | P1 | Old-generation/provider lifetime for in-flight settlement and durable retries is not an explicit contract. | Retain generation/provider resolution until live and pending work drains. |
| **G-11** | P0 | `Close` may run concurrently with `Recv` while both can read/mutate terminal accounting and settlement state. | Introduce one atomic terminal owner and state machine. |
| **G-12** | P1 | Frontend encoder failure and some post-lease/pre-backend exits are not uniformly represented in one terminal model. | Define one terminal command covering every request/attempt exit. |
| **G-13** | P1 | Request settlement and lease-release errors can be ignored while live state is marked complete. | Require truthful state plus durable retry work. |
| **G-14** | P1 | Existing reconciliation reports journal issues but does not own retry of live authority/lease side effects. | Add a technical terminal-work store and bounded processor. |
| **G-15** | P1 | Frontend/backend ingress are in-memory checkpoints rather than a complete durable four-boundary journal. | Persist both ingress boundaries after trusted scope/counting and before their authority stages. |
| **G-16** | P1 | Egress fact identity depends on request-local sequence allocation and is not inherently stable after restart. | Define deterministic source-event identity with identity version, event kind, and revision. |
| **G-17** | P1 | Journal uniqueness can collide across logical `store_id` namespaces, and fact validation permits ambiguous negative/presence states. | Namespace keys by store and strengthen fact/money/quantity validation. |
| **G-18** | P1 | Supersession is recorded but same-stream existence, cycles, and deterministic removal/replacement need stronger contracts. | Add an immutable supersession graph and validated aggregation semantics. |
| **G-19** | P1 | `renew_before >= lease_ttl` can create immediate renewal loops; external lease shape validation is shallow. | Enforce timing bounds and context-aware lease validation. |
| **G-20** | P0 | Fail-closed renewal can stop heartbeat while the request continues past lease expiry, allowing capacity to be reused. | Cancel before expiry or preserve uncertain-but-occupied durable capacity. |
| **G-21** | P1 | Matching concurrency rules acquire sequentially and rollback is best-effort; renewal/release are per occupancy. | Add atomic lease-set acquire, renew, and release for the built-in distributed authority. |
| **G-22** | P1 | Existing release gates explicitly lack dedicated dual-plane state-machine fuzzing and no-feature overhead certification. | Add fault, race, fuzz/model, benchmark, and clean-environment gates. |

## Requirement-to-Asset Map

| Final requirement area | Reusable assets | Missing or constrained capability |
| --- | --- | --- |
| Customer/operator evidence | metering perspectives, checkpoints, runtime settlement hooks | dedicated final customer accumulator; customer money isolation |
| Final exposure/rating | final call assembly, preflight, widening guard, public rater | rate exact checkpoint quantities and bind fact/rating/reservation |
| Provider posture | descriptors, coordinators, panic isolation | descriptor-bound registration and full posture semantics |
| Validation/money | money helpers, explicit presence, static catalog | result-wide validation, range checks, rounding execution, mixed-currency rejection |
| Four-boundary journal | checkpoint types, recorder/querier, stores | ingress facts, deterministic IDs, store namespace isolation |
| Corrections | aggregate/reconcile, authoritative correction | same-stream graph integrity and signed-correction rules |
| Terminal ownership | lifecycle owners, committed flags, fresh cleanup contexts | one request/stream terminal CAS owner |
| Durable recovery | durable stores and bounded query patterns | terminal-work domain, outbox store, processor, provider router |
| Executable generations | publisher/controller, snapshot refs | actual evaluator objects and old-generation lifetime |
| Distributed concurrency | leases, CAS, durable stores, heartbeat | atomic sets, timing validation, strict renewal-loss behavior |
| Migration/open core | additive migrations, public facade, enterprise fixture | identity versions, compatibility registrations, pending-work rollback |
| Operations/certification | readiness reports, metrics, QA/PostgreSQL gates | economic-control readiness, backlog metrics, dedicated fuzz/fault/no-feature gates |

## Implementation Approach Options

### Option A — Patch existing runtime methods and DTOs only

Extend `retryRecvStream`, `authorityLifecycle`, existing provider slices, checkpoint helpers, and current stores without new lifecycle components.

**Advantages**

- smallest immediate file count;
- reuses familiar code paths;
- quick for isolated customer-evidence and validation fixes.

**Risks**

- further concentrates terminal concurrency in already complex stream code;
- no durable owner remains after the request exits;
- metadata-only generations and lease-set semantics remain difficult to reason about;
- high probability of fixing one path while missing another terminal path.

**Disposition:** viable for Phase 1 hotfixes only; rejected as the complete design.

### Option B — Replace the merged foundation

Build a new economics runtime, journal, authority, snapshot, and concurrency subsystem alongside the existing implementation, then migrate wholesale.

**Advantages**

- clean conceptual model;
- fewer compatibility compromises in the new code.

**Risks**

- discards proven authority-store and PostgreSQL work;
- duplicates lifecycle integrations and migration state;
- creates an extended period with two sources of truth;
- excessive delivery and regression risk.

**Disposition:** rejected.

### Option C — Hybrid staged hardening

Correct immediate runtime evidence, harden public contracts, complete the durable journal, add a dedicated terminal owner/outbox, replace metadata-only publication with executable generations, then harden lease sets and certify the combined system.

**Advantages**

- preserves valuable implementation and migrations;
- introduces new components only for distinct lifecycle/transaction responsibilities;
- supports chronological rollout and rollback;
- maintains public open-core seams and transaction-pool compatibility.

**Risks**

- requires careful dual-write/compatibility windows;
- crosses several core and infrastructure packages;
- needs strong state-machine and fault-injection testing.

**Recommendation:** selected for design.

## Complexity and Risk

- **Effort:** XL — seven dependent phases spanning public contracts, streaming lifecycle, durable schemas, workers, generations, and distributed concurrency.
- **Risk:** High — defects can affect strict money/limit correctness, post-output behavior, multi-instance capacity, and restart recovery.
- **Risk reducers:** preserve existing transactional stores; contract-first TDD; additive migrations; one phase per bounded responsibility; direct and pooled PostgreSQL gates; explicit rollback; separate proprietary finance scope.

## Requirements Remediation

The initial requirements draft was committed before this analysis. The final requirements were revised to close the identified gaps:

- required a dedicated final-canonical customer accumulator rather than scope filtering;
- prohibited provider cost from customer facts and made customer rating independent;
- bound final checkpoint/fact identity, rating, and reservation;
- replaced separate descriptors/provider slices with descriptor-bound registrations;
- added complete decision, hold, settlement, rating, lease, and renewal validation;
- required trusted-scope ingress persistence and a complete durable four-boundary journal;
- added identity version, source event kind/revision, store namespace, signed-correction, and supersession graph rules;
- expanded terminal coverage to `Recv`, `Close`, cancellation, frontend encoding, post-lease denial, errors, timeouts, and panics;
- required durable per-action/per-provider terminal work and truthful release state;
- changed snapshot requirements from metadata envelopes to executable generation objects with retained lifetime;
- required `renew_before < lease_ttl`, atomic lease-set acquire/renew/release, and strict renewal-loss cancellation or uncertain occupancy;
- clarified `EconomicControlReady` as an OSS technical posture, not commercial billing readiness;
- added dedicated race, state-machine fuzz, fault-injection, no-feature benchmark, and clean-environment certification gates.

## Design Carry-Forward Decisions

- Preserve existing usage-authority stores and adapters; do not rewrite them.
- Add domain/application components only where there is a distinct lifecycle or durability boundary.
- Keep the terminal-work outbox technical and provider-neutral; it is not a financial journal.
- Use durable intent plus idempotency instead of claiming distributed transactions across external providers.
- Bind executable generations at request admission and retain compatibility for pending handles.
- Keep proprietary enterprise pricebooks, wallets, credit, payments, invoices, tax, and commercial reporting in a separate specification.
