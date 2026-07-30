# Research Notes

## Research Objective

Determine whether the direct `openai-codex` backend can use native encrypted compaction items as a safe connector-private performance optimization, and identify the exact brownfield seams required for an implementation-ready design.

## Upstream Codex Findings

### Current compaction mechanisms

The upstream Codex CLI contains three compaction paths:

1. **Local prompt-based handoff summary** for providers without remote support.
2. **Legacy remote `POST /responses/compact`** returning replacement history.
3. **Responses Compaction V2**, currently the default for eligible OpenAI/Azure providers.

V2 appends a typed compaction trigger to a normal Responses request and expects exactly one `type: "compaction"` output item. The output carries `encrypted_content`. The CLI stores and replays the item; it has no client-side decryptor.

### Opaque-state behavior

- `encrypted_content` is a provider-owned state capsule, not ordinary prompt text.
- The client only validates, persists, and re-sends it.
- The upstream backend interprets it.
- Open-source client code does not expose the cryptographic format or keys.
- A replay item is therefore useful without being human-readable, but it must remain bound to the compatible backend lineage.

### Replacement history shape

Current V2 behavior retains recent `user`, `developer`, and `system` message context under a 64,000-token text budget and appends the compaction item. Assistant messages, tool traffic, and prior reasoning are represented by the opaque compaction state rather than retained verbatim.

For this connector, system content is already moved into the `instructions` field. The connector replacement window therefore retains recent user-context items and keeps instructions unchanged.

### Trigger metadata

Upstream model metadata includes:

- `context_window`;
- `max_context_window`;
- optional `auto_compact_token_limit`;
- optional opaque `comp_hash`.

When no explicit auto-compaction limit exists, upstream Codex derives a limit near 90% of the resolved context window.

## Current Proxy Connector Findings

### Request construction

The direct connector already sends:

- stable conversation/session headers;
- a per-conversation `prompt_cache_key`;
- optional `previous_response_id`;
- reasoning effort and summary controls;
- `include: ["reasoning.encrypted_content"]` when reasoning is active.

This provides most of the static request contract needed by native compaction.

### WebSocket continuation

The connector's continuation store already implements:

- keying by session, model, account, prompt-cache key, and client family;
- input and output item fingerprints;
- static instructions/tools fingerprint checks;
- TTL/LRU bounds;
- in-flight reservation;
- rollback on stale or missing previous response;
- delta-only request slicing.

This is the closest existing pattern for native checkpoint storage. The new store should remain separate because a checkpoint replaces history and resets response-chain authority, while the continuation store references one existing response chain.

### Missing output fidelity

The connector requests encrypted reasoning content but currently records only completed function-call output items for continuation. Completed `reasoning`, assistant-message, and `compaction` items are ignored by its output-item handler.

The root OpenAI Responses backend already supports exact canonical reasoning replay using `ReasoningPart` dialect `openai.responses.reasoning_item.v1`. The direct Codex connector can consume and emit that existing contract without adding a new canonical type.

### Catalog limitations

The connector's catalog parser currently retains context windows but not `auto_compact_token_limit` or `comp_hash`. These fields are connector-private model metadata and can be added without changing the root model catalog.

### Configuration boundary

The independent connector service decodes YAML into a service config and maps it separately into direct HTTP and app-server configs. A nested `native_compaction` block can be accepted only by the direct HTTP kind; any enabled block for app-server should fail validation rather than silently do nothing.

## Design Decisions

### D1. Connector-private ownership

Native compaction remains inside `connectors/codex`. No canonical compaction item or operation is added in this spec.

**Reason:** one provider currently supplies and understands the opaque item; the active OpenResponses specification already owns future portable/canonical compaction.

### D2. V2 only initially

The first implementation uses `compaction_trigger` over the normal Responses endpoint. The legacy `/responses/compact` endpoint is not implemented.

**Reason:** V2 is the current upstream default and avoids maintaining two protocol paths before live evidence exists.

### D3. Pre-output synchronous execution

Compaction executes inline before the normal response opens. It is not detached or background work.

**Reason:** the request must either install a valid checkpoint or safely fall back before output commitment. Detached work would introduce ownership, cancellation, stale-history, and shutdown complexity without evidence of benefit.

### D4. Per-account planning

Checkpoint lookup and creation happen after the actual managed OAuth account and effective model are selected.

**Reason:** the opaque item may be account-bound, and account rotation must never inherit another account's state.

### D5. Prefix/live-tail split

The compactable prefix ends immediately before the latest user message. That user message and all later function calls/results remain in the live tail.

**Reason:** this mirrors upstream pre-turn compaction and prevents duplication or summarization of the currently active instruction/tool state.

