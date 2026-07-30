# Requirements Document

## Introduction

The direct `openai-codex` backend currently forwards canonical conversation history to the ChatGPT Codex Responses endpoint. It already uses a stable `prompt_cache_key` and, in experimental WebSocket mode, can send incremental turns with `previous_response_id`. These mechanisms reduce retransmission and can preserve cache locality, but they do not reduce the model-visible context accumulated by long-running agent sessions.

The upstream Codex CLI now supports native remote compaction. A compaction request produces an opaque `type: "compaction"` item containing `encrypted_content`; the client does not decrypt it and instead stores and replays it as provider-owned state. This feature adds the same class of optimization to the direct `openai-codex` connector while retaining the proxy's existing canonical, routing, streaming, accounting, and security boundaries.

The implementation is experimental. It is disabled by default, enabled per connector instance through typed configuration, and must preserve current behavior exactly when disabled. Initial scope uses the current Responses Compaction V2 mechanism over the normal Codex Responses endpoint. The legacy `/responses/compact` endpoint, generic client-facing compaction, durable checkpoint persistence, and cross-provider reuse remain outside this specification.

## Boundary Context

- **In scope**: the direct `openai-codex` HTTP backend; connector-local opaque reasoning and compaction item fidelity; model compaction metadata; threshold planning; pre-output native compaction requests; account- and model-bound in-memory checkpoint state; exact-prefix history substitution; WebSocket continuation interaction; managed OAuth account isolation; provider usage accounting; diagnostics; configuration; tests; live validation; benchmarks.
- **Out of scope**: the `openai-codex-app-server` backend; new client-facing routes; a canonical context-compaction operation; generic OpenResponses compaction; the legacy `/responses/compact` endpoint; decryption or interpretation of `encrypted_content`; persistence across process restart; sharing checkpoints across users, accounts, models, providers, or connector instances; background/detached compaction; changing routing or failover policy.
- **Adjacent expectations**: reuse the existing OpenAI Responses reasoning dialect and reasoning-preservation feature where applicable; keep `openresponses-api-support` authoritative for future portable/canonical compaction; preserve the existing WebSocket continuation and managed OAuth semantics.
- **Boundary ownership**: optional backend connector (`connectors/codex`) with existing canonical reasoning contracts only; no new `pkg/lipapi`, `pkg/lipsdk`, or `internal/core` concept.
- **Revalidation triggers**: exact reasoning replay; connector process ABI/config decoding; managed account selection; WebSocket continuation; token accounting; no-retry-after-output; secret-safe diagnostics.

## Requirements

### Requirement 1: Experimental Configuration and Default Parity

**Objective:** As an operator, I want native compaction guarded by explicit connector configuration, so I can evaluate it without changing stable production behavior.

#### Acceptance Criteria

1. The `openai-codex` connector shall keep native compaction disabled when its configuration omits the native-compaction block.
2. Where native compaction is enabled, the connector shall accept typed settings for the trigger token override, retained-message token budget, state TTL, maximum state entries, and failure cooldown.
3. If a native-compaction setting is negative, internally inconsistent, above a hard safety bound, or supplied for an unsupported connector kind, the connector shall reject configuration before serving requests.
4. While native compaction is disabled, the connector shall not issue compaction requests, allocate checkpoint state for calls, rewrite input history, alter `previous_response_id`, or change canonical output events.
5. When configuration is resolved, diagnostics shall report only effective enablement and bounded numeric settings and shall not expose credentials or checkpoint contents.
6. When the connector runtime is replaced or closed, the connector shall discard all in-memory native-compaction state.
7. The feature shall remain disabled by default until a later reviewed change explicitly promotes it.

### Requirement 2: Opaque OpenAI Item Fidelity

**Objective:** As a long-running agent client, I want provider-owned reasoning and compaction items preserved without interpretation, so continuation retains useful native state.

#### Acceptance Criteria

1. When the Codex backend emits a completed OpenAI Responses reasoning item, the connector shall preserve its allowlisted bounded envelope exactly and emit the existing canonical OpenAI Responses reasoning-part dialect.
2. When a canonical request contains an exact OpenAI Responses reasoning part compatible with the selected Codex backend, the connector shall replay the original bounded envelope rather than converting it to text.
3. When an internal compaction request emits a completed compaction item, the connector shall preserve the complete bounded item needed for replay.
4. The connector shall treat `encrypted_content` as opaque and shall not decrypt, decode, summarize, inspect, edit, or tokenize it as ordinary text.
5. If a reasoning or compaction item is malformed, oversized, missing required identity/content, or uses an unsupported discriminator, the connector shall reject or discard it according to request/output safety rules and shall never store it as a valid replay item.
6. The connector shall bind opaque replay items to the OpenAI Codex implementor and compatible model lineage and shall not project them to unrelated backends.
7. Logs, errors, diagnostics, metrics labels, and ordinary audit records shall never contain opaque item payloads or ciphertext.

