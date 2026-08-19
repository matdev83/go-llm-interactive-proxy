# Research and Architecture Decisions

## Purpose

This document records the brownfield research behind issue #369 and the decisions that prevent the feature from duplicating existing infrastructure or weakening exact reasoning continuity.

## Current-State Inventory

### Reasoning artifact capture and replay

`internal/plugins/features/reasoningpreservation` currently owns config/catalog eligibility, final canonical stream observation, authoritative-session partitioning, bounded in-memory turn artifacts, matching/classification, per-candidate AttemptTransform restoration, and privacy-safe telemetry.

The current artifact retains exact placements:

```go
type TurnArtifact struct {
    ID             string
    Anchor         [32]byte
    SourceBackend  string
    SourceModel    string
    Reasoning      []PlacedReasoning
    CreatedAt      time.Time
    ReasoningBytes int
}
```

`PlacedReasoning.BeforeNonReasoningPart` is a structural contract. Any surrogate must preserve placement rather than replacing an entire assistant message with free-form summary prose.

The current `TurnStore` exposes `Append`, `Snapshot`, and `Delete`; it defensively clones/clears artifacts and applies TTL/FIFO/session-byte bounds to authoritative reasoning.

### Exact/native continuity

The repository already represents exact-state forms including OpenAI Responses reasoning items, direct Codex encrypted reasoning and native compaction checkpoints, Anthropic signed thinking, and Anthropic redacted/opaque thinking. These are not interchangeable with textual summaries.

### Generic auxiliary/background infrastructure

`pkg/lipsdk/auxiliary.BackgroundClient` currently exposes:

```go
SubmitCollect(...)
Await(...)
Forget(...)
```

`internal/core/auxreq.BackgroundScheduler` already owns bounded workers/queue/results, coalescing, job timeout, result TTL/bytes, generation pinning, shutdown and originating scope attribution.

The completed compaction-continuity implementation demonstrates the correct child-call pattern—detached/private, no-tools, independent route, plugin self-disable, bounded untrusted input, normal routing/billing and validated result—but its capsule/extractor semantics are feature-specific and must not be imported by reasoning preservation.

## Decision 1: This Is a Follow-Up, Not a Superseding Spec

The new active spec is `reasoning-preservation-semantic-compression`.

It does **not** supersede the completed reasoning-preservation, OpenAI Responses preservation, Codex native compaction or compaction-continuity specs. Those remain historical authorities. There was no previous dedicated #369 Kiro implementation spec to supersede.

## Decision 2: Original Artifact Is Always the Source of Truth

Compression never replaces `TurnArtifact.Reasoning`. It attaches bounded optional state to the same artifact.

```text
TurnArtifact
  OriginalReasoning[]       authoritative
  Compression?              optional
      pending job ref
      immutable source/policy digest
      validated surrogate segments
      size/savings metadata
```

Rejected alternatives:

- storing only compressed text;
- deleting original after successful compression;
- using compressor success as a prerequisite for preservation.

All three would make a lossy optimization continuity authority and would prevent exact fallback for later destinations.

## Decision 3: V1 Uses a Canonical Artifact/Dialect Semantic Profile

The first design draft proposed a new semantic-permission field in `ReasoningReplaySupport` and a new `ReplaySupport` field in `response.StreamMeta`. Brownfield design review rejected that as broader than necessary.

V1 instead uses one conservative canonical artifact/dialect classifier, conceptually:

```go
type ReplaySemantics uint8

const (
    ReplaySemanticsUnknown ReplaySemantics = iota
    ReplaySemanticsExact
    ReplaySemanticsSemanticText
)

func ClassifyReplaySemantics(part lipapi.Part) ReplaySemantics
```

Initial policy:

- plain `openai.chat.reasoning_text.v1` with ordinary text only -> `SemanticText`;
- OpenAI Responses exact reasoning item -> `Exact`;
- Anthropic signed thinking -> `Exact`;
- Anthropic redacted/opaque thinking -> `Exact`;
- unknown/native/mixed/contradictory structure -> `Unknown` or `Exact`, never semantic.

Exact/native structural properties override readable text.

This is capability/profile driven through canonical dialect/artifact semantics and introduces no provider-name matching in core.

Destination replay still uses the already-existing `AttemptMeta.ReplaySupport` to prove that the selected candidate can represent the original dialect. No new backend plugin ABI field is required in v1.

If implementation finds a real provider that uses the same canonical plain-text dialect while requiring exact-byte replay, implementation must stop and revise the canonical profile contract rather than introduce provider-name exceptions.

## Decision 4: Two Separate Safety Gates

**Source gate:** after original commit, classify each placed reasoning part. Only `SemanticText` segments may leave the feature for compression.

