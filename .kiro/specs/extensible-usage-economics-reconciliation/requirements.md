# Requirements Document

## Introduction

Establish pre-OSS, extensible usage economics and reconciliation for the existing B2BUA proxy. Local measurements and provider claims must coexist. Component quantities and charges must remain queryable and immutable. Customer charging is independent of supplier cost, while attributable supplier charges roll up across every executed B-leg.

**Baseline:** `3da34d7875443355d65cb9d7df649555dfad3edb`, rechecked on 2026-09-09. This is a brownfield change, not a replacement proxy or a new price-discovery service.

## Boundary Context

- **In scope:** canonical evidence, field normalization, measurement hooks, per-component rating and storage, independent retail policy, allowance observations, all-leg COGS, discrepancy detection, generic statement import, idempotent adjustments, stable external bindings, migration and certification.
- **Out of scope:** obtaining or maintaining universal provider price lists; vendor invoice parsers; taxes, invoice generation and payment collection; new inference modalities or new provider connectivity; the actual repository/product split and rebrand.
- **Adjacent work:** preserve #532/#503 fast-path fallback, #394 measurement discipline, #429 naming revalidation and #398 release gating. Completed ownership and provider-expansion specs are historical baselines, not new blockers.
- **Product interpretation:** A-leg operator cost is the sum of attributable supplier costs; A-leg customer charge is a separate contractual result. Local cache/hidden-compute estimates may be unknown. Account utilization is a gauge, not an additive request debit.
- **Ownership:** core lifecycle captures neutral evidence; provider adapters normalize their wire evidence; metering owns quantities and source identity; billing owns policy and postings; infrastructure owns persistence; explicit host binding owns optional monetary composition.

Acceptance criteria use `N.M` identifiers. Requirements describe observable contracts; design choices and exact package placement are specified in `design.md`.

## Requirements

### Requirement 1: Independent economic evidence

**Objective:** As an operator, I want independent local and upstream economic records, so that a disagreement remains visible and auditable.

#### Acceptance Criteria

1.1. The accounting system shall preserve local measurements, locally modelled charges, provider-reported measurements, provider-reported charges, and later statement evidence as independently identifiable records.

1.2. When provider measurements are rated using a local tariff, the accounting system shall label the result provider-quantity local rating rather than a provider-reported charge or an independent local measurement.

1.3. When an observation is captured, the accounting system shall identify its economic perspective, measurement boundary, subject scope, source, acquisition method, quality, effective time, and recording time.

1.4. If two evidence sources disagree, the accounting system shall retain both original values and shall not overwrite one source with the other during financial selection or reconciliation.

1.5. The accounting system shall distinguish unknown, unavailable, not applicable, estimated, observed zero, and observed nonzero amounts without encoding unknown amounts as zero.

1.6. Where only provider aggregate cost is available, the accounting system shall preserve that aggregate independently of component costs and shall not invent a provider-reported component allocation.


### Requirement 2: Extensible quantities and charge detail

**Objective:** As a maintainer, I want one extensible quantity contract, so that new billable units do not require another token-specific storage redesign.

#### Acceptance Criteria

2.1. The accounting system shall durably represent uncached input, cache reads, cache writes, output, and any separately meaningful reasoning or modality components with independent quantity, unit, presence, and optional component charge.

2.2. The accounting system shall represent requests, user submissions, tool invocations, images, audio or video duration, compute duration, storage-duration products, credits, and other schema-qualified quantities without adding a database column for each component.

2.3. The accounting system shall represent fractional quantities and monetary evidence with bounded exact decimal arithmetic and shall reject overflow, invalid units, and unsupported precision explicitly rather than round them silently.

2.4. When two quantities differ in unit, modality, cache lifetime, billing account or allowance pool, resource, or other price-relevant qualifiers, the accounting system shall preserve their distinct identity.

2.5. The accounting system shall keep monetary currency, nonmonetary credits, and allowance percentages distinct; an optional economic allocation shall not turn a nonmonetary unit into an upstream monetary charge.

2.6. When rated amounts are persisted, the accounting system shall retain component quantities, applied pricing rule and snapshot references, pre-round amount, rounding scope and policy, final amount, and completeness.


### Requirement 3: Normalization and inclusion semantics

**Objective:** As an operator, I want reproducible normalization, so that cached and reasoning tokens are neither omitted nor charged twice.

#### Acceptance Criteria

3.1. When provider fields are normalized, the accounting system shall use a versioned provider-family mapping and shall preserve the original field identity and exact value evidence.

