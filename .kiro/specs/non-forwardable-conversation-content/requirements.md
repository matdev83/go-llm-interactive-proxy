# Requirements Document

## Introduction

Go-LIP needs a canonical mechanism for conversation messages that are part of the client-visible A-leg session but must **never** be included in any request sent on an inference B-leg. The proxy must recognize those messages when clients/agents later replay full conversation history, remove them from every backend projection, and allow trusted proxy features to create safe local replies without opening an inference backend.

This specification implements the reusable non-forwardable infrastructure only. It deliberately does **not** implement interactive command syntax, command parsers/handlers, routing-setting commands, quota-notification policy, or any other concrete producer. A future interactive-command feature should be able to contribute a handler to the generic local-turn seam and use the infrastructure without changing frontend/backend adapters or the core enforcement architecture.

The first canonical unit is a **complete conversation message**. Partial-message substring/part stripping is not part of this feature; trusted producers must keep local-only content in a standalone message when they need never-forward semantics.

## Boundary Context

### In scope

- Deterministic replay-stable identity for complete canonical message units.
- Authoritative A-leg-scoped never-forward tag storage.
- Bounded in-memory and SQLite/PostgreSQL durable persistence following A-leg lifecycle.
- Early derivation of a backend-effective call with tagged historical messages removed.
- A mandatory final guard before PTB traffic capture and backend `Open`.
- A generic ordered local-turn extension seam for successful proxy-local responses with no B-leg.
- Tag-before-release semantics for proxy-generated local replies.
- Canonical local text response streams encoded by existing frontends.
- Full-history and OpenResponses continuation replay compatibility.
- Metrics/diagnostics, failure behavior, race coverage, SDK/plugin documentation, and architecture tests required to make the mechanism production-ready.

### Out of scope

- `!/` or any other command grammar.
- Interactive command parsing, dispatch, command handlers, or setting/routing mutations.
- Concrete quota/budget/usage notifications or their scheduling/policy.
- A generic asynchronous notification scheduler.
- Partial text/content-part deletion from mixed backend-relevant messages.
- Client APIs allowing callers to mark their own content non-forwardable.
- Provider/backend-specific filtering.
- A new routing, billing, continuation, or response protocol.

### Boundary owner

- **Canonical message semantics:** `pkg/lipapi` remains the existing owner; no non-forwardable flag is added to the wire/canonical request model.
- **Never-forward domain/application policy:** new focused `internal/core/nonforwardable` capability.
- **Trusted producer contracts:** focused `pkg/lipsdk/nonforwardable` and `pkg/lipsdk/localturn` contracts.
- **Extension composition:** existing FeatureBundle/runtime snapshot machinery.
- **A-leg persistence:** standard `internal/core/b2bua` memory store and `internal/core/continuity/bunstore` adapters through an optional focused capability.
- **Runtime enforcement:** `internal/core/runtime` request preparation and shared candidate-open boundary.
- **Frontend/backend adapters:** remain protocol/provider translation edges and do not own this policy.

### Hexagonal lens

- Driving adapters: existing client frontends.
- Application orchestration: runtime local-turn stage and backend-call preparation.
- Core policy/domain: semantic identity, tags, projection/enforcement.
- Driven adapters: memory/Bun A-leg tag stores.
- Composition root: process services + immutable runtime generation/FeatureBundle snapshot.

### Revalidation trigger

Any implementation that introduces a new `lipapi.Call`/`lipapi.Event` wire-visible classification field, moves enforcement into provider/frontends, widens base `b2bua.Store`/public continuity contracts, adds partial-message regex rewriting, adds a backend bypass around the final guard, or allows a local-only reply to become client-visible before its tag commit MUST return to design validation.

## Requirement 1: Canonical Replay-Stable Message Identity

**Objective:** As the proxy, I want one deterministic semantic identity for a complete conversation message so that the same local-only message can be recognized after a client reconstructs and resubmits session history.

### Acceptance Criteria

1.1. THE identity service SHALL support complete canonical message units represented by legacy `lipapi.Message`/`Instructions`/`Messages` and by `lipapi.Item` where `Kind == ItemKindMessage`.

1.2. WHEN two supported messages have equivalent canonical role and ordered semantic content, THE identity service SHALL produce the same versioned identity regardless of whether the source representation is legacy message authority or item authority.

1.3. THE v1 identity SHALL use SHA-256 over a deterministic semantic projection and SHALL include the message role and ordered semantic content.

