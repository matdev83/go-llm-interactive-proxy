# Requirements Document

## Introduction

Go-LIP's current usage/accounting implementation mixes evidence capture, stream processing, rating, quota/budget settlement, reporting, and retry lifecycle state across runtime, token-accounting, metering, control-plane, and usage-authority packages. That architecture is materially more complicated than the underlying business problem.

This specification replaces the live-stream accounting model with a **CDR-first billing architecture** modeled after telecom call-detail-record processing:

1. before an LLM call starts, perform one synchronous affordability check using a conservative maximum customer-charge estimate;
2. atomically reserve that pessimistic amount against the account's prepaid balance or credit limit;
3. execute the turn without billing/rating logic in the streaming path;
4. when attempts and the logical turn terminate, seal one immutable Turn CDR containing final provider usage/cost evidence;
5. process the CDR after execution to rate actual usage, settle the reservation, release unused funds, and produce reporting/audit records.

Concurrent prepaid safety is provided by **atomic pessimistic reservations for every in-flight turn**. Outstanding reservations reduce spendable balance, so multiple sessions can execute concurrently without collectively exceeding the prepaid balance or credit line. The alternative softswitch technique of dynamically reducing a low-balance account to one concurrent session is deliberately not selected as the primary architecture because atomic reservations are deterministic, model-aware, and already match the repository's existing `reserved`/`consumed` storage semantics.

The specification prioritizes simplicity, explicit ownership, deterministic testing, and aggressive deletion of current runtime accounting machinery. It does not introduce a generic event bus, CQRS framework, workflow engine, DI container, or a second streaming economic model.

## Boundary Context

- **In scope:** post-turn CDR billing; immutable Turn/Attempt CDR contracts; final provider usage/cost evidence capture; pessimistic maximum customer-charge estimation; synchronous affordability admission; atomic prepaid/credit reservations across concurrent turns; post-turn rating and reservation settlement; durable retry-safe CDR processing; control-plane billing/reporting projections; compatibility migration; deletion of runtime usage aggregation/rating/settlement machinery; architecture tests proving billing isolation.
- **Out of scope:** invoices, taxation, payment collection, card processing, top-up UX, accounts-receivable workflows, double-entry general ledger, foreign-exchange conversion, real-time per-token debiting, terminating an in-flight generation when a monetary threshold is crossed, arbitrary user-defined billing scripts, changing routing semantics, changing provider wire protocols, or changing no-retry-after-client-output semantics.
- **Supersession:** this specification supersedes the implementation direction in `.kiro/specs/usage-accounting-architecture-convergence/`. That prior spec remains historical evidence until maintainers decide whether to archive it; implementation shall follow this CDR-first spec instead.
- **Primary ownership:** runtime owns execution; backend adapters own provider evidence extraction; `internal/core/billing` owns affordability, CDR interpretation, rating orchestration, and settlement policy; a driven billing store owns atomic balance/reservation/CDR persistence; control-plane and legacy surfaces are read projections.
- **Only live billing seam:** after routing has produced a side-effect-free candidate plan and before any upstream request/process work, runtime may call one affordability/reservation service. No other billing/rating/settlement decision belongs in the live stream path.
- **Revalidation triggers:** changes to routing candidate-plan semantics; backend final billing evidence; canonical usage event compatibility; account/reservation persistence; request terminal ownership; client-visible usage emission; or any reintroduction of stream-time billing decisions.

## Requirements

### Requirement 1: Separate Execution From Billing

**Objective:** As a maintainer, I want billing to operate on completed turn records rather than live stream events, so that execution remains simple and accounting failures cannot corrupt stream lifecycle behavior.

#### Acceptance Criteria

1.1. The runtime shall perform no customer charging, provider-cost rating, usage reconciliation, economic deduplication, balance mutation, or reservation settlement while provider output is streaming.

1.2. Before the first upstream network or connector-process action for a logical turn, the runtime may invoke exactly one synchronous affordability/reservation decision for that turn.

1.3. After the logical turn reaches a terminal state, the runtime shall submit exactly one sealed Turn CDR to the billing persistence seam using an idempotent turn identity.

1.4. Runtime stream handlers shall not iterate usage events for billing purposes or maintain running customer/operator token or money totals.

1.5. Runtime retry/failover, output-commit, cancellation, terminal CAS, and B2BUA continuity semantics shall remain independent of billing outcome processing.

