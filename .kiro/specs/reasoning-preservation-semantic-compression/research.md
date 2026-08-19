# Research and Architecture Decisions

## Purpose

This document records the brownfield research behind issue #369 and the decisions that prevent the feature from duplicating existing infrastructure or weakening exact reasoning continuity.

## Current-State Inventory

### Reasoning artifact capture and replay

`internal/plugins/features/reasoningpreservation` currently owns:

- config/catalog eligibility;
- final canonical stream observation;
- authoritative-session partitioning;
- bounded in-memory turn artifacts;
- exact artifact matching/classification;
- per-candidate AttemptTransform restoration;
- privacy-safe telemetry.

The current artifact is intentionally simple:

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

`PlacedReasoning.BeforeNonReasoningPart` is an important structural contract. A future surrogate must preserve placement rather than replacing an entire assistant message with free-form summary text.

The current `TurnStore` only supports `Append`, `Snapshot`, and `Delete`. It clones/clears reasoning defensively and applies FIFO/TTL/session-byte bounds to authoritative artifacts.

### Final-stream observer semantics

`pkg/lipsdk/response.StreamObserver` has exactly-once terminal outcomes. `OutcomeSuccessReleased` means the runtime released a successful terminal response toward the frontend encoder. Reasoning preservation already commits artifacts only on that outcome.

This is the correct compression submission boundary because it excludes:

- parallel losers;
- swallowed failover attempts;
- cancellation/close;
- pre-output replacement;
- completion-gate replacement.

`response.Services` is currently deliberately empty. Final-stream observation is described as read-only and does not currently receive Aux/State services.

### Exact/native continuity

The repository now has several exact-state forms:

- OpenAI Responses exact reasoning items;
- direct Codex encrypted reasoning continuity;
- Codex native `/responses/compact` checkpoint replacement state;
- Anthropic signed thinking;
- Anthropic redacted/opaque thinking.

These are not interchangeable with textual summaries. The design must make exactness an explicit semantic property rather than inferring compressibility from which fields happen to contain text.

### Generic auxiliary/background infrastructure

`pkg/lipsdk/auxiliary.BackgroundClient` currently exposes:

```go
SubmitCollect(...)
Await(...)
Forget(...)
```

`internal/core/auxreq.BackgroundScheduler` supplies the implementation. It already owns bounded workers, bounded queue, coalescing, job timeout, bounded results/TTL, generation pinning, shutdown, and captured principal scope.

The compaction-continuity feature demonstrates the correct child-call pattern:

- detached/private session mode;
- no tools;
- independent route or explicit inheritance;
- plugin self-disable;
- fixed system policy;
- bounded untrusted input;
- normal billing/admission/routing path;
- explicit validation of collected output.

That package's domain-specific extractor must not be imported into reasoning preservation. It is precedent, not shared semantic code.

## Decision 1: This Is a Follow-Up, Not a Superseding Spec

**Decision:** create `reasoning-preservation-semantic-compression` as a new active follow-up specification.

**Rationale:** the completed specs accurately describe behavior that remains required. Reopening or superseding them would blur historical ownership and imply that exact/native continuity is being replaced. There is no prior dedicated #369 Kiro implementation spec to supersede.

**Consequences:**

- old archived specs remain authoritative for their completed behavior;
- this spec cites them as prerequisites/constraints;
- implementation should modify existing packages where ownership already exists rather than create parallel feature owners.

## Decision 2: Original Artifact Is the Source of Truth

**Decision:** compression never replaces `TurnArtifact.Reasoning`. It attaches optional state to the same artifact.

Conceptual state:

```text
TurnArtifact
  ID / Anchor / Source / CreatedAt
  OriginalReasoning[]       authoritative
  OriginalReasoningBytes
  Compression?              optional
      pending job ref
      immutable source/policy digest
      validated surrogate
      surrogate size/savings metadata
```

**Rejected alternative: store only the compressed text.** This would make compressor failure/semantic drift a correctness dependency and destroy the ability to replay exactly when a future destination requires it.

**Rejected alternative: keep original only until compression succeeds, then delete it.** Destination semantics can differ from source semantics; later exact replay may still be required.

