# Research and Design Decisions

## Summary

- **Feature:** `usage-economics-b-leg-multimodal-refinement`
- **Parent SDD:** `.kiro/specs/extensible-usage-economics-reconciliation/`
- **Execution tracker:** #620
- **Brownfield revalidation commit:** `bc7e5ce664c51d68bee771d477c53ce5de6265e1`
- **Scope:** refinement, not a parallel accounting subsystem
- **Risk:** high semantic impact, moderate implementation delta because the parent already provides generic component/evidence infrastructure.

The parent SDD is structurally sound but leaves three material ambiguities for implementation: multimodal accounting is generic rather than concretely certified; terminal-oriented wording can be mistaken for session finality; and customer rating still permits customer-boundary usage as an equal inference-usage authority instead of making B-leg execution evidence the primary inference basis.

## Brownfield findings

### R01 — A-leg is continuity, not an execution meter

`internal/core/b2bua/store.go` defines `ALegRecord` as the core-owned logical session row for routing and lineage. `BLegRecord` identifies one backend attempt slot within an A-leg. The A-leg has continuity, creation and last-seen state; it is not a provider execution record and carries no canonical inference-usage quantity.

**Implication:** session/A-leg economics should be a rollup/projection, not a mutable terminal bill.

### R02 — BillingCallID already separates resumed invocations

The archived `decoupled-exposure-and-post-usage-billing` SDD intentionally introduced one `BillingCallID` per incoming invocation, distinct across later calls on the same A-leg, and the current runtime stamps that ID during request preparation before B-leg execution.

**Implication:** a resumed session naturally creates another billing call and B-leg sequence without reopening or mutating prior economic records.

### R03 — Current parent SDD already separates call, A-leg and B-leg identities

The parent design states that one A-leg/session may contain multiple calls, one call may contain multiple B-legs, and a B-leg may contain multiple provider charge events. Requirement 6.2 already requires all attributable B-leg costs for operator COGS.

**Gap:** this identity model is stronger than the current retail wording in parent Requirement 8.1, which still treats customer-boundary usage as an equal inference-rating basis.

### R04 — Terminal markers are execution lifecycle markers

Current billing storage has independent call and call-leg records, while the runtime can resume the same A-leg with a later invocation. Provider DONE/EOF/response-completed markers therefore prove only that the current provider execution/call reached its terminal state; they cannot prove the A-leg will never progress again.

**Implication:** no economic correctness rule may depend on permanent session finality.

### R05 — Evidence model already supports incremental semantics

The parent requires delta, cumulative, correction and authoritative-replacement semantics plus revision-aware late evidence. This is sufficient to model incremental B-leg economics without inventing a new event platform.

**Gap:** the main parent lifecycle diagram funnels capture through terminal handoff and can be read as though economic visibility starts only at terminal. The implementation needs explicit pre-terminal checkpoint behavior while retaining the prohibition on stream-time rating/journal mutation.

### R06 — Repository protocols are already multimodal

Current OpenResponses/reference surfaces recognize `input_image`, `input_audio`, `input_video`, `output_image`, files and related multimodal parts. Provider/connector inventory also includes image, audio and video-capable families. The parent SDD already supports generic modality qualifiers and non-token units.

**Gap:** parent certification does not require concrete asymmetric input/output media fixtures, so an implementation could remain nominally generic while still being tested almost entirely with text/token economics.

### R07 — Multimodal provider economics are heterogeneous

The parent research already records examples where provider pricing distinguishes text/audio modalities, cache storage duration, grounding/tool charges and other non-token/resource dimensions. Image/audio/video systems may report tokens, items, seconds, frames, pixels/resolution-derived units, or aggregate charges.

**Implication:** the refinement must preserve provider-native economic units and direction rather than standardize everything into text tokens.

## Gap Analysis

| Gap | Severity | Current parent state | Refinement |
|---|---|---|---|
| M01 multimodal direction | P0 | modality is a qualifier, direction is mostly implicit in component names | make input/output direction economically mandatory and certify both sides |
| M02 transform boundaries | P0 | final provider payload and pre-customer output are mentioned, mostly in tokenizer language | explicitly cover resize/transcode/sample/tile/media transforms |
| L01 session finality | P0 | identity model is correct, but terminal lifecycle prose is easy to over-read | state that A-leg/session is never an economic-finality prerequisite |
| L02 pre-terminal economics | P0 | evidence supports deltas/revisions, flow emphasizes terminal envelope | require bounded durable checkpoints and revision-triggered post-processing before terminal |
| B01 inference usage authority | P0 | operator COGS is B-leg based; retail basis includes customer-boundary usage | make request-scoped inference usage B-leg-rooted by default |
| B02 retry economics | P0 | all B-legs affect COGS; retail selection is configurable | explicitly separate all-B-leg COGS from selected-B-leg retail usage |
| B03 non-request supplier costs | P1 | resource/account-period subjects already exist | retain them rather than manufacture synthetic B-legs |
| B04 A-leg customer measurement | P1 | can participate directly in retail basis | demote to explicit proxy-service tariff/diagnostic measurement unless a named offer selects it |

