# Requirements Gap Analysis

## Scope and Method

This analysis compares `requirements.md` with the current direct `openai-codex` connector and adjacent canonical/runtime assets. It follows `.kiro/rules/gap-analysis.md`: current assets are identified first, gaps are classified as **Missing**, **Constraint**, or **Unknown**, and multiple implementation approaches are evaluated before design selection.

The analysis is intentionally limited to information needed to choose a design. Deep protocol verification against the live ChatGPT Codex backend remains a design/implementation research item.

## Current State Investigation

### Existing connector assets

| Asset | Current responsibility | Reuse value |
|---|---|---|
| `connectors/codex/internal/codex/payload.go` | Builds Codex Responses requests, including `prompt_cache_key`, `previous_response_id`, reasoning controls, and `include: reasoning.encrypted_content`. | Provides the normal request envelope and existing cache/continuation controls. |
| `connectors/codex/internal/codex/payload_input.go` | Projects canonical messages and function call/output history into connector-local input items. | Existing typed item seam can be extended with validated provider-native replay items. |
| `connectors/codex/internal/codex/continuation.go` | Stores response IDs and exact input/output fingerprints for WebSocket delta continuation with TTL/LRU and in-flight protection. | Strong pattern for scoped bounded checkpoint state and exact-prefix validation. |
| `connectors/codex/internal/codex/ws.go` | Owns WebSocket sessions, continuation application/rollback, stale retry, and recording. | Integration point for chain reset and post-checkpoint continuation. |
| `connectors/codex/internal/codex/stream.go` | Parses SSE/WS events into canonical output and records response IDs plus completed function-call items. | Natural location to capture bounded exact reasoning items and internal compaction output. |
| `connectors/codex/internal/codex/attempt.go` | Prepares request identity, prompt-cache key, HTTP attempts, OAuth refresh, and stream opening. | Pre-output orchestration point for applying or creating checkpoints. |
| `connectors/codex/internal/codex/plugin.go` | Owns backend runtime state and static/managed HTTP/WS paths. | Composition root for a connector-private compaction manager/store. |
| `connectors/codex/internal/codex/sessionturns.go` | Bounded per-conversation state with reservation/commit/release behavior. | Additional concurrency/lifecycle precedent. |
| `connectors/codex/internal/catalog` | Parses model context windows and reasoning profiles from `codex debug models`. | Can retain auto-compaction threshold and compatibility hash without a core catalog change. |
| `connectors/codex/internal/localtok` and `usage_estimator.go` | Estimates request/output token usage with existing image handling and accounting provenance. | Can support threshold planning and before/after performance evidence. |
| `connectors/codex/internal/service/config.go` | Decodes connector YAML and maps service config to direct HTTP/app-server configs. | Existing typed configuration boundary for an opt-in nested block. |
| `pkg/lipapi.ReasoningPart` and `internal/plugins/protocols/openairesponsesitem` | Existing dialect-tagged exact OpenAI Responses reasoning envelope. | Avoids a new canonical reasoning concept; direct connector can emit/consume the existing dialect. |
| `internal/plugins/features/reasoningpreservation` | Captures and restores exact reasoning parts across compatible routes. | Existing feature integration to revalidate, not duplicate. |

### Dominant patterns and constraints

- `openai-codex` is an optional executable backend connector. Provider protocol logic belongs inside `connectors/codex`; core and public SDK contracts must remain provider-neutral.
- Streaming is primary. Internal optimization may happen only before downstream output commitment.
- Managed OAuth selection is account-scoped and may rotate accounts before output. Any opaque provider state must therefore be account-bound.
- WebSocket continuation is an optimization with a full-payload rollback path. Native compaction must preserve that fallback property.
- The canonical message model is intentionally not a universal ordered Responses item model. A connector-private checkpoint is viable only while it remains private and exact-lineage-bound.
- Existing debug and error code deliberately bounds payload leakage. Ciphertext must not enter logs or errors.

## Requirement-to-Asset Map

