# Requirements Document

## Introduction

Repair the merged billing pipeline at `b560dbff3a06dc44a324aa15a0245ccaed5f76cb` so a paid host enforces the user's eleven financial contracts. This is a brownfield refinement of the billing work behind #620/#659/#666 and preserves the non-monetary uncertainty reporting implemented by #698 for #692/#694. It is not another general billing-engine rewrite.

The original review findings are retained as F01-F14 in `reference/source-review.md`. This specification is normative for new strict paid work. The original review is evidence, not a license to carry its undecided alternatives into implementation.

## Boundary Context

In scope: funded economic attempts, conservative component quotation, account serialization, mandatory per-dispatch authorization, durable obligation capture, chargeability/evidence semantics, double-entry settlement and debt, account-wide cancellation, retries, frontend financial errors, backend-family capability enforcement, dual-dialect migration, and release gates. The scope also includes lossless frontend/backend media codecs, full input and output mixtures, joint economic option contracts, every concrete builtin/connector contribution, independent transports, and exhaustive generated combination certification. Existing core and SDK ownership is preserved. A-leg/session identity remains continuity, not a forever-open invoice.

Out of scope: billing UI, payment-processor integration, tax accounting, automatic debt forgiveness, new provider offerings, a new message broker, provider SDK imports into core, a TypeScript rewrite, and changes to ordinary stock non-billing behavior. Native-inexpressible or inherently unbounded economic request shapes are explicitly rejected and documented. A natively representable finite-priced shape missing proxy or billing code is an implementation gap that blocks completion, not an acceptable negative result. This work does not invent unmounted provider service APIs.

## Normative interpretation of the eleven contracts

1. A **root submission** is an authenticated user request. Its **BillingCallID** and any child call IDs are financial lineage. A B-leg may contain one or more independently payable economic attempts; every payable attempt needs its own dispatch identity and funding. Proven idempotent repeats of the same provider charge must not be billed twice.
2. **Affordability** is balance less credit floor, uncollected debt, and all unresolved commitments. Completed-but-unsettled work and unknown dispatch liability are commitments, not available credit. Prepaid floor is zero; authorized postpaid credit is explicitly represented by its existing negative floor.
3. **Catalog-first means catalog-first.** Reserve the valid models.dev output maximum. Do not replace it with `min(client_limit, model_limit)`. Respect the smaller client limit on the wire, but do not reduce the reservation for it. Missing-catalog fallback is disabled by default and is permitted only when explicitly enabled and enforceable.
4. **All attributable** describes charge coverage, not equality of customer and provider prices. Actual usage follows the frozen retail tariff and supplier COGS follows its separate source. Unfunded discounts/subsidies are forbidden in the new default profile. A formally budgeted operator allocation is not needed for this implementation and must not be introduced as an escape hatch.
5. A financial rejection of an unadmitted independent root is not exhaustion of an admitted request. Otherwise a rogue user could cancel funded siblings by repeatedly submitting unaffordable roots. An unfundable required continuation/child of admitted work or a real bound breach does trigger the account breaker.
6. **Immediately halt** has a financial linearization point and explicit propagation limits (D08). No newly authorized dispatch may cross the committed account fence. Already authorized/in-flight remote work is cancelled and remains fully reserved; this specification does not claim instantaneous remote physical cessation.
7. **Never dropped** means durable ownership until posting or an explicit audited reconciliation resolution. Transient failures never exhaust a retry count. A database unavailable forever, a missing provider invoice, or destruction of every durable replica cannot be solved by retry loops alone. Missing evidence remains visible and funded; it must never be fabricated as a zero or quote-sized actual invoice.
8. The reservation is not the invoice. A charge exceeding a proven bound is a detected invariant/provider-contract breach, not an approved way to overspend. Debt recording and cancellation are containment, not evidence that C02 passed.
9. Every nonzero monetary operation is balanced. Operational reservation movements are balanced in a separate exposure book and do not debit settled customer money at admission. Known-zero dispositions are recorded idempotently without fictitious positive journal entries.

## Original contract index

