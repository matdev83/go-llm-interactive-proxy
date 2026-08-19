# Final Spec Review

## Verdict

**GO as a specification; not approved for implementation yet.**

The full Kiro SDD workflow has completed through tasks generation and brownfield correction. `spec.json` approvals intentionally remain false and `ready_for_implementation` remains false pending maintainer review.

## Supersession Decision

This spec is a **follow-up**, not a superseding specification.

It does not supersede or reopen:

- `reasoning-output-preservation`;
- `reasoning-preservation-e2e-validation`;
- `openai-responses-reasoning-preservation`;
- `openai-codex-native-compaction`;
- `compaction-continuity-preservation`.

Those completed specs still describe required behavior. The new spec extends reasoning preservation with one optional semantic-surrogate lane and reuses generic auxiliary infrastructure produced by later work.

There was no previous dedicated Kiro implementation spec for issue #369, so there is no #369 spec to supersede.

## Workflow Audit

Completed steps:

1. initialized a new isolated Kiro spec;
2. generated EARS-style requirements from issue #369 plus current architecture;
3. performed brownfield requirements/current-state gap analysis;
4. recorded architectural research and dependency decisions;
5. generated the initial design;
6. performed brownfield design validation;
7. returned Round-1 NO-GO for unnecessary ABI/routing breadth;
8. corrected requirements/research/design;
9. performed Round-2 design validation and reached GO;
10. generated TDD-first dependency-ordered implementation tasks;
11. performed this final traceability/scope review.

## Key Brownfield Corrections

The design-review loop materially simplified the initial approach:

### Removed new backend semantic-permission ABI

V1 does not add a new `SemanticTextDialects` field through backend/plugin replay capabilities and does not add source ReplaySupport to final StreamMeta. A conservative canonical artifact/dialect classifier is sufficient for the initial plain-text positive class; existing destination `ReasoningReplaySupport` still proves representability.

This reduces backend/plugin/profile churn and avoids a new compatibility surface before evidence requires it.

### Removed primary-route inheritance

V1 requires an explicit independently configured compressor route. It does not widen final stream metadata with the original route selector or reconstruct routing from selected backend/model identity.

### Strengthened optional-state safety

Both pending references and surrogate text have separate optional budgets. Optional compression state cannot force eviction of an otherwise-retained authoritative original merely to fit an optimization.

### Resolved async result adoption explicitly

Current `BackgroundClient` lacks a non-blocking result state API. The spec introduces one small generic Poll/equivalent operation rather than using zero-timeout Await, blocking response/replay, callbacks, or another feature-owned worker.

## Core Invariants Audit

### Exact/native continuity

PASS.

- exact OpenAI Responses items never compress;
- signed/redacted/opaque Anthropic reasoning never compresses;
- direct Codex encrypted/native checkpoint state is outside semantic compression;
- readable text cannot override exact/signature/opaque authority;
- unknown/malformed/mixed uncertainty fails closed;
- original artifact remains retained after successful compression.

### Surfaced-winner lifecycle

PASS.

Compression submission occurs only after the existing original TurnStore append succeeds for `OutcomeSuccessReleased`. Parallel losers, swallowed retries/failovers, cancellation, close, response replacement and completion-gate replacement cannot submit compressor work.

### Storage authority

PASS.

`reasoning-output-preservation` remains sole owner of the artifact store and historical reinjection. No second transcript/reasoning database is introduced.

### Auxiliary architecture

PASS.

Compressor inference uses generic `pkg/lipsdk/auxiliary` and ordinary Executor/routing/B2BUA/billing. No provider client, second scheduler, callback worker, second ledger or `compactioncontinuity` feature dependency is introduced.

### Latency behavior

PASS.

Observer Finish never waits for compression. V1 AttemptTransform only performs non-blocking Poll; a pending result means immediate original replay. No preservation barrier is introduced.

### Billing

PASS.

Additional inference is attributable to the originating principal, uses ordinary admission/metering/settlement, gets a separate auxiliary workload role, remains excluded from primary protocol-visible usage, and is still accountable when incurred work produces invalid/stale/unused output.

### Privacy/security

PASS.

Only eligible reasoning text reaches the compressor. Transcript, ordinary assistant output, tools/results, files/media, signatures, opaque/native data, session/account identifiers and credentials are excluded. Model output is strict bounded indexed JSON; telemetry is content-free.

### Scalability

PASS.

The design uses canonical semantic/exact fixtures plus existing replay support and routing lifecycle tests. It explicitly rejects provider-by-provider Cartesian compatibility matrices.

## Implementation Order Audit

The task plan has **32 tasks across 7 phases**, with no phase exceeding five tasks:

1. 5 RED safety/ownership contract tasks;
2. 5 minimal foundation tasks;
3. 4 isolated compressor-domain tasks;
4. 4 original-first shadow-submission tasks;
5. 4 non-blocking shadow-adoption tasks;
6. 5 active destination-gated replay tasks;
7. 5 certification/closeout tasks.

The dependency chain prevents unsafe early activation:

```text
RED exact + disabled contracts
-> classifier/config/Poll/store foundations
-> compressor domain
-> original-first shadow submission
-> shadow non-blocking adoption
-> active replay
-> release certification
```

Backend-visible semantic substitution cannot be implemented before Phase 6.

## Requirements Traceability Audit

All 13 requirement groups are represented in the design traceability table and implementation tasks.

High-risk requirements receive multiple independent evidence lanes:

- R1/R2 exact and semantic classification: Phase 1, 2, 6, 7;
- R4 surfaced-winner/original-first: Phase 1 and 4 plus E2E;
- R5 store safety: Phase 1, 2, 4, 5;
- R6/R7 auxiliary and billing: Phase 1, 3, 4, 7;
- R9 async non-blocking adoption: Phase 1, 2, 5;
- R10 active replay: Phase 5 shadow proof then Phase 6 active proof;
- R11 shadow evidence: Phase 3, 5, 7;
- R12 lifecycle/security architecture: every integration/certification phase;
- R13 release gates: Phase 7 plus focused gates throughout.

No implementation task is intentionally orphaned from requirements, and no requirement relies only on documentation/manual review.

## Revalidation Triggers Before Implementation

Implementation agents must re-read current `main` before coding because active request/terminal ownership simplification specs may merge first. Revalidation is mandatory if any of these move:

- final surfaced stream observer ownership/order;
- AttemptTransform ownership/order;
- feature/runtime composition;
- auxiliary BackgroundClient/scheduler lifecycle;
- generation retirement/pinning semantics.

The implementation must adapt to moved owners rather than recreating stale seams. Semantic ordering from this SDD remains authoritative.

## Explicit Non-Goals Preserved

The spec does not plan:

- durable/distributed compression state;
- a second transcript database;
- native Codex compaction replacement;
- semantic compression of exact/signed/opaque reasoning;
- primary-route inheritance in v1;
- a new backend semantic capability ABI in v1;
- synchronous compressor waits;
- completion callbacks or feature maintenance workers;
- provider Cartesian conformance matrices;
- claims that lossy semantic compression is mathematically equivalent to original reasoning.

## Final Scope Assessment

The expected implementation is substantially narrower than issue #369 would have been before the reasoning-preservation and compaction-continuity work landed. Most difficult infrastructure already exists. The new generic work is intentionally limited to non-blocking background result inspection; remaining behavior stays inside the existing reasoning-preservation owner.

**Final spec decision: GO for maintainer review / approval, as a follow-up SDD.**