### Requirement 3: Model Metadata and Trigger Planning

**Objective:** As an operator, I want compaction triggered from model-aware limits, so it runs only when the expected context benefit justifies its cost.

#### Acceptance Criteria

1. When the Codex model catalog provides `auto_compact_token_limit` or `comp_hash`, the connector shall preserve those fields in its internal model profile.
2. The effective trigger threshold shall use an explicit configured token override first, then the model catalog auto-compaction limit, then a conservative derived fraction of the resolved context window.
3. If the effective threshold is not below the resolved hard context limit or cannot leave the configured retained-message budget plus safety headroom, configuration or model admission shall fail deterministically.
4. Before planning compaction, the connector shall estimate the effective upstream input after any valid checkpoint rewrite rather than using the client's unreduced full-history size.
5. When the effective input reaches the trigger threshold and a safe compactable prefix exists, the connector shall plan compaction before opening the client-visible response stream.
6. The compactable prefix shall exclude the latest live instruction turn: the most recent user message and every item after it shall remain verbatim in the live tail.
7. If no safe split boundary exists, the live tail alone exceeds the threshold, or call/output pairing would cross the split, the connector shall skip automatic compaction and preserve the unmodified request path.
8. The connector shall allow at most one compaction attempt in flight for the same checkpoint lineage.

### Requirement 4: Native Compaction Request and Checkpoint Creation

**Objective:** As a maintainer, I want the connector to create validated native checkpoints, so later turns can replace old history safely.

#### Acceptance Criteria

1. Where native compaction is enabled and planned, the connector shall issue a pre-output Responses Compaction V2 request to the existing Codex Responses endpoint by appending one compaction-trigger item to the compactable prefix.
2. The compaction request shall use the same selected account, model, instructions, tools, prompt-cache identity, reasoning controls, and conversation identity as the pending normal request.
3. The compaction request shall omit `previous_response_id` and shall not inherit an earlier WebSocket continuation chain.
4. The connector shall accept a compaction result only after receiving exactly one completed compaction item and one successful response completion.
5. If the compaction response emits assistant text, tool calls, zero or multiple compaction items, malformed events, or a non-success terminal state, the connector shall reject the candidate checkpoint.
6. A valid replacement window shall contain recent retained user-context items within the configured retained-message budget followed by the opaque compaction item; system instructions shall remain in the request's instruction field.
7. The checkpoint shall record the exact source-prefix fingerprints, replacement items, compaction-output token count when reported, model compatibility identity, and static request fingerprints required for later validation.
8. Internal compaction response items shall not be forwarded as ordinary client-visible assistant output.
9. The connector shall not make the legacy `/responses/compact` endpoint part of the initial implementation.

### Requirement 5: Checkpoint Isolation and Lifecycle

**Objective:** As a security-conscious operator, I want checkpoints tightly scoped and bounded, so opaque state cannot leak or grow without limit.

#### Acceptance Criteria

1. Each checkpoint shall be scoped by authoritative session identity, connector instance, selected account, model, prompt-cache key, client family, compaction compatibility hash when available, instructions fingerprint, and tools fingerprint.
2. While a checkpoint is stored, the connector shall retain it in memory only and shall enforce configured TTL and maximum-entry bounds with deterministic eviction.
3. When a request's source prefix, instructions, tools, model, account, client family, or compatibility identity differs from the checkpoint, the connector shall not reuse the checkpoint.
4. If managed OAuth selection rotates to another account, the connector shall use only a checkpoint created for that account or fall back to full history.
5. Concurrent requests for one lineage shall not corrupt, partially replace, or observe another in-flight checkpoint candidate.
6. A failed, cancelled, incomplete, or rejected compaction attempt shall leave the previously committed checkpoint unchanged.
7. Checkpoint lookup failure, expiry, incompatibility, or eviction shall be treated as an optimization miss rather than as authoritative conversation loss.
8. The checkpoint store shall expose only bounded counters/state summaries for diagnostics and shall never expose stored item bodies.

### Requirement 6: Exact-Prefix Rewrite and Continuation Reset

**Objective:** As a long-running client, I want a checkpoint substituted only for the history it summarizes, so conversation order and live work remain correct.

#### Acceptance Criteria

