# Research: Non-Forwardable Conversation Content

## Scope

This research supports a spec for a protocol-neutral Go-LIP capability that lets trusted proxy features classify complete conversation messages as **never forward to any inference B-leg** while keeping those messages visible on the A-leg/client side. The immediate motivating use case is future interactive proxy control, but interactive command parsing, command handlers, routing-setting mutation, and any specific quota-notification policy are deliberately outside this spec.

The goal of this work is the reusable plumbing: replay-stable identity, A-leg-scoped durable classification, early backend projection, a final no-leak guard, and a generic local-turn response seam that future features can consume without changing frontend/backend adapters.

Repository baselines reviewed:

- Go-LIP: `matdev83/go-llm-interactive-proxy` at `b54982384840ba85c0af2a019ccc35becdd63f10` (`main`, 2026-08-18).
- Python LIP lineage: `matdev83/llm-interactive-proxy` at `71822d5d3e5a375d119386c660301b397e4465e2`.

## Python Lineage: What Is Worth Carrying Forward

### Early command sanitization was text-rewrite based

`src/core/services/command_sanitizer.py` removes detected `!/` command text from a string, including commands embedded at the start, middle, or end of ordinary text. `command_content_processor.py` wraps that sanitizer for text parts.

This solves one immediate request but does not establish a durable replay invariant. A coding agent can resend its whole transcript on every request, so the original command and any proxy-generated reply can reappear indefinitely. Re-running regex/string stripping on every frontend also couples policy to text formatting and becomes fragile for multipart messages and richer canonical item trajectories.

### The later Python design moved to server-owned replay identity

The newer command service (`src/core/commands/service.py`) computes the identity of the **original command message before it is rewritten**, records it in a non-forwardable registry, and fails closed if that classification cannot be persisted. The later archived design (`.kiro/specs/archive/non-forwardable-message-tagging/design.md`) separates:

1. identifying/tagging local-only messages; and
2. enforcing those tags before backend execution.

`src/core/services/non_forwardable_message_identity_service.py` uses deterministic SHA-256 identities derived from semantic message fields rather than client metadata. `src/core/services/non_forwardable_message_enforcer.py` removes classified messages immediately before backend execution and fails closed if enforcement is indeterminate.

That separation is the architectural idea to preserve. The Go implementation should not reproduce the Python service layout or regex sanitizer because Go-LIP now has a stronger canonical/B2BUA execution architecture.

## Current Go-LIP Assets

### Canonical request model already gives one protocol-neutral inspection surface

`pkg/lipapi.Call` is the shared request envelope across frontends. It supports two conversation authorities:

- legacy `Instructions` + `Messages`; and
- ordered `Items` when `Items != nil`.

`pkg/lipapi/walkers.go` already provides `NormalizedItems`, `WalkCallItems`, `WalkCallContentParts`, and `WalkCallTexts`. Legacy messages are projected into canonical message items, so the repository already has the basic representation bridge needed for protocol-neutral message identity.

Important constraint: `lipapi.Message.Metadata` is explicitly proxy-owned, `json:"-"`, and never serialized to wire. It is useful for current-request provenance but cannot identify a message after a client reconstructs/resubmits history.

### Ordered items make whole-message removal safer than substring rewriting

`pkg/lipapi/items.go` models message, item-reference, tool-call, tool-result, reasoning, compaction, and extension items. `Call.Validate` enforces ordering and dependency invariants such as backward item references and tool-call/tool-result linkage.

Therefore the first canonical non-forwardable unit should be a **complete message unit**, not an arbitrary substring or content part. The filter can preserve every retained unit byte/field-equivalently, remove item references that point to removed in-call message IDs, and validate the projected call before it can proceed.

This also gives future producers a clear rule: never mix local-only and backend-relevant content into one message if the whole message must later be suppressed. A local notice or local reply should be a standalone conversation message.

### Secure session already establishes the right authority key

The secure-session preparation path resolves/fetches the proxy-owned A-leg before routing. `SessionRef` clearly distinguishes client hints from proxy authority. The A-leg is already the owner for sticky routing state, B-leg sequencing, interleaved state, and route overrides.

Non-forwardable classification should therefore be keyed by authoritative `ALegID`, not by a raw client session ID, frontend connection, response ID, or other client-controlled carrier.

### Optional continuity capability is an established pattern

