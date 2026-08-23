# Conversation View

Canonical A-leg/B-leg visibility for proxy-owned content.

## Both directions

The conversation view is an authoritative **A-leg-scoped** snapshot that determines the model-visible B-leg trajectory.

```
A-leg / client truth          B-leg / model truth
---------------------         -------------------

normal client message  ─────> normal client message
client-visible local   ──X    (removed by projection)
(never_backend)              \

(not present)          ─────> proxy steering (injected)
```

* `never_backend`: complete A-leg-visible messages (client-visible local replies, tagged input spans) whose semantic identity is persisted. Every later B-leg removes them deterministically, even when the client replays full history. Source and reply are tagged **before** release (`Tag-Before-Release`), no eventual persistence.

* `persistent steering`: complete proxy-owned messages persisted under the A-leg (role+text, ≤64KiB, ≤64 active/256KiB total). The client never sees them and never returns them, so the proxy reinjects them on **every** later B-leg from durable state.

No client or data-plane field can mark its own content `never_backend` or create steering. Only trusted in-process producers via `pkg/lipsdk/nonforwardable` and `pkg/lipsdk/steering` may mutate state.

## Whole-message granularity

V1 operates only on **complete canonical messages** (`lipapi.Message` in `Instructions`/`Messages` or `ItemKindMessage`). Partial substring or content-part surgery is not supported. Non-message items are rejected for identity/anchor, and dangling `item_reference` targeting a removed message is cleaned during projection.

## Trusted producer boundaries

* `nonforwardable.Registrar`: `TagMessages(aLeg, identities, reason)` over the authoritative `Tagger` port. Batch-atomic, idempotent, 4096 cap.
* `steering.Writer`: `Put(overlayID, message, placement, anchorPolicy, reason)` and `Deactivate`. Stores the **rendered** model-visible payload verbatim; semantic no-op is idempotent.

Both are constructed via explicit `sdkadapter` helpers (`NewRegistrar`, `NewWriter`, `NewConversationViewServices*`) with an authoritative `ALegID` and optional narrow `TrajectoryResolver` + `Observer`. No global locator, no client-frontend exposure.

## Placement and cache stability

Two producer-facing placements:

* `stable_prefix`: after the deterministic static instruction prefix and before mutable history. Stable across turns.
* `after_ingress_tail`: at registration time resolved to a **fixed semantic anchor** (`MessageAnchor{Identity, Occurrence}`) immediately after the current terminal forwardable user message. Persisted as `PlacementAfterMessage`.

For a given overlay revision, role/text/anchor/order are byte-stable. No per-turn timestamp, trace ID, nonce, or counter may enter the model-visible payload.

Cache invariant: with append-only forwardable history and unchanged steering, `M(T)` is an exact prefix of `M(T+1)` through `T`'s final content. Activation `… U_N, STEERING` stays `… U_N, STEERING, A_N, U_N+1` later.

## Explicit discontinuities and fallback

Create, content-replace, placement-move, and deactivate are **explicit cache discontinuities**. `SteeringState.CacheDiscontinuityKind`/`Placement` is emitted via the narrow `conversationview.Observer` seam (`OnSteeringMutation`) with bounded `operation`/`placement` labels only.

If a fixed anchor disappears (history compacted/truncated):

* `stable_prefix_fallback`: deterministic prefix fallback + bounded `anchor_missing_fallback` diagnostic (`OnAnchorFallback(stage, policy)` with `stage=early|final`).
* `fail_closed`: reject before backend (`OnAnchorFailure(policy)` + `OnProjectionFailure(stage)` only when `errors.Is(err, ErrAnchorMissing/ErrAnchorNotFound)`).

Never silently relocate to the moving tail.

## Hidden steering is not a secret

Backend-only steering **is sent to the remote provider/model** and may be quoted or paraphrased in model output. The transport/session property hides it from the client, not from the model. **Do not place credentials, tokens, secrets, API keys, or other sensitive material in steering text.** Treat at-rest steering as sensitive application data under existing DB/access controls, but assume the model will see it. Diagnostics and metrics are content-free: counts, revisions, placement classes, bounded reason codes; no OverlayID, ALegID, digest, or plaintext ever becomes a metric label or log field.

## Observability

Bounded Prometheus series `lip_conversation_view_*_total` with labels `stage` (`early`, `final`), `placement` (`stable_prefix`, `after_message`), `operation` (`create`, `replace`, `move`, `deactivate`), `policy` (`stable_prefix_fallback`, `fail_closed`). `conversation_view_anchor_fallback_total` is now `stage`+`policy`. `OnProjection`/`OnProjectionFailure`/`OnAnchorFallback`/`OnSteeringMutation` are panic-isolated via `conversationview.SafeObserver` so no observer panic affects request/mutation. Injected via `conversationview.Observer` (nil is no-op) at early projection and final reassertion chokepoints. Production composition via `internal/infra/metrics.NewConversationViewServicesWithMetrics(store, aLegID, resolver, bundle)` which combines `Bundle.ConversationViewObserver()` with `sdkadapter.NewConversationViewServicesWithObserver`; `internal/infra/metrics.NewSteeringWriterWithMetrics` similarly. No global, no HTTP exposure. No high-cardinality labels.

## Secret guard ordering

`runSecretGuardStage` runs before the generic two-phase local-turn seam (`runLocalTurnStage`) on the accepted ingress view. Local handlers see already-guarded content.
