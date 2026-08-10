# Requirements Document

## Introduction

Go-LIP's usage/economic subsystem has accumulated several generations of correct ideas at the same time: provider usage parsing and explicit presence in `lipapi`, token counting/reconciliation, static cost estimation, fixed-window usage authority, dual-plane metering facts, generic rating/authority SDK ports, control-plane usage projections, a token ledger, and terminal-work recovery. The newer dual-plane architecture is materially stronger, but the older event/reconciliation/reporting paths remain live and runtime still coordinates much of the economic policy directly.

The result is a correctness surface larger than the domain requires. The same usage can be deduplicated in runtime and again during token reconciliation, aggregated by several helpers, priced on stream events and later represented as metering money, persisted into more than one ledger-like store, and projected into control-plane rows through both legacy usage-observer and metering-fact paths. Recent fixes around explicit zero vs absence, multi-scope settlement, all-zero provider usage, and authoritative corrections demonstrate that these are semantic rather than cosmetic risks.

This specification converges the subsystem onto the simpler architecture already implied by Go-LIP's dual-plane work: **metering facts are technical economic evidence; one reducer owns fact aggregation/correction semantics; rating converts reduced quantities to money; authority owns quota/budget/concurrency reservation state; runtime owns execution and terminal timing; reports and compatibility ledgers are one-way projections.** It is a contraction/refactor specification, not a new financial billing platform.

The target is deliberately ordinary Go: cohesive internal packages, concrete request/attempt lifecycle objects, small public ports only where real external substitution already exists, explicit composition, no framework or event-bus layer, no second economic DTO hierarchy, and architecture tests that prove dependency direction and deleted legacy paths.

## Boundary Context

- **In scope:** metering fact identity/presence; delta/cumulative/correction/replacement reduction; provider/client measurement ownership; request/attempt accounting lifecycle; static/injected rating convergence; built-in/external usage-authority coordination; runtime accounting-state extraction; provider billing sideband/finalizer normalization; control-plane report reduction; token-ledger compatibility/deletion; safe economic replay/debugging; architecture/import/deleted-symbol/size ratchets; brownfield migration and deletion of obsolete token reconciliation/runtime accounting semantics.
- **Out of scope:** customer wallets, balances, double-entry journals, invoices, payments, taxes, marketplace settlement, proprietary pricebooks/provider contracts, commercial margin logic, a new financial ledger, a generic event-sourcing framework, a generic event bus, a DI container, a generic workflow engine, routing/failover algorithm changes, B2BUA redesign, secure-session redesign, terminal CAS redesign, or usage-authority window/storage algorithm rewrite.
- **Primary ownership:** provider SDK/wire usage parsing stays in backend adapters/connectors; `pkg/lipapi` remains canonical stream transport; `pkg/lipsdk/metering` remains the public technical evidence contract; `internal/core/metering/aggregate` owns fact reduction; `internal/core/accounting` owns request/attempt technical accounting orchestration; `pkg/lipsdk/economics` owns rating ports; request/attempt authority coordinators own authority stacks; usage-authority store/service owns fixed-window rule transactions; runtime owns execution/terminal winner selection; control plane owns read projections.
- **Financial accounting boundary:** future enterprise balances, credits/debits, invoices and payments remain a separate bounded context and shall consume settled technical/economic evidence through public contracts rather than becoming part of this OSS accounting lifecycle.
- **Brownfield constraint:** migration must preserve current streaming, provider usage/cost fidelity, output-commit/no-retry semantics, failover/parallel attempt accounting, cancellation settlement, generation-bound policy/rating versions, authority idempotency, durable terminal recovery, and existing public compatibility until explicit deletion gates are satisfied.
- **Revalidation triggers:** changes to `lipapi.EventUsageDelta` semantics, metering Fact identity/kinds/presence, backend-plugin AccountingEvidence/FinalizeBilling, rating/provider binding, authority coordinator lifecycle, terminal/cancellation ordering, control-plane economic query semantics, or token-ledger compatibility.