## Architecture options considered

### Option A — Keep three equal retail inference bases

Retain customer-boundary usage, selected supplier quantities and pass-through as peers.

**Rejected as default architecture.** It preserves two independent notions of inference consumption for one request and makes reconciliation between A-side and B-side measurement part of ordinary retail correctness. Customer-boundary measurement remains useful, but it should not be the default inference meter.

### Option B — Bill every B-leg directly to the customer

Rate all B-leg quantities using retail prices.

**Rejected.** It would make internal retries, failover and speculation automatically customer-billable and would couple retail economics to implementation quality.

### Option C — B-leg-rooted inference usage with policy-selected retail aggregation

All request-scoped inference usage originates from B-leg evidence. Operator COGS selects all operator-payable attributable provider charges. Retail inference usage selects the B-leg set defined by the frozen customer policy, normally surfaced/winning execution, while independent fixed/submission/account fees remain commercial lines.

**Selected.** This matches B2BUA ownership, preserves independent pricing, and cleanly separates operator execution cost from customer contract semantics.

## Design decisions

### D-R1 — Session is a continuity container

A-leg/session identity never becomes a financial-finality barrier. DONE/EOF closes the owned execution scope only. Later invocations on the same A-leg receive new BillingCallIDs/B-legs.

### D-R2 — B-leg is the request-scoped inference-usage root

Provider-bound input, provider-origin output, provider reported usage and request-scoped provider charges attach to B-leg/provider-charge identity. BillingCallID and A-leg expose projections.

### D-R3 — Retail is selected-B-leg, not all-B-leg

Operator cost and retail usage intentionally use different selectors. Normal retail defaults to surfaced/winning B-leg quantities; internal failure/retry/loser usage remains COGS unless an explicit offer includes it.

### D-R4 — Commercial scope is orthogonal to inference usage

Submission fee, per-call fee, customer credits and account/subscription charging remain legitimate customer lines keyed to their own trusted scope. They are not fabricated B-leg usage.

### D-R5 — Genuine resource/account economics stay outside B-legs

Prompt-cache storage, reservations, account-period subscriptions and shared resources remain their real economic subjects. Allocation to calls/B-legs is explicit and conserved.

### D-R6 — Multimodal direction is part of full meter identity

The V2 key must distinguish input from output even when component/unit/modality are otherwise identical. Provider-native units are preserved; no universal media-to-token conversion is introduced.

### D-R7 — Transform boundaries are economically observable boundaries

Expected supplier inference usage is measured from the final B-leg representation after proxy transforms. Provider output usage is measured before customer transforms. Customer-boundary measurement remains a separate service/reconciliation observation.

### D-R8 — Economic progress is revision-driven, not session-terminal-driven

Stable evidence may be appended pre-terminal. Pure rating/reconciliation workers can advance from durable revisions. Monetary posting, when enabled, uses revision-aware delta operations. Stream receive remains free of financial posting.

## Design Review Loop

### Review pass 1 — B2BUA consistency

**Critical concern:** “B-leg-only billing” could be read as charging the customer for every retry and as forcing resource/account costs onto fake B-legs.

**Repair:** distinguish **B-leg-rooted inference usage** from **retail B-leg selection**, and retain genuine non-request economic subjects. Requirements 2 and 5 encode this distinction.

**Result:** GO.

### Review pass 2 — Session lifecycle safety

**Critical concern:** removing session-finality dependence could accidentally remove useful per-call closure and deterministic retry-set freezing.

**Repair:** preserve call/B-leg closure as local execution checkpoints. Requirement 3.5 explicitly allows call closure to freeze the invocation-owned B-leg set while forbidding it from finalizing the parent A-leg.

**Result:** GO.

### Review pass 3 — Incremental accounting hot-path risk

**Critical concern:** “on the fly” could be implemented as per-chunk money mutation or per-token SQL.

**Repair:** observations/checkpoints may append incrementally, but rating/posting runs through bounded durable workers; Requirement 4.6 explicitly prohibits per-token writes and per-chunk posting requirements.

**Result:** GO.

### Review pass 4 — Multimodal completeness

**Critical concern:** generic modality support could still miss asymmetric image/audio/video input versus output tariffs and transformations.

**Repair:** make direction part of economic identity; add explicit media transform boundaries and mandatory image/audio/video/document certification vectors.

**Result:** GO.

## Final Assessment

**GO for implementation after this refinement is merged and attached to #620.** No new subsystem is required. The refinement narrows authority and lifecycle semantics already compatible with the parent design. Implementation must read the parent SDD and this refinement together; where they conflict on the named topics, this refinement has precedence.