| Contract | Enforced outcome | Requirement groups |
|---|---|---|
| C01 | All attributable B-legs charged | 1, 2, 4, 7, 8, 9, 12, 14, 17, 18, 19, 20, 21, 22, 23, 24 |
| C02 | No unfunded user consumption | 3, 4, 5, 6, 9, 17, 18, 19, 20, 21, 22, 23, 24 |
| C03 | 1,000-request burst cannot create unfunded work | 3, 4, 5, 13, 15, 17, 18, 19, 20, 21, 22, 23, 24 |
| C04 | Exhaustion stops all ongoing same-account work | 6, 7, 16, 17, 18, 19, 20, 21, 22, 23, 24 |
| C05 | Full pessimistic calculation before paid handling | 3, 4, 12, 17, 18, 19, 20, 21, 22, 23, 24 |
| C06 | Subtract all concurrent pessimistic commitments | 5, 9, 13, 17, 18, 19, 20, 21, 22, 23, 24 |
| C07 | Billing errors retry, never silently dropped | 7, 8, 9, 10, 16, 17, 18, 19, 20, 21, 22, 23, 24 |
| C08 | Database spikes preserve billing work | 7, 10, 13, 16, 17, 18, 19, 20, 21, 22, 23, 24 |
| C09 | Readable affordable-token error | 3, 5, 11, 17, 18, 19, 20, 21, 22, 23, 24 |
| C10 | Double-sided ledgers | 9, 14, 17, 18, 19, 20, 21, 22, 23, 24 |
| C11 | Customer debit, spend record, operator revenue | 2, 8, 9, 17, 18, 19, 20, 21, 22, 23, 24 |

## Requirements

### Requirement 1: Enforced paid-host contract

**Objective:** As the proxy operator, I want to prevent optional composition choices from defeating the eleven financial promises.

#### Acceptance Criteria

1. Where monetary billing is enabled for new work, the host shall enforce the all-attributable-pessimistic/v1 contract rather than silently selecting legacy winner-only, advisory, or degraded billing.
2. When a monetary host is constructed or reloaded, it shall refuse publication unless funding, dispatch fencing, durable recovery, settlement, account cancellation, and request-specific billing capability are all available from one monetary authority.
3. Where the stock non-monetary host is used, the system shall preserve its non-billing behavior and shall not create accounts, require PostgreSQL, or imply financial protection.
4. When an unsupported backend or external billing binding is selected, the monetary host shall reject it before billable dispatch with a typed capability error; a missing marker or interface shall never imply support.
5. While a previously admitted call is outstanding, the system shall preserve its frozen commercial contract and shall not reinterpret it using a newly published default.

### Requirement 2: Every attributable economic attempt

**Objective:** As the proxy operator, I want to charge attributable work without confusing delivery success with provider liability.

#### Acceptance Criteria

1. When a user submission causes provider-payable primary, failover, parallel, thinker, executor, continuation, or auxiliary work, the system shall retain its trusted account, submission, call, B-leg, and economic-attempt attribution.
2. When an attempt is dispatched or dispatch is ambiguous, the system shall retain its liability regardless of error, timeout, cancellation, hidden output, losing a race, or failure to surface output.
3. If no provider dispatch occurred and no payable side effect occurred, the system shall record proven-not-dispatched rather than create an inference charge.
4. When more than one B-leg refers to the same proven provider charge, the system shall recognize that economic charge exactly once and retain every attributable reference.
5. When a submission-scoped fixed fee applies, the system shall charge it once under a trusted submission identity, while retaining independent per-attempt fees.
6. When customer and supplier prices differ, the system shall retain separate customer revenue and supplier COGS and shall reject new strict offers without a certified funding and tariff-dominance proof; this profile shall not assume an operator subsidy.

### Requirement 3: Pessimistic per-request quotation

**Objective:** As the proxy operator, I want to reserve a real upper bound rather than an ordinary-price estimate.

#### Acceptance Criteria

1. Before admitting paid work, the system shall calculate a finite nonnegative upper bound for every chargeable dimension of the effective request under full cache miss.
2. When cache creation is possible, the input bound shall cover the full current input at the most expensive permitted cache-write treatment, including TTL and pricing modifiers, without incorrectly adding inclusive prices twice.
3. When models.dev provides a valid output maximum for the resolved provider/model, the reservation shall use that maximum even if the client requests fewer output tokens.
4. If the catalog output maximum is unavailable, the system shall reject before dispatch unless the host explicitly enables a positive client-limit fallback that the selected adapter can enforce on all billed output dimensions.
5. When computing a quote, the system shall include all applicable reasoning, candidate multiplicity, modalities, paid tools, fixed, and resource charges, and shall reject any relevant dimension without a proven bound.
6. When arithmetic overflows, currency mismatches, authoritative prices are missing, or a positive bound cannot be established, the system shall reject rather than use an arbitrary static ceiling, zero default, or optimistic token estimate.
7. When a quote is accepted, the system shall retain the immutable request-limit, catalog, tariff, policy, schema, capability, and execution-envelope identities needed to reproduce it after restart.

