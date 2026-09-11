# Implementation Plan

## Execution Contract

This is a normative refinement of `.kiro/specs/extensible-usage-economics-reconciliation/`, not an independent implementation stream. Issue #620 remains the owning work order. Implementation agents must read the parent SDD and this refinement together; this document has precedence only for the topics enumerated in `design.md` under **Parent-Spec Amendments**.

Use TDD. Keep the parent's shadow/cutover fencing and single monetary writer. Do not introduce session-final settlement, a second evidence journal, a second B2BUA model, per-chunk financial writes, or synthetic B-legs for genuine resource/account-period economics.

## Tasks

- [ ] 1. Rebaseline the parent plan against refined economic authority

- [ ] 1.1 Add red architecture tests for A-leg, call, and B-leg authority
  - Prove A-leg/session is continuity and rolling aggregation only, BillingCallID groups one invocation, and B-leg is the request-scoped inference-usage root.
  - Prove a normal terminal on one call cannot mark the parent A-leg economically final.
  - Completion: tests fail on any path that creates an authoritative A-leg inference meter or session-final bill.
  - _Requirements: 2.1, 2.2, 2.6, 3.1, 3.2, 3.5, 3.6_
  - _Boundary: tests: B2BUA and billing architecture_
  - _Depends: none_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/billing/... ./internal/archtest/..._

- [ ] 1.2 Add red retail-selection tests
  - Characterize retry, failover, loser, winner, surfaced and cost-pass-through cases before changing retail rating.
  - Prove operator COGS and customer inference usage intentionally use different B-leg selectors.
  - Completion: normal retail does not bill internal retry/loser usage, while COGS still includes operator-payable attempts.
  - _Requirements: 2.3, 5.1, 5.2, 5.3, 5.4, 5.5_
  - _Boundary: tests: billing domain_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingcompose/..._

- [ ] 1.3 Add red multimodal and continuation fixtures
  - Add synthetic input/output image, audio, video and document economics plus one A-leg with multiple BillingCallIDs separated by DONE/terminal events.
  - Include pre-terminal usage revision and post-terminal correction fixtures.
  - Completion: every new acceptance vector has a named failing test or contract fixture before implementation.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 3.3, 3.4, 4.1, 4.4, 4.5, 6.1, 6.2, 6.3, 6.4_
  - _Boundary: tests and conformance fixtures_
  - _Depends: 1.1_
  - _Validation: go test ./pkg/lipsdk/metering/... ./internal/core/runtime/...; make parity-checks_

- [ ] 2. Refine canonical multimodal component identity

- [ ] 2.1 Add explicit economic direction to V2 component identity
  - Make input/output/request/resource/gauge direction part of the canonical key, serializer and fingerprint.
  - Provide lossless adapters for existing component names that already imply direction; do not reinterpret historical V1 hashes.
  - Completion: identical modality/unit values in opposite directions cannot collide or select the same rate accidentally.
  - _Requirements: 1.1, 1.2, 1.3_
  - _Boundary: SDK/public metering schema_
  - _Depends: 1.3_
  - _Validation: go test ./pkg/lipsdk/metering/... ./internal/core/metering/..._

- [ ] 2.2 Define bounded multimodal qualifiers and native units
  - Support bill-relevant resolution/quality/dimensions/duration/sample-rate/channel/frame-rate/page/resource qualifiers without raw media retention.
  - Preserve provider-native tokens, counts, seconds, frames, pixels/products, pages, bytes, queries and schema-qualified units rather than converting everything to text tokens.
  - Completion: all synthetic media keys round-trip exactly and unsupported precision/unit combinations fail explicitly.
  - _Requirements: 1.2, 1.3, 1.6_
  - _Boundary: SDK/public metering schema and validation_
  - _Depends: 2.1_
  - _Validation: go test ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/..._