## Requirements

### Requirement 1: Preserve Product and Economic Correctness During Convergence

**Objective:** As an operator and maintainer, I want the architecture cleanup to preserve supported behavior, so that simplification does not introduce billing, quota, routing, or protocol regressions.

#### Acceptance Criteria

1.1. When an existing frontend or backend executes through the migrated architecture, the system shall preserve current legal request/response wire behavior, canonical event ordering, cancellation, terminal ownership, and error mapping unless separately approved protocol requirements change them.

1.2. When a logical request retries or fails over before output commitment, the system shall preserve one customer logical-request lifecycle while recording each committed backend attempt as an independent operator lifecycle.

1.3. When multiple backend attempts race, lose, are swallowed, fail, or are canceled, the system shall preserve incurred operator usage/cost settlement and shall not attribute losing-attempt usage to the surfaced customer response.

1.4. After the first client-visible output is committed, the system shall not introduce transparent retry, failover, backend migration, or economic rollback of already delivered output.

1.5. While runtime generations reload, each in-flight request and attempt shall continue to use the immutable rule/rating/provider generation bound at admission rather than silently switching to current live configuration.

1.6. The system shall preserve provider SDK isolation: provider SDK/wire types shall remain at adapter/connector edges and shall not enter `internal/core/accounting`, `internal/core/metering`, `pkg/lipsdk/metering`, `pkg/lipsdk/economics`, or generic authority contracts.

1.7. The system shall preserve streaming as the primary execution model; non-streaming behavior shall remain collection/encoding over the canonical event stream.

1.8. The implementation shall not introduce a DI container, reflection registry, generic workflow engine, generic event bus, Go native plugin loading, or a second financial/accounting framework.

1.9. When the migrated architecture cannot produce complete authoritative usage or money, it shall preserve explicit estimated/advisory/unavailable status rather than inventing zero or silently treating missing evidence as authoritative.

### Requirement 2: Establish One Directional Economic Dependency Flow

**Objective:** As a maintainer, I want one direction from observation to evidence to decisions to projections, so that downstream components cannot independently reinterpret billing semantics.

#### Acceptance Criteria

2.1. After provider/client usage is converted into `metering.Fact` evidence, rating, quota/budget authority settlement, economic reporting, and compatibility-ledger projection shall consume metering facts or reduced metering results rather than re-reading raw `[]lipapi.Event` arrays.

2.2. Runtime shall not own token/cost aggregation rules, usage dedupe algorithms, price-catalog math, quota/budget mutation algorithms, or report reconstruction logic after migration.

2.3. `internal/core/accounting` shall own only technical-accounting use-case sequencing and request/attempt lifecycle state; it shall delegate fact semantics to metering, price evaluation to raters, and quota/budget mutations to authority providers/coordinators.

2.4. `internal/core/metering/aggregate` shall be the sole production owner of delta, cumulative, correction, authoritative-replacement, supersession, replay, checked-addition, and mixed-currency reduction semantics.

2.5. Provider-specific usage extraction, cumulative-vs-delta interpretation, provider cost parsing, and provider billing finalization shall remain backend adapter/connector responsibilities before evidence enters the generic accounting lifecycle.

2.6. Control-plane, diagnostics, metrics, client usage payloads, and any retained token-accounting admin views shall be downstream projections and shall not feed back into live quota/budget settlement.

2.7. Architecture tests shall fail when runtime or a read-model package reintroduces a second implementation of metering reduction, pricing fallback, direct usage-authority mutation, or direct legacy token-ledger mutation after the corresponding migration phase retires it.

2.8. The target architecture shall retain explicit construction through the existing runtime composition root and shall not create package-global economic state.

### Requirement 3: Use Metering Facts as the Canonical Technical Economic Evidence

**Objective:** As an accounting maintainer, I want one presence-aware and identity-aware evidence model, so that retries, corrections, zero values, and multi-attempt flows are deterministic.