### Requirement 4: Execution cannot exceed its funding envelope

**Objective:** As the proxy operator, I want to bind each payable dispatch to the effective payload and finite execution graph.

#### Acceptance Criteria

1. Before the first paid dispatch of a root request, the system shall reserve the full bound of its declared finite execution envelope, including planned failover, parallel, and thinker/executor slots.
2. Before any additional or enlarged paid work is dispatched, the system shall atomically allocate an unused funded slot or increase the reservation using current account headroom.
3. When transformations, route parameters, memo injection, provider encoding, or wire-path handling change economic inputs, the system shall validate the final effective request against its allocation before transmission.
4. When an execution permit is consumed, the system shall bind it to one account, call, B-leg/economic-attempt, immutable payload/limit fingerprint, authority epoch, and dispatch identity and shall reject replay or foreign reuse.
5. Where a provider SDK, HTTP redirect, retry, reconnect, or connector can create another payable attempt, the system shall either obtain separate funding and attribution or prevent that attempt.
6. After customer-visible output is committed, the system shall preserve the prohibition on transparent failover/replay; an explicitly permitted semantic continuation shall receive separate funded attribution.

### Requirement 5: Atomic affordability across concurrent work

**Objective:** As the proxy operator, I want to prevent overspending under concurrent calls and a 1,000-request burst.

#### Acceptance Criteria

1. When admitting a request or extending its envelope, the system shall compare its incremental bound with balance minus credit floor, outstanding uncollected debt, and all open unsettled commitments atomically for that account.
2. While a dispatched attempt has unresolved liability, the system shall include that liability in the open commitment even after the client disconnects, the stream ends, a lease expires, or a worker fails.
3. When 1,000 simultaneous roots share an account funded for N complete request bounds, the system shall authorize no more than N such liabilities and shall not dispatch any unfunded attempt across supported process topologies.
4. When identical admission or extension operations are retried after uncertain acknowledgement, the system shall return the existing result without increasing or duplicating the commitment.
5. When a known charge is collected, the system shall preserve funds already allocated to other unresolved work and atomically update its own commitment and the account projection.
6. When an account mutation would consume committed funds or invalidate its credit floor, the system shall refuse the mutation or perform an explicitly authorized frozen-account remediation that records every resulting liability.

### Requirement 6: Account-wide exhaustion handling

**Objective:** As the proxy operator, I want to halt all same-account execution when ongoing work becomes unfundable.

#### Acceptance Criteria

1. When already-admitted work cannot fund a required extension or an actual liability violates its bound, the system shall atomically freeze new financial authorization for that account and schedule cancellation of all its active work.
2. When an unadmitted independent root is rejected for insufficient headroom, the system shall reject that root without cancelling already-funded siblings solely because of that rejection.
3. When an account is frozen, the system shall cancel its active A-legs, B-legs, detached children, queued attempts, and continuations without cancelling another account.
4. While an account freeze is in force, registration, launch, restart, and configuration reload shall not produce a new authorization under an obsolete account epoch.
5. Where several processes serve an account, the system shall durably propagate the freeze, make duplicate delivery harmless, and cancel locally within the bounded propagation/lease policy defined in design D08.
6. When cancellation is requested, the system shall retain the full bound for provider work that may still complete; a closed socket shall not prove zero remaining liability.
7. When funds are replenished, the system shall not automatically resume previously cancelled execution or clear a freeze whose debt, reconciliation, or authority conditions remain unresolved.

### Requirement 7: Durable financial obligation before side effects

**Objective:** As the proxy operator, I want to prevent a failed first terminal append from losing billable work.

#### Acceptance Criteria

