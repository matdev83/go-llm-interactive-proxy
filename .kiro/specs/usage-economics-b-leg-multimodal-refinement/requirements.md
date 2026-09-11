# Requirements Document

## Introduction

Refine the approved `extensible-usage-economics-reconciliation` SDD before implementation so inference economics follows the actual B2BUA execution model. This refinement is normative for issue #620 and supersedes only the parent clauses it explicitly narrows: multimodal component identity, session/call/attempt finality, inference-usage authority, customer usage basis, and incremental economic processing.

The central model is:

- an **A-leg/session is continuity and aggregation**, not a final bill or inference-usage authority;
- a **BillingCallID is one client invocation/commercial grouping**, not an independent inference meter;
- **request-scoped inference usage is rooted in B-leg execution evidence**;
- customer inference charging uses a policy-selected set of B-leg evidence and independent customer prices;
- supplier resource/account-period costs that are not request-scoped remain separate economic subjects and require explicit conserved allocation before attribution;
- terminal markers close only the current execution scope and never make an A-leg economically final.

**Brownfield revalidation baseline:** `bc7e5ce664c51d68bee771d477c53ce5de6265e1` on 2026-09-11. The original SDD remains authoritative except where this refinement explicitly changes its semantics.

## Boundary Context

- **In scope:** multimodal input/output accounting, B-leg-rooted inference usage, resumable-session semantics, incremental observation/valuation, retail B-leg selection, transform-boundary metering, late evidence and correction behavior, and required certification changes.
- **Out of scope:** changing routing/failover policy, inventing provider tariffs, requiring per-chunk monetary database writes, replacing the A-leg/B-leg continuity model, vendor invoice parsers, or introducing synthetic B-legs for genuine account/resource-period costs.
- **Boundary ownership:** provider adapters/connectors own provider wire semantics; metering owns neutral observations; billing owns economic selection/rating/posting policy; runtime owns B2BUA lifecycle and trusted correlation; infrastructure owns durable processing.

## Requirements

### Requirement 1: Multimodal economic identity

**Objective:** As an operator, I want media usage represented in its native economic form, so that images, audio, video, files, and text are neither collapsed nor mispriced.

#### Acceptance Criteria

1.1. The accounting system shall treat input and output direction as economically significant component identity and shall not merge otherwise identical units across directions.

1.2. The accounting system shall represent text, image, audio, video, document/file, tool, and provider-defined modalities using native reported or locally measurable units such as tokens, items, seconds, frames, pixels or megapixels, pages, bytes, queries, or schema-qualified provider units without forcing conversion to text tokens.

1.3. When price depends on resolution, quality, dimensions, duration, sample rate, channel count, frame rate, cache treatment, or another bounded media qualifier, the component key and frozen pricing context shall preserve that qualifier.

1.4. When a client media input is resized, transcoded, sampled, extracted, tiled, compressed, or otherwise transformed before provider submission, the system shall distinguish customer-boundary measurement from the final provider-bound B-leg representation and shall use the latter for provider-side expected inference usage.

1.5. When provider output is transformed before customer delivery, the system shall preserve provider-side output measurement separately from customer-visible service measurement and shall not reduce supplier economics to the transformed A-leg payload.

1.6. If a provider exposes only a derived multimodal meter or aggregate monetary charge, the system shall retain that native evidence and shall not invent a lower-level media decomposition.

### Requirement 2: B-leg-rooted inference usage authority

**Objective:** As an operator, I want inference usage anchored to actual backend execution, so that retries, races, resumptions, and provider costs have one coherent source of truth.

#### Acceptance Criteria

2.1. Every request-scoped inference usage observation shall be attributed to a concrete B-leg/attempt and provider charge identity; an A-leg/session shall not be an independent authoritative inference-usage source.

2.2. A BillingCallID shall group B-legs caused by one client invocation and may carry commercial metadata, but its inference-usage totals shall be projections over B-leg evidence rather than separately measured competing truth.

2.3. Operator COGS for a call shall include every operator-payable attributable B-leg/provider-charge event, including failed attempts, retries, canceled work, parallel losers, and auxiliary execution, subject to validated inclusive/additive coverage and payer rules.

2.4. Genuine provider resource, reservation, cache-storage, subscription, or account-period costs that are not request-scoped shall remain resource/account-period subjects and shall not be forced onto synthetic B-legs.

2.5. Where a non-B-leg supplier cost is attributed to calls or B-legs, the allocation shall retain the original economic subject, policy/version, exact conserved weights, and any unallocated remainder.

2.6. A-leg/session economic totals shall be query-time or maintained projections over calls, B-legs, and explicitly allocated resource costs; they shall not require or imply a one-time final session settlement.

### Requirement 3: Non-final session continuity

**Objective:** As a maintainer, I want accounting independent of session finality, so that any conversation can resume after a normal DONE/EOF/terminal marker.

#### Acceptance Criteria