3.2. Where an input total includes cache components, the accounting system shall derive uncached input only when all required operands and their inclusion relationship are known.

3.3. Where provider input excludes cache creation or cache reads, the accounting system shall preserve that exclusion and shall not subtract those components again.

3.4. When totals and their subcomponents coexist, the accounting system shall distinguish aggregate observations from disjoint chargeable components and shall reject overlapping additive charge rules unless explicitly defined as surcharges.

3.5. If cache, modality, or reasoning classifications are unavailable or inconsistent, the accounting system shall expose partial or incomparable evidence rather than assign the residual to an invented category.

3.6. When a normalization mapping changes, the accounting system shall retain its previous results and version references and shall produce a separately traceable corrected derivation.


### Requirement 4: Independent local boundary metering

**Objective:** As an operator, I want measurement of the actual provider-facing traffic, so that our expectation is not a copy of the upstream claim.

#### Acceptance Criteria

4.1. When a provider attempt is prepared, the accounting system shall measure the final provider-bound representation after applicable rewrites and distinguish prepared, transport-attempted, and provider-accepted work.

4.2. When backend content is received, the accounting system shall measure it before customer-side filtering, compression, or projection and shall separately record what is delivered at the customer boundary.

4.3. If exact provider tokenization, hidden reasoning, internal tool work, cache disposition, or compute usage cannot be observed locally, the accounting system shall mark the corresponding local measurement unavailable or estimated with its method and limitations.

4.4. When a provider counting endpoint or provider usage result supplies a quantity, the accounting system shall identify it as provider-derived evidence, not as independently measured local evidence.

4.5. While streaming, the accounting system shall preserve measurement semantics across arbitrary chunk boundaries and shall not equate a sum of independently tokenized chunks with exact whole-output tokenization.

4.6. Where the large-payload fast path cannot supply required measurement evidence within its bounded-memory contract, the runtime shall use canonical fallback or explicit unsupported-accounting behavior before upstream commitment.


### Requirement 5: Provider evidence acquisition and connector compatibility

**Objective:** As an operator, I want all upstream economic evidence retained without leaking it to clients.

#### Acceptance Criteria

5.1. When a supported provider returns usage or cost in response fields, headers, trailers, stream events, finalization, or host-only sideband data, the adapter shall capture the supported economic fields with request and charge correlation.

5.2. When stream, sideband, finalizer, or duplicate delivery refer to the same provider charge, the accounting system shall preserve their evidence relationship without charging that provider event twice.

5.3. Where an upstream returns quantities but no money, the accounting system shall leave provider-reported money absent even when a local tariff can produce an estimate.

5.4. If an existing connector cannot transmit a requested evidence capability, negotiation shall reject required usage semantics before execution or explicitly declare reduced evidence coverage; it shall not silently drop required data.

5.5. The accounting system shall support multiple provider charge events within one backend attempt and shall distinguish a parent inclusive total from separately chargeable auxiliary events.

5.6. When an unknown schema-qualified field is retained, the adapter shall apply explicit bounds and shall not enable arbitrary payload or header persistence under the guise of accounting.


### Requirement 6: B2BUA attribution and cost completeness

**Objective:** As an operator, I want every attributable provider cost reflected in the proper client traffic rollup.

#### Acceptance Criteria

6.1. The accounting system shall preserve separate identities for A-leg or session, logical billing call, submission, B-leg or attempt, provider request, provider charge, and auxiliary workload.

6.2. When reporting operator cost for a call or A-leg, the accounting system shall include all attributable executed B-leg costs, including failed attempts, canceled work, race losers, retries, and attributable auxiliary work, without duplicating a shared charge.

6.3. If any included cost is missing or unresolved, the accounting system shall return known subtotal and completeness information instead of labelling the subtotal the complete cost.

6.4. The accounting system shall calculate customer charges independently of operator cost attribution and shall use cost pass-through only when an explicit customer policy selects it.

6.5. Where a resource or subscription cost is shared, the accounting system shall retain the original resource or account-period subject and shall require an explicit conserved allocation before attributing it to calls.

6.6. When costs use different currencies or payment parties, the accounting system shall keep them separate unless an explicit versioned conversion or payer policy applies; customer-owned upstream credentials shall not automatically create operator payables.


### Requirement 7: Versioned component rating

**Objective:** As a billing integrator, I want reproducible pricing of independent evidence sets without building a new pricing-data acquisition service.

#### Acceptance Criteria

