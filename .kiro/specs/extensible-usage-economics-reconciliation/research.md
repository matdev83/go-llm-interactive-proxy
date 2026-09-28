# Research and Design Decisions

## Summary

- **Feature:** `extensible-usage-economics-reconciliation`
- **Classification:** brownfield / complex integration
- **Original discovery baseline:** `3da34d7875443355d65cb9d7df649555dfad3edb`
- **B2BUA/resumability/multimodal revalidation baseline:** `5a8174161a2d4d502ee55692b9c0121ae8c74800` on 2026-09-11
- **Complexity:** XL; **risk:** high because financial identity, multimodal evidence, persistence compatibility, connector contracts and resumable B2BUA lifecycle intersect. These are complexity labels, not delivery estimates.

The existing code already has useful metering facts, B2BUA lineage, per-invocation `BillingCallID`, immutable call/leg usage storage, independent customer/provider workers, operational exposure admission and balanced journals. The target change is therefore a convergence/migration, not a new accounting subsystem.

The follow-up review adds three clarified invariants:

1. **Multimodal usage is first-class economic evidence.** Image/audio/video/document input and output require distinct direction, units, qualifiers and provider-transform boundaries; they must not be forced into text-token equivalents.
2. **A-leg/session is never an economic-finality boundary.** `DONE`, EOF or a terminal marker seals only the current B-leg/call checkpoint. The same A-leg can later resume and create a new `BillingCallID` and new B-legs.
3. **Request-scoped inference economics are B-leg-rooted.** All operator-payable request inference costs aggregate from attributable B-legs. Retail inference quantities come from a frozen policy-selected subset of B-legs. Genuine shared resource/account-period economics remain at native scope until explicit conserved allocation.

## Evidence discipline

Source-derived statements below come from inspected repository code/specifications or named provider documentation. Design decisions are explicitly marked and are not claims about current implementation. Specification review does not claim production tests, database parity, race tests or performance gates have run; those remain implementation tasks.

## Project constraints reviewed

Repository `AGENTS.md`, `.kiro/AGENTS.md`, steering, Kiro templates/rules and the prior accounting convergence specs require:

- provider-neutral core; provider wire semantics remain in adapters/connectors;
- streaming-first execution and no retry/failover after client-visible output;
- no stream-time rating/journal balance mutation;
- explicit host/resource ownership and no second monetary authority;
- public `lipruntime.Options` remains non-money;
- dual-dialect persistence parity and bounded family-level contract tests;
- brownfield TDD and versioned compatibility rather than rewriting historical evidence.

## Brownfield findings