1. Before a provider dispatch is authorized, the system shall durably retain the funding allocation, dispatch identity, trusted lineage, and a recoverable financial obligation.
2. Before accepting new paid work, the system shall reserve bounded durable terminal/recovery capacity; capacity pressure shall reject new work rather than sacrifice admitted obligations.
3. When terminal evidence is received, the system shall retain it or a durable evidence-pending recovery obligation independently of request context and stream-object lifetime.
4. If first terminal persistence, sealing, validation, or acknowledgement fails, the system shall not report successful financial completion and shall retain a retryable or explicitly repairable durable obligation.
5. When a process restarts after ambiguous dispatch, the system shall reconcile that attempt without blindly reissuing the provider request.
6. When non-monetary quota settlement, frontend egress, diagnostics, or observer execution fails, the system shall still durably schedule the monetary obligation.
7. When terminal cleanup competes or Close is repeated, the system shall preserve at-most-once stream effects and at-least-once financial delivery without duplicate financial posting.

### Requirement 8: Evidence, missing usage, and finality

**Objective:** As the proxy operator, I want to never confuse unavailable usage with a known zero charge.

#### Acceptance Criteria

1. When required usage is absent, the system shall distinguish known-zero, known-positive, pending, disputed, and not-applicable states and shall not finalize a dispatched attempt as free.
2. When provider usage arrives in deltas, cumulative snapshots, or late revisions, the system shall apply the adapter-declared semantics exactly once and retain source/version identity.
3. When known valid charges coexist with unresolved charges, the system shall post the valid obligations without releasing the unresolved liability or suppressing unrelated valid charges.
4. When all applicable economic finality conditions are proven, the system shall release only the unused remainder of the corresponding allocation; elapsed time alone shall not establish finality.
5. When usage or an invoice correction changes a previously recognized amount, the system shall append an idempotent linked adjustment rather than overwrite evidence or post the full amount twice.
6. When evidence is permanently contradictory or unavailable, the system shall retain a named reconciliation case, its exposure/debt, and a next action; it shall not invent actual usage or silently waive the charge.

### Requirement 9: Double-sided financial and exposure accounting

**Objective:** As the proxy operator, I want to record customer spend, operator revenue, provider cost, and residual debt coherently.

#### Acceptance Criteria

1. When a positive valid customer obligation is recognized, the system shall atomically record its customer-side debit and operator revenue credit with durable spend attribution.
2. When collecting a recognized obligation, the system shall atomically debit available customer funds and reduce the corresponding receivable without consuming sibling commitments.
3. If collection cannot cover a valid incurred obligation, the system shall record the unpaid remainder as receivable/debt and trigger the account breaker rather than drop the obligation or truncate its amount.
4. When an account is frozen or reconciliation-required, the system shall still permit safe posting of incurred obligations, debt collection, corrections, deposits, and refunds.
5. When provider liability is recognized, the system shall record the supplier COGS debit and provider payable credit independently of customer collection.
6. When a nonzero financial or operational-exposure operation is applied, the system shall retain balanced same-currency entries, an immutable operation key, fingerprint, and before/after projections in one transaction.
7. When any posting is replayed or its commit acknowledgement is lost, the system shall converge to one effect; a conflicting payload at the same key shall become a visible repair case.
8. When all projections are rebuilt from the authoritative entries and immutable evidence, the resulting balance, debt, commitments, revenue, and payable totals shall equal their stored projections.

### Requirement 10: Unbounded retry lifetime and bounded resource use

**Objective:** As the proxy operator, I want to survive database spikes without forgotten billing work.

#### Acceptance Criteria

1. While a valid financial operation fails for a transient reason, the system shall retry it until successful without a terminal attempt-count limit.
2. When retrying operational failures, the system shall use capped backoff, jitter, finite worker concurrency, and durable next-attempt state without one unbounded goroutine per request.
3. When a worker dies or its acknowledgement is lost, another worker shall reclaim the operation under a lease and complete it idempotently.
4. If an operation is semantically invalid, the system shall retain it under active reconciliation ownership with reason, age, and requeue criteria rather than treat it as a successful or abandoned operation.
5. When persistent storage is unavailable or queues reach capacity, the system shall stop accepting additional liabilities and preserve existing obligations until processing can resume.
6. When workers select pending work, the system shall make fair progress despite malformed, incomplete, or repeatedly failing items and shall not permanently starve valid later work.

### Requirement 11: Readable financial errors across all client protocols