- [ ] 2.3 Extend generic rating fixtures for asymmetric media pricing
  - Add direction-specific media unit rates, fixed/image generation fees and mixed-native-unit rules using the parent exact arithmetic and snapshot contracts.
  - Ensure a provider-derived conversion is used only when its frozen schema explicitly defines it.
  - Completion: input image, output image, input audio, output audio, input video, output video and document/page vectors rate independently.
  - _Requirements: 1.1, 1.2, 1.3, 5.4, 6.1_
  - _Boundary: billing/economics reference rating tests_
  - _Depends: 2.2_
  - _Validation: go test ./internal/core/billing/... ./pkg/lipsdk/economics/..._

- [ ] 3. Measure provider-facing multimodal boundaries

- [ ] 3.1 Capture final provider-bound input representation
  - Extend the parent final-backend-payload hook to record bounded text/media measurements after resize/transcode/extraction/tiling/rewrites and before upstream commitment.
  - Keep customer-ingress measurements separate and retain method/quality when exact provider billing units cannot be reconstructed locally.
  - Completion: an input-image transform changes B-leg expected economics without rewriting customer-boundary evidence.
  - _Requirements: 1.4, 1.6, 2.1_
  - _Boundary: backend adapter attachment plus core neutral checkpoint_
  - _Depends: 2.3_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/metering/... ./internal/plugins/backends/..._

- [ ] 3.2 Capture provider-origin multimodal output before customer transforms
  - Measure available provider output units/metadata before projection, compression, trimming, resampling or frontend encoding.
  - Preserve customer-egress measurement separately for explicit service tariffs and reconciliation.
  - Completion: provider-origin output and customer-visible output can differ without changing provider usage identity.
  - _Requirements: 1.5, 1.6, 2.1_
  - _Boundary: backend ingress/core metering/frontends_
  - _Depends: 3.1_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/metering/... ./internal/plugins/frontends/..._

- [ ] 3.3 Preserve fast-path and raw-media safety
  - Keep accounting metadata bounded and avoid media-body duplication; use canonical fallback before commitment when required economic measurement is unavailable on a fast path.
  - Completion: disabled monetary mode adds no accounting I/O and enabled multimodal capture stays within configured observation/frame limits.
  - _Requirements: 1.3, 1.4, 1.5, 4.6_
  - _Boundary: runtime performance and security tests_
  - _Depends: 3.2_
  - _Validation: go test ./internal/core/runtime/... ./internal/archtest/...; make parity-checks_

- [ ] 4. Make evidence and valuation revision-driven rather than session-final-driven

- [ ] 4.1 Append bounded pre-terminal B-leg economic checkpoints
  - Extend existing V2 observation/journal ownership to persist stable usage deltas or cumulative snapshots before execution terminal where providers/local measurement expose them.
  - Coalesce/batch safely; no requirement for one observation per wire frame.
  - Completion: long-running B-leg usage becomes durably queryable before terminal without direct money mutation from receive callbacks.
  - _Requirements: 3.1, 4.1, 4.2, 4.4, 4.6_
  - _Boundary: runtime evidence capture and metering journal_
  - _Depends: 3.3_
  - _Validation: go test ./internal/core/runtime/... ./internal/infra/metering/journalstore/..._

- [ ] 4.2 Trigger pure valuation/reconciliation from durable revisions
  - Make economic worker identity include the latest evidence revision/input hash so pre-terminal, terminal and late-correction revisions are processed idempotently.
  - Preserve customer/provider queue isolation and avoid balance locks during pure computation.
  - Completion: repeated or reordered revisions produce one deterministic current valuation head.
  - _Requirements: 4.2, 4.4, 4.5, 4.6_
  - _Boundary: billing app orchestration and durable work queues_
  - _Depends: 4.1_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingstore/..._

- [ ] 4.3 Support configured incremental financial deltas without A-leg finality
  - Where policy settles incrementally, reuse parent selected-cost heads/operation keys to post only the difference from the previously posted selected valuation.
  - Where settlement is deferred to call/B-leg checkpoint, expose accrued valuation without waiting for A-leg retirement.
  - Completion: both modes survive replay and late correction with no duplicate posting or session-final trigger.
  - _Requirements: 4.3, 4.4, 4.5_
  - _Boundary: billing posting domain and driven store_
  - _Depends: 4.2_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingstore/...; make test-db-parity_

- [ ] 5. Enforce resumable-session economics

