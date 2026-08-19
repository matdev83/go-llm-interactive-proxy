# Brownfield Design Review

## Review Scope

Reviewed `requirements.md`, `gap-analysis.md`, `research.md`, and the initial `design.md` against current `main`, focusing on:

- minimum-change architecture;
- exact/native continuity safety;
- existing reasoning preservation ownership;
- existing auxiliary/background API;
- feature/runtime composition;
- async lifecycle and generation retirement;
- billing/authority boundaries;
- compatibility with the project's expected large provider/backend count.

## Review Round 1

**Verdict: NO-GO pending correction.**

The overall architecture is sound, but two initial design choices create unnecessary brownfield surface area and one store rule needs to be made more explicit before implementation tasks are generated.

### Blocker 1: Replay semantic permission is over-designed as new candidate ABI metadata

Initial design proposed extending `lipapi.ReasoningReplaySupport` with `SemanticTextDialects` and adding `ReplaySupport` to `response.StreamMeta`.

This is safe in isolation but not minimal. Current canonical reasoning artifacts already carry a dialect and structural fields. V1 has one deliberately narrow positive semantic class: plain textual historical reasoning. Exact/native/signed classes are identifiable conservatively from the canonical artifact/dialect contract.

Adding semantic-text capability across candidate metadata, StreamMeta, backend plugin ABI/profile composition and out-of-tree plugin surfaces would enlarge the change before evidence shows provider-specific semantic permission is required.

#### Required correction

Use one canonical **artifact/dialect semantic profile** in v1:

```text
plain openai.chat.reasoning_text.v1 with ordinary text only -> semantic-text candidate
OpenAI Responses exact item -> exact
Anthropic signed thinking -> exact
Anthropic redacted/opaque -> exact
unknown/mixed/native -> exact/unknown, never semantic
```

Destination replay still uses existing `AttemptMeta.ReplaySupport` to prove that the candidate can legally represent the artifact's dialect. Active substitution requires both:

1. artifact profile = semantic text; and
2. existing destination replay support represents that dialect.

No provider-name matching is introduced. This satisfies capability/profile-driven behavior through the canonical artifact profile while avoiding a new backend ABI field.

If implementation discovers a real provider that uses the same canonical text dialect but requires exact bytes, stop and revise the profile contract before active rollout; do not add provider string exceptions.

### Blocker 2: `extractor.inherit`/compressor route inheritance is unnecessary and underspecified at the final-stream seam

Initial design copied the compaction-continuity route/inherit model. However `response.StreamMeta` carries selected backend/model identity, not the original route selector. Reconstructing an inherited route from backend/model would create new routing semantics, while adding the original selector to StreamMeta would widen another SDK contract solely for convenience.

Issue #369 explicitly permits independently configuring the compressor route/model. An explicit route is safer and simpler.

#### Required correction

V1 compressor configuration requires an explicit `compression.route`. Remove route inheritance from requirements/design/tasks.

A future inherit-primary-route mode can be separately added if a real operator need justifies the extra correlation semantics.

### Blocker 3: Optional compression budget behavior must be fail-safe at pending attachment as well as surrogate attachment

Initial design states that a surrogate cannot evict an authoritative original, but pending references also consume bounded state and can accumulate when a session produces many eligible turns.

#### Required correction

Define both pending and surrogate optional budgets such that:

- optional state is admitted/rejected independently from authoritative FIFO bytes;
- pending-budget exhaustion causes `AttachPendingCompression` to reject and the job result to be forgotten when possible;
- no optional-state admission path triggers authoritative original eviction;
- original eviction/expiry clears all optional state;
- optional state byte/count accounting is defensive and included in store tests.

## Non-Blocking Poll Review

**Approved.** Adding one generic `BackgroundClient.Poll` is justified by the current API gap. It is preferable to:

- waiting during response release;
- zero-duration `Await` timing tricks;
- callbacks into retired feature state;
- another feature-owned worker/poller.

The API must remain small, non-blocking, defensive-copying, and feature-neutral.

## Original-First Ordering Review

**Approved.** The only production submission point is after the existing `TurnStore.Append` succeeds for `OutcomeSuccessReleased`. This preserves surfaced-winner ownership and ensures auxiliary compression cannot become preservation authority.

## Result Adoption Review

**Approved with one clarification.** Poll/adopt must be an optional fail-open sub-step inside the existing AttemptTransform. Compression-specific errors must not be mapped to the feature's existing authoritative `on_state_error=reject` path. Only original TurnStore/matching errors retain existing fail-closed semantics.

## Billing/Session Review

**Approved.** Detached child inference uses ordinary auxiliary routing/admission/billing and originating trusted principal scope. No child identity may become session/partition authority, and no parent IDs enter model-facing input.

## Scalability Review

**Approved.** The design uses artifact/dialect profiles and existing replay support rather than provider-pair matrices. Backend growth does not require Cartesian conformance checks.

## Required Correction Loop

Before tasks are generated:

1. update requirements to require an explicit compressor route in v1 and remove route inheritance language;
2. update research to record canonical artifact/dialect profile as the minimal semantic authority;
3. update design to remove `SemanticTextDialects`/StreamMeta ReplaySupport additions and route inheritance;
4. strengthen optional pending/surrogate budget behavior;
5. re-review corrected design for traceability and implementation order.

## Round 1 Summary

The feature remains a **follow-up** and does not supersede completed specs. No architectural rewrite is needed. Corrections reduce the expected change set and preserve the desired implementation order.