7.1. When work is admitted, the billing host shall bind immutable effective customer policy and relevant provider tariff identities for later rating and shall retain enough snapshot material to replay the charge.

7.2. The rating system shall independently support local-measurement expected cost, provider-quantity local cost, provider-reported monetary evidence, and customer-policy charges without one result substituting for another.

7.3. The reference rater shall support component unit rates, fixed fees, block rounding and minimums, conditional rate selection, and explicit tier rules using exact arithmetic and a declared aggregation scope.

7.4. When price depends on service tier, model version, context threshold, cache lifetime, modality, geography, batch mode, or contractual qualifiers, rating shall use recorded effective qualifiers and the matching frozen rule.

7.5. If necessary quantities or rate snapshots are missing or ambiguous, the rater shall return a typed incomplete or unsupported result rather than a free charge or an unrelated default tariff.

7.6. The system shall adapt the existing local pricing source and permit injected versioned raters without making tariff crawling, universal provider price discovery, taxes, or a new commercial price database prerequisites.


### Requirement 8: Independent customer charging and submission semantics

**Objective:** As a proxy operator, I want custom retail offers that need not mirror provider charges.

#### Acceptance Criteria

8.1. The customer rater shall select its input basis explicitly from customer-boundary usage, provider quantities for selected attempts, or an explicitly declared provider-cost pass-through policy.

8.2. The customer rater shall support separate uncached, cache-read, cache-write, and output rates and non-token charges without reducing them to one blended input rate.

8.3. Where charging is per user submission, the system shall count a trusted new submission once and shall not charge its tool continuations, replayed history, or transport retries as additional submissions.

8.4. If reliable submission identity is unavailable, the system shall declare prompt-based charging unsupported or use an explicitly selected per-call offer; it shall not infer billable identity from a last-message role alone.

8.5. Where a customer plan uses credits or included allowances, the system shall record customer allowance debits independently of supplier allowances and shall preserve nonmonetary settlement identity.

8.6. When provider reconciliation is delayed, ordinary independent customer settlement shall remain operable; only a customer policy explicitly depending on supplier cost may wait on a declared bounded pending state.


### Requirement 9: Subscription and account-window evidence

**Objective:** As an operator, I want subscription usage visible without pretending account-wide percentages are per-request bills.

#### Acceptance Criteria

9.1. When a provider reports window utilization, the system shall retain provider-account identity, pool or limit identity, primary or secondary window identity, reset time, observed utilization, and receipt time as an account-window observation.

9.2. The system shall not sum account-window percentage snapshots across requests, across overlapping windows, or across different pools.

9.3. If only account-wide snapshots are available, the system shall not claim exact per-request consumption from their difference; resets, rounding, delayed updates, external traffic, and concurrency shall remain explicit limitations.

9.4. Where a provider reports a genuine request-scoped credit debit, the system shall retain that debit as additive request evidence independently of account-window snapshots.

9.5. Where subscription amortization or opportunity cost is modelled locally, the system shall identify the allocation method and period and shall not present it as provider-reported marginal money.

9.6. Where provider allowances influence admission, the system shall use separately versioned nonmonetary authority rules and shall not make telemetry snapshots a second financial ledger.


### Requirement 10: Immutable lifecycle and delivery semantics

**Objective:** As a maintainer, I want reproducible accounting under retries, cancellation, and process failure.

#### Acceptance Criteria

10.1. When the same source event identity and revision are delivered again, the system shall perform an idempotent replay; if its semantic payload differs, the system shall report an identity conflict.

10.2. When applying delta or cumulative evidence, the system shall respect source stream identity, sequence, and explicitly present fields; absence shall not erase a previous known component.

10.3. When evidence is corrected, the system shall append a revision or adjustment referencing the prior evidence and shall not mutate previously sealed facts.

10.4. When an attempt terminates, the system shall close its observed execution state exactly once while permitting explicitly identified later economic evidence.

10.5. If evidence persistence fails, the runtime shall preserve a durable recoverable accounting intent where available, expose degraded completeness, and shall never retry upstream work after downstream output commitment.

10.6. When restart or replay occurs, the system shall rebuild the same effective observations, valuations, and posting state from durable records without using transient map order or newly generated attempt identity.


### Requirement 11: Durable representation and query projections

**Objective:** As an operator, I want database-level component evidence and auditable monetary breakdowns.

#### Acceptance Criteria

11.1. The persistent model shall retain independent local and provider component quantities and optional costs at call, attempt, resource, or account-window scope as applicable, with source and pricing provenance.