| Ref | Source | Verified observation | Design implication |
|---|---|---|---|
| B01 | `internal/core/billing/records.go` | `FinalBillingEvidence` is fixed-token plus one aggregate cost. | Replace live financial evidence with extensible V2 observations/valuations; retain V1 readers. |
| B02 | `internal/core/billing/rating.go` | Current operator/customer rating contains token-specific dimensions and selected-leg customer semantics. | Generic component rating is needed; selected-leg behavior is a useful seed for explicit B-leg retail selection. |
| B03 | `internal/core/billing/provider_cost.go` | Provider money and local tariff fallback are selected rather than retained independently; missing accepted evidence can become zero. | Preserve E/Q/P independently; unknown attempted work is not reconciled zero. |
| B04 | `internal/core/billing/call_usage.go` | Call and B-leg identities are already distinct; B-leg attempt sequence is financial replay identity. | Keep B-leg as request-scoped inference economic root; call/A-leg remain grouping scopes. |
| B05 | `internal/core/runtime/billing_call_id.go` | Each prepared invocation gets its own `BillingCallID`. | A resumed session naturally creates a new accounting invocation without reopening prior records. |
| B06 | `internal/core/b2bua/store.go` | `ALegRecord` is explicitly a logical session row with continuity and `LastSeenAt`; `BLegRecord` is a backend attempt slot. | Never wait for A-leg/session finality to account or settle completed calls/B-legs. |
| B07 | archived `decoupled-exposure-and-post-usage-billing` SDD | It characterized multiple billable calls on one A-leg and one BillingCallID per incoming invocation. | Resume-after-DONE is existing architectural intent, not a new session model. |
| B08 | `internal/core/runtime/attempt_usage_evidence.go` | Attempt-owned evidence can accumulate before terminal reduction. | V2 observations can be durable/reducible as acquired; terminal remains a checkpoint, not the source of usage truth. |
| B09 | `internal/core/runtime/metering_egress.go` | Backend/customer boundary metering already has separate checkpoint concepts. | Reuse boundary ownership, but generalize beyond text/token evidence. |
| B10 | `pkg/lipsdk/metering/quantity.go` and aggregate | Metering is extensible in name but currently integer/component-only in keying. | V2 requires exact decimal + direction + unit + schema + qualifiers. |
| B11 | backend-plugin accounting ABI | Current sideband/finalizer is fixed-token oriented. | Negotiate a V2 neutral sideband that can carry multimodal/non-token observations and charges. |
| B12 | OpenResponses reference/protocol fixtures | Current protocol surfaces already distinguish `input_image`, `input_audio`, `input_video`, `output_image`, files, etc. | Accounting must certify current multimodal traffic; it is not merely speculative future extensibility. |
| B13 | existing provider-cost/customer workers | Supplier and customer financial work are already separate post-usage concerns. | Preserve queue/lock separation while changing the evidence/rating basis. |
| B14 | current B2BUA failover/parallel semantics | One client invocation may execute several provider attempts, including losers/failures. | Operator COGS includes all payable B-legs; ordinary retail must not accidentally bill internal retries. |

## Provider billing-shape research

These sources illustrate representational requirements; the spec does not hardcode a universal tariff catalog and test prices remain synthetic.

| Ref | Source | Relevant billing shape | Implication |
|---|---|---|---|
| S01 | Anthropic prompt-caching documentation | separate input/cache-read/cache-creation with lifetime breakdown | Cache write lifetime and inclusion semantics are first-class. |
| S02 | OpenAI Usage/Costs APIs | granular usage and financial costs are separate acquisition planes | Provider quantities, locally rated Q, and provider/statement money remain independent. |
| S03 | Gemini pricing documentation | text/image/video/audio, cache storage, grounding and batch economics | Modality, direction, duration/resource units and qualifiers cannot be reduced to one token counter. |
| S04 | Replicate billing/prediction lifecycle | compute-duration billing; started canceled work may still cost money | Attempt outcome is separate from billable provider execution; proxy wall-clock is not automatically provider compute. |
| S05 | GitHub Copilot request-billing documentation | user prompts and autonomous tool actions can have distinct request semantics | Submission identity is commercial metadata, not a proxy for B-leg usage. |
| S06 | OpenAI Codex rate-limit implementation | named primary/secondary windows, used-percent and credit snapshots | Account-window observations are gauges, not per-request debits. |
| S07 | OpenRouter usage-accounting documentation | explicit upstream usage/cost can be returned separately from local calculation | Preserve upstream P separately from locally calculated Q. |
| S08 | OpenAI realtime/model documentation | text/audio input/output and cached categories differ | Economic direction and modality must be part of meter identity. |

## Gap analysis