**Objective:** As the proxy operator, I want to show a useful account-specific affordability explanation.

#### Acceptance Criteria

1. When a request cannot be funded, the system shall return a stable financial-denial code rather than an internal error or a misleading rate-limit response.
2. When a simple completion-token allowance is meaningful, the rejection shall explain the affordable token count, requested/catalog reservation maximum, balance, outstanding commitments, and input/fixed bound from the same atomic admission snapshot.
3. When a nonlinear or multi-attempt tariff makes a scalar formula misleading, the system shall derive the allowance from the full monotone upper-bound quote and disclose the binding resource/route.
4. When catalog-first reservation is active, the rejection shall disclose that a smaller client limit does not reduce reservation while a catalog maximum is available.
5. When errors cross each supported frontend, streaming/non-streaming path, or large-body path, the system shall preserve the same financial classification and redact private provider details and other accounts.
6. When the funding store is unavailable or a billing capability is unsupported, the system shall distinguish that condition from insufficient funds.

### Requirement 12: Native family support and fail-closed extension

**Objective:** As the proxy operator, I want to remove blanket native-usage rejection without making support implicit.

#### Acceptance Criteria

1. Where a frontend, backend, or connector exposes a natively representable finite-priced operation or modality combination, the strict paid host shall support its complete economic path through request-specific immutable capability and unit-schema proofs; absence of a billing implementation shall be a release blocker, not a protocol-unsupported disposition.
2. When native counters overlap, the system shall honor their declared inclusion/disjointness, direction, cache, reasoning, modality, and candidate semantics and shall not double count inclusive totals.
3. When a new provider profile or connector is registered without adequate proof, the strict host shall reject it automatically before paid execution even if no author marked it unsupported.
4. When a requested multimodal, prediction, paid-tool, or persistent-resource shape is natively representable and has finite enforceable provider bounds, the system shall implement and certify its complete mixed-component billing; when the protocol genuinely cannot represent it or liability is inherently unbounded, the system shall reject before any payable side effect and state that specific limitation.
5. Where an external monetary binding is used, the host shall require the same funding, per-dispatch, recovery, and cancellation contract as the internal reference binding.
6. For release, the system shall enumerate every registered frontend/backend pair and every input/output modality signature, expand every supported operation and independent delivery/transport mode, and produce complete positive or justified negative evidence; family tests and selected sentinels alone shall not substitute for this coverage.

### Requirement 13: Database topology and pressure safety

**Objective:** As the proxy operator, I want to keep funding serialized only where required and bound overload behavior.

#### Acceptance Criteria

1. Where PostgreSQL serves multiple accounts, ordinary financial work shall permit independent accounts to progress concurrently while preserving exclusive cutover transitions.
2. When computing account headroom, the system shall use a transactionally maintained rebuildable commitment projection rather than decode every active exposure on each admission.
3. Where SQLite is used, the system shall preserve equivalent financial behavior with bounded writer ownership and local durability without requiring an external database.
4. Where distributed strict operation is selected, the system shall require a shared authoritative transactional store and reject unsupported local-only or partitioned multi-authority topologies.
5. When database pressure delays settlement or cancellation, the system shall give admitted-obligation completion and account freezes priority over accepting more liabilities, without permanently starving other work.
6. When a mandatory topology or persistence gate is run, absent infrastructure, skipped cases, and zero executed tests shall count as failure rather than certification.

### Requirement 14: Brownfield preservation and controlled activation

**Objective:** As the proxy operator, I want to repair the existing implementation without erasing history or weakening safeguards.

#### Acceptance Criteria

1. When the new implementation is installed, it shall preserve existing journal keys, posting ownership, immutable historical evidence, advisory semantics, and explicitly non-monetary host behavior.
2. When old data is imported or repaired, the system shall classify dispatched timeouts, missing closures, stuck retries, missing quantities, and thinker attribution errors without silently rewriting completed invoices.
3. Before strict-contract activation, the system shall quiesce incompatible writers, apply additive dual-dialect migrations, reconcile projections, and verify the complete required acceptance set.
4. When a legacy writer or binding cannot honor the new contract, the system shall prevent it from admitting new strict work while preserving an explicit historical drain/repair path.
5. When the uncertain-overlap advisory feature reports uncertainty, report limits, or historical replay, the system shall preserve its non-monetary behavior and shall not use an advisory as proof of a financial upper bound.
6. When activation or rollback is interrupted, the system shall preserve one posting owner per operation and shall never revert new-contract obligations to legacy scalar or winner-only handling.