1. When the current input begins with a checkpoint's exact source-prefix fingerprints, the connector shall replace only that prefix with the checkpoint replacement and append the untouched current suffix.
2. The rewrite shall preserve item order, message roles, content order, function-call/output pairing, and the complete latest live turn tail.
3. The first normal request after installing a new checkpoint shall omit `previous_response_id` and start a new native response chain.
4. When a checkpoint is installed, the connector shall invalidate any prior WebSocket continuation entry for the same lineage.
5. After a successful post-checkpoint response completes, the connector may establish a new WebSocket continuation baseline from the compacted chain.
6. If the client rolls back, edits, forks, truncates, or otherwise changes any item inside the checkpoint source prefix, the connector shall reject the rewrite and use the full supplied history.
7. If a valid rewritten request still reaches the trigger threshold after sufficient new history growth, the connector may create a later checkpoint from the current compacted chain under the same validation rules.
8. The connector shall never combine a checkpoint with a response ID or opaque item from an incompatible chain.

### Requirement 7: Failure Handling and Streaming Invariants

**Objective:** As a client, I want compaction failures isolated before output, so experimental optimization cannot corrupt an otherwise valid turn.

#### Acceptance Criteria

1. Native compaction shall run only before client-visible output is committed for the logical request.
2. If compaction fails and the unmodified request remains within the model's hard context limit, the connector shall discard the failed candidate and continue once with the normal full-history request.
3. If compaction fails and the unmodified request cannot fit the hard context limit, the connector shall return a deterministic pre-output context/compaction error.
4. After any client-visible canonical content event, the connector shall not start compaction, retry compaction, switch checkpoint lineage, or transparently fail over because of compaction.
5. When cancellation or deadline expiry occurs during compaction, the connector shall stop the internal request, release its in-flight reservation, and return or fall back according to the caller's cancellation state.
6. After a compatibility or protocol failure, the connector shall apply a bounded per-lineage failure cooldown so repeated turns do not create a compaction retry storm.
7. Authentication and rate-limit failures during a managed-account compaction attempt shall follow existing managed-account rotation rules without reusing the failed account's checkpoint on another account.
8. A full-history fallback shall preserve the existing no-retry-after-output and streaming-first behavior.

### Requirement 8: Usage Accounting, Diagnostics, and Performance Evidence

**Objective:** As an operator, I want the cost and benefit of native compaction observable, so enablement decisions are evidence-based.

#### Acceptance Criteria

1. When the compaction response reports token usage, the connector shall account for that provider-billable usage separately from the subsequent normal response and shall not double count it.
2. If provider usage is absent, any estimate shall use existing authority/provenance metadata and shall remain distinguishable from provider-reported usage.
3. The connector shall record bounded metrics for attempts, successes, validation failures, fail-open fallbacks, hard failures, checkpoint hits, misses, evictions, cooldown skips, input tokens before/after, and serialized request bytes before/after.
4. Diagnostics shall identify the effective trigger source and the checkpoint outcome without recording raw prompts, reasoning envelopes, compaction items, credentials, or ciphertext.
5. A deterministic long-history benchmark fixture shall demonstrate that checkpoint reuse reduces both serialized upstream input size and estimated model-visible input compared with the equivalent full-history request.
6. The benchmark and metrics shall include the one-time compaction cost so performance claims reflect break-even behavior rather than only steady-state savings.
7. The connector shall expose enough evidence to compare native compaction with existing prompt-cache and WebSocket continuation behavior without changing their accounting semantics.

### Requirement 9: Compatibility, Verification, and Rollout Safety

**Objective:** As a maintainer, I want the feature isolated and thoroughly verified, so it can be disabled or promoted without architectural drift.

#### Acceptance Criteria

1. The initial implementation shall not add a new canonical item kind, canonical operation, public SDK contract, core routing rule, frontend route, or generic provider capability.
2. The `openai-codex-app-server` backend and all non-Codex backends shall remain behaviorally unchanged.
3. The direct connector shall support the feature for static credentials and managed OAuth accounts and for HTTPS and experimental WebSocket normal-request transports.
4. Tests shall be written before behavior changes and shall cover configuration, catalog metadata, item validation, split planning, checkpoint isolation, rewriting, continuation reset, failure cooldown, cancellation, usage accounting, and disabled-path parity.
5. Integration tests shall use deterministic HTTP/SSE and WebSocket emulators to prove one compaction request followed by a compacted normal request and to prove zero extra requests when disabled.
6. Race tests shall cover checkpoint and continuation interaction, and fuzz tests shall cover bounded opaque-item and compaction-stream parsing.
7. An environment-gated live Codex smoke test shall verify that the current ChatGPT Codex backend accepts the V2 trigger and later replays the returned compaction item before the feature may be considered stable.
8. Disabling the feature shall be a complete rollback requiring no state migration and shall restore the established request path on the next connector runtime.
9. Promotion to enabled-by-default shall require a separate reviewed change supported by live compatibility, quality, performance, usage, and failure-rate evidence.
