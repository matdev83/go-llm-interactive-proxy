# Research and Design Decisions

## Summary

**Feature:** `extensible-usage-economics-reconciliation`  
**Classification:** brownfield / complex integration.  
**Inspected source baseline:** `3da34d7875443355d65cb9d7df649555dfad3edb`; `main` was rechecked against that SHA on 2026-09-09.  
**Complexity:** XL; **risk:** high, because financial identity, persistence compatibility, multiple usage producers and host composition intersect. These are complexity labels, not delivery estimates.

The current code already has useful metering facts, B2BUA lineage, immutable usage storage, independent customer/provider processing and safe monetary admission boundaries. The remaining gap is not simply adding cache columns: financial evidence collapses source distinctions, component rating is narrower than the public metering model, account gauges require different semantics, and late corrections need explicit posting identity.

## Scope and evidence discipline

The user requires independent local and upstream quantities/costs, per-unit monetary detail, custom customer offers, all-attributable-B-leg operator cost and telecom-style discrepancy detection. The specification preserves those requirements while correcting two unsafe assumptions: actual cache/hidden usage cannot always be measured independently, and provider account utilization does not prove a particular request's debit.

**Source-derived facts** below refer to inspected source at the pinned revision or official provider documentation. **Design decisions** are the architecture proposed by this spec. Source inspection did not execute the repository's tests or prove production invoice errors. Tests/benchmarks listed in the plan are work to execute, not completed certification.

The attached `SKILL(1).md` was read in full. Its eight `references/*.md` files were not included in the upload and were not available beside it. The supplied orchestration text and the repository's own loaded EARS, gap, design, review and task rules/templates were used; no claim is made that unavailable bundled references were read. Research, gap analysis and review-repair findings are kept here, rather than adding extra permanent validation reports to the canonical spec directory.

## Project rules reviewed