## Decision 3: Two Separate Eligibility Gates

**Source gate:** after original commit, determine whether a subset of the artifact may be sent to semantic compression.

**Destination gate:** during later AttemptTransform, determine whether the selected candidate may receive the surrogate.

Both use one typed semantic classification authority.

This prevents a source such as plain compatible text from becoming a surrogate that is incorrectly replayed into a destination requiring exact/native history.

## Decision 4: Explicit Semantic Classification, Unknown Fails Closed

A design-equivalent type should distinguish at least:

```text
unknown
exact-replay-required
semantic-text-replay-permitted
not-applicable/not-persisted
```

Exact naming/package location may change during implementation, but there must be one authority.

### Initial positive scope

The safest first active candidate is plain historical textual reasoning represented by `openai.chat.reasoning_text.v1`, and only when profile/capability policy explicitly marks semantic replay safe.

### Explicit negative scope

- `openai.responses.reasoning_item.v1` exact items;
- signed Anthropic thinking;
- redacted/opaque thinking;
- native compaction/checkpoints;
- unknown future dialects;
- mixed structures where the safe textual subset cannot be isolated unambiguously.

## Decision 5: Compression Configuration Belongs to Reasoning Preservation

Add a nested `compression` section to the existing feature because the feature already owns the artifact store and restoration.

Proposed operator shape (illustrative, not wire-frozen until implementation):

```yaml
plugins:
  features:
    - id: reasoning-output-preservation
      enabled: true
      config:
        action: restore
        use_builtin_catalog: true
        # existing policies/state...
        compression:
          enabled: true
          mode: shadow          # shadow | active
          route: "openai-responses:small-model"
          # inherit: true       # mutually exclusive with route
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

Exact defaults must be conservative. `enabled` defaults false. Standard injection must not turn compression on.

## Decision 6: Use Generic Background Aux; Do Not Widen Observer Services

Compression-enabled standard/runtime composition should bind a narrow generation-local port into the reasoning feature, for example:

```go
type CompressionAux interface {
    SubmitCollect(...)
    Poll(...)
    Forget(...)
}
```

The concrete capability may simply be `auxiliary.BackgroundClient` after an additive Poll method.

The observer and AttemptTransform already share feature-instance state via `InstanceParts`; constructor injection is sufficient.

**Rejected alternative: add Aux to `response.Services`.** This broadens a generic read-only observer contract for one optimization and makes disabled composition unnecessarily aware of auxiliary execution.

**Revalidation:** if concurrent pipeline simplification work changes feature construction ownership, preserve the semantic rule: compression-enabled feature gets a narrow generation-bound aux capability; disabled feature does not require it.

## Decision 7: Add Generic Non-Blocking Background Polling

Current `Await` cannot be safely used as a poll without timing tricks.

The preferred generic addition is an explicit non-blocking result inspection API. Illustrative shape:

```go
type BackgroundState uint8

const (
    BackgroundUnknown BackgroundState = iota
    BackgroundPending
    BackgroundCompleted
    BackgroundFailed
    BackgroundNotFound
)

type BackgroundResult struct {
    State     BackgroundState
    Collected lipapi.Collected
    Err       error
}

Poll(id JobID) BackgroundResult
```

Design properties:

- synchronous and non-blocking;
- no context wait;
- defensive copy of completed data;
- terminal errors exposed without string parsing;
- no removal as a side effect; consumer calls `Forget` explicitly;
- disabled/closed implementation has deterministic terminal behavior;
- does not expose scheduler internals, queue pointers, or feature-specific correlation.

**Rejected alternative: zero/1ms `Await` timeout.** It is race-prone and encodes timing rather than state.

**Rejected alternative: completion callback.** It creates asynchronous mutation of feature/store state after generation retirement and needs another lifecycle owner.

**Rejected alternative: feature-owned polling goroutine.** It duplicates scheduler/workflow infrastructure and adds idle process work solely for an optimization.

## Decision 8: Store Pending Job State and Adopt on a Later Replay

Sequence:

```text
successful surfaced response
    -> append original artifact
    -> source semantic gate
    -> build compressor request
    -> SubmitCollect
    -> CAS attach pending job reference
    -> Finish returns