- [ ] 5.1 Preserve local call closure while keeping A-leg open-ended
  - Keep current call/B-leg closure and expected-attempt freezing for one BillingCallID, but remove any economic assumption that A-leg DONE/idle/retirement prevents later calls.
  - Completion: call 1 closes deterministically, call 2 can later allocate a new BillingCallID/B-leg on the same A-leg, and neither rewrites the other.
  - _Requirements: 3.1, 3.2, 3.3, 3.5, 3.6_
  - _Boundary: runtime B2BUA and billing closure_
  - _Depends: 4.3_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/billing/..._

- [ ] 5.2 Keep late economics appendable after execution close
  - Allow provider finalizer/statement/correction observations to reference the closed B-leg economically without reopening execution or allocating a replacement B-leg.
  - Completion: late evidence adjusts completeness/valuation while the B-leg lifecycle remains closed exactly once.
  - _Requirements: 3.4, 4.5_
  - _Boundary: metering identity and billing correction_
  - _Depends: 5.1_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/metering/... ./internal/core/billing/..._

- [ ] 5.3 Make A-leg reports rolling `as_of` projections
  - Remove/avoid customer-facing or operator report semantics implying permanent session economic finality.
  - Return calls/B-leg contribution lineage, current known totals, completeness and unresolved late evidence at a revision/time.
  - Completion: A-leg retirement produces no new charge and resumed calls appear in later projections naturally.
  - _Requirements: 2.2, 2.6, 3.1, 3.3, 3.6_
  - _Boundary: billing query seam and reports_
  - _Depends: 5.2_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/..._

- [ ] 6. Refine retail inference usage to policy-selected B-legs

- [ ] 6.1 Implement explicit retail B-leg selector
  - Resolve the frozen policy to surfaced/winner, named outcome subset, all attributable attempts, or cost-pass-through basis.
  - Return immutable selected B-leg/observation refs plus policy/version/completeness; do not calculate COGS in this selector.
  - Completion: default independent retail selects surfaced/winner inference usage and does not inherit runtime retry count.
  - _Requirements: 5.1, 5.2, 5.3_
  - _Boundary: billing domain customer policy_
  - _Depends: 5.3_
  - _Validation: go test ./internal/core/billing/... ./internal/infra/billingcompose/..._

- [ ] 6.2 Rate selected B-leg quantities with independent customer prices
  - Apply the parent's generic customer rate rules, including modality-specific input/output rates, to selected B-leg quantities.
  - Keep submission/call/account fees outside B-leg iteration and preserve explicit customer credit/allowance handling.
  - Completion: retail amount changes with customer tariffs but not with provider tariff or unselected retry B-legs.
  - _Requirements: 5.4, 5.5_
  - _Boundary: billing domain retail rating_
  - _Depends: 6.1_
  - _Validation: go test ./internal/core/billing/... ./pkg/lipsdk/economics/..._

- [ ] 6.3 Preserve explicit proxy-service customer-boundary tariffs
  - Permit separately named customer-boundary media/byte/item service meters without labelling them provider inference usage or silently substituting them for B-leg quantities.
  - Completion: an offer can charge egress bandwidth/image service independently while inference usage remains B-leg-rooted.
  - _Requirements: 5.6_
  - _Boundary: billing domain customer service policy_
  - _Depends: 6.2_
  - _Validation: go test ./internal/core/billing/... ./internal/core/metering/..._

- [ ] 7. Migrate provider and resource producers under the refined model

- [ ] 7.1 (P) Extend OpenAI/OpenResponses and compatible fixtures for media economics
  - Cover supported input media and assistant media output usage/cost fields or explicitly negotiated absence.
  - Test direction identity, provider-bound transforms and late/final usage semantics.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 6.1, 6.2_
  - _Boundary: provider family adapters and conformance fixtures_
  - _Depends: 6.3_
  - _Validation: go test ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/openresponsescompat/...; make parity-checks_