### Requirement 15: Evidence-based implementation and release

**Objective:** As the proxy operator, I want to prevent false completion from test-shaped code or oversized agent tasks.

#### Acceptance Criteria

1. When an implementation task is executed, it shall have one bounded work packet with named inputs, dependencies, permitted changes, ordered instructions, outputs, and executable acceptance criteria.
2. When verification succeeds, it shall include independent literal monetary expectations and external dispatch observation rather than using the production quoter/rater as its own oracle.
3. When the final gate is evaluated, it shall require proof for every original contract C01-C11, every finding F01-F14, all mandatory scenarios, and all required database and protocol families.
4. When a task approaches its context or change-surface budget, execution shall stop at a durable checkpoint and restart from a fresh packet without compaction or inventing new design decisions.
5. When any mandatory assertion, compatibility row, data-repair decision, or gate remains unresolved, the implementation shall remain incomplete and strict release shall remain disabled.
6. When compiling a previously unseen supported-family contribution, architecture and executable gates shall detect missing guard routing, missing capability rows, or bypasses without depending on an optional author-supplied safety label.

### Requirement 16: Operational ownership and honest guarantees

**Objective:** As the proxy operator, I want to make pending liabilities and the limits of distributed cancellation visible.

#### Acceptance Criteria

1. While unresolved financial work exists, the system shall expose bounded-cardinality health and per-obligation authorized diagnostics for pending age, debt, retry state, reconciliation owner, and retained commitment.
2. When shutting down or reloading, the host shall stop new admissions, transfer or finish owned durable work, preserve snapshots, and never release commitments merely because a process ends.
3. Where exact collection depends on later provider evidence, the system shall report the pending obligation honestly; loss of all authoritative replicas or a provider violating its certified bound shall not be described as successful contract fulfillment.
4. When cancellation propagation exceeds its configured bound or its control store becomes unreachable, the host shall fail closed, expire local launch authority, cancel local work, and expose the breach.
5. When a reconciliation case requires human-supplied evidence, the system shall provide an idempotent authenticated repair/requeue operation with an immutable audit trail and shall not bury that prerequisite in logs.

### Requirement 17: Complete interface and operation universe
**Objective:** As the paid-host operator, I require complete and non-vacuous multimodal financial correctness across the exposed interface universe.

#### Acceptance Criteria
1. The system shall inventory all registered frontend interfaces, concrete builtin backend contributions, executable connector modules, compatible-provider profiles, routed operations, and actually available delivery transports independently of whether billing support has been implemented.
2. When a contribution, operation, profile, transport, content carrier, or economic field is added or changed, the release gate shall fail until its positive support obligations and any source-supported negative dispositions are included in the coverage universe.
3. For every inventory entry, the system shall distinguish native protocol or model impossibility, missing billing implementation, temporarily unavailable pricing or evidence, and supported operation; it shall not relabel the latter two as native impossibility.
4. Where a registered interface accepts finite-priced content or output combinations, the release gate shall require successful attributable billing through that interface, including Bedrock, Alibaba, custom-compatible adapters, and each executable connector, rather than accepting only a common-interface mock.
5. When different frontends express the same effective provider request under the same account, policy, and tariff, the system shall produce the same economic quantities and charges except for separately declared frontend-service fees.
6. When an operation only reads, polls, replays, or cancels previously created work, the system shall not charge again for the original inference; any independently payable remote operation shall have a distinct funded obligation.

### Requirement 18: Lossless mixed input and requested output intent
**Objective:** As the paid-host operator, I require complete and non-vacuous multimodal financial correctness across the exposed interface universe.