#### Acceptance Criteria

3.1. Every economic measurement used for live settlement or durable reporting shall be representable as `metering.Fact` with stable StreamID/FactID identity, sequence, perspective, boundary, lifecycle, correlation, source, authority, and explicit quantity/money presence.

3.2. Replaying the same fact identity with the same semantic payload shall be idempotent, while two different fact identities with identical numeric values shall remain distinct legitimate evidence unless one explicitly supersedes the other.

3.3. The system shall distinguish explicit zero token or money observations from absent observations across ingestion, reduction, persistence, rating, authority settlement, report projection, and JSON round-trip where applicable.

3.4. Each reduced stream shall retain sufficient identity/provenance to explain which facts contributed, which facts were superseded, the effective per-component/source authority, money presence/currency/source, and completeness/unavailability.

3.5. Frontend-ingress facts shall represent logical-request customer input; frontend-egress facts shall represent client-visible delivered output; backend-ingress facts shall represent final authorized operator attempt input/exposure; backend-egress facts shall represent provider attempt output/usage/cost.

3.6. The system shall retain independent customer and operator perspectives and shall not infer one perspective's quantities or money from the other merely because values happen to match.

3.7. A request/attempt may keep its current facts in memory for the live lifecycle while optionally appending the exact same fact objects to a durable recorder; durable persistence shall not be a prerequisite for in-memory technical accounting unless the configured authority/report contract explicitly requires durability.

3.8. The system shall not introduce a second durable usage evidence schema or database alongside the existing metering journal.

3.9. If durable metering is unavailable where a durable report is requested, the query/report shall return explicit partial/unavailable evidence rather than silently reconstruct from a weaker legacy observer path.

### Requirement 4: Make One Reducer Authoritative for Fact Semantics

**Objective:** As a developer, I want one pure reducer with replayable semantics, so that billing/reporting results are independent of provider chunking and restart timing.

#### Acceptance Criteria

4.1. Given any ordered fact stream, the reducer shall apply `delta` facts additively, `cumulative` facts as present-component replacement snapshots, `correction` facts according to their correction semantics, and `authoritative_replacement` facts as replacement of explicitly present components tied to superseded evidence.

4.2. Replaying the same valid fact set in any storage/restart context shall produce the same reduced result.

4.3. Splitting one logical additive measurement into N additive facts shall produce the same result as one equivalent additive fact.

4.4. A cumulative value of 5 followed by a cumulative value of 10 for the same component/stream shall reduce to 10, not 15.

4.5. An authoritative replacement shall not be added to the value it supersedes.

4.6. The reducer shall use checked arithmetic and reject overflow and incompatible present currencies rather than wrap or silently coerce.

4.7. The reducer shall preserve explicitly present zero quantities/money and shall not convert them to absence.

4.8. Production report/accounting code shall not contain another reducer that independently implements the same fact-kind rules.

4.9. The reducer API shall support reducing a bounded set of facts grouped by StreamID without requiring a new framework, global registry, background worker, or storage dependency.

### Requirement 5: Normalize Provider Usage Once at the Adapter/Accounting Ingress Boundary

**Objective:** As a backend/connector author, I want one clear usage evidence contract, so that provider event shape cannot change accounting totals.

#### Acceptance Criteria

5.1. Canonical `lipapi.EventUsageDelta` emitted into the main stream shall have one documented generic semantic for aggregation; provider adapters that receive repeated cumulative vendor snapshots shall normalize them before they are treated as canonical additive stream deltas.

5.2. Final-only provider usage responses shall continue to be representable without unnecessary stateful normalization.

5.3. Canonical usage ingestion shall preserve `UsagePresence`, accounting source/authority/plane metadata, and `DedupeKey` where supplied.

5.4. If an ordinary canonical usage event has no provider identity key, the accounting ingestion seam shall generate stable lifecycle-local fact identity using the request/attempt stream identity and monotonic sequence rather than deduplicating by numeric value.