`internal/core/b2bua.Store` is intentionally narrow and mirrored by public continuity contracts. Existing interleaved state and route overrides avoid widening that base interface by using optional focused capabilities.

`internal/core/routeoverride/store.go` is the closest precedent: a narrow reader/store contract is implemented by standard `b2bua.MemoryStore` and `internal/core/continuity/bunstore.Store` without changing the base Store API.

Non-forwardable storage should use the same approach.

### Durable continuity supports the required lifecycle

`internal/core/continuity/bunstore` already provides SQLite/PostgreSQL migrations and A-leg-owned durable state. Route override persistence demonstrates transactional A-leg locking/liveness behavior and cascade-safe lifecycle integration.

A small A-leg tag table can therefore persist versioned message digests and bounded reason codes without introducing another database stack or process-local lifecycle authority.

### There is a natural early projection point

`internal/core/runtime/executor_prepare_secure.go` currently:

1. validates/resolves principal/workspace;
2. runs `SecureSession.BeginTurn` and fetches the A-leg;
3. runs secret-guard and submit/request/pre-request stages;
4. constructs the effective call;
5. freezes `preparedRequest.baseline` for routing and B-leg execution.

The client/work call and backend-effective call are already conceptually distinct because runtime route overrides are reasserted on an effective clone before route planning.

Non-forwardable history should be projected out **after A-leg/client evidence and submit processing, but before backend-oriented request/pre-request transforms, routing, context estimation, billing authorization, capability checks, or baseline freeze**. Otherwise local-only history could still distort context limits, token/cost estimates, and route decisions even if it is removed later.

### There is an authoritative last B-leg boundary

Every planned backend candidate eventually reaches the shared candidate-open path in `internal/core/runtime/executor_open_attempt.go`. After per-candidate shaping/transforms and candidate adaptation, runtime builds `wireCall`, emits PTB traffic capture from that exact backend-facing call, and invokes:

`be.Open(openCtx, wireCall, routing.BackendFacingCandidate(c))`

This is the right final safety boundary. A second enforcement pass immediately before PTB capture/backend open ensures that attempt transforms or future request shaping cannot accidentally reintroduce a previously tagged local message.

Initial, failover, retry, parallel, TTFT, and interleaved attempts all need contract tests proving they share this guard rather than each implementing separate filtering.

### CTP/PTB planes already model the desired evidence split

The proxy already distinguishes client-to-proxy (CTP) and proxy-to-backend (PTB) traffic capture. This maps directly to the feature:

- CTP/A-leg evidence may show what the client actually submitted, including local-only messages, subject to existing redaction/security policy.
- PTB evidence must be generated only after non-forwardable projection and must never contain classified local-only content.

The feature must not rewrite historical A-leg evidence merely to make B-leg projection clean.

### Canonical EventStream already supports backend-free replies

`Executor.Execute` returns `lipapi.EventStream`, and `internal/plugins/frontends/frontendpipe` performs the shared decode -> execute -> encode flow. Frontends encode canonical events regardless of which backend produced them.

Therefore a generic proxy-local turn can return a canonical stream without any provider/frontend-specific synthetic-response code. A local text reply can use the normal response/message/text/finished event sequence and omit usage events entirely.

### Existing extension stages cannot express a successful local turn cleanly

Submit hooks can mutate or reject. Pre-request handlers can allow/deny. Response observers are read-only, and response part hooks mutate one existing event. None represents:

> this authenticated A-leg turn was successfully handled by the proxy; emit this client-visible assistant reply and open no B-leg.

Overloading SubmitHook rejection to carry a success response would blur responsibilities and complicate error semantics. A small dedicated `localturn` extension point is justified.

### OpenResponses continuation is compatible with enforcement-after-materialization

The OpenResponses frontend materializes `previous_response_id` history before calling the executor and records canonical input/output through the existing continuation observer. A proxy-local response can therefore be recorded normally on the A-leg. When a later request materializes that history, the core non-forwardable projection removes the tagged local input/reply before B-leg preparation.

Correctness should not depend on rewriting continuation history or teaching each frontend how to delete local messages. Dedicated tests are still required for full-history replay and `previous_response_id` chains.

## Design Implications

### Message identity must be semantic and versioned

The stored key should be `version + SHA-256 digest` over a deterministic semantic projection of one canonical message. The projection should:

