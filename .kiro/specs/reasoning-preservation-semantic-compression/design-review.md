# Brownfield Design Review

## Review Scope

Reviewed `requirements.md`, `gap-analysis.md`, `research.md`, and `design.md` against current `main`, focusing on minimum-change architecture, exact/native continuity safety, reasoning-preservation ownership, auxiliary/background lifecycle, billing/authority, reload/shutdown, and scalability across a rapidly growing backend set.

## Review Round 1

**Verdict: NO-GO pending correction.**

The overall architecture was sound, but the initial design widened brownfield surfaces more than necessary.

### Blocker 1: Replay semantic permission was over-designed as new candidate ABI metadata

The first design proposed extending `lipapi.ReasoningReplaySupport` with semantic-text permission and carrying it through final `StreamMeta`.

That would be safe but unnecessarily broad for v1. Current canonical reasoning artifacts already carry dialect and structural presence semantics. The narrow initial positive case—plain historical reasoning text—can be classified conservatively without provider/model string checks.

**Correction required:** use one canonical artifact/dialect semantic classifier. Plain `openai.chat.reasoning_text.v1` with ordinary text may be semantic; OpenAI Responses exact items, Anthropic signed/redacted/opaque reasoning, native/unknown/malformed structures fail closed. Destination still requires existing `AttemptMeta.ReplaySupport` to represent the original dialect. Add a new backend semantic ABI only if real implementation evidence proves this insufficient.

### Blocker 2: Compressor route inheritance was unnecessary and underspecified

The first design copied compaction-continuity's route/inherit model, but final stream metadata does not contain the original route selector. Reconstructing it from backend/model would invent routing semantics, while widening StreamMeta solely to support inheritance adds unnecessary coupling.

**Correction required:** v1 requires an explicit independent `compression.route`. Inherit-primary-route is out of scope.

### Blocker 3: Optional pending state needed the same memory-safety rule as surrogates

A surrogate was prohibited from evicting an authoritative original, but pending references could also accumulate.

**Correction required:** separately bound pending count and surrogate bytes. Optional-state admission/rejection must never evict an otherwise-retained original. If pending attachment fails after a job was submitted, forget the retained result when possible while preserving incurred billing truth.

### Approved in Round 1

- original artifact commits before any compressor submission;
- no compressor work for non-`success_released` attempts/losers;
- generic BackgroundAux reuse rather than another provider/worker subsystem;
- one additive non-blocking BackgroundClient poll operation;
- no completion callbacks or feature-owned polling goroutine;
- detached child billing/admission under originating principal;
- one call per artifact with locally indexed semantic-text segments;
- shadow mode before active substitution;
- exact/native regression and privacy gates;
- no provider Cartesian matrix.

## Correction Loop Applied

The requirements, research, and design were updated to:

1. remove route inheritance and require explicit `compression.route`;
2. replace new backend semantic-permission metadata with a conservative canonical artifact/dialect classifier;
3. retain existing destination `ReasoningReplaySupport` only for representability;
4. strengthen independent pending/surrogate optional budgets;
5. explicitly distinguish compression failures from authoritative reasoning `on_state_error` policy;
6. preserve the original-first -> shadow submission -> non-blocking adoption -> active replay implementation order.

## Review Round 2

**Verdict: GO for task generation.**

### Ownership

PASS. `reasoning-output-preservation` remains sole owner of capture/store/matching/reinjection. The auxiliary scheduler stays process-owned. No second transcript, reasoning store, provider client, ledger, or generic worker is introduced.

### Exact/native continuity

PASS. Exact/native/signed/opaque structures are fail-closed by canonical artifact profile and never leave the feature for compression. The original artifact remains retained even after successful semantic compression. Codex native compaction and exact Responses continuity remain separate authorities.

### Minimum-change architecture

PASS. The corrected design removes both proposed ABI expansions that were not needed: no new semantic backend capability field and no source ReplaySupport/route selector added to final StreamMeta. The only generic SDK addition is a non-blocking background result inspection method.

### Async lifecycle

PASS. The design avoids an auxiliary completion callback, a feature-owned maintenance goroutine, and response/replay waits. Pending results are adopted opportunistically by the current AttemptTransform. Stale/evicted artifacts fail open to original state.

### Storage safety

PASS. Authoritative original limits retain existing semantics. Pending/surrogate state has separate optional bounds and cannot trigger authoritative eviction merely to fit an optimization. CAS-style correlation prevents late/cross-session/different-policy adoption.

### Billing and authority

PASS. Child inference uses ordinary auxiliary routing/admission/metering/settlement, originating trusted principal scope, and a bounded workload role. Child IDs never become session/partition authority and are not placed in model-facing input.

### Replay correctness

PASS. Active substitution is a defensive projection of stored originals, preserving `BeforeNonReasoningPart` and all non-reasoning/exact structure. Existing destination `ReplaySupport` still governs whether the original dialect is representable. Shadow mode is backend-visible original-only.

### Scalability

PASS. Canonical semantic/exact fixtures and existing protocol/routing lifecycle tests replace provider-pair matrices. Backend growth does not create Cartesian conformance work.

### Security/privacy

PASS. Compressor input is only eligible reasoning text, with exact/signed/opaque/tool/file/transcript material excluded. Fixed untrusted-data prompting, no-tools child, strict indexed schema, hard bounds, fuzzing and content-free telemetry are specified.

### Interaction with active refactors

PASS with revalidation trigger. Active request/terminal pipeline simplification work may move functions/packages. Implementation must re-read current `main` and preserve the semantic owners/order rather than mechanically following stale helper names.

## Final Design Gate

**GO.** Task generation may proceed.

The task plan must maintain this hard dependency sequence:

```text
RED exact/disabled contracts
-> canonical classifier + config + generic Poll
-> non-destructive bounded store state
-> compressor domain
-> original-first shadow submission
-> non-blocking shadow adoption
-> destination-gated active replay
-> full certification
```

No task may enable backend-visible semantic substitution before shadow-safe result adoption and exact/native non-regression evidence exist.