| Requirement area | Existing assets | Gap classification | Gap |
|---|---|---|---|
| 1. Configuration/default parity | Typed YAML config, connector config validation, runtime composition | Missing | No native-compaction block, defaults, bounds, diagnostics, or disabled-path parity tests. |
| 2. Opaque item fidelity | Existing canonical reasoning dialect; Codex requests encrypted reasoning | Missing | Codex stream mapper drops non-function completed items; payload builder cannot replay exact reasoning or compaction items. |
| 3. Trigger planning | Context windows in catalog; local token counter | Partial | Catalog drops `auto_compact_token_limit` and `comp_hash`; no payload-level post-rewrite estimator or safe split planner. |
| 4. Compaction request | Normal `/responses` HTTP/WS clients and stream parser | Missing | No compaction-trigger item, internal collector, strict output validation, retained-message construction, or hidden internal request path. |
| 5. Checkpoint lifecycle | Continuation/session-turn TTL/LRU stores | Partial | No account/model/static-fingerprint-bound checkpoint store or committed-vs-candidate lifecycle. |
| 6. Rewrite/chain reset | Exact input fingerprints and continuation rollback | Partial | No source-prefix substitution, live-tail preservation, explicit previous-response reset, or continuation invalidation on checkpoint install. |
| 7. Failure handling | Pre-output HTTP/WS fallback and no-post-output retry rules | Partial | No compaction-specific fail-open/hard-fail classification, cancellation cleanup, or negative cooldown. |
| 8. Usage/diagnostics | Provider usage mapping, estimator, debug shape logging | Partial | No internal-request usage aggregation, compaction metrics, before/after measurements, or ciphertext-specific privacy tests. |
| 9. Verification/rollout | Connector unit/integration tests, WS tests, managed account tests | Partial | No compaction emulator scenarios, live compatibility smoke, race/fuzz coverage, or default-off promotion gate. |

## Requirements Feasibility Findings

### Confirmed feasible

1. **Opaque pass-through does not require decryption.** The connector can validate the outer item shape, store exact JSON, and replay it only to the same implementor/lineage.
2. **A connector-private implementation fits current architecture.** The feature can live entirely in the independent Codex connector while reusing the existing canonical reasoning dialect.
3. **Exact-prefix substitution is compatible with stateless clients.** The connector already fingerprints request items for WebSocket continuation and can apply the same technique to client-replayed full histories.
4. **Default-off rollout is straightforward.** The connector config already uses explicit experimental transport gates and typed validation.
5. **Pre-output fail-open is compatible with routing invariants.** The internal compaction request can complete or fail before the normal response stream is opened.

### Constraints

1. **Opaque state is not portable.** It must pin account, model, implementor, static request shape, and compatibility hash.
2. **The current canonical call is message-authority.** A provider-private compaction item must not be forced into `PartJSON` or widened into a public concept in this spec.
3. **Managed account selection occurs after common request preparation.** Checkpoint lookup/creation must happen per selected account rather than mutating one shared request before account choice.
4. **The latest live turn cannot be summarized accidentally.** The implementation must split old history from the latest user message and subsequent tool tail.
5. **Compaction starts a new chain.** Reusing the old `previous_response_id` would mix incompatible state and must be prevented explicitly.
6. **Ciphertext length is not model token cost.** Threshold estimates must use provider-reported compaction output tokens or conservative replay-cost metadata rather than tokenizing encrypted bytes as prompt text.

### Research Needed

1. Verify with an environment-gated live test that the ChatGPT Codex endpoint accepts `type: "compaction_trigger"` with the connector's current authentication/header contract.
2. Confirm whether successful replay is bound only to account/model or also to a backend-generated compatibility identity not exposed in the current catalog.
3. Confirm response event details and usage counters for V2 compaction across current Codex models.
4. Measure break-even turns and latency for HTTPS and WebSocket sessions before proposing default enablement.

## Implementation Approach Options

### Option A: Add Canonical Compaction Now

Add ordered compaction items and a context-compaction operation to `pkg/lipapi`, extend the backend plugin ABI, and route compaction through core.

**Advantages**
- Aligns with the long-term OpenResponses design.
- Enables future cross-backend capability negotiation and client-facing compaction.