1.6. Billing processing failure after a turn has terminated shall not alter which backend attempt won, what content was already surfaced, or whether retry/failover was permitted.

1.7. Client-visible protocol usage fields/events may continue to be emitted for wire compatibility, but billing shall not consume those frontend-facing events as its source of truth.

### Requirement 2: Define One Immutable Turn CDR

**Objective:** As a billing maintainer, I want one compact immutable record describing what happened during a logical turn, so that rating and settlement can be deterministic and replayable.

#### Acceptance Criteria

2.1. The system shall define one versioned `TurnCDR` contract identified by a stable logical turn/request ID.

2.2. A Turn CDR shall contain the billing principal/account identity, reservation identity, start/end timestamps, logical model/routing identity needed for audit, final turn outcome, and an ordered list of Attempt CDRs.

2.3. Each Attempt CDR shall contain attempt identity, backend/provider identity, model identity, timestamps, terminal outcome, whether the attempt became client-visible/surfaced, and final provider billing evidence.

2.4. Final provider billing evidence shall preserve explicit presence for supported token quantities and monetary cost so that absent values differ from authoritative zero values.

2.5. The CDR shall record pricing/rating snapshot identity needed to reproduce the admission estimate and final customer charge without consulting mutable "current price" state.

2.6. CDRs shall not contain full prompt/completion content, credentials, secrets, raw authorization headers, or provider SDK objects.

2.7. Once sealed, a CDR payload shall be immutable; replay of the same CDR ID with different semantic content shall be treated as an integrity error rather than silently replaced.

2.8. The CDR schema shall be protocol-neutral and shall not contain OpenAI-, Anthropic-, Gemini-, or other provider-specific wire types.

### Requirement 3: Capture Final Provider Evidence at the Adapter/Attempt Boundary

**Objective:** As a backend maintainer, I want provider usage and cost evidence finalized at attempt termination, so that core billing never reconstructs economic truth from arbitrary stream chunks.

#### Acceptance Criteria

3.1. Backend adapters may observe provider usage metadata while decoding a stream, but they shall retain/normalize billing evidence privately and expose one final attempt-level evidence result when the attempt terminates.

3.2. If a provider exposes cumulative usage repeatedly, the adapter shall expose the final authoritative cumulative value rather than forwarding every cumulative sample into billing.

3.3. If a provider requires a post-stream `FinalizeBilling` operation, that operation shall complete or reach a classified final outcome before the Attempt CDR is sealed.

3.4. Connector `FinalizeBilling` and sideband accounting-evidence capabilities shall be reused where practical rather than introducing a second executable-plugin billing protocol.

3.5. A backend without authoritative provider cost may return final token/resource usage with cost absent; post-turn rating shall determine cost from the bound pricing snapshot.

3.6. Adapter-level evidence collection shall not change canonical content-event ordering, cancellation propagation, no-retry-after-output behavior, or frontend streaming latency except for existing terminal finalization requirements.

3.7. Multiple provider evidence observations that belong to one attempt shall be resolved inside the owning adapter/finalization boundary, not by a generic runtime structural-value deduper.

### Requirement 4: Compute a Conservative Maximum Customer-Charge Bound Before Upstream Work

**Objective:** As a prepaid/credit operator, I want each turn to reserve a provable upper bound on what the customer can be charged, so that admission can prevent overspending before provider work starts.

#### Acceptance Criteria

4.1. After routing produces a side-effect-free eligible attempt plan and before upstream work, billing shall compute a finite `MaxCustomerCharge` for the logical turn.

4.2. The estimate shall use a versioned pricing snapshot and the customer charging policy that will later rate the CDR.

4.3. Input exposure shall use a deterministic preflight token/resource estimate; when cache discounts are uncertain, the maximum-cost calculation shall assume the non-discounted/higher applicable input price.

4.4. Output exposure shall use the smaller of a valid client-requested maximum and the model/provider maximum when the client explicitly lowers the maximum; otherwise it shall use the model/provider maximum supported for that routed candidate.

4.5. The estimator shall include every chargeable dimension that can increase the customer's bill under the selected pricing policy, including fixed per-request or non-token charges when configured.

4.6. When the customer charging policy bills only the surfaced logical turn and absorbs failed/losing provider attempts as operator cost, the reservation bound shall not incorrectly multiply customer exposure by internal retry count.