- include role and ordered semantic content;
- normalize CRLF/CR to LF while otherwise preserving text whitespace and Unicode;
- deterministically canonicalize JSON/opaque structured content before hashing;
- ignore item ID, item status, assistant phase, generated positional IDs, message metadata, session/route fields, and transport/cache wrappers;
- produce the same identity for equivalent legacy `Message` and `ItemKindMessage` representations.

Ignoring transient IDs is required because many agents reconstruct history and because `NormalizedItems` generates local positional IDs for legacy messages.

### Duplicate semantic messages intentionally share disposition within one A-leg

Without a stable end-to-end client message ID, two role/content-identical messages cannot be distinguished reliably after arbitrary transcript reconstruction. The safe deterministic behavior is that identical semantic messages in the same A-leg share the same never-forward classification.

For the intended sources this is desirable: repeating the exact same local command or proxy-generated notice does not make it backend-relevant.

### Classification must be committed before local output becomes visible

The core causal invariant is:

> a message designated never-forward is not eligible for client release until its A-leg tag commit succeeds.

This makes one tag snapshot per later logical turn safe even with durable/shared continuity: a client cannot legitimately replay a proxy-local message before it was released, and release occurs only after the tag is committed.

Current-turn successful registrations are also merged into the request-local guard set so final candidate enforcement sees them without another store round trip.

### One durable snapshot per turn, not per B-leg

The standard runtime should load the bounded A-leg tag set once after authority is known for a normal backend turn. The snapshot is carried in `preparedRequest` and reused for early projection and all candidate attempts. No per-B-leg database lookup, watcher, polling loop, or indefinite process cache is required.

A hard per-A-leg tag cap makes snapshot memory/work bounded. A proposed initial cap of 4096 unique identities keeps even pathological sessions bounded while allowing far more local control/notification messages than normal sessions should produce.

### Local-turn matching should be two-phase

The future interactive-command implementation must not execute a server-side state mutation and only afterward discover that the triggering message could not be protected.

The generic local-turn contract should therefore separate:

1. `Match`: pure detection/claim that identifies normalized input message indexes to mark never-forward; and
2. `Handle`: local execution/reply production, called only after core has successfully persisted the claimed source tags.

The first matching handler claims the turn. After claim, failures never fall back to a B-leg. The returned reply is validated and tagged before the canonical local response stream is exposed.

No interactive parser or handler is part of this spec; tests use a deterministic fake/reference handler only.

## Rejected Approaches

### Client/message metadata flag

Rejected because metadata is not guaranteed to round-trip and is explicitly non-wire in `lipapi.Message`.

### Regex or marker-only stripping

Rejected as the enforcement authority. Text markers may be useful to a future producer for UX/provenance, but correctness must derive from server-owned semantic identity and A-leg state.

### Frontend-specific filtering

Rejected because it duplicates policy across OpenAI/Anthropic/Gemini/OpenResponses/etc. and still leaves internal/retry paths exposed.

### Backend-adapter filtering

Rejected because provider adapters are too late for route/context/billing decisions and would create a backend Cartesian maintenance problem.

### Final-boundary-only filtering

Rejected because local-only history would still affect token/context estimates, policy transforms, routing, and billing preflight.

### Early-filter-only enforcement

Rejected because per-attempt transforms/shaping run later. A final wire guard is a cheap defense-in-depth invariant.

### Process-local registry only

Rejected because durable A-leg continuity can survive restart and can be shared through PostgreSQL. Losing tags while the client transcript survives would violate the feature.

### Extending base `b2bua.Store`

Rejected because this feature is an optional focused continuity capability and should not force public continuity compatibility churn.

### Partial-message/substring rewriting in v1

Rejected because it reintroduces parser/formatting ambiguity and can corrupt multipart/tool/item semantics. The canonical unit in this spec is a complete message. Producers must keep local-only content in standalone messages.

## Recommended Architecture

Use a focused `internal/core/nonforwardable` domain/application capability with:

- versioned semantic message identity;
- bounded append-only A-leg registry;
- memory and Bun continuity adapters;
- one per-turn immutable tag snapshot;
- pure call projection plus validation;
- final candidate wire guard;
- request-bound registrar for trusted producer stages.

Add a small `pkg/lipsdk/localturn` extension contract and FeatureBundle contribution that proves backend-free client-visible responses can use this capability safely. The extension is generic and contains no command syntax or command-specific dependencies.

This architecture is the smallest change that creates a reusable, enforceable A-leg/B-leg separation instead of another command-specific text filter.