| Gap | Priority | Finding | Repair |
|---|---|---|---|
| G01 | P0 | Financial path collapses E/Q/P into one selected amount. | Independent immutable observations and valuations plus reconciliation. |
| G02 | P0 | Token total/cache/reasoning inclusion can overlap. | Versioned inclusion schemas and disjoint charge partitions. |
| G03 | P0 | Missing provider evidence can appear as zero. | Explicit unknown/incomplete versus proven never-started zero. |
| G04 | P0 | Financial storage lacks durable component detail. | Generic component projections and immutable valuation lines. |
| G05 | P0 | Terminal-selected usage does not prove independent local provider-bound measurement. | Measure final B-leg input representation and provider-side output before customer transforms. |
| G06 | P0 | Connector ABI cannot carry arbitrary multimodal/non-token evidence losslessly. | Negotiated V2 sideband/finalizer and family TCK. |
| G07 | P0 | Account utilization can be mistaken for request cost. | Dedicated account-window gauge subject and separate request debit evidence. |
| G08 | P0 | Late provider corrections need safe financial replay. | Revisioned evidence, cost heads, currency-safe delta adjustments. |
| G09 | P0 | Cutover can create two monetary writers. | Shadow no-post mode, durable epoch, worker fencing and in-flight ownership. |
| G10 | P0 | Current public billing composition is internal-only. | Narrow typed `BuildWithBilling` binding using the same Host. |
| G11 | P0 | Multimodal support was generic but not concretely certified. | Direction-qualified image/audio/video/document schemas, transform boundaries and acceptance vectors. |
| G12 | P0 | Terminal/DONE wording could imply a finished session. | A-leg is resumable continuity; B-leg/call closure is checkpoint-only; later calls append economics. |
| G13 | P0 | Retail usage had competing A-leg/B-leg inference authorities. | B-leg-rooted retail inference quantities with frozen B-leg selection policy; customer-boundary metering only for diagnostics/service charges. |
| G14 | P1 | Shared cache/storage/subscription economics do not naturally belong to one B-leg. | Keep native resource/account-period subject and require conserved allocation; do not create synthetic B-legs. |
| G15 | P1 | Multimodal transforms can make customer and provider representations differ. | Observe both boundaries independently and never overwrite provider usage with delivered-media measurements. |

## Approach evaluation

| Option | Strength | Problem | Decision |
|---|---|---|---|
| Add more scalar token/media columns | Familiar initial implementation | Cartesian schema growth, weak source identity, cannot model provider-specific units cleanly | Reject |
| Treat A-leg/session as the billing meter | Simple aggregate | Session can resume indefinitely; retries/failover and provider execution are hidden; terminal finality is false | Reject |
| Bill every B-leg directly to the customer | Aligns with provider execution | Leaks internal retry/race costs into normal retail contracts | Reject |
| **B-leg-rooted inference economics + policy-selected retail + native resource subjects** | Matches B2BUA execution, preserves COGS truth, supports custom retail and resumability | Requires explicit selection/allocation policy | **Select** |

## Design decisions

### D-01 Observation is not valuation
Measurements and provider monetary claims retain source identity. E/Q/P/S/R valuations reference evidence; selecting a posting basis never erases alternatives or implies reconciliation.

### D-02 Canonical V2 uses exact component identity
A V2 component key includes economic direction, component, unit, schema and sorted bounded qualifiers. Exact decimals avoid float-based drift; postings remain checked integer nano-money.

### D-03 B-leg-rooted inference economics
Every request-scoped provider inference quantity/charge belongs to a B-leg or provider-charge event beneath it. `BillingCallID` groups the B-legs of one invocation; A-leg/session supplies continuity and aggregate projections only.

### D-04 Retail selection is independent from supplier COGS
All operator-payable attributable B-legs contribute to COGS. Customer inference rating uses the frozen retail policy's B-leg/component selection. Internal retries/losers are not customer-billable merely because they cost the operator. Fixed submission/call fees and separately declared proxy-service charges remain separate commercial components.

### D-05 Resource/account economics retain native scope
Shared prompt-cache storage, reserved capacity, subscriptions and account-period costs are not forced into synthetic B-legs. They can enter call/A-leg economics only through explicit allocation whose weights/remainder conserve the original cost.

### D-06 Session continuity has no financial finality
`DONE`, EOF and terminal events close only the current execution checkpoint. Accounting/valuation for a completed call does not wait for A-leg retirement. Later resume creates a new `BillingCallID` and B-legs while earlier records remain immutable.

### D-07 Evidence may be incremental without stream-time money mutation
Bounded immutable observations can be persisted/reduced while a B-leg is active. Rating/posting remains host-owned worker/settlement work at the applicable B-leg/call/commercial checkpoint. This avoids both “wait for session end” and direct receive-loop financial mutation.