11.2. The persistent model shall preserve an immutable canonical record and transactionally consistent query projections, without treating a mutable report table as an independent economic authority.

11.3. When adding a new schema-qualified usage component, the system shall persist, round-trip, and query it without a new component-specific SQL migration.

11.4. The system shall enforce store or tenant isolation, stable event uniqueness, source revision uniqueness, referential integrity, and bounded indexed pagination across supported SQLite and PostgreSQL topologies.

11.5. When a projection is rebuilt, the system shall reproduce its values from canonical records and shall detect or repair projection-version drift without rewriting financial history.

11.6. Where old records contain only aggregate money or incomplete provenance, migration shall preserve the original facts and mark unsupported breakdown or local-versus-provider separation unavailable.


### Requirement 12: Discrepancy reconciliation

**Objective:** As an operator, I want to distinguish metering, classification, and tariff discrepancies promptly.

#### Acceptance Criteria

12.1. The reconciler shall compare independent local quantities with comparable provider quantities and shall record signed and absolute quantity differences by full component identity.

12.2. When sufficient evidence exists, the reconciler shall distinguish the cost effect of differing quantities from the residual between provider-quantity local rating and provider-reported monetary charge.

12.3. If evidence differs in scope, currency, tokenizer semantics, inclusion schema, charge coverage, or aggregation period without an explicit mapping, the reconciler shall return incomparable or partial status instead of matched.

12.4. The reconciler shall apply versioned absolute and relative tolerances, handle zero denominators explicitly, and retain differences even when within tolerance.

12.5. The reconciler shall produce bounded operational signals for missing, late, conflicting, classification, metering, tariff-context, monetary, and allowance discrepancies, including gross absolute as well as signed aggregates.

12.6. When reconciled evidence is selected for a financial view, the system shall retain the selection policy and all competing evidence; source selection alone shall not be labelled successful reconciliation.


### Requirement 13: Late statements and financial adjustments

**Objective:** As an operator, I want later upstream corrections reconciled without duplicate settlements.

#### Acceptance Criteria

13.1. The system shall accept authenticated, schema-valid statement or correction evidence through a generic import port, preserving statement identity, line identity, revision, billing period, account, and original aggregation granularity.

13.2. If a statement line cannot be matched unambiguously to a provider charge or compatible aggregate scope, the system shall retain it as unmatched rather than attach it to a guessed B-leg.

13.3. When new evidence changes a previously posted operator cost, the system shall append an idempotent balanced adjustment referencing the original posting only when the old and new selected valuations share a native currency or an explicit frozen FX conversion basis; otherwise it shall leave the correction pending or reject it without posting a monetary delta, and it shall never post the full replacement amount again.

13.4. When an upstream correction changes operator cost, the system shall not automatically alter a settled customer charge unless the frozen customer policy explicitly permits a separate audited adjustment.

13.5. The system shall preserve distinct economic completeness, reconciliation status, and financial posting status so a dispute does not erase an incurred cost or imply customer nonpayment.

13.6. The system shall provide generic statement ingestion and reconciliation contracts without requiring vendor invoice parsers, invoice generation, tax engines, or dispute-management workflow in this implementation.


### Requirement 14: Exposure and financial safety

**Objective:** As an operator, I want richer billing without weakening existing admission or settlement safeguards.

#### Acceptance Criteria

14.1. The runtime shall retain one cheap pre-route credit screen, one atomic operational exposure-admission authority, and terminal evidence handoff, with no financial rating or journal writes in stream receive processing.

14.2. Where a customer rule can create non-token charges, admission shall bound those charges using the same policy semantics as settlement or reject an unbounded strict offer before provider execution.

14.3. When a request is canceled, fails, or exceeds an estimate, the system shall retain incurred usage and shall not silently cap recorded actual cost to the admission estimate.

14.4. The financial system shall atomically deduplicate and apply settlement or adjustment together with the corresponding balance and posting transitions.

14.5. If all selected providers lack evidence required by a strict customer offer, admission shall fail explicitly before spending rather than execute work that cannot be rated honestly.

14.6. While supplier costing or reconciliation is backlogged, the system shall avoid taking customer admission balance locks for provider-only work and shall keep the two operational queues independently observable.


### Requirement 15: Architecture and external integration

**Objective:** As a maintainer, I want isolated extension points that survive the Open Core split.

#### Acceptance Criteria

15.1. The system shall keep provider-field parsing in adapters, generic evidence semantics in the metering domain, financial policy in billing, SQL in storage adapters, and lifecycle ownership in the runtime.