4.7. When a customer charging policy can legitimately charge multiple attempts or route legs, the estimator shall include the maximum chargeable exposure of all such legs, including parallel legs that can incur cost concurrently.

4.8. The estimator shall not make provider network calls, spawn connector processes, or mutate routing/account state.

4.9. If any routed path that can be charged lacks a finite safe upper bound, strict prepaid/credit admission shall fail closed unless an administrator configured an explicit conservative per-call ceiling for that path.

4.10. Checked integer arithmetic shall be used for all money/token upper-bound calculations; overflow shall cause deterministic admission failure.

4.11. The estimate result shall include a human/debuggable basis describing the pricing version, input estimate, output ceiling, relevant pricing components, and route/charging-policy assumptions without containing secrets.

### Requirement 5: Atomically Reserve Pessimistic Cost Against Prepaid Balance or Credit

**Objective:** As an operator, I want a request to start only when its worst-case customer charge fits within currently spendable funds, so that a single turn cannot exceed the account's prepaid/credit capacity.

#### Acceptance Criteria

5.1. Affordability admission shall atomically compare the turn's `MaxCustomerCharge` with the account's spendable amount and create a reservation/hold when sufficient capacity exists.

5.2. Spendable capacity shall subtract all active reservations from the account's prepaid balance plus permitted credit allowance, using one currency per enforced account/balance scope.

5.3. Reservation creation and account reserved-total mutation shall occur in one storage transaction or equivalent atomic compare-and-reserve operation.

5.4. When capacity is insufficient, the system shall deny the turn before any upstream work and return a stable payment/insufficient-credit classification suitable for frontend mapping (for HTTP frontends, normally 402).

5.5. Reservation identity shall be deterministic/idempotent for one account + logical turn so that admission replay cannot reserve funds twice.

5.6. A successful reservation shall bind the account ID, turn ID, reserved amount, currency, pricing snapshot, charging-policy version, and a bounded expiry/deadline.

5.7. If execution never starts after reservation, the system shall release the reservation idempotently with an explicit reason.

5.8. Reservation/read APIs shall expose remaining spendable capacity without requiring enumeration of every active request in application code.

5.9. Monetary storage shall use integer nano-units or an equivalently exact fixed-point representation; floating point shall not be used for authoritative balance/reservation mutation.

### Requirement 6: Prevent Concurrent Sessions From Collectively Overspending

**Objective:** As a prepaid/credit operator, I want multiple concurrent LLM sessions to share one account safely, so that concurrency cannot race past the prepaid balance or credit limit.

#### Acceptance Criteria

6.1. Every concurrently admitted turn that is subject to hard monetary enforcement shall hold its own pessimistic reservation until post-turn settlement or explicit release.

6.2. A new admission shall evaluate capacity after accounting for all already-active reservations, so no separate in-memory scan of running sessions is required.

6.3. Two or more simultaneous reserve attempts against the same account shall be serialized/atomically arbitrated by the balance store so that the sum of accepted reservations never exceeds spendable capacity.

6.4. The architecture shall not rely on a heuristic "low balance => concurrency 1" threshold for correctness.

6.5. A low balance may naturally cause all new requests whose pessimistic bound does not fit to be denied while already-reserved requests continue.

6.6. The correctness invariant shall hold across multiple proxy processes sharing the same durable balance store, not only within one Go process.

6.7. Tests shall prove that arbitrary concurrent reserve/release/settle interleavings cannot produce negative remaining capacity when every final charge is less than or equal to its reserved bound.

6.8. If an external/non-transactional balance provider cannot provide atomic reservation semantics, it shall not be eligible for strict concurrent prepaid enforcement without a separately specified safety mechanism.

### Requirement 7: Process Billing From Sealed CDRs After Execution

**Objective:** As a billing maintainer, I want actual charging to be a deterministic post-turn function, so that it can be tested without runtime streams or backend mocks.

#### Acceptance Criteria

7.1. The core billing processor shall accept a sealed Turn CDR plus immutable pricing/charging-policy snapshots and produce a deterministic Billing Result.

7.2. Billing calculation tests shall be executable using plain Go values without starting the runtime, an HTTP server, a provider adapter, or goroutines.

7.3. Operator cost shall be calculated from every provider-billable attempt represented by the CDR, including failed/losing attempts when provider evidence shows cost.

7.4. Customer charge shall follow the bound customer charging policy and shall be computed independently of operator cost.