1.4. THE identity projection SHALL exclude transient/non-semantic carriers including generated/transport item IDs, item status, assistant phase, positional indexes, proxy-only `Message.Metadata`, call/session/routing fields, response IDs, and transport/cache wrapper metadata.

1.5. THE identity projection SHALL normalize CRLF and CR line endings to LF while otherwise preserving text whitespace and Unicode, and SHALL canonicalize JSON/opaque structured message content deterministically before hashing.

1.6. WHEN a supported message is encoded through an official frontend representation and later decoded from an equivalent client replay, THE decoded message identity SHALL remain equal for all content forms covered by the frontend contract tests.

1.7. THE persistent registry and normal logs/metrics SHALL store or emit only the identity version/digest and bounded metadata; they SHALL NOT persist or log message plaintext merely to enforce non-forwardability.

1.8. IF an item is not a complete message item, THEN the message-tagging API SHALL reject it rather than infer partial-item semantics.

1.9. THE first implementation SHALL NOT classify an individual substring or content part independently from its containing message.

1.10. WHEN role/content-identical messages occur more than once in one A-leg, THE system SHALL intentionally treat them as the same semantic identity and therefore the same never-forward disposition.

## Requirement 2: Authoritative A-Leg Never-Forward Registry

**Objective:** As the proxy, I want non-forwardable classification to follow authoritative session continuity so that replay protection survives turns, reloads, and durable-session restart.

### Acceptance Criteria

2.1. THE registry SHALL key all tags by proxy-authoritative `ALegID`; client session hints, frontend connection IDs, raw response IDs, B-leg IDs, or client-supplied A-leg strings SHALL NOT independently authorize tag lookup or mutation.

2.2. THE registry SHALL model a never-forward tag as append-only A-leg state containing identity version/digest, a bounded non-secret reason code, and creation metadata sufficient for diagnostics.

2.3. WHEN the same identity is tagged repeatedly on one A-leg, THE mutation SHALL be idempotent and SHALL NOT consume additional capacity.

2.4. WHEN multiple new identities are submitted in one tagging batch, THE store SHALL commit all new tags atomically or commit none of them.

2.5. THE standard in-memory B2BUA store SHALL implement the focused registry capability under the existing A-leg lock/lifecycle without widening the base `b2bua.Store` contract.

2.6. THE standard Bun continuity store SHALL persist the same semantics for SQLite and PostgreSQL using an A-leg-owned migration and SHALL remove tag rows when the owning A-leg is deleted.

2.7. WHEN durable continuity is configured and the process restarts, THE next resumed A-leg turn SHALL observe all previously committed never-forward tags.

2.8. WHEN immutable runtime generations reload, THE tag state SHALL remain process/continuity-owned and SHALL NOT be copied into or reset with a generation.

2.9. WHEN an A-leg is deleted and later recreated/replaced, THE new A-leg SHALL NOT inherit tag state from the deleted A-leg.

2.10. THE registry SHALL enforce a hard maximum of 4096 unique message identities per A-leg in v1.

2.11. IF a tagging operation would exceed the A-leg capacity, THEN the operation SHALL fail without partial mutation and the caller SHALL NOT be allowed to expose newly designated local-only content as if it were protected.

2.12. THE feature SHALL NOT expose a client/data-plane API that lets an untrusted caller create or remove never-forward tags.

## Requirement 3: Tag-Before-Release Safety

**Objective:** As the proxy, I want classification to become durable before local-only content is exposed so that any later client replay is causally guaranteed to be filterable.

### Acceptance Criteria

3.1. BEFORE any proxy-generated message designated never-forward becomes visible through a frontend response, THE system SHALL commit that message's A-leg tag successfully.

3.2. IF reply tagging fails, THEN the proxy SHALL NOT release any event containing that local-only reply.

3.3. WHEN a local-turn handler claims existing client input as local-only, THE runtime SHALL persist the claimed input-message tags before invoking the handler's side-effecting/response-producing phase.

3.4. IF claimed-input tagging fails, THEN the local-turn handler's execution phase SHALL NOT run and no B-leg SHALL be opened as fallback for that claimed turn.

3.5. AFTER a current-turn tag mutation commits, THE request-local enforcement view SHALL include the new identity immediately without waiting for another store read.

3.6. THE implementation SHALL NOT rely on asynchronous/eventual tag persistence after client output.

3.7. IF a local-turn execution phase fails after its input was tagged, THEN the input tag SHALL remain authoritative and the request SHALL fail without falling back to inference.

## Requirement 4: Early Backend Projection of Client History

**Objective:** As the routing/billing/runtime pipeline, I want local-only history removed before backend-oriented processing so that it cannot influence model selection, context limits, policy, or cost.