5.5. Backend-plugin host-only `AccountingEvidence` shall map directly into operator metering facts without becoming client-visible stream output solely to reach accounting.

5.6. `FinalizeBilling` or equivalent complete post-terminal provider evidence shall map to a cumulative/replacement/correction fact with stable source identity rather than being blindly added after an estimated or prior cumulative result.

5.7. A reusable backend/connector usage-evidence contract suite shall cover all-zero usage presence, explicit zero provider money when supported, repeated cumulative snapshots, duplicate identity replay, equal-value/different-identity evidence, cache/reasoning inclusion, and final authoritative correction.

5.8. A provider/connector that violates accounting evidence identity, presence, or finalization contracts shall fail its adapter/connector contract tests without requiring provider-specific logic in core.

### Requirement 6: Introduce One Focused Request/Attempt Accounting Application Owner

**Objective:** As a runtime maintainer, I want one small accounting collaborator, so that stream/failover code no longer implements economic policy and lifecycle bookkeeping itself.

#### Acceptance Criteria

6.1. `internal/core/accounting` shall expose a concrete service that creates one Request lifecycle for a logical request and one Attempt lifecycle for each committed backend attempt.

6.2. The Request lifecycle shall own customer frontend-ingress/frontend-egress measurement state, customer rater/authority bindings, customer fact references, and exactly-once request settlement state.

6.3. The Attempt lifecycle shall own backend-ingress/backend-egress measurements, provider usage/billing evidence, local fallback final measurement, operator rater/authority bindings, correction references, and exactly-once attempt settlement state.

6.4. Runtime stream/request types shall retain only narrow accounting lifecycle handles/identifiers and shall not retain duplicate fields such as last authority/customer usage aggregates, runtime usage-dedupe maps, economic merge state, or token-ledger writer state after cutover.

6.5. Runtime shall notify accounting of lifecycle facts/events and terminal outcome, while runtime remains the sole owner of routing/failover decisions, output commitment, and which terminal path wins.

6.6. Normal finish, cancellation, EOF/recovery, post-output errors, gate replacement, frontend encoder failure, close, swallowed/losing attempts, and late finalization shall reach the same accounting lifecycle APIs rather than each reimplementing settlement policy.

6.7. Accounting terminal/finalization methods shall be idempotent and safe to invoke from detached non-canceled contexts where existing requirements mandate post-cancellation accounting.

6.8. Durable terminal-work recovery shall carry immutable accounting/fact/reservation references needed to finish work and shall not require keeping a live stream object or request object in background state.

6.9. Runtime accounting-specific production code shall materially shrink after migration and shall not simply move unchanged runtime helpers into a differently named package.

6.10. Performance telemetry such as TTFT, duration and TPS shall be named/owned separately from economic accounting state.

### Requirement 7: Use One Rating Contract for Static and Injected Pricing

**Objective:** As a pricing integrator, I want one `economics.Rater` path, so that static OSS pricing and external customer/operator pricing cannot silently diverge in invocation semantics.

#### Acceptance Criteria

7.1. The existing static `PriceCatalog` behavior shall be preserved through an `economics.Rater` implementation/adaptor rather than a separate runtime pricing branch.

7.2. The static rater shall preserve current cache-read/cache-write/reasoning inclusion semantics, optional authoritative zero rates, checked overflow, currency handling, rounding behavior, and catalog version provenance.

7.3. When a generation-bound external rater is configured for a perspective, that rater shall be the selected rating authority for that perspective; a failure shall not silently fall back to an unrelated static catalog.

7.4. Provider-reported present operator money shall remain authoritative provider cost evidence and shall not be overwritten by local rating merely because a rater is configured.

7.5. When provider money is absent and rating is applicable, the rater shall consume reduced/presence-aware quantities and shall return version/source/authority provenance that remains associated with the settled/reported money.

7.6. Customer and operator rating shall remain separate invocations and results; operator provider cost shall not automatically become customer charge.