7.5. Authoritative provider-reported monetary cost shall remain provider/operator evidence and shall not automatically become the customer charge unless the customer policy explicitly uses pass-through cost.

7.6. When final authoritative cost is absent, configured pricing shall rate final usage/resource quantities using the CDR's bound pricing snapshot.

7.7. Explicit authoritative zero monetary cost shall be preserved as zero and shall not trigger fallback rating merely because the numeric value is zero.

7.8. The processor shall use checked arithmetic and deterministic currency validation.

7.9. The Billing Result shall contain actual customer charge, operator cost where available, released reservation amount, pricing/policy versions, and explainable calculation components sufficient for audit/debugging.

### Requirement 8: Settle Reservations Exactly Once From Billing Results

**Objective:** As an operator, I want post-turn settlement to consume actual charge and release unused reserved funds atomically, so that balances remain correct under retries and crashes.

#### Acceptance Criteria

8.1. Applying a Billing Result shall atomically convert the turn reservation into actual consumed customer charge and release the unused reservation remainder.

8.2. Settlement shall be idempotent by CDR/turn ID; replay of an already applied Billing Result shall not charge the account again.

8.3. For a normal strict-prepaid turn, actual customer charge shall be less than or equal to the reserved `MaxCustomerCharge`.

8.4. If actual charge exceeds the reservation, the system shall classify this as an invariant/pricing-bound failure, record diagnostic evidence, prevent silent further overspend, and require an explicit recovery policy rather than silently treating the overage as normal.

8.5. A failed/canceled turn whose charging policy yields zero customer charge shall release the full reservation during settlement.

8.6. Reservation settlement shall not depend on whether the client connection is still open.

8.7. Settlement and CDR-processing status shall be recoverable after process restart without re-running the LLM request.

### Requirement 9: Persist and Recover CDR Processing Without an Accounting Event Framework

**Objective:** As an operator, I want post-turn billing to survive crashes while remaining operationally simple, so that durable correctness does not require a generic event-sourcing platform.

#### Acceptance Criteria

9.1. The system shall persist a sealed Turn CDR durably before considering billing handoff complete.

9.2. The CDR store shall distinguish unprocessed, processing/retryable, processed, and terminal-error states using a bounded simple state model.

9.3. A single in-process worker/poller or equivalent simple terminal-work mechanism may process pending CDRs; the design shall not require Kafka, a generic message bus, CQRS, or a workflow engine.

9.4. Processing retry shall be idempotent and safe after crashes at any point between CDR persistence and reservation settlement.

9.5. A failed post-turn processor shall leave the pessimistic reservation held, preferring temporary under-availability to potential prepaid overspend.

9.6. Stale reservation cleanup shall release a hold only after the associated turn is known not to be executing and the configured maximum request lifetime plus safety grace has elapsed.

9.7. Operators shall be able to query stuck/unprocessed CDRs and reservations with bounded pagination and safe diagnostic fields.

9.8. The system shall not keep an unbounded in-memory map of all historical CDRs or reservations.

### Requirement 10: Make Reporting and Audit Read From Processed CDR/Billing Records

**Objective:** As an operator, I want one reporting truth derived from completed billing records, so that dashboards and audits do not re-interpret raw stream usage differently from charging.

#### Acceptance Criteria

10.1. Authoritative customer spend reports shall be derived from applied Billing Results/settlements rather than summing raw `lipapi.Event` usage deltas or raw metering facts.

10.2. Operator-cost reports shall be derived from CDR/Billing Result attempt evidence using the same post-turn calculation semantics.

10.3. Control-plane reporting shall preserve customer charge and operator cost as separate perspectives.

10.4. Legacy token-accounting or metering views that still have consumers shall become one-way projections from CDR/Billing Results or terminal attempt evidence and shall not feed billing decisions back into the processor.

10.5. If consumer inventory shows a legacy token ledger has no required consumer, it shall be deleted rather than retained for symmetry.

10.6. `pkg/lipsdk/usage.Observer` or equivalent usage-observer hooks may remain best-effort telemetry surfaces, but they shall not be authoritative for charging, balance mutation, or spend reporting.

10.7. A diagnostic explanation for one turn shall be reconstructible from the reservation, sealed CDR, pricing/policy version, Billing Result, and settlement record without inspecting live runtime state.

### Requirement 11: Remove Stream-Time Accounting Machinery