#### Acceptance Criteria
1. When a request contains multiple input modalities, the system shall preserve every ordered occurrence, its modality and resource identity, nesting and role, and all cost-affecting options through decoding, canonical transformation, connector conversion, and final backend preparation.
2. When media occurs inside historical messages, structured items, tool results, or retained conversation state, the system shall account for the complete effective input submitted to each provider attempt rather than counting only new top-level user parts.
3. When a request selects multiple output modalities, candidates, resolutions, durations, image counts, or other economic options, the system shall preserve their joint semantics and explicit presence; it shall not silently drop unsupported cost-affecting fields or replace a mixed request with text-only generation.
4. When input media is supplied as inline data, a URL, a provider file identifier, or another reference, the system shall bind quotation to trusted resolved properties or a proven finite provider processing bound rather than reference length, compressed byte length, or client-declared metadata.
5. When a referenced asset can change between preparation and execution, the system shall send the bound immutable asset or invalidate the preparation and obtain funding for the replacement before the payable operation.
6. When a frontend or backend cannot represent the full requested combination without loss, the system shall reject before payable execution; it shall perform a modality conversion only when explicitly selected by trusted policy and every payable conversion step is funded and attributed.

### Requirement 19: Joint modality and economic capability contracts
**Objective:** As the paid-host operator, I require complete and non-vacuous multimodal financial correctness across the exposed interface universe.

#### Acceptance Criteria
1. For admission, the system shall evaluate support for the whole input-modality set, output-modality set, operation, model, transport, and economic option combination; independent single-modality flags shall not establish support for a mixture.
2. When the provider constrains output modalities as alternative valid sets, the system shall enforce those sets exactly and shall not infer arbitrary subsets or unions to be supported.
3. For every accepted request, the system shall bind an immutable economic capability contract to the concrete backend implementation, provider profile, model, API revision, preparation digest, unit schema, pricing basis, and enforcement limits.
4. When an input count is an estimate rather than a guaranteed upper bound, the system shall not authorize liability using that estimate alone; it shall use a justified bounded error or a larger enforced processing limit.
5. Where the strict paid path accepts image, audio, video, document, or binary content, the system shall preserve native units, qualifiers, and upper bounds for all applicable costs and shall not require every cost to be expressible as text tokens.
6. When the whole joint contract cannot be established at runtime, the system shall deny dispatch without silently selecting a weaker accounting path, while a missing implementation for a required support cell shall remain an unfinished release obligation.

### Requirement 20: Pessimistic mixed-modality funding
**Objective:** As the paid-host operator, I require complete and non-vacuous multimodal financial correctness across the exposed interface universe.

#### Acceptance Criteria
1. Before each payable attempt, the system shall reserve an upper bound covering the complete feasible set of mixed input processing, maximum allowed output, candidate multiplicity, reasoning, cache-write behavior, tools, and associated resource work for that attempt.
2. When a provider charges image tiles, native image tokens, audio tokens or duration, video frames or duration, document pages, or binary processing units, the system shall use the corresponding enforceable bounds and qualified tariffs rather than an unrelated model text-output limit.
3. When costs share a total or overlap across modality, cache, reasoning, or prediction dimensions, the reservation shall remain conservative without converting an unproved allocation into actual billable usage.
4. When one upstream call produces multiple candidates or output assets, the system shall apply input, output, request, candidate, and resource fees at their actual contractual scopes and shall neither duplicate shared input nor omit discarded outputs.
5. When a provider performs billable OCR, transcription, media conversion, upload, cache storage, hosted tools, or follow-on inference on behalf of a request, the system shall fund and record that work before the relevant side effect, including a finite lifetime for recurring resource costs.
6. When a mixed request is unaffordable, the system shall return the limiting monetary and unit budgets honestly; it shall not claim that reducing completion tokens fixes an independent unfunded image, audio, video, or persistent-resource charge.

### Requirement 21: Complete native mixed-usage settlement
**Objective:** As the paid-host operator, I require complete and non-vacuous multimodal financial correctness across the exposed interface universe.

#### Acceptance Criteria
1. When native usage contains both aggregate totals and component details, the system shall charge a proved non-overlapping economic basis and retain the original evidence; it shall not add totals, included detail counters, and local estimates for the same work.
2. When modality and cache or reasoning counters provide only marginal totals, the system shall not invent their intersection; if rates differ and exact allocation is required, it shall retain the ambiguous obligation for reconciliation while posting independently known charges.
3. When output includes several candidates, modalities, assets, or tool effects, the system shall retain usage by actual attempt and economic support scope, independent of frontend visibility, content filtering, truncation, or winner selection.
4. When streamed metadata is sparse, cumulative, delta-based, duplicated, out of order, or revised, the system shall apply its declared source semantics exactly once and shall preserve omitted versus null versus explicit zero for every required economic component.
5. When a response has no text or no user-visible content, the system shall still bill all evidenced chargeable computation and media or resource work, including hidden reasoning and failed or filtered output.
6. When final media is delivered by an expiring asset URL or a result is retrieved repeatedly, the system shall preserve durable economic evidence independently of asset availability and shall not re-infer or double charge solely to reconstruct that evidence.