### Acceptance Criteria

4.1. AFTER authoritative A-leg resolution and A-leg/client evidence handling, and BEFORE backend-oriented request/pre-request transforms, route planning, context-size estimation, billing authorization, capability negotiation, or B-leg creation, THE runtime SHALL derive a backend-effective call from the accepted client/work call.

4.2. FOR each normal logical backend turn, THE runtime SHALL load at most one bounded authoritative never-forward snapshot from continuity and SHALL carry that snapshot with request-local state across all B-leg attempts.

4.3. THE early projection SHALL operate on a deep clone/effective call and SHALL NOT rewrite the original client call or CTP evidence merely to hide local-only history from the B-leg.

4.4. WHEN a legacy `Instructions` or `Messages` entry matches the A-leg tag snapshot, THE projection SHALL remove that complete message while preserving the order and fields of every retained message.

4.5. WHEN an `ItemKindMessage` matches the snapshot under item authority, THE projection SHALL remove that complete item while preserving the order and fields of every retained item.

4.6. WHEN an in-call `ItemKindItemReference` refers to an item ID removed by the projection, THE projection SHALL remove the dependent reference rather than forward a dangling in-call reference.

4.7. AFTER filtering and dependency cleanup, THE projected call SHALL pass normal canonical validation before any backend-oriented stage uses it.

4.8. IF filtering leaves no valid forwardable request content or produces an unresolved canonical dependency, THEN the request SHALL fail closed before route planning/backend execution.

4.9. THE backend-oriented request/pre-request stages, route/context estimation, billing/credit calculations, capability checks, and baseline freeze SHALL use the filtered backend-effective call rather than the unfiltered client-history view.

4.10. THE projection logic SHALL be frontend- and provider-neutral and SHALL NOT import protocol DTOs or provider SDK types.

## Requirement 5: Final Backend Wire Guard

**Objective:** As the proxy security boundary, I want a last enforcement point shared by every B-leg so that later attempt shaping cannot reintroduce local-only content.

### Acceptance Criteria

5.1. IMMEDIATELY BEFORE PTB traffic capture and backend `Open`, THE shared candidate-open path SHALL enforce never-forward classification on the final backend-facing canonical call after per-candidate shaping/transforms and candidate adaptation.

5.2. THE final guard SHALL use the logical turn's authoritative tag snapshot plus any identities successfully registered during that turn; it SHALL NOT perform an independent mutable-policy interpretation per B-leg.

5.3. WHEN a later request/attempt transform reintroduces a message whose identity is tagged never-forward, THE final guard SHALL remove that whole message before PTB capture/backend open.

5.4. AFTER final filtering, THE backend-facing call SHALL validate; any enforcement/store/projection uncertainty SHALL fail closed with no PTB payload and no backend invocation.

5.5. THE same final guard SHALL cover initial opens, failover, retry-before-output, parallel/race arms, TTFT replacement, and interleaved thinker/executor B-legs.

5.6. THE feature SHALL NOT create a retry/failover path after downstream output has started and SHALL preserve the existing no-retry-after-output invariant.

5.7. PTB traffic capture SHALL be generated from the already-enforced backend-facing call, never from the unfiltered client call.

5.8. Backend plugins/connectors SHALL require no non-forwardable-specific implementation.

## Requirement 6: Generic Local-Turn Extension Seam

**Objective:** As a future proxy-local feature, I want a supported way to claim and answer a client turn locally so that I do not need to fake a backend failure or modify every frontend.

### Acceptance Criteria

6.1. THE SDK FeatureBundle SHALL support an ordered optional collection of generic local-turn handlers without implementing any command-specific handler in this spec.

6.2. THE local-turn stage SHALL run only after authentication/workspace/secure-session authority, A-leg resolution, ingress secret guarding, and existing submit-policy processing have established an accepted authoritative turn, and SHALL run before backend request/pre-request transforms, credit/billing authorization, route planning, keepwarm/model work, or B-leg creation.

6.3. THE runtime SHALL retain a deep canonical ingress view from before mutating backend-oriented stages so local-turn matching and source tagging can refer to what the client actually submitted rather than a later rewritten message.

6.4. EACH local-turn handler SHALL expose a pure `Match` phase that either passes or claims the turn and identifies zero or more normalized complete message indexes to mark never-forward plus a bounded reason code.

6.5. WHEN a handler claims a turn, THE runtime SHALL validate the claimed indexes against the normalized ingress trajectory and SHALL persist those source-message tags before invoking the handler's `Handle` phase.