**Objective:** As a maintainer, I want the migration to delete obsolete economic state and interpretation paths, so that the new CDR design replaces rather than layers on top of the old architecture.

#### Acceptance Criteria

11.1. Billing shall stop using `tokenaccounting/streamusage.Reconstruct` or equivalent stream-event reconciliation as a settlement source.

11.2. Runtime `enrichUsageCost` or equivalent per-usage-event price mutation shall be removed from the execution path.

11.3. Runtime economic dedupe maps such as `internalUsageKeys` shall be removed once final adapter evidence/CDR identity is authoritative.

11.4. Runtime fields whose only purpose is remembered authority/customer usage, token-accounting-finalized state, economic raw-event merging, or legacy ledger recording shall be deleted.

11.5. Duplicate `projectAggregatedUsageCounters`-style billing aggregation helpers shall be removed; client-wire projection helpers may remain only where they serve protocol output and are named/owned as such.

11.6. Monetary reservation settlement shall no longer accept raw metering facts, usage-event arrays, exposure bases, or stream lifecycle classification as its normal input; it shall consume a Billing Result/CDR settlement input.

11.7. Direct runtime writes to the legacy token ledger and raw-fact economic reporting paths shall be retired.

11.8. Existing `metering.Fact` infrastructure may remain for non-billing telemetry/audit consumers, but billing correctness shall not require its reducer or correction semantics.

11.9. The final implementation shall include architecture guards preventing the deleted stream-time billing responsibilities from being reintroduced into runtime receive/stream handlers.

### Requirement 12: Preserve Go-LIP Runtime and Adapter Safety Boundaries

**Objective:** As a project maintainer, I want billing simplification to leave unrelated protocol/runtime guarantees intact, so that economic refactoring does not regress proxy behavior.

#### Acceptance Criteria

12.1. Provider SDKs and provider wire types shall remain confined to backend/frontend adapter edges and shall not enter `internal/core/billing`.

12.2. `pkg/lipapi` shall remain protocol-neutral; this specification shall not require adding customer-balance, reservation, invoice, or provider-pricing concepts to canonical message/event types.

12.3. Streaming shall remain the primary execution path and non-streaming shall remain collection over canonical streams.

12.4. Transparent retry/failover after the first client-visible output shall remain prohibited.

12.5. Routing candidate legality, capability negotiation, secure-session behavior, credential handling, and B2BUA continuity shall remain unchanged except for inserting the side-effect-free cost-bound/admission check before upstream execution.

12.6. The CDR terminal handoff shall not create a second terminal owner or bypass existing terminal-work/cleanup ownership.

12.7. No DI container, reflection registry, package `init()` registration, dynamic Go plugin loading, generic service locator, or generic economic event bus shall be introduced.

### Requirement 13: Make the Architecture Small and Provable

**Objective:** As a maintainer, I want few explicit billing components with executable architectural rules, so that future billing features have a clear home and tests can prove ownership.

#### Acceptance Criteria

13.1. Production billing policy/orchestration shall live in one focused internal billing bounded context rather than being split across runtime, tokenaccounting, metering, controlplane, and usageauthority application services.

13.2. Interfaces shall be defined at real substitution/storage boundaries and remain narrow; interfaces shall not be introduced solely for mocking or layer symmetry.

13.3. The live runtime dependency surface shall be limited to an affordability/reservation seam before upstream work and a CDR persistence/handoff seam at terminal completion.

13.4. Rating, customer charging policy, settlement, and CDR processing shall be unit-testable as deterministic functions/services with fake clocks/stores only where persistence/time requires substitution.

13.5. Concurrency tests shall exercise a real store contract (including SQLite/PostgreSQL implementations where supported) rather than mocking transaction ordering.

13.6. Architecture tests shall fail if runtime stream-handler packages import or invoke rating, balance mutation, reservation settlement, CDR processing, or economic-reducer responsibilities.

13.7. Implementation shall follow RED → GREEN → REFACTOR for each migration slice and shall retain characterization tests until the old path is deleted.

13.8. The migration shall prefer deleting obsolete packages/files/fields over creating compatibility facades that permanently preserve both architectures.

13.9. At completion, maintainers shall have one documented data flow: `route plan -> max-cost reserve -> execute -> seal CDR -> process/rate -> settle -> report`.

13.10. The implementation shall update architecture/package documentation so future contributors can determine where affordability, CDR capture, rating, settlement, and reporting changes belong without tracing runtime stream internals.