later matching request
    -> AttemptTransform finds artifact
    -> Poll pending job once
       pending -> original replay now
       failed  -> clear/forget; original replay
       complete-> validate result + CAS attach surrogate + forget
    -> shadow/active destination gate
    -> select original or surrogate
```

This means the first follow-up may still replay the original if compression is not ready. That is acceptable in v1 because it avoids introducing hidden latency or a synchronization barrier.

If no later turn occurs, the generic scheduler result expires under existing bounds. The feature need not create cleanup work merely to harvest metrics.

## Decision 9: Pending/Surrogate Store Operations Are CAS-Like

The existing TurnStore needs additive atomic operations. Exact API design may use a mutation command or explicit methods, but required preconditions include:

- authoritative session partition;
- artifact ID;
- original anchor or immutable source digest;
- expected pending state/job ID for final adoption;
- optional expected semantic policy/profile hash.

Outcomes should be typed: attached, stale/not found, budget rejected, already attached/conflict.

This prevents:

- late result attaching after artifact reuse/replacement;
- concurrent attempts double-adopting;
- old job overwriting a newer compression policy;
- cross-session result adoption.

## Decision 10: Optional Compression Memory Cannot Sacrifice the Original

Current store's `MaxSessionBytes` can evict old original turns as new original artifacts arrive. That behavior remains authoritative.

Optional compression state must have separate limits or attachment rules such that adding a surrogate/pending record never causes an otherwise-retained original to be evicted immediately just to fit the optimization.

Preferred policy:

- original limits remain current `state.*` limits;
- `compression.max_pending_per_session` limits pending references;
- `compression.max_surrogate_bytes_per_session` and per-turn max limit optional surrogate bytes;
- optional state budget exhaustion rejects/drops optional state, leaving original unchanged;
- optional state is cleared whenever original is deleted/expired/evicted.

This makes memory behavior monotonic in safety: compression can fail to optimize, but cannot reduce exact retention.

## Decision 11: Compressor Input Is the Eligible Text, Not Conversation History

The compressor receives:

- fixed instructions;
- the eligible textual reasoning segment(s), preserving segment boundaries if needed;
- minimal non-sensitive structural identifiers only when validation requires them, preferably hashes or local sequence indexes.

It does not receive:

- user transcript;
- ordinary assistant answer text;
- tool calls/outputs;
- files/images;
- opaque reasoning/signatures;
- native compaction data;
- session/account IDs;
- API keys or provider metadata.

This is both cheaper and safer than reusing the broader compaction-continuity source preparation pipeline.

## Decision 12: One Textual Surrogate Can Represent the Compressible Subset, But Placements Stay Structural

A turn can contain multiple reasoning placements. The compressor input should preserve segment boundaries, and result schema should let validation map output back to those placements.

Two viable implementations:

1. compress each eligible placed reasoning segment independently;
2. compress all eligible segments in one auxiliary call and require a bounded structured array keyed by local segment index.

The design selects **one call per artifact** with a tiny structured result containing one surrogate text per eligible local segment. This avoids N auxiliary calls for one turn while preserving placement exactly.

Illustrative output:

```json
{
  "schema_version": 1,
  "segments": [
    {"index": 0, "text": "..."},
    {"index": 2, "text": "..."}
  ]
}
```

The schema contains no provider/session/tool identifiers. The validator requires exactly the expected eligible indexes, no duplicates/unknown indexes, bounded UTF-8 text, and aggregate savings thresholds.

A single-segment artifact still uses the same schema for consistency/fuzzability.

## Decision 13: Active Replay Is Lossy by Definition, So Policy Must Be Explicit

No LLM summarizer can prove exact preservation of hidden reasoning semantics. Therefore:

- `shadow` is the safe initial production mode;
- `active` means the operator accepts bounded semantic replay for explicitly allowed artifact/candidate profiles;
- docs/telemetry must not call the surrogate "equivalent" or claim model-quality improvement without evaluation evidence;
- exact/native continuity always remains available as the retained original fallback.

## Decision 14: Content-Free Observability Reuses Existing Privacy Posture

Feature-level observations can include:

- categorical outcome;
- original eligible bytes/token estimate;
- compressed bytes/token estimate;
- savings bytes/token estimate;
- ratio bucket;
- compressor latency;
- pending/completed/stale counts;
- active-use counts.

Economic truth (provider usage/cost, admission/settlement) remains in ordinary auxiliary/billing systems.

Never log raw reasoning, surrogate, child prompt, opaque content, signatures, raw anchor/digest, or session partition key.

## Decision 15: Workload Identity and Recursion Prevention

LLM compressor child:

```text
class: auxiliary
role:  reasoning_preservation_compressor
visibility: private
session mode: detached
disable plugins: reasoning-output-preservation
```

Use parent trace/A-leg/B-leg correlation only through trusted auxiliary metadata/context. Do not put lineage IDs in the model-facing text.

## Decision 16: Coalescing Key Is Content-Free and Artifact-Specific

A deterministic coalescing key should be derived from content-free local identifiers/digests such as:

```text
reasoning-compress:v1:<artifact-id>:<source-digest>:<policy-digest>
```

If artifact IDs are considered sensitive in diagnostics, the scheduler key can contain a hash. It must identify one committed original artifact and compression policy revision, so duplicate submit paths coalesce rather than double-bill.

## Decision 17: Failure Matrix

| Failure | Effect on primary/original | Optional-state action |
|---|---|---|
| source exact/ineligible | unchanged | no job |
| below minimum source size | unchanged | no job |
| aux unavailable | unchanged | no pending |
| admission denied | unchanged | no provider work/pending or typed failed outcome |
| queue full | unchanged | no pending |
| provider timeout/failure | unchanged | pending becomes terminal on later Poll; forget |
| malformed/tool result | unchanged | reject; clear pending; forget |
| insufficient savings | unchanged | reject; clear pending; forget |
| artifact expired/evicted | gone by normal store policy | result cannot attach; forget when encountered |
| optional budget full | original retained | reject surrogate/pending optional state |
| destination exact/unknown | original replay | keep valid surrogate for other semantic destinations until artifact expiry |
| shadow mode | original replay | surrogate may remain for measurement |
| active + valid destination | original retained | surrogate substituted only for eligible segments |

## Decision 18: No New Cartesian Compatibility Matrix

The project expects many backends/providers. Validation should be capability/profile driven and scenario-oriented:

- semantic-text positive conformance fixture;
- exact item negative fixture;
- signed/opaque negative fixture;
- destination semantic vs exact profile cases;
- standard routing lifecycle scenarios (sequential/failover/weighted/parallel).

Do not build provider × provider compression matrices. New backends prove their profile/canonical dialect contracts rather than adding pairwise cases.

## Decision 19: Active Concurrent Refactors Are Revalidation Triggers

The active `turn-recv-terminal-ownership-simplification` and `request-attempt-pipeline-state-simplification` specs may alter function/package structure around the observer/AttemptTransform lifecycle.

Implementation must re-read current `main` before coding and preserve semantic owners:

- final surfaced winner before original commit;
- original commit before compressor submission;
- candidate AttemptTransform owns replay selection;
- background scheduler remains process-owned;
- no retry after downstream output.

The SDD deliberately avoids requiring today's incidental helper names where active refactors may move them.

## Open Questions Resolved for This SDD

### Does #369 supersede reasoning-output-preservation?

No. It extends the artifact stored by that completed feature and its replay selection policy.

### Does #369 supersede Codex native compaction?

No. Native compaction is preferred/authoritative for exact native material and is explicitly excluded from lossy compression.

### Should #369 reuse compaction-continuity's extractor?

No. Reuse `auxiliary` infrastructure and architectural patterns only.

### Should compression happen inline in observer Finish?

No. Original commit is synchronous; compression submission is background; result adoption is later/non-blocking.

### Should a completed compressor result automatically callback into the store?

No in v1. Poll/adopt on a later replay avoids asynchronous state mutation and another lifecycle owner.

### Can active replay delete the original after proven success?

No. Destination semantics can change and exact fallback remains necessary.

## Implementation Readiness

The existing repository provides the required foundations. The design can proceed without a new provider client, transcript store, generic scheduler, compaction detector dependency, or new billing ledger.