6.6. THE first successfully claimed handler in deterministic order SHALL own the turn; later local-turn handlers SHALL NOT run for that turn.

6.7. AFTER a handler claims a turn, any handler error/panic, invalid reply, or reply-tagging failure SHALL fail the request and SHALL NOT fall through to backend execution.

6.8. BEFORE a handler claims a turn, Match-phase failures MAY follow the handler's declared fail-open/fail-closed extension failure mode; security-sensitive future handlers are expected to declare fail-closed.

6.9. THE `Handle` phase SHALL return one bounded assistant text reply through the local-turn contract; the core SHALL validate and tag that reply before constructing the local response stream.

6.10. A successfully handled local turn SHALL open zero B-legs, perform zero backend route/model/provider calls, perform no inference credit/billing authorization, and emit no provider usage.

6.11. A local turn SHALL still preserve normal authenticated A-leg/session/trace correlation, client-visible secure-session resume data, CTP evidence, frontend response encoding, and continuation recording where the frontend normally records responses.

6.12. IF request-level concurrency/admission authority was acquired before local-turn matching to preserve existing submit ordering, THEN the local-turn path SHALL release that authority deterministically without creating billing/usage records for a nonexistent B-leg.

6.13. THE implementation SHALL NOT add `!/`, `set`, `unset`, routing commands, command registries, command state, or any other interactive-command behavior.

## Requirement 7: Canonical Proxy-Local Response Stream

**Objective:** As a frontend, I want proxy-local turns to look like ordinary canonical successful responses so that no frontend-specific synthetic-response implementation is required.

### Acceptance Criteria

7.1. WHEN a local turn succeeds, THE runtime SHALL return a normal `lipapi.EventStream` representing exactly one assistant text response.

7.2. THE local stream SHALL produce a valid canonical response/message/text/terminal sequence for both streaming and non-streaming frontend collection paths.

7.3. THE local stream SHALL NOT emit provider usage/cost events or fabricate a B-leg/backend identity.

7.4. THE existing shared frontend encode pipeline SHALL encode the local stream without branching on provider/backend type.

7.5. THE assistant message used to compute the pre-release never-forward tag SHALL be semantically identical to the assistant content encoded by the local stream so that later client replay produces the same identity.

7.6. THE local stream SHALL obey normal context cancellation/Close behavior and SHALL not create a background goroutine merely to synthesize a finite reply.

7.7. Official frontend contract tests SHALL prove that local text replies encode successfully and replay into identity-equivalent canonical assistant messages for supported frontend forms.

## Requirement 8: Replay and Continuation Compatibility

**Objective:** As a client that resends or references session history, I want local-only turns to remain visible locally while never contaminating future backend context.

### Acceptance Criteria

8.1. WHEN an agent resends complete legacy message history containing previously tagged client-origin or proxy-origin local messages, THE next backend-effective call SHALL remove all matching messages before B-leg processing.

8.2. WHEN OpenResponses `previous_response_id` materialization reconstructs a history containing a tagged local-turn input/reply, THE core projection after materialization SHALL remove those concrete message items before backend processing.

8.3. THE feature SHALL NOT require frontends to delete local-only turns from their normal A-leg/continuation records for correctness.

8.4. THE feature SHALL NOT use response IDs, continuation IDs, or client-returned proxy metadata as the sole replay recognition mechanism.

8.5. WHEN the same A-leg is served across runtime generation reload, previously recorded local-only messages SHALL continue to be filtered even if the producer/handler that originally created them is no longer present in the active generation.

8.6. WHEN durable PostgreSQL continuity is shared by multiple processes, THE implementation SHALL read authoritative tag state per logical turn and SHALL NOT rely on an indefinitely stale process-global tag cache.

8.7. An opaque out-of-call item reference that contains no concrete message content SHALL remain governed by existing continuation/item-reference semantics; this feature SHALL guarantee removal of concrete tagged message items and in-call references to messages removed in the same projection.

## Requirement 9: Observability, Audit, and Security

**Objective:** As an operator, I want the A-leg/B-leg distinction to remain auditable without leaking sensitive local control content into backend evidence or high-cardinality telemetry.

### Acceptance Criteria

9.1. CTP/client-turn evidence SHALL continue to describe what the client actually submitted, including local-only messages when present, subject to existing secret redaction/capture policy.

9.2. PTB/backend-attempt evidence SHALL contain only the final enforced backend-facing call and SHALL never contain a tagged never-forward message.

9.3. WHEN messages are filtered, THE runtime MAY emit bounded counters/diagnostics containing trace/A-leg correlation, filtered count, and bounded reason/category data, but SHALL NOT emit message plaintext or unbounded digest labels.

9.4. Registry/store lookup failure on a turn that requires enforcement SHALL fail closed before backend execution rather than assume the tag set is empty.

9.5. The existing secret-guard boundary SHALL run before local-turn handlers are allowed to inspect/handle an accepted turn.

9.6. Reason codes SHALL be bounded identifiers and SHALL NOT be used to store command arguments, message text, quota values, or other unbounded/sensitive payloads.

9.7. THE feature SHALL NOT add a client-authoritative `non_forwardable` field to `lipapi.Call`, `lipapi.Message`, `lipapi.Item`, or frontend wire DTOs.

9.8. THE feature SHALL NOT add provider SDK dependencies to the core identity/registry/enforcer/local-turn packages.

## Requirement 10: Performance, Lifecycle, and Compatibility

**Objective:** As the proxy runtime, I want the safety invariant without a new hot-path framework or backend Cartesian maintenance cost.

### Acceptance Criteria

10.1. WHEN an A-leg has no tags and no local-turn handler claims the request, normal routing/streaming/retry/accounting semantics SHALL remain unchanged except for the bounded registry snapshot/projection work required by this feature.

10.2. THE normal backend path SHALL perform no more than one continuity tag-snapshot read per logical turn; failover/parallel/retry B-legs SHALL reuse request-local enforcement state.

10.3. THE implementation SHALL use no polling loop, watcher, background cleanup goroutine, or global mutable service locator for non-forwardable enforcement.

10.4. In-memory tag state SHALL be protected by the existing A-leg store synchronization/lifecycle and SHALL be evicted with the owning A-leg.

10.5. The base `internal/core/b2bua.Store`, public `pkg/lipsdk/continuity.Store`, and unrelated external continuity contracts SHALL remain unchanged; standard stores SHALL implement a focused optional capability.

10.6. IF a runtime generation contributes local-turn handlers but the configured continuity implementation cannot provide the required tagger/reader capability, THEN runtime composition SHALL fail deterministically rather than run a producer that cannot guarantee replay protection.

10.7. Local-turn handler lists SHALL be frozen with the immutable request runtime snapshot and SHALL follow existing FeatureBundle merge/order/panic-isolation conventions.

10.8. Disabling/removing a producer in a later generation SHALL NOT disable enforcement of tags already stored on an A-leg.

10.9. THE implementation SHALL not add pairwise frontend/backend translators or backend-specific compatibility matrices for this feature.

10.10. THE implementation SHALL not introduce a regex/history-rewrite fallback when semantic identity lookup fails.

## Requirement 11: TDD, Documentation, and Quality Gates

**Objective:** As a maintainer, I want the no-leak invariant certified before production code ships so that later features can safely depend on it.

### Acceptance Criteria

11.1. BEFORE production implementation, failing tests SHALL freeze identity normalization/equivalence, store semantics, projection behavior, local-turn claim/reply behavior, final-wire enforcement, and error/fail-closed contracts.

11.2. Memory, SQLite, and PostgreSQL store contract tests SHALL cover idempotency, batch atomicity, capacity, A-leg deletion/recreation, restart/load behavior, and concurrent tag/snapshot operations.

11.3. Runtime tests SHALL prove that tagged history does not affect the effective route/context/billing call and never reaches PTB capture or a fake backend.

11.4. Runtime tests SHALL deliberately reintroduce a tagged message from a late attempt transform and SHALL prove the final wire guard removes it before backend open.

11.5. Runtime coverage SHALL include initial, failover/retry-before-output, parallel/race, TTFT, and interleaved paths without adding path-specific filter implementations.

11.6. Local-turn tests SHALL use only fake/reference generic handlers and SHALL prove source-tag-before-handle, reply-tag-before-release, zero B-legs, zero inference billing/usage, deterministic ordering, and post-claim no-fallback behavior.

11.7. Frontend/continuation tests SHALL cover full-history replay and OpenResponses `previous_response_id` materialization of a local-turn input/reply.

11.8. Race tests SHALL cover concurrent tag/snapshot activity and generation reload/producer removal while preserving already-committed enforcement.

11.9. SDK/plugin-authoring and architecture documentation SHALL describe the local-turn contract, whole-message never-forward granularity, tag-before-release rule, and prohibition on client-authoritative tagging.

11.10. Final implementation validation SHALL run repository formatting/vet/architecture checks, deterministic unit suites, focused SQLite/PostgreSQL integration tests, and targeted `go test -race` coverage for the new core/runtime/store packages.