### D6. New response chain

A new checkpoint invalidates the old continuation entry and the first post-checkpoint request omits `previous_response_id`.

**Reason:** the replacement window is a new chain; combining it with an older response ID can cause invalid or semantically mixed state.

### D7. Fail-open under the hard limit

A failed experimental compaction falls back once to full history if it still fits. Failure is hard only when the original request cannot fit or the caller is cancelled.

**Reason:** enabling an optimization should not reduce availability for valid requests.

### D8. Ciphertext-safe accounting and diagnostics

Opaque payloads are never logged. Compaction token usage is accounted separately and included in break-even measurement.

**Reason:** encrypted state can still contain sensitive derived context, and hidden internal work consumes subscription/API usage.

## Proposed Configuration Shape

```yaml
plugins:
  backends:
    - id: codex
      kind: openai-codex
      enabled: true
      config:
        native_compaction:
          enabled: false
          trigger_tokens: 0
          retained_message_tokens: 64000
          state_ttl_seconds: 3600
          max_entries: 1024
          failure_cooldown_seconds: 300
```

Semantics:

- `enabled`: explicit feature flag; default `false`.
- `trigger_tokens`: `0` selects model catalog limit, then derived limit.
- `retained_message_tokens`: local retained user-context budget.
- `state_ttl_seconds`: checkpoint TTL; zero uses default, not unlimited.
- `max_entries`: process-local LRU bound; zero uses default.
- `failure_cooldown_seconds`: negative-cache duration after protocol/compatibility failure.

Hard implementation caps remain code-owned so hostile configuration cannot remove memory or payload limits.

## Candidate Internal Contracts

```go
type NativeCompactionPlanner interface {
    Plan(input PlanInput) PlanResult
}

type NativeCompactionClient interface {
    Compact(ctx context.Context, request CompactRequest) (CompactResult, error)
}

type NativeCheckpointStore interface {
    Reserve(key CheckpointKey) (Reservation, bool)
    Get(key CheckpointKey) (Checkpoint, bool)
    Commit(reservation Reservation, checkpoint Checkpoint) error
    Abort(reservation Reservation)
    Invalidate(key CheckpointKey)
    Close() error
}
```

These interfaces are consumer-owned and connector-private. Implementations remain concrete where substitution is unnecessary.

## Token Estimation Notes

- Before first compaction, existing message/tool/file estimation can measure the compactable prefix.
- Encrypted ciphertext byte length must not be treated as model token cost.
- The compaction response's reported output tokens provide the best available estimate for the replay-item contribution.
- A checkpoint estimate is therefore: unchanged instructions/tools + retained message tokens + reported compaction output tokens + local estimate of the live suffix.
- Provider-reported input usage from later compacted requests should update diagnostics and benchmark evidence but must not change opaque item contents.

## Failure Classification Notes

| Failure | Checkpoint action | Pending normal turn |
|---|---|---|
| Malformed/multiple/missing compaction item | Reject candidate, cooldown | Full-history fallback if hard limit permits |
| Auth/rate limit under managed OAuth | Reject account candidate | Existing account rotation; no cross-account checkpoint |
| Cancellation/deadline | Abort candidate | Respect caller cancellation |
| Network/server failure before output | Reject candidate, cooldown | Full-history fallback if hard limit permits |
| Full history above hard limit | Reject candidate | Deterministic context/compaction error |
| Failure after normal output commitment | No compaction action | Existing committed-stream behavior; no retry |

## Validation Evidence Required Before Default Enablement

1. Deterministic V2 protocol emulator tests.
2. HTTP and WebSocket normal-turn integration after checkpoint creation.
3. Static and managed OAuth isolation tests.
4. Race tests around concurrent reservations, invalidation, and runtime close.
5. Fuzz tests for raw reasoning/compaction envelopes and stream event order.
6. Environment-gated live compatibility test against current ChatGPT Codex.
7. Long-history before/after request byte and input-token measurements.
8. Break-even analysis including compaction request latency and token cost.
9. Multi-turn task-quality comparison to detect state loss or repeated work.

## Open Questions

1. Does the ChatGPT Codex endpoint require additional turn-metadata headers for V2 compaction beyond the connector's current contract?
2. Is a compaction item reusable across a model slug change when `comp_hash` is equal, or should initial implementation always require exact model equality?
3. How stable is the 64,000 retained-message budget across backend revisions?
4. Should later implementation expose an explicit manual checkpoint request through a connector extension, or remain automatic only?
5. Should a successful checkpoint be persisted after the direct connector gains an approved encrypted state store?

Initial design answers conservatively: exact model equality, fixed default retained budget with config override and hard caps, automatic-only behavior, and memory-only state.