7.7. If rating cannot produce valid money, the system shall preserve absent/unavailable monetary evidence rather than using `0` with an ambiguous currency as an enforceable spend value.

7.8. Core accounting orchestration shall depend on the `economics.Rater` port rather than directly importing infrastructure static-catalog implementation details.

7.9. Composition shall be able to use the static rater without introducing new runtime registration frameworks or changing enterprise/public rater seams.

### Requirement 8: Route Built-In and External Usage Authority Through One Coordination Path

**Objective:** As an authority maintainer, I want one request/attempt provider stack, so that built-in fixed-window rules and external authorities obey the same lifecycle and compensation semantics.

#### Acceptance Criteria

8.1. The built-in usage-authority service shall participate through request/attempt authority-provider adapters and existing request/attempt coordinators rather than a separate direct runtime lifecycle path.

8.2. Admission, multi-rule reservation descriptor sets, failure posture, compensation, version binding, and settle/release sequencing shall remain behaviorally equivalent during migration.

8.3. Authority settlement amounts shall be selected from the same reduced metering facts/exposure basis used by the accounting lifecycle, with independent per-unit token/request authority and monetary authority.

8.4. Estimated settlement followed by stronger authoritative evidence shall preserve existing adjustment/re-settlement semantics and idempotency rather than double-count usage or permanently ignoring the correction.

8.5. Losing/swallowed attempts shall settle incurred operator exposure and release only residual reservation according to current rule semantics.

8.6. Customer request authority shall settle only the customer logical-request evidence and shall not import provider-attempt quantities/money into the customer plane.

8.7. Clamp preview/admission shall use the same provider/coordinator architecture and generation-bound rating/rule inputs as committed authority execution rather than a second direct usage-authority bypass.

8.8. Runtime/configuration shall construct built-in and external authority providers explicitly through the existing generation/composition model without global mutation.

8.9. After coordinator cutover, architecture tests shall reject new direct runtime calls to built-in usage-authority Admit/Settle/Release/ApplyUsage lifecycle operations except an explicitly documented temporary compatibility adapter.

8.10. The migration shall retire or isolate legacy single-reservation/scalar authority mirrors when in-repo consumers no longer require them; complete descriptor sets remain the internal mutation contract.

### Requirement 9: Make Economic Reporting Reducer-Backed and Presence-Aware

**Objective:** As an operator, I want reports to use the same fact semantics as live accounting, so that cumulative/replacement facts cannot produce different reported totals.

#### Acceptance Criteria

9.1. `DualPlaneReportInputsFromFacts` and metering-backed control-plane report paths shall group facts into legal streams, reduce each stream with the authoritative reducer, and aggregate reduced stream results rather than summing raw cumulative/replacement facts.

9.2. Report results shall remain invariant under equivalent delta chunking and shall correctly handle cumulative, correction, authoritative replacement, replay, and explicit zero/absence semantics.

9.3. Customer charge, operator cost, compression quantities, and routing-overhead inputs shall remain independently queryable and shall only be combined through explicit documented calculations.

9.4. The legacy `pkg/lipsdk/usage.Observer` path shall remain best-effort telemetry/extension observation and shall not be an authoritative billing/reporting source after metering-backed cutover.

9.5. The system shall not require widening `usage.Event` with billing presence fields solely to keep the legacy observer path authoritative.

9.6. A report built from incomplete historical/legacy facts shall mark completeness/provenance explicitly and shall not invent missing ingress, token presence, or money presence.

9.7. Report/reducer regression tests shall cover repeated cumulative snapshots and authoritative replacement so the current raw-sum failure mode cannot return.

9.8. Economic report rows that contain rated money shall preserve relevant rating version/source provenance where the underlying facts/results provide it.

### Requirement 10: Retire the Legacy Token Ledger as an Independent Write Authority

**Objective:** As a maintainer, I want one writable usage evidence journal, so that durable token records cannot diverge from metering facts.