15.2. The system shall expose typed, versioned public contracts for provider-neutral economic observations and connector sideband, injected raters and quoters, statement import, and reconciliation consumption without requiring third-party imports of repository internal packages; provider-shaped evidence normalization shall remain owned by the supplying adapter or connector.

15.3. The system shall provide a narrow explicit opt-in monetary host binding using the same underlying host and existing billing admission and terminal seams, while leaving ordinary public runtime Options and stock startup nonmonetary.

15.4. The system shall reject incomplete, duplicate, or incompatible bindings before publication and shall preserve one owner for process workers, generation snapshots, cleanup, and reload retirement.

15.5. When an external provider adds a new component within an existing schema contract, the generic executor, ledger core, and storage schema shall not require provider-specific branches.

15.6. The implementation shall retire superseded live token-only billing selection and conversion paths after cutover and shall retain legacy compatibility only as documented readers or one-way projections.


### Requirement 16: Security and operational reporting

**Objective:** As an operator, I want useful discrepancy evidence without exposing customer content or supplier credentials.

#### Acceptance Criteria

16.1. The system shall persist only allowlisted bounded usage and billing evidence by default, excluding authorization headers, cookies, prompts, tool arguments, ciphertext, and raw model output.

16.2. When provider raw evidence is retained, the system shall preserve exact economic lexemes and safe field locations, identify the sanitizer and mapping versions, and apply access control and retention policy.

16.3. The system shall authorize queries and imports using trusted tenant or account scope and shall not accept client-supplied correlation or charge claims as provider authority.

16.4. The operator query surface shall expose component evidence, valuation basis, completeness, discrepancy status, provenance, and adjustment links; customer-facing responses shall not leak supplier costs or account utilization.

16.5. The observability system shall bound metric dimensions and alert deduplication and shall expose backlog age, rejected evidence, incomparable records, conflicts, and unreconciled monetary exposure.

16.6. The system shall preserve account balances and minimum audit linkage when optional raw-evidence retention expires, and shall distinguish a canonical-record hash from a hash of an entire upstream response.


### Requirement 17: Brownfield migration and release sequencing

**Objective:** As a maintainer, I want a controlled pre-OSS migration rather than permanent parallel accounting engines.

#### Acceptance Criteria

17.1. The implementation shall characterize the actual starting revision, supported producer families, existing financial behavior, database migrations, and public and connector contracts before modifying production code.

17.2. The migration shall support old durable records through versioned readers without reinterpreting historical fingerprints or inventing lost local-versus-provider evidence.

17.3. While new accounting runs in shadow mode, the old monetary writer shall remain the sole posting authority and the new system shall not debit accounts or post provider payables.

17.4. When cutover occurs, the system shall fence writers, pin a durable accounting-version boundary, and ensure that each logical settlement and adjustment has exactly one posting authority.

17.5. If rollback is requested after new-format postings exist, the system shall preserve and drain them through a compatible reader or explicit recovery procedure rather than run an old writer against unsupported data.

17.6. The release gate shall require completed evidence capture, component storage and rating, reconciliation, public binding certification, migration and deletion gates; universal price discovery and vendor-specific statement importers shall remain outside that gate.


### Requirement 18: Certification and bounded cost

**Objective:** As a maintainer, I want executable proof of correctness without another unbounded test or runtime cost increase.

#### Acceptance Criteria

18.1. The implementation shall provide red-first unit and contract tests for every requirement and shall certify component extensibility with a synthetic non-token meter without generic-core or schema changes.

18.2. The implementation shall certify local/provider separation, inclusion arithmetic, absent-versus-zero, fractional units, idempotency, late corrections, partial costs, and all-leg attribution using deterministic fixtures.

18.3. The implementation shall certify canonical streaming and non-streaming parity, connector negotiation, public external-module integration, and lifecycle races through bounded family-level tests rather than a frontend-by-backend Cartesian matrix.

18.4. The implementation shall certify canonical SQLite and PostgreSQL persistence contracts, transaction-pooler-safe behavior where supported, and restart/cutover crash tests.

18.5. The implementation shall keep stream-time work bounded by configured accounting evidence limits, avoid per-token database writes and rating, and measure disabled-path and enabled-path overhead against the fresh baseline.

18.6. The implementation shall pass the repository quality, unit, parity, wide QA, architecture, and applicable race gates and shall report Windows-authoritative test-cost evidence without silently increasing budgets.