- [ ] 7.2 (P) Extend Gemini/Vertex-family fixtures for multimodal native units
  - Cover modality-specific input/output, media duration/resolution and resource/storage evidence actually exposed by supported APIs.
  - Preserve native units and negotiated unavailable fields rather than infer from price sheets.
  - _Requirements: 1.1, 1.2, 1.3, 1.6, 6.1, 6.2_
  - _Boundary: Gemini/Vertex provider families_
  - _Depends: 6.3_
  - _Validation: go test ./internal/plugins/backends/protocols/geminigenerate/...; changed connector module tests_

- [ ] 7.3 (P) Re-inventory remaining media-capable providers and connectors
  - Mechanically identify image/audio/video/file-capable existing producers and assign certified, lossless bridge, or explicitly unsupported economic coverage.
  - Completion: no current media-capable producer silently claims complete economics without a fixture/capability disposition.
  - _Requirements: 1.1, 1.2, 1.6, 6.1_
  - _Boundary: provider profiles/connectors/test inventory_
  - _Depends: 6.3_
  - _Validation: make parity-checks; focused module tests for changed connectors_

- [ ] 7.4 Close non-request resource attribution
  - Keep prompt-cache storage, reservations, subscription/account costs and shared resources on their real subjects; allocate only through explicit conserved policy.
  - Prohibit synthetic B-legs introduced solely to attach such costs.
  - Completion: resource/account costs remain payable and roll up when allocated without contaminating B-leg inference-usage totals.
  - _Requirements: 2.4, 2.5_
  - _Boundary: billing allocation and resource accounting_
  - _Depends: 7.1, 7.2, 7.3_
  - _Validation: go test ./internal/core/billing/... ./pkg/lipsdk/promptcache/... ./internal/standardplugins/featurehost/..._

- [ ] 8. Certify refinement and integrate into #620 release gate

- [ ] 8.1 Run multimodal direction and transform certification
  - Execute all required image/audio/video/document input/output and transform vectors through neutral evidence, rating and durable round-trip.
  - Completion: no test relies on universal conversion to text tokens and every direction-specific rate is independently provable.
  - _Requirements: 6.1, 6.2_
  - _Boundary: tests: multimodal economics_
  - _Depends: 7.4_
  - _Validation: make test-unit; make parity-checks; make test-db-parity_

- [ ] 8.2 Run resumable-session and incremental-revision certification
  - Execute same-A-leg sequential BillingCallIDs, pre-terminal checkpoint, terminal checkpoint, post-terminal correction, restart and concurrent late-revision cases.
  - Completion: accounting never requires A-leg finality and no duplicate usage/posting appears.
  - _Requirements: 6.3, 6.4, 3.1, 3.2, 3.3, 3.4, 4.1, 4.2, 4.3, 4.4, 4.5_
  - _Boundary: tests: lifecycle/persistence/race_
  - _Depends: 8.1_
  - _Validation: make test-db-parity; make test-race; go test ./internal/core/runtime/... ./internal/core/billing/..._

- [ ] 8.3 Run COGS-versus-retail selector certification
  - Verify all-attributable-B-leg COGS against winner-only, retry-inclusive and cost-pass-through retail policies, including multimodal quantities and non-request allocations.
  - Completion: operator cost and customer charge remain independently explainable from immutable contribution refs.
  - _Requirements: 6.5, 2.3, 2.4, 2.5, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6_
  - _Boundary: tests: integrated economics_
  - _Depends: 8.2_
  - _Validation: make test-unit; make parity-checks; go test ./internal/core/billing/... ./internal/infra/billingstore/..._

- [ ] 8.4 Reconcile parent task evidence and final release traceability
  - Update #620 implementation evidence so parent tasks use this refinement's B-leg/multimodal/session semantics wherever listed in `design.md`.
  - Cross-check all 36 refinement criteria against named tests and parent task owners; no parent acceptance may be satisfied by contradictory A-leg/session-final behavior.
  - Completion: #620 cannot close until both parent and refinement acceptance criteria pass at the same release candidate SHA.
  - _Requirements: 6.6, 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 6.1, 6.2, 6.3, 6.4, 6.5_
  - _Boundary: tests and release certification_
  - _Depends: 8.3_
  - _Validation: make quality-checks; make test; make parity-checks; make test-db-parity; make qa_