#### Acceptance Criteria

10.1. Runtime shall stop writing the legacy token-accounting ledger directly after the metering/accounting lifecycle cutover.

10.2. The implementation shall inventory every remaining token-ledger consumer before deleting or changing its query/storage contract.

10.3. If a retained consumer needs legacy token-ledger rows, those rows shall be a one-way query/rebuildable projection of metering facts or reduced metering results rather than a separately mutated source of truth.

10.4. Money shall remain on metering/rating evidence; legacy token-ledger projection shall not become a second monetary ledger.

10.5. If no supported consumer requires the durable token ledger after inventory, the memory/SQLite/PostgreSQL token-ledger store/schema/wiring shall be deleted rather than preserved speculatively.

10.6. The optional metering journal shall remain the only durable technical usage/cost evidence journal introduced by this spec.

10.7. Token counting/preflight measurement functionality that remains independently useful shall stay focused and shall not retain reconciliation/ledger ownership merely to preserve package history.

10.8. Architecture tests shall prevent reintroduction of direct legacy-ledger writes from runtime/accounting after deletion or projection cutover.

### Requirement 11: Make Billing/Usage Decisions Deterministically Explainable

**Objective:** As an operator debugging a billing or quota discrepancy, I want a stable evidence chain, so that a final number can be explained without reconstructing hidden runtime branches.

#### Acceptance Criteria

11.1. For one request/attempt, safe diagnostic/replay code shall be able to correlate lifecycle ID -> metering stream/fact IDs -> reduced quantities/money -> rating version/source -> authority reservation/settlement mutation identifiers where those stages exist.

11.2. The replay/explanation path shall invoke the same production metering reducer used by reports/accounting rather than a diagnostics-only aggregation algorithm.

11.3. Replay shall be deterministic from the supplied facts/version refs and shall not require raw prompt/completion content or provider SDK objects.

11.4. Diagnostic evidence shall preserve redaction/scope safety and shall not expose credentials, raw authorization headers, or proprietary pricebook internals.

11.5. When evidence is incomplete, superseded, corrected, estimated, authoritative, or unavailable, the explanation shall report that state rather than emitting an unexplained scalar total.

11.6. Terminal-work/reconciliation failures shall retain sufficient stable identifiers for operators to locate the pending/retried accounting work without retaining live stream objects.

### Requirement 12: Prove Semantics With Reusable Contract, Property, and Lifecycle Tests

**Objective:** As a maintainer, I want accounting correctness proven at boundaries, so that implementation refactors do not rely on brittle internal call-graph tests.

#### Acceptance Criteria

12.1. Before replacing a current semantic owner, the implementation shall add RED characterization tests for the supported behavior and known recent defect classes it owns.

12.2. The reducer test suite shall include metamorphic/property-style cases proving delta chunking equivalence, replay identity, equal-value/different-identity distinction, cumulative replacement, correction/replacement behavior, explicit zero/absence, overflow, and currency invariants.

12.3. Backend accounting-evidence tests shall use real canonical/metering types and small probes/stubs rather than mocking internal runtime call graphs.

12.4. Runtime lifecycle tests shall cover normal winner, sequential failover, parallel loser, swallowed attempt, pre-output failure, post-output failure, cancellation before/after output, EOF recovery, completion-gate replacement, frontend encoder failure, close, and late authoritative correction.

12.5. Tests shall prove one customer lifecycle across failover/parallelism and independent operator lifecycles for each provider attempt.

12.6. Control-plane tests shall prove report output equals aggregation of reducer results and shall include cumulative/replacement regression cases.

12.7. Durable metering and authority tests shall prove memory/SQLite/PostgreSQL restart/replay idempotency where those adapters exist.

12.8. Provider/connector contract tests shall prove stable evidence identity/presence and no duplicate billing across sideband/finalizer replay.

12.9. Concurrency-sensitive accounting/terminal code shall be exercised under the race detector where practical and shall not introduce per-request background goroutines.

12.10. Architecture tests shall prove forbidden dependency/symbol reintroduction and shall be part of existing quality gates.

### Requirement 13: Keep the Target Architecture Small and Go-Idiomatic

**Objective:** As a Go maintainer, I want cohesive concrete packages and narrow real seams, so that the cleanup does not replace existing complexity with abstraction complexity.

#### Acceptance Criteria

13.1. Concrete internal accounting constructors shall return concrete types unless a stable external/public substitution boundary already requires an interface.

13.2. Interfaces shall be defined where consumed and shall represent real storage/rating/authority/counting substitution; no interface shall be introduced solely for mocking or architectural symmetry.

13.3. Request/Attempt lifecycle types shall keep contexts method-scoped and shall not store contexts in structs.

13.4. The accounting lifecycle shall not start one goroutine per request/attempt; durable terminal processing shall reuse existing terminal-work ownership.

13.5. Reducer and rating math shall remain pure/deterministic where possible and shall not perform I/O.

13.6. Provider-specific conversion logic shall stay at adapters/connector edges; no provider switch shall be introduced into generic accounting/metering code.

13.7. Architecture/file budgets shall target hotspots rather than encourage mechanical package/file proliferation.

13.8. Documentation shall classify metering, rating, usage authority, control-plane projections, and future financial accounting as distinct responsibilities and shall update stale core package ownership descriptions.

### Requirement 14: Perform a Deletion-Oriented Brownfield Migration

**Objective:** As the project owner, I want the migration to finish rather than layer another system on top, so that maintenance cost measurably decreases.

#### Acceptance Criteria

14.1. Before implementation, the project shall record a baseline of accounting-specific production lines, runtime accounting helpers/fields, direct token-ledger writers, direct usage-authority lifecycle calls, duplicated aggregation helpers, and dependency edges.

14.2. Migration shall proceed in independently green phases: characterize -> establish fact/reducer contracts -> introduce accounting lifecycle -> migrate rating/authority -> migrate runtime -> migrate reports/compatibility views -> delete legacy paths -> certify.

14.3. During migration, old and new calculations may be compared in tests/fixtures, but production shall not run two independently mutating accounting authorities as a permanent shadow architecture.

14.4. After accounting lifecycle cutover, `tokenaccounting/domain.Reconcile` and `tokenaccounting/streamusage` shall be deleted or reduced to unrelated measurement functionality only if a documented supported consumer still requires them.

14.5. After runtime cutover, duplicate runtime usage merge/projection helpers, value-based reconciliation dedupe, per-event cost enrichment, runtime usage-dedupe state, and customer-evidence economic state shall be deleted rather than left as alternate paths.

14.6. After metering cutover, direct runtime token-ledger writes and obsolete token-ledger observability/wiring shall be removed according to the consumer inventory.

14.7. After query cutover, raw-fact report aggregation that bypasses the reducer and legacy usage-observer economic truth paths shall be deleted/demoted.

14.8. The final implementation shall reduce accounting-specific production lines inside `internal/core/runtime` by at least 40% from the implementation-start baseline.

14.9. The final implementation shall reduce the defined legacy economic-semantic production surface by at least 20% from the implementation-start baseline and shall not increase total `internal/core` production lines attributable to this spec after required deletions.

14.10. Architecture tests shall permanently forbid reintroduction of deleted major semantic owners/symbols or equivalent direct dependency paths.

14.11. Retained compatibility adapters/fields shall have a documented consumer and retirement trigger; compatibility with no supported consumer shall be deleted.

14.12. Final certification shall include focused unit/property tests, runtime lifecycle/race tests, durable metering/authority tests, architecture gates, `make quality-checks`, `make test`, and applicable PostgreSQL/integration gates with environment limitations recorded truthfully.

14.13. `spec.json` shall remain implementation-disabled until maintainers explicitly approve requirements, design, and tasks; this spec-only PR shall not authorize production code changes.