Repository `AGENTS.md`, `.kiro/AGENTS.md`, `.kiro/steering/{product,structure,tech}.md`, `.kiro/settings/templates/specs/{requirements,design,research,tasks}.md`, and `.kiro/rules/{ears-format,gap-analysis,design-principles,design-review,tasks-generation}.md were read during this conversation. They require TDD, provider-neutral core, streaming-first, explicit host ownership, no post-output failover, no monetary public Options, supported database parity and bounded family-level tests.

Prior completed accounting/slimming specs are historical design context only. Their existence is not proof that the current source implements the desired new contracts. The actual current source and tests remain the execution baseline.

## Brownfield source inventory

| Ref | Inspected source | Relevant symbols | Verified observation |
|---|---|---|---|
| B01 | [`internal/core/billing/records.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/billing/records.go) | `FinalBillingEvidence` | Six fixed token quantities, one MoneyEvidence and record-wide provenance. |
| B02 | [`internal/core/billing/rating.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/billing/rating.go) | `fallbackOperatorCost; chargeLeg; selectCustomerLegs` | Fixed operator dimensions; customer input/output and fixed/resource charges; selected-leg retail semantics. |
| B03 | [`internal/core/billing/provider_cost.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/billing/provider_cost.go) | `RateProviderCost` | Provider-cost selection/fallback and evidence-absence zero branch. |
| B04 | [`internal/core/billing/call_usage.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/billing/call_usage.go) | `CallUsageRecord; CallLegUsageRecord; SemanticFingerprint` | Separate call/leg identities, expected B-leg set, persisted attempt sequence and historical fingerprints. |
| B05 | [`internal/core/billing/call_rating.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/billing/call_rating.go) | `CallRatingInput; CallRatingResult` | Independent customer inputs, aggregate customer amount and fingerprint. |
| B06 | [`internal/core/billing/estimate.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/billing/estimate.go) | `PricingSnapshot; ChargeComponent; EstimateMaxCustomerCharge` | Token rates plus pre-valued fixed/resource components; finite output admission bound. |
| B07 | [`pkg/lipsdk/metering/quantity.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/pkg/lipsdk/metering/quantity.go) | `Quantity; DefaultInclusionSchemaID` | Extensible int64 quantities and explicit cache/reasoning inclusion documentation. |
| B08 | [`pkg/lipsdk/metering/fact.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/pkg/lipsdk/metering/fact.go) | `Fact; SameFactReplay; Validate` | Source identity, corrections, provenance, but existing customer/operator lifecycle restrictions. |
| B09 | [`internal/core/metering/aggregate/aggregate.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/metering/aggregate/aggregate.go) | `Snapshot; Apply; replacePresent` | Component-only int64 keying and replay/cumulative/replacement behavior. |
| B10 | [`internal/core/runtime/billing_leg.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/runtime/billing_leg.go) | `billingLegRecord; finalBillingEvidenceFromEvent; mergeStreamCostOntoLeg` | Terminal evidence is reduced to fixed financial fields; source merging can broaden authority. |
| B11 | [`internal/core/runtime/attempt_usage_evidence.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/runtime/attempt_usage_evidence.go) | `rememberUsageEvidenceOnce; usageOrAccumulated; augmentBillingUsage` | Bounded attempt dedupe by a charge key and substitution/backfill of accumulated evidence. |
| B12 | [`internal/core/runtime/metering_egress.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/runtime/metering_egress.go) | `emitBackendEgressMeteringFact; emitFrontendEgressMeteringFact` | Existing boundary/freeze and customer-plane separation; append failure is logged. |
| B13 | [`pkg/lipsdk/backendplugin/types.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/pkg/lipsdk/backendplugin/types.go) | `AccountingEvidence; FinalizeBillingResponse` | Host-only sideband and optional finalizer, currently fixed token-oriented DTOs. |
| B14 | [`api/backendplugin/v1/backend.proto`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/api/backendplugin/v1/backend.proto) | `AccountingEvidence; FinalizeBilling` | Versioned executable connector transport integration point. |
| B15 | [`internal/infra/billingstore/20260818000000_billing_usage_leg.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/infra/billingstore/20260818000000_billing_usage_leg.go) | `usage_leg_records DDL` | Immutable JSON evidence payload, SQL indexes and mutation triggers. |
| B16 | [`internal/infra/billingstore/call_leg_usage_store.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/infra/billingstore/call_leg_usage_store.go) | `AppendCallLegUsage; ListPendingProviderCostWork` | Transactional leg append and provider-cost work insertion; sealed-record replay. |
| B17 | [`internal/infra/billingstore/provider_cost_store.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/infra/billingstore/provider_cost_store.go) | `ApplyProviderCost; applyProviderCostAttempt` | Provider COGS postings are separate from customer balance mutations. |
| B18 | [`internal/infra/billingcompose/resolver.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/infra/billingcompose/resolver.go) | `JoinRatingResolver; ProviderCostJoinResolver` | Independent retail resolver; provider selection first and tariff fallback second. |
| B19 | [`internal/infra/runtimebundle/billing_compose.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/infra/runtimebundle/billing_compose.go) | `ComposeBilling` | Explicit internal-only monetary host composition with complete prerequisite validation. |
| B20 | [`pkg/lipruntime/build.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/pkg/lipruntime/build.go) | `Build` | Public runtime translates non-money options into one internal BuildHost. |
| B21 | [`docs/enterprise-extension-boundaries.md`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/docs/enterprise-extension-boundaries.md) | `Allowed integration points; Billing` | Documents internal monetary injection and prohibits runtime forks; not a compilable external billing API. |
| B22 | [`pkg/lipsdk/economics/snapshot.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/pkg/lipsdk/economics/snapshot.go) | `RatingSnapshotSource; Snapshot` | Versioned immutable snapshot wrapper; current catalog view is not a full component tariff. |
| B23 | [`pkg/lipsdk/authority/provider.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/pkg/lipsdk/authority/provider.go) | `RequestProvider; AttemptProvider` | Existing nonmoney request/attempt authority registration and lifecycle contracts. |
| B24 | [`internal/core/billing/call_provider_cost_worker.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/billing/call_provider_cost_worker.go) | `CallProviderCostWorker` | Existing durable, bounded post-turn supplier work owner. |
| B25 | [`internal/core/billing/reports.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/core/billing/reports.go) | `TurnResultSummary` | Separate revenue/provider cost/margin report concepts already exist. |
| B26 | [`pkg/lipsdk/promptcache/accounting.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/pkg/lipsdk/promptcache/accounting.go) | `AccountingEvidence` | Existing maintenance usage contract must migrate with foreground capture. |
| B27 | [`internal/standardplugins/featurehost/keepwarm.go`](https://github.com/matdev83/go-llm-interactive-proxy/blob/3da34d7875443355d65cb9d7df649555dfad3edb/internal/standardplugins/featurehost/keepwarm.go) | `maintenanceEvidence` | Provider maintenance accounting adaptation; preserve feature ownership. |

## Official billing-format research

These are examples of formats the model must represent, not a hardcoded universal tariff catalog. Numerical acceptance-test prices are synthetic and are not copied from provider pricing pages.

| Ref | Official source and observation | Architectural implication |
|---|---|---|
| S01 | [Anthropic prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching): separate input/cache-read/cache-creation counters, with creation breakdown by cache lifetime. | Preserve original fields; distinct cache-write lifetime keys; versioned inclusion mapping. |
| S02 | [OpenAI Usage API](https://platform.openai.com/docs/api-reference/usage): usage and financial Costs are distinct, and financial reconciliation can differ from granular usage. | Provider quantities, local rating and financial statement evidence must be separate and need not share request granularity. |
| S03 | [Gemini pricing](https://ai.google.dev/gemini-api/docs/pricing): different modality rates, cache storage duration, grounding charges and batch-specific economics appear in the published structure. | Unit/dimension/resource-interval model; no assumption that all cost comes from text tokens or one request. |
| S04 | [Replicate billing](https://replicate.com/docs/topics/billing) and [prediction lifecycle](https://replicate.com/docs/topics/predictions/lifecycle/): compute-duration pricing exists; started canceled work can remain billable while aborted never-started work is not. | Keep provider runtime evidence and execution outcome; proxy wall-clock latency is not automatically billed compute. |
| S05 | [GitHub legacy premium requests](https://docs.github.com/en/copilot/reference/copilot-billing/request-based-billing-legacy/copilot-requests): the page explicitly scopes legacy plans and distinguishes user prompts from autonomous tool actions. | Support submission-based meters and model multipliers without claiming all current plans use that model. |
| S06 | [OpenAI Codex rate-limit implementation](https://github.com/openai/codex/blob/17e64839eb1e30632eef4a0147862345fccb61cc/codex-rs/codex-api/src/rate_limits.rs): primary/secondary used-percent, reset/window identity, named limit families and credit snapshots are parsed separately. | Treat snapshots as account-window gauges; separate genuine request debits from observed account status. This source was read through the GitHub connector. |
| S07 | [OpenRouter usage accounting](https://openrouter.ai/docs/cookbook/administration/usage-accounting): explicit usage/cost reporting is a different acquisition path from a locally calculated charge. | Preserve provider-reported money when supplied, and distinguish the operator's direct upstream counterparty from another underlying provider. |
| S08 | [OpenAI Realtime model reference](https://developers.openai.com/api/docs/models/gpt-realtime-mini): text/audio modalities and cached-input pricing demonstrate modality-specific units. | Keep priced categories distinct; do not equate tokens across all modalities or meters. |

### Billing shapes to retain

The researched examples motivate token partitions, per-request/per-submission units, modality and resolution qualifiers, compute time, storage-duration products, provider tools, credits and subscription windows. The architecture additionally permits explicit context thresholds, tiering, block rounding, fixed/minimum charges, batch/service-tier/region qualifiers, discounts, credits/refunds and later corrections. These latter capabilities are **design requirements for extensibility**, not a claim that every provider uses each one.

A provider-reported usage counter is not automatically a financial invoice. A provider monetary aggregate is not automatically a component breakdown. A proxy locally applying a tariff to provider-reported counters has produced Q, not independent E or provider-reported P. Independently reconstructing hidden computation may be impossible; retaining uncertainty is a required result, not incomplete implementation.

## Requirement-to-asset gap analysis

Severity P0 denotes financial/contract correctness or pre-split compatibility risk; P1 denotes material supporting work. `Re-inventory` denotes a required mechanical execution-baseline check, not an unresolved product decision.

| Gap | Priority | Class | Requirement groups | Source | Current gap or constraint | Required action |
|---|---|---|---|---|---|---|
| G01 | P0 | Missing | 1, 7, 12 | B01, B03, B18 | Independent E/Q/P/S records collapse into one selected provider amount. | Extend evidence and valuation, keep all origins; replace selection-as-reconciliation. |
| G02 | P0 | Constraint | 2–3, 7 | B02, B07 | Operator fallback has no declared disjoint-component inclusion contract. | Add schema-aware normalization/rating and synthetic regression vector; do not assume every current provider is misbilled without an end-to-end test. |
| G03 | P0 | Missing | 1, 6, 14 | B03 | No accepted evidence currently yields present reconciled zero. | Require proven never-started/nonbillable basis; otherwise preserve missing/incomplete cost. |
| G04 | P0 | Missing | 2, 7–8 | B01, B02, B06 | Retail snapshots lack independent cache rates; resource charges are pre-valued, not generic meter rules. | Component rule snapshots and a shared finite quote/settlement model. |
| G05 | P0 | Missing | 2, 11 | B05, B15, B17 | Financial results/postings have aggregate money, not durable component valuation detail. | Persist immutable valuations and transactionally derived line projections. |
| G06 | P1 | Constraint | 2–3, 10 | B07–B09 | Integer quantity and component-only aggregation cannot safely mix scale/unit/qualifier/source. | Canonical V2 measure/key/decimal; reuse journal/reducer owner with explicit V1 conversion. |
| G07 | P0 | Missing | 4 | B10–B12 | Selected terminal usage does not prove independent local measurement of final provider representation. | Capture post-adapter input and pre-customer output; record method/visibility limits. |
| G08 | P0 | Constraint | 4, 12 | B07, B12 | Local cache status and hidden reasoning/compute are not always independently observable. | Requirements repaired: allow unavailable/estimated local evidence; never copy remote data into local truth. |
| G09 | P0 | Missing | 5 | B13–B14 | Connector sideband/finalization loses dimensions and per-component or aggregate money detail. | Negotiated V2 sideband/finalizer, conformance tests and explicit reduced-capability path. |
| G10 | P0 | Constraint | 5, 10 | B10–B11 | Dedupe by one charge key cannot alone represent multiple revisions/source events. | Stable source identity + revision, strict same-key conflict and explicit charge coverage. |
| G11 | P1 | Missing | 5–6 | B04, B13, B26–B27 | Nested auxiliary charges can be inclusive or additive; fixed call/leg fields do not encode coverage. | Original provider-charge/resource identity, parent/child coverage and conserved attribution. |
| G12 | P0 | Missing | 9 | S06 | Account-window utilization is not an additive per-request cost. | Dedicated subject/semantics; request may reference gauge, but not claim exact debit. |
| G13 | P1 | Missing | 8 | B04, B06 | BillingCallID is not a trusted human submission across tool continuations. | Trusted submission identity and unsupported prompt-tariff capability when unavailable. |
| G14 | P0 | Missing | 12 | B03, B18 | No E-to-Q-to-P discrepancy decomposition in the financial selection path. | Pure comparison, tolerance, comparability and completeness state. |
| G15 | P0 | Missing | 13 | B04, B17 | Immutable usage and one provider operation need a safe late correction/statement path. | Append revisions; selected-cost head + balanced delta adjustments, not original-row mutation. |
| G16 | P1 | Constraint | 11, 17 | B04, B15–B16 | Old record fingerprints are historical contracts; lost breakdown cannot be regenerated honestly. | V1 reader preserves bytes/semantics; V2 hashes versioned; unknown historical provenance stays unknown. |
| G17 | P0 | Constraint | 10–11, 14 | B12, B16 | Optional metering appends and terminal money evidence have different durability guarantees. | Strict terminal evidence/closure/work share local transaction manager; no assumed distributed atomicity. |
| G18 | P0 | Missing | 14 | B06 | Existing maximum charge assumes output-token bounds for a narrower pricing shape. | Finite non-token quote or strict deny; actual cost never clipped to quote. |
| G19 | P0 | Missing | 15 | B19–B21 | Documented internal ComposeBilling is not externally importable. | Minimal public binding and explicit BuildWithBilling through the same host; preserve non-money Options. |
| G20 | P1 | Constraint | 15 | B19, B23 | A parallel financial authority would defeat prior slimming/convergence. | Keep the two runtime billing seams and single terminal handoff; no money via token ledger/observer. |
| G21 | P1 | Missing | 6, 16 | B25 | Aggregate margin needs explicit incomplete/native-currency/payer treatment. | Known subtotal, missing count, per-currency totals; BYOK payer and allocation labels. |
| G22 | P1 | Missing | 16 | B13, B15 | Generic raw capture could retain secrets or unsupported payloads. | Allowlisted economic paths/lexemes, bounded retention and scoped access; no complete transport dump. |
| G23 | P0 | Missing | 17 | B04, B16–B19 | Changing evidence/rating while workers run can create duplicate old/new financial effects. | Shadow no-write mode, durable cutover epoch, fencing, in-flight ownership and compatible rollback. |
| G24 | P1 | Constraint | 18 | B11–B12 | Metadata capture and decimal processing can hurt fast-path/test-suite cost. | Integer hot capture, bounded retained evidence, post-turn math, fresh performance and Windows gates. |
| G25 | P1 | Re-inventory | 5, 17–18 | B13–B14; provider profiles | Full current producer inventory was not exhaustively executed in this session. | Task 1 enumerates mechanically; Task 8 assigns the specified certified/bridge/unsupported disposition per family. |
| G26 | P1 | Missing | 8–9, 14 | B06, B23 | Customer unit-credit debit is not the same as provider gauge or monetary account. | Separate unit accounts/operations using the existing exposure lifecycle and atomic settlement authority. |

### Approach evaluation

| Option | Benefits | Rejected cost or selected tradeoff |
|---|---|---|
| Add expected/reported scalar columns to every billing struct | Familiar SQL, small first patch | Every modality/rate category widens DTOs, hashes and queries; does not solve origin/scope semantics. Rejected. |
| Replace the entire accounting subsystem with a new event platform | Clean conceptual start | Duplicates existing lifecycle/durability, reopens completed convergence, creates migration and operational risk. Rejected. |
| Extend canonical metering with V2 evidence, line-level valuations and narrow adapters | Reuses journal, B2BUA, host and financial transaction owners; confines future extension to components/schemas/adapters | Controlled initial contract migration and legacy readers are unavoidable. Selected. |

## Design decisions

**D-01 — Observation is not valuation.** Measurements and reported monetary claims retain source identity; E/Q/P/S/R valuations reference them. Selecting a provider cost for reporting/posting does not erase other evidence or imply agreement.

**D-02 — One canonical V2 path.** Use a versioned observation DTO in the existing metering journal. Keep V1 DTOs only for historical decoding and explicit one-way protocol/authority projections. Do not add an optional second exact-value field to the existing integer quantity and then maintain conflicting values indefinitely.

**D-03 — Exact bounded arithmetic.** Decimal coefficients and explicit scale preserve fractional units. Posting remains in current checked integer nano-money. Rate multiplication and comparison are bounded post-turn math, not float operations in the receive loop.

**D-04 — Qualifier and inclusion identity.** Full component keys and schema-defined inclusion avoid both unit collisions and additive total/subcomponent double charging. Local cache estimates remain estimates. Normalizers cannot rely on provider names in core.

**D-05 — Honest subscription metering.** Account-window gauges coexist with request debits. Snapshot differencing is not asserted exact per-request consumption; resource allocation is explicit and conserves original cost.

**D-06 — Minimal host extension.** The internal-only billing injection is a real Open Core gap. Add a narrow explicit public binding entry point to the existing host, retaining non-money Options and stock startup. This is a reviewed exception to the prior broader public-surface restriction, not a hidden workaround through nonmoney authority APIs.

**D-07 — Local transactional authority.** Strict evidence/closure/work acknowledgement uses the same local DB transaction manager. External commercial systems consume idempotent durable envelopes. No cross-store atomicity is assumed merely because both adapters implement interfaces.

**D-08 — Correction is a delta financial operation.** Append evidence and valuations, compare against the previously posted selected cost under a revision fence, and post the difference exactly once. Customer rebilling is policy-governed, not a side effect of supplier reconciliation.

**D-09 — Finite pre-OSS scope.** The implemented foundation includes real adapter capture, DB storage, reference component pricing, discrepancy queries, generic statement import, corrections and external binding tests. Universal tariffs, vendor invoice importers and commercial invoice/payment products are out of scope, not hidden requirements delegated to execution agents.

## Requirements repair and design review loop

This is a sequential author-performed adversarial review; no independent review agent or production test execution is claimed.

### Pass 1 — Requirements and brownfield repair

1. **Independent local quantities were overpromised.** Source B10–B12 and provider-hidden semantics do not establish local cache/reasoning visibility. Repaired 4.1–4.6 and 12.3 to require honest boundaries, estimator provenance and partial/incomparable status.
2. **Allowance percentage could be misclassified as a charge.** S06 provides window snapshots, not request debits. Repaired 9.1–9.6, the subject model, reconciliation rules and provider-capture tests.
3. **A-leg cost and customer charge were conflated.** Repaired 6.1–6.6 and 8.1–8.6 to separate all-leg COGS, independently rated revenue and allocation completeness.

Gate result: requirements are feasible without inventing provider data, prices or user behavior.

### Pass 2 — Architecture validation and repair

1. **Public integration could have depended on illegal internal imports.** B19–B21 required an explicit host-binding design. Added C7, requirement group 15 and external-module certification; ordinary Options remain nonmonetary.
2. **Generic units would collide in the old reducer.** B07–B09 required full keys, exact values, per-source streams and explicit V1 adaptation. Added D2/D3 and migration/deletion tests.
3. **Separate record writes would weaken durability.** B12/B16 required a shared local transaction boundary. Added D5/C6, strict host validation, crash tests and financial health behavior.

Gate result: GO for the bounded architecture. There is one host, one monetary admission authority, one canonical evidence journal and one posting owner per financial effect.

### Pass 3 — Financial and migration review

1. **Full replacement amounts could be double-posted.** Added cost-head compare-and-swap and new-minus-previous adjustment, including aggregate statements and replay tests (13.1–13.5).
2. **Fixed fees could repeat for every B-leg.** C3 moves scope-level fees outside selected-leg iteration; submission provenance and legacy policy migration are explicit (7.3, 8.3–8.4).
3. **Shadow/cutover might introduce two writers.** Added durable accounting epoch, in-flight format ownership, claim fences and forward-compatible rollback (17.2–17.5).

Gate result: GO for task decomposition. The synthetic acceptance vectors distinguish known defects from historical behavior that must stay versioned.

### Pass 4 — Final cross-artifact checks

The delivery validator checks canonical files, spec metadata, numeric requirement coverage in both design and tasks, unique sequential task IDs, valid dependency targets, acyclic dependency graph, reachable completion gate, task annotations, local Markdown references and absence of unresolved template markers. Validation output is provided outside the canonical directory in the delivery package.

These mechanical checks do not prove implementation correctness. Final production acceptance remains the explicit task gates. Required source re-inventory, provider-family disposition and baseline benchmarks are concrete first tasks, not evidence already obtained.

## Residual risks and mitigations

- **Provider evidence may remain incomplete:** preserve missing state; strict unsupported offers fail before execution; generic import permits later reconciliation.
- **No universal independent cache or hidden-reasoning count:** local estimates are bounded/method-labelled; compare only compatible available measures.
- **Initial migration touches multiple owners:** use chronological small PRs and the existing 100-Go-file gate; delete live compatibility paths at the end.
- **New host seam broadens public surface:** expose only named ports and test from a separate module; no internal types or second runtime.
- **Shared allowances/resources have ambiguous attribution:** preserve original scope; optional allocations are explicit estimates with conservation.
- **Tariff snapshots supplied externally may disappear:** durable snapshot content/hash is required for accepted priced work; no live-price lookup during replay.
- **Financial crash recovery cannot reconstruct unreceived evidence:** keep attempted state and incomplete cost, rather than certifying zero.

## Release scheduling

Implement before #398 completes the OSS Base split. Do not reopen completed core-ownership work. #532/#503 and #394 coordinate on capture/fast-path eligibility and benchmark revalidation without cyclic hard dependencies; #429 only changes path/name revalidation. The execution issue is the authoritative work order for this spec. The spec-only PR must not auto-close that issue.