**Disadvantages**
- Large public-contract and core change for a single provider optimization.
- Overlaps the active `openresponses-api-support` specification.
- Requires broad FE×BE conformance and plugin ABI revalidation.
- Delays a focused experiment and increases risk.

**Assessment:** Technically viable but disproportionate for the requested experiment. Effort **XL**, risk **High**.

### Option B: Connector-Private Native Checkpoints

Add validated opaque item types, a compaction planner/client, and a bounded checkpoint store inside `connectors/codex`. Use existing canonical reasoning contracts only where they already exist.

**Advantages**
- Respects provider ownership and keeps core/public contracts unchanged.
- Reuses the continuation store, model catalog, request pipeline, and token estimator.
- Can be feature-gated and fully removed or disabled without migration.
- Supports direct performance measurement before standardization.

**Disadvantages**
- Native state is intentionally nonportable.
- Requires careful integration across static/managed and HTTP/WS paths.
- Some implementation may later be superseded by canonical OpenResponses compaction.

**Assessment:** Best fit for initial scope. Effort **L**, risk **Medium-High**.

### Option C: Delegate All Compaction to `openai-codex-app-server`

Recommend the app-server backend for long sessions and avoid changes to the direct HTTP connector.

**Advantages**
- Upstream Codex CLI already owns compaction behavior.
- Lowest implementation cost.

**Disadvantages**
- Does not improve the direct connector requested by the user.
- Requires a local Codex subprocess and changes operational characteristics.
- Cannot provide transparent performance benefits to existing direct-backend deployments.

**Assessment:** Useful operational alternative, not a solution. Effort **S**, risk **Low**, requirement coverage **insufficient**.

### Option D: Hybrid Private First, Canonical Later

Implement Option B now and define explicit migration boundaries to a later canonical compaction operation once OpenResponses item authority lands.

**Advantages**
- Delivers experimental evidence quickly.
- Preserves a clean future migration path.
- Avoids prematurely changing public contracts.

**Disadvantages**
- Requires discipline to keep connector-private types private.
- May entail future replacement rather than direct reuse.

**Assessment:** Preferred program strategy. Initial implementation remains Option B. Overall effort **L**, risk **Medium-High**.

## Requirements Corrections Applied After Gap Analysis

The first requirements draft was revised to add these mandatory constraints:

1. **Account/model binding** — opaque state cannot survive managed OAuth rotation unless the same account is selected.
2. **Live-tail split** — compaction must exclude the latest user message and all subsequent tool state.
3. **Chain reset** — the first post-checkpoint request must omit `previous_response_id` and invalidate old WS continuation.
4. **Failure cooldown** — fail-open behavior without negative caching would create repeated high-cost failures.
5. **Usage honesty** — compaction usage must be separately accounted and included in break-even evidence.
6. **No canonical expansion** — this spec must not pre-empt the OpenResponses ordered-item and context-compaction design.
7. **Exact reasoning prerequisite** — the direct connector currently requests encrypted reasoning but drops exact completed items; fidelity must be corrected before relying on native history state.

## Recommendation for Design

Adopt **connector-private native checkpoints** with these design commitments:

- nested typed config, disabled by default;
- V2 compaction only in the initial implementation;
- per-account/per-model planning after account selection;
- exact-prefix source fingerprinting and a verbatim live tail;
- committed checkpoint state separate from in-flight candidates;
- explicit old-chain invalidation and post-checkpoint chain restart;
- strict bounded opaque item validation and ciphertext-safe observability;
- pre-output fail-open with hard-limit fail-closed behavior;
- deterministic emulators plus environment-gated live verification;
- no canonical/public/core contract changes.

## Complexity and Risk

- **Effort: L (1–2 weeks)** — multiple connector subsystems, four execution variants (static/managed × HTTP/WS), new state lifecycle, protocol parsing, accounting, and broad tests.
- **Risk: Medium-High** — the architecture path is clear, but live backend acceptance, opaque lineage compatibility, token economics, and continuation interaction require direct evidence.