3.1. The system shall not require an A-leg/session completion marker, retirement event, TTL expiry, or disconnect to recognize, value, post, reconcile, or report already incurred economics.

3.2. A provider DONE, EOF, terminal frame, response-completed event, or equivalent shall close only the associated execution/call scope defined by trusted lifecycle ownership and shall not mark the parent A-leg/session economically final.

3.3. When a previously quiescent A-leg is resumed, the new invocation shall receive its own BillingCallID and newly allocated B-leg identities while prior economic records remain immutable and continue to contribute to rolling A-leg projections.

3.4. Closing a B-leg execution state shall not prohibit later provider economic evidence, statements, or corrections from appending new revisions against that B-leg's economic identity.

3.5. Call closure may freeze the set of B-legs owned by that invocation for deterministic customer settlement, but it shall not prevent later invocations on the same A-leg or require the A-leg to become terminal.

3.6. Retention or retirement of an A-leg shall be a continuity/storage concern and shall not be the trigger that creates previously unrecognized usage or supplier cost.

### Requirement 4: Incremental economic processing

**Objective:** As an operator, I want economics to advance as evidence arrives, so that long-lived or resumed traffic does not depend on a mythical final session event.

#### Acceptance Criteria

4.1. When a B-leg produces a stable usage delta, cumulative usage snapshot, reported charge, or locally measured checkpoint, the system shall be able to durably append that evidence before B-leg terminal closure.

4.2. The system shall support revision-triggered provisional valuation and reconciliation over the latest durable B-leg evidence without performing rating or financial journal mutation inside the stream receive callback.

4.3. Where monetary policy permits incremental posting, the system shall post only the difference from the previously posted selected valuation under revision/fence identity; where policy defers posting, the current accrued valuation shall remain queryable without waiting for A-leg finality.

4.4. Terminal processing shall act as an execution checkpoint/completeness transition for the current call or B-leg and may trigger final-at-that-time valuation, but shall not be the only path that makes usage economically visible.

4.5. Late provider evidence or corrected usage shall reuse the same append/revision and delta-adjustment mechanisms regardless of whether it arrives before or after execution terminal.

4.6. Incremental accounting shall remain bounded: no per-token SQL writes, no unbounded response copy, no per-chunk monetary posting requirement, and no new retry/failover after client-visible output.

### Requirement 5: Customer charging from B-leg evidence

**Objective:** As a proxy operator, I want customer inference charges derived from actual execution usage while preserving independent retail pricing and retry policy.

#### Acceptance Criteria

5.1. The default customer inference-usage basis shall be normalized quantities from a policy-selected set of B-legs belonging to the BillingCallID/submission, not a separately reconstructed A-leg inference-usage meter.

5.2. Customer policy shall explicitly define which B-legs contribute to retail inference usage, with surfaced/winning execution as the normal independent-retail default and all-attributable B-legs available only for explicit policies such as cost pass-through or retry-inclusive offers.

5.3. Internal retry, failover, speculative, or race-loser B-leg usage shall affect operator COGS but shall not automatically increase customer inference usage unless the frozen retail policy explicitly includes it.

5.4. Customer prices for selected B-leg quantities may differ arbitrarily from supplier prices and shall support modality-specific input/output rates, fixed fees, minimums, blocks, credits, and other existing generic rule types.

5.5. Submission-, call-, account-, or subscription-scoped customer charges shall remain separate commercial line items keyed to their trusted scope and shall not be multiplied merely because one logical action caused multiple B-legs.

5.6. Customer-boundary bytes/media/items may be used by an explicitly declared proxy-service tariff, diagnostics, or reconciliation, but such measurements shall not be labelled canonical provider inference usage.

### Requirement 6: Certification and parent-spec integration

**Objective:** As a maintainer, I want executable proof that the refined model works across modalities, continuation, and B2BUA edge cases before #620 is implemented.

#### Acceptance Criteria

6.1. The implementation shall certify distinct image input, image output, audio input, audio output, video input, video output, and document/file fixtures with provider-native units and direction-specific pricing behavior.

6.2. The implementation shall certify at least one media transformation case in each direction, proving provider-bound/provider-origin economics remain separate from customer-boundary representations.

6.3. The implementation shall certify one A-leg with multiple sequential BillingCallIDs separated by normal DONE/terminal markers, proving economics continue correctly when the session resumes.

6.4. The implementation shall certify incremental pre-terminal evidence, terminal checkpointing, and post-terminal correction for the same B-leg without duplicate usage or postings.

6.5. The implementation shall certify operator all-B-leg COGS against customer selected-B-leg retail usage under retry, failover, parallel-loser, and cost-pass-through policies.

6.6. Before implementation begins, issue #620 and the parent SDD execution plan shall treat this refinement as normative; any parent wording that permits A-leg inference usage as an equal default authority or session-finality-dependent settlement shall be interpreted according to this refinement.