### Requirement 22: Output transport, framing, and lifecycle independence
**Objective:** As the paid-host operator, I require complete and non-vacuous multimodal financial correctness across the exposed interface universe.

#### Acceptance Criteria
1. When frontend delivery and backend transport differ between streaming and non-streaming modes, the system shall preserve the same full economic result for equivalent provider work and shall not make billing depend on either stream flag.
2. When output media arrives in chunks, the system shall preserve item, candidate, modality, and chunk identity through canonical framing and shall distinguish partial delivery from final economic usage.
3. When an encoder, collector, media-size guard, or client connection fails after provider work is incurred, the system shall retain the original liability, capture available final usage, and complete billing independently of frontend success.
4. When a WebSocket or another reusable session carries multiple logical requests, the system shall allocate and fund each logical operation separately while retaining the original identity for replay of the same result.
5. When an accepted continuous or asynchronous operation can incur further cost, the system shall enforce a finite funded horizon or a provider-enforced finite total before continuing; disconnect and timeout alone shall not prove that liability ended.
6. When an account is financially frozen during mixed-output, queued, asynchronous, or connector work, the system shall fence all new payable dispatches and cancel every ongoing same-account operation through its actual owner while retaining funded cancellation tails.

### Requirement 23: Exhaustive combination evidence and independent oracles
**Objective:** As the paid-host operator, I require complete and non-vacuous multimodal financial correctness across the exposed interface universe.

#### Acceptance Criteria
1. For the declared modality vocabulary, the release gate shall enumerate the full input-set and output-set product for every frontend/backend pair before capability filtering, including empty sets, mixtures of three or more modalities, and all-modality signatures.
2. For every combination admitted by native and configured finite-economic capabilities, the release evidence shall show a successful complete decode, preparation, provider-bound invocation, usage capture, and balanced settlement; missing or skipped cases shall fail certification.
3. For every natively impossible combination, the release evidence shall identify the precise unsupported protocol or model predicate and verify zero payable dispatches; implementation-missing combinations shall not count as passed negative cases.
4. The release gate shall compare independent literal economic fixtures and actual recorded provider-bound requests with the canonical economic record, rather than reuse the production estimator, decoder, or capability decision as the expected-result oracle.
5. The release gate shall detect lost modalities, unhandled economic fields, unsupported mixture inference, omitted transport cells, native-unit flattening, duplicate inclusive charges, all-deny implementations, and connector bypasses by independent negative or mutation tests.
6. When the universe or a model/profile contract changes, the release gate shall invalidate affected certificates and require complete updated coverage; pairwise tests or selected representative pairs shall not be reported as exhaustive verification.

### Requirement 24: Compositional coverage beyond a finite fixture set
**Objective:** As the paid-host operator, I require complete and non-vacuous multimodal financial correctness across the exposed interface universe.

#### Acceptance Criteria
1. For accepted ordered multimodal parts, the system shall apply the same validated composition rule to arbitrary allowed counts and orderings, including repetition and nesting within configured limits, rather than recognize only fixed sample payloads.
2. When economically identical content is carried in different supported encodings, the system shall preserve the same quantities while still charging repeated actual provider executions separately; resource-content deduplication shall not become billing deduplication.
3. For mixed economics, the release gate shall include property tests over order, chunk partition, count boundaries, revision order, and equivalent encodings and shall compare exact independent monetary oracles.
4. When schema, protobuf, canonical enum, or JSON conversion gains a new content or usage field, the build and release gates shall require its explicit accounting disposition through every affected layer instead of accepting a default drop branch.
5. For original financial safety contracts, the release gate shall repeat burst funding, freeze propagation, first-write failure, crash recovery, long outage, and idempotent settlement with genuinely mixed input and output work.
6. The specification and release report shall distinguish coverage of a finite signature universe and tested properties from proof over every possible byte sequence, every future provider, or an unsupported external protocol.