### D-08 Multimodal direction and transforms are economic identity
Input/output image, audio, video and documents remain distinct even when they share a physical unit. Provider-bound input is observed after proxy transforms; provider-side output before customer transforms. Cross-unit conversions require an explicit versioned schema/rating rule.

### D-09 Corrections are delta operations
Late evidence creates new immutable valuations. A correction posts only the comparable new-minus-previous delta, with same-currency or explicit frozen-FX validation and idempotent posting identity.

### D-10 One host and one monetary authority
The new public binding maps to the existing Host/admission/terminal owners. It does not expose internals, create a second runtime or put money on best-effort observers.

## Design review loop

The review is author-performed and adversarial; no independent-agent certification or production test execution is claimed.

### Pass 1 — Original brownfield/accounting review

- Repaired independent local measurement overclaims where cache/hidden compute is not observable.
- Separated allowance gauges from request debits.
- Separated all-leg operator COGS from customer revenue policy.
- Added exact/extensible components, reconciliation, statement corrections, migration fencing and public binding.

**Gate:** GO after repairs.

### Pass 2 — External PR review repairs

Code review of the original spec required:

- typed, store-scoped charge coverage and cycle/overlap/allocation validation;
- currency/FX comparability before selected-cost correction deltas;
- a precise provider-neutral public contract boundary rather than a provider-shaped normalizer API;
- task evidence wording consistent with chronological migration/release gates.

All were incorporated before the original spec merged.

**Gate:** GO after repairs.

### Pass 3 — B2BUA/resumability/multimodal review

Three critical questions were checked against current `main`:

1. **Does the model account for current and future multimodal inputs/outputs?** Structurally yes, but certification was too generic. Economic direction, explicit media units/qualifiers, transform-boundary observation, provider-family fixtures and acceptance vectors were added.
2. **Does accounting rely on a finished session?** It must not. Current B2BUA uses A-leg as continuity and creates per-invocation BillingCallIDs. Requirements/design/tasks now make terminal/DONE checkpoint-only and test resume after DONE on the same A-leg.
3. **Should usage be B-leg based?** Yes for request-scoped inference economics, with two qualifications: retail policy selects customer-billable B-legs rather than automatically billing retries/losers, and genuine shared resource/account-period costs remain native-scope until allocation.

A further consistency repair avoids overcorrecting lifecycle behavior: incremental durable observations are allowed while a B-leg is active, but authoritative financial mutation still does **not** occur in receive callbacks and does not require arbitrary per-chunk posting. Settlement uses the applicable frozen B-leg/call/commercial checkpoint and never waits for session retirement.

**Gate:** GO after repairs.

## Residual risks and mitigations

- **Provider evidence may remain incomplete:** preserve missing state; strict unsupported offers fail before execution; statement import supports later reconciliation.
- **Provider media units vary materially:** retain provider-family unit/schema/qualifiers; do not normalize through lossy conversion for convenience.
- **Proxy transforms change media economics:** retain provider-bound/provider-output and customer-boundary observations separately.
- **A-leg can resume long after DONE:** no session-final billing prerequisite; immutable call/B-leg records plus rebuildable projections bound runtime state.
- **B-leg-rooted retail could accidentally expose internal retries:** require explicit frozen B-leg selection; normal offers do not include non-surfaced attempts unless contractually declared.
- **Shared resources are hard to attribute:** preserve original scope and require explicit conserved allocation; unknown allocation remains unallocated.
- **Migration spans several owners:** implement chronologically in bounded PRs and remove live V1 monetary paths only after fenced cutover.
- **Crash recovery cannot invent evidence never received:** attempted work remains incomplete/unknown rather than zero.

## Release scheduling

The implementation work remains tracked by #620 and is a direct prerequisite of #398. This follow-up SDD repair changes no production code and does not complete #620. Task 1 must re-inventory the actual implementation-starting revision; #532/#503 and #394 remain coordination points for large-payload capture and benchmark refresh without cyclic dependencies.