**Destination gate:** during later AttemptTransform, classify the original segment again and require existing `ReasoningReplaySupport` to represent its dialect before active surrogate substitution.

A stored surrogate is never itself proof of destination permission.

## Decision 5: Compression Configuration Belongs to Reasoning Preservation

Add a nested `compression` section because reasoning preservation already owns artifact state and reinjection.

V1 requires an **explicit compressor route**. It does not support inherit-primary-route behavior. This avoids adding primary route selectors to final-stream metadata or reconstructing routing semantics from backend/model identity.

Illustrative operator shape:

```yaml
plugins:
  features:
    - id: reasoning-output-preservation
      enabled: true
      config:
        action: restore
        use_builtin_catalog: true
        compression:
          enabled: true
          mode: shadow
          route: "openai-responses:small-model"
          timeout: 8s
          max_input_tokens: 12000
          max_output_tokens: 1500
          max_input_bytes: 1048576
          max_surrogate_bytes: 131072
          min_source_bytes: 4096
          min_saved_bytes: 1024
          min_savings_ratio: 0.30
          max_pending_per_session: 8
          max_surrogate_bytes_per_session: 524288
```

Exact defaults remain a design/implementation detail, but `enabled` defaults false and mode defaults `shadow`.

## Decision 6: Do Not Widen Final-Stream `response.Services`

`response.Services` is deliberately empty today and final-stream observation is described as read-only. Compression does not justify turning that generic service bag into another cross-cutting dependency surface.

Preferred composition:

```text
runtime/standard composition
  -> generation-bound auxiliary.BackgroundClient
  -> reasoning preservation runtime-aware constructor
  -> shared observer + AttemptTransform + TurnStore
```

Disabled compression uses the existing construction path and does not require BackgroundAux.

## Decision 7: Add One Generic Non-Blocking Background Poll

Using `Await` with zero/1ms deadlines is timing-dependent; waiting in `Finish` or AttemptTransform would add hidden latency; callbacks or a feature-owned polling goroutine would add another lifecycle owner.

Therefore extend `auxiliary.BackgroundClient` with a feature-neutral, non-blocking `Poll`/equivalent state inspection operation.

Required states are pending, completed, failed and not-found/expired. Completed data is defensively copied. Poll has no removal side effect; consumers call `Forget` explicitly.

This is the only new generic infrastructure justified by #369.

## Decision 8: Store Pending Job State and Adopt on a Later Replay

Sequence:

```text
successful surfaced response
  -> append original artifact
  -> semantic source gate + thresholds
  -> build compressor request
  -> SubmitCollect
  -> CAS attach pending ref
  -> Finish returns

later matching request
  -> AttemptTransform snapshots artifacts
  -> Poll pending job once
      pending -> original now
      failed/not found -> clear pending; original
      complete -> validate + CAS surrogate + Forget
  -> shadow/active + destination gate
  -> original or surrogate
```

The first immediate follow-up may still replay original if compression is not ready. V1 deliberately chooses this over any synchronization barrier.

No later turn means the generic scheduler result expires under its existing bounded retention. No new cleanup worker is required.

## Decision 9: Pending and Surrogate Updates Are CAS-Like

Store mutations require authoritative partition plus artifact ID, original anchor/source digest, policy digest and expected job ID where relevant.

Typed outcomes distinguish attached, stale/not-found, budget-rejected and already-present/conflict.

This prevents cross-session adoption, double adoption, stale results after eviction/reload and old-policy results overwriting newer optional state.

## Decision 10: Optional Compression State Has Separate Safety Budgets

Current `state.max_session_bytes` governs authoritative originals. Optional compression must not use that path in a way that evicts originals merely to make room.

V1 adds independent optional limits:

- `max_pending_per_session`;
- `max_surrogate_bytes` per artifact/turn;
- `max_surrogate_bytes_per_session`.

Pending or surrogate budget exhaustion rejects optional state and leaves the original retained. Original expiry/eviction clears all optional state. Pending and surrogate lifetime never exceeds original TTL.

## Decision 11: Compressor Input Is Only Eligible Reasoning Text

The compressor receives fixed instructions plus locally indexed eligible reasoning segments. It does not receive user transcript, ordinary assistant answer text, tools/results, files/media, signatures/opaque reasoning, native compaction data, session/account IDs or provider credentials.

Source text is explicitly delimited as untrusted data.

This is much narrower and cheaper than the compaction-continuity source preparation pipeline.

## Decision 12: One Auxiliary Call per Artifact, Preserving Segment Placement

One artifact may contain multiple semantic-text reasoning placements. V1 sends all eligible local segments in one child call and requires a strict result keyed by local reasoning placement index:

```json
{
  "schema_version": 1,
  "segments": [
    {"index": 0, "text": "..."},
    {"index": 2, "text": "..."}
  ]
}
```

Parser requires exactly expected indexes, no duplicates/unknown indexes and bounded text. Exact/signed/opaque placements are absent from compressor input and remain original.

This avoids one billable call per segment while preserving `BeforeNonReasoningPart` structure exactly.

## Decision 13: Active Replay Is Explicitly Lossy

An LLM compressor cannot mathematically prove identical hidden reasoning semantics. Therefore:

- `shadow` is default when compression is enabled;
- `active` is explicit operator acceptance of bounded semantic replay for canonical semantic-text artifacts;
- original remains available as fallback;
- docs/telemetry do not claim semantic equivalence or quality improvement without separate evaluation evidence.

## Decision 14: Feature-Local Compression Failures Are Fail-Open

The existing AttemptTransform is fail-closed for **authoritative reasoning state** according to `on_state_error`. Compression-specific failures are different: Poll error, invalid output, optional CAS failure, budget rejection or stale job all fall back to original and must not be mapped to `on_state_error=reject`.

Only failure of the original TurnStore/matching path retains existing reasoning-preservation policy.

## Decision 15: Content-Free Observability Reuses Existing Privacy Posture

Feature observations may contain outcome enums, counts, source eligible bytes, surrogate bytes, estimated saved bytes/tokens, latency and mode/profile enums.

Raw reasoning, surrogate, child prompt/result, signatures/opaque data, raw anchor/source digest and session partition are forbidden.

Auxiliary scheduler/billing remain authoritative for queue/provider/token/cost truth rather than duplicating feature-local economic calculations.

## Decision 16: Workload Identity and Recursion Prevention

Child classification:

```text
class: auxiliary
role: reasoning_preservation_compressor
visibility: private
session mode: detached
disable plugin: reasoning-output-preservation
```

Parent trace/A-leg/B-leg and principal correlation stay in trusted auxiliary metadata/context, not model-facing text.

## Decision 17: Coalescing Key Is Content-Free and Artifact-Specific

Use a versioned deterministic key derived from committed artifact/source/policy identity, preferably hashed if direct artifact IDs are not suitable for scheduler diagnostics:

```text
reasoning-compress:v1:<artifact/source/policy digest>
```

Duplicate submission paths coalesce rather than double-bill.

## Decision 18: Failure Matrix

| Failure | Primary/original behavior | Optional-state action |
|---|---|---|
| exact/unknown source | unchanged | no job |
| below source threshold | unchanged | no job |
| aux unavailable / queue full | unchanged | no pending |
| admission denied | unchanged | no provider work |
| provider timeout/failure | unchanged | clear on later Poll; Forget |
| malformed/tool result | unchanged | reject; clear; Forget |
| insufficient savings | unchanged | reject; clear; Forget |
| pending budget full | original retained | Forget result ref when possible |
| surrogate budget full | original retained | reject surrogate; Forget |
| artifact expired/evicted | normal store behavior | cannot attach |
| destination unsupported | original replay | surrogate may remain for other destinations |
| shadow | original replay | surrogate may remain for measurement |
| active + valid destination | original still retained | eligible segments may use surrogate |

## Decision 19: No Provider Cartesian Matrix

Verification uses semantic/exact capability fixtures and routing lifecycle scenarios, not provider-by-provider combinations. New backends prove canonical dialect/replay-support contracts rather than adding pairwise compression cells.

## Decision 20: Active Pipeline Refactors Are Revalidation Triggers

Current active terminal/request-pipeline simplification specs may move function/package structure. Implementation must re-read current `main` before coding and preserve semantic owners:

- final surfaced winner before original commit;
- original commit before compressor submission;
- AttemptTransform owns later reinjection/selection;
- auxiliary scheduler remains process-owned;
- no retry after output.

## Resolved Questions

- **Supersede old specs?** No; follow-up only.
- **Replace Codex native compaction?** No; exact/native path stays authoritative.
- **Reuse compaction-continuity extractor?** No; reuse generic auxiliary infrastructure/pattern only.
- **Inline compression in observer Finish?** No; submit background after original commit.
- **Completion callback into store?** No in v1; poll/adopt on later replay.
- **Delete original after successful compression?** Never.
- **Inherit primary route?** No in v1; require explicit compressor route to minimize SDK/routing changes.
- **New backend semantic-permission ABI?** Not in v1; canonical artifact/dialect profile plus existing destination replay support is sufficient until evidence proves otherwise.

## Implementation Readiness

The existing repository provides the required foundations. The corrected design can proceed without a new provider client, transcript store, generic scheduler, compaction detector dependency, backend semantic-permission ABI expansion, route-inheritance metadata, or new billing ledger.
