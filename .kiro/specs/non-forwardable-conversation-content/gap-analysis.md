# Brownfield Requirements Gap Analysis

## Scope and Method

This analysis validates the final requirements for `non-forwardable-conversation-content` against Go-LIP `main` at `b54982384840ba85c0af2a019ccc35becdd63f10` and compares the intended behavior with the later non-forwardable architecture in the Python LIP lineage.

The review covers:

- canonical legacy/message/item authority in `pkg/lipapi`;
- shared frontend decode/execute/encode composition;
- secure-session/A-leg authority;
- submit/request/pre-request ordering and effective baseline construction;
- route planning, billing/context/capability preparation;
- initial/failover/parallel/interleaved candidate opening;
- CTP/PTB traffic evidence;
- B2BUA memory continuity and Bun SQLite/PostgreSQL durability;
- immutable FeatureBundle/runtime generation composition;
- OpenResponses continuation materialization/recording;
- existing extension contracts and public SDK compatibility constraints;
- current interleaved-thinking proxy message metadata;
- repository TDD/hexagonal architecture rules.

Classifications:

- **Missing** — required capability does not exist.
- **Partial** — useful infrastructure exists but does not satisfy the requirement by itself.
- **Constraint** — existing ownership/API/lifecycle behavior constrains the implementation.
- **Risk** — current behavior can create leakage or semantic drift unless explicitly protected.

## Current Assets Worth Preserving

### Canonical request authority already eliminates frontend Cartesian work

`lipapi.Call` already represents all frontend requests in a provider-neutral form. `NormalizedItems` gives one traversal view across legacy and item authority. Non-forwardable matching should therefore happen after frontend decode, not independently in each OpenAI/Anthropic/Gemini/OpenResponses adapter.

### A-leg authority is established before backend planning

Secure-session `BeginTurn` resolves a proxy-owned A-leg and `FetchALeg` occurs before route planning. This is the correct registry partition and avoids trusting client session carriers.

### Client truth and backend-effective truth are already conceptually separate

Runtime route overrides already preserve client evidence while building a separate effective routing call before baseline freeze. The non-forwardable feature can use the same conceptual split: retain A-leg/CTP truth; derive a sanitized B-leg projection.

### The backend open path is centralized

All planned candidate execution reaches the shared `openPlannedCandidate` flow, which eventually constructs `wireCall`, emits PTB capture, and calls `be.Open`. That is a materially stronger final enforcement seam than the old Python architecture, where direct backend calls had to be audited/refactored.

### Continuity already supports focused optional capabilities

`b2bua.InterleavedStateStore` and `routeoverride.Store` demonstrate the correct pattern for feature-specific A-leg persistence without widening base/public Store interfaces. Memory and Bun can implement another small optional capability.

### Frontends already encode any canonical EventStream

A backend-free local response does not need a frontend-specific synthetic protocol implementation. The shared frontend pipe expects `Executor.Execute` to return `lipapi.EventStream`; the source of those canonical events is intentionally abstract.

### OpenResponses materializes continuation before executor entry

`previous_response_id` replay becomes a concrete canonical call before the core runtime prepares the B-leg, allowing the same core filter to remove local-only historical input/output.

## Gap Register

| ID | Severity | Class | Effort | Current finding | Required disposition |
|---|---:|---|---:|---|---|
| G-01 | P0 | Missing | M | No semantic replay identity exists for canonical message units. | Add versioned deterministic message identity independent of client/proxy metadata. |
| G-02 | P0 | Constraint | S | `Message.Metadata` is proxy-owned and non-wire, so it disappears on client replay. | Keep metadata as current-turn provenance only; never use it as durable enforcement authority. |
| G-03 | P0 | Partial | M | `NormalizedItems` unifies read traversal but there is no canonical message-filter/projector that preserves the active Call authority. | Add pure legacy/item projection with dependency cleanup + `Call.Validate`. |
| G-04 | P0 | Missing | M | No A-leg registry stores never-forward identities. | Add focused append-only bounded registry capability. |
| G-05 | P0 | Constraint | M | Extending `b2bua.Store` would propagate into public continuity contracts/wrappers. | Use optional internal Store/Reader/Tagger capability like route overrides/interleaved state. |
| G-06 | P0 | Missing | M | Memory A-leg state has no non-forwardable tag set. | Add bounded tag state under existing leg lock/eviction lifecycle. |
| G-07 | P0 | Missing | M | Bun schema has no tag table. | Add SQLite/PostgreSQL migration with A-leg FK/cascade and transactional batch writes. |
| G-08 | P0 | Risk | M | Process-only tags would be lost while durable client/session history survives restart. | Persist tags with durable A-leg continuity; restart tests mandatory. |
| G-09 | P0 | Partial | M | A-leg is known before backend preparation, but current flow does not derive a sanitized history projection. | Insert one early filter after A-leg/client evidence and before backend-oriented stages. |
| G-10 | P0 | Risk | M | Filtering only immediately before `be.Open` would let local-only history influence context estimates, route/policy/cost/billing. | Require early effective projection. |
| G-11 | P0 | Risk | M | Filtering only early can be defeated by later attempt transforms/interleaved shaping. | Add final shared wire guard before PTB capture/open. |
| G-12 | P0 | Partial | S | Shared candidate open is already centralized. | Wire one final guard there and certify all initial/failover/parallel/interleaved paths. |
| G-13 | P0 | Missing | M | No extension outcome represents successful proxy-local handling with a normal assistant reply and no B-leg. | Add dedicated ordered local-turn stage; do not overload rejection hooks. |
| G-14 | P0 | Constraint | S/M | Submit hooks can mutate calls, but a future local command must tag the original replayable client message. | Preserve a deep ingress view for local-turn Match/source indexing. |
| G-15 | P0 | Risk | M | One-phase local handler could mutate server state before discovering source tag persistence failed. | Make local-turn contract two-phase: Match/claim -> tag source -> Handle. |
| G-16 | P0 | Risk | M | A proxy-generated local reply could be sent before its registry write and become unfilterable on immediate replay. | Enforce tag-before-release; reply tag failure releases no reply. |
| G-17 | P1 | Partial | S | `lipapi.EventStream` can carry local output, but no production-owned local reply stream/factory exists. | Add bounded finite canonical local text stream with normal cancellation/Close semantics. |
| G-18 | P1 | Constraint | M | OpenResponses response/continuation IDs are generated outside core and are not cross-frontend semantic identity. | Do not hash response/item IDs; certify materialized content identity instead. |
| G-19 | P0 | Risk | M | Item-authoritative calls may contain references to in-call message IDs that a filter removes. | Remove references to removed in-call IDs and revalidate; fail closed on remaining invalid dependencies. |
| G-20 | P1 | Constraint | S | Opaque out-of-call `item_reference` has no concrete message content to hash. | Keep existing reference semantics; guarantee concrete-message and in-call-reference removal only. |
| G-21 | P0 | Risk | M | A registry read failure interpreted as “no tags” would leak protected history. | Snapshot/enforcement errors fail closed before route/open. |
| G-22 | P1 | Risk | M | Reading durable tags per B-leg would add avoidable failover/race I/O and mutable policy timing. | Load one bounded snapshot per logical turn; reuse through attempts. |
| G-23 | P0 | Risk | M | Indefinite process caching would be stale with shared PostgreSQL continuity. | No authoritative global cache; read a fresh turn snapshot from continuity. |
| G-24 | P1 | Partial | S | CTP/PTB capture already separates planes. | Preserve client truth on CTP and guarantee PTB is post-enforcement. |
| G-25 | P1 | Missing | S/M | FeatureBundle has no local-turn handler contribution/accessor/runner. | Add optional schema-v1-compatible field, merge/snapshot/sort/runner wiring. |
| G-26 | P1 | Constraint | S | FeatureBundle v1 permits new optional fields without schema bump. | Keep schema version v1; update empty/Validate/merge/public-surface tests. |
| G-27 | P1 | Risk | S/M | Custom continuity stores may not implement the new capability. | Standard stores implement it; composition fails if local-turn producers are configured without required storage. |
| G-28 | P1 | Risk | M | Same semantic message can appear multiple times with no stable client ID. | Document deterministic same-identity/same-disposition behavior; role is part of identity. |
| G-29 | P1 | Constraint | M | Full semantic identity must survive legacy/item/frontend projection and structured JSON encoding differences. | Define typed semantic normalization + frontend round-trip TCKs. |
| G-30 | P1 | Risk | S | Raw local command/notification text could become high-cardinality telemetry. | Persist digest + bounded reason only; metrics use counts/category, not plaintext/digest labels. |
| G-31 | P1 | Constraint | M | Current response part hooks mutate a single event; they are not a safe general standalone-message injector. | Do not broaden response pipeline in this spec; expose reusable non-forwardable registrar contract for future trusted producer stages. |
| G-32 | P0 | Scope | — | Interactive commands are not implemented in Go and would materially broaden this infrastructure spec. | Explicitly exclude parser/handlers/settings; local-turn tests use fakes only. |
| G-33 | P1 | Scope | — | Quota-notification generation/scheduling is only an example future producer. | Do not implement policy/scheduler; require standalone local-only message granularity and reusable registrar. |
| G-34 | P1 | Risk | M | If all backend-driving content is removed, sending remaining history can trigger an unintended model continuation. | Define no-forwardable-content failure before route planning. |

## Requirements Review Round 1

### Finding R1-A: “Do not forward” cannot be a message metadata flag

**Problem:** The obvious Go shortcut is to set `Message.Metadata["non_forwardable"]`. That metadata is intentionally not serialized, so a coding agent replay loses it.  
**Remediation:** Requirements 1–3 define server-derived semantic identity plus A-leg registry; metadata remains non-authoritative provenance only.

### Finding R1-B: A one-time request scrub is insufficient

**Problem:** The future agent will resend the same local input/reply on every turn.  
**Remediation:** Requirement 2 makes classification A-leg-lifetime state and Requirement 4 re-applies it to every backend turn.

### Finding R1-C: Regex/marker matching is not a canonical policy boundary

**Problem:** Command-specific stripping repeats old Python coupling and cannot safely generalize to structured messages.  
**Remediation:** Requirement 1 defines identity over canonical whole-message semantics and Requirement 10 forbids regex fallback.

## Requirements Review Round 2

### Finding R2-A: Final-only filtering corrupts economics/routing semantics

**Problem:** If filtering occurs only at `wireCall`, old local messages still consume estimated context and influence billing/route/capability logic.  
**Remediation:** Requirement 4 introduces an early B-leg-effective projection before backend-oriented stages.

### Finding R2-B: Early-only filtering has no security backstop

**Problem:** Attempt transforms/interleaved shaping can append/replace messages after baseline preparation.  
**Remediation:** Requirement 5 mandates a final guard at the shared PTB/open boundary.

### Finding R2-C: Two store reads per B-leg are unnecessary

**Problem:** Naively enforcing again from persistence on every failover/race arm would add I/O and mutable timing.  
**Remediation:** Requirements 2, 3, 5, and 10 define a bounded per-turn tag snapshot plus immediate request-local registration of new committed tags.

## Requirements Review Round 3

### Finding R3-A: Existing hooks cannot return successful local replies

**Problem:** `SubmitHook` can mutate/reject and `PreRequestHandler` can allow/deny. Encoding a successful local result as a rejection would mix transport errors and normal output and still leave future handler writers to bypass billing/open manually.  
**Remediation:** Requirement 6 adds one dedicated local-turn extension seam.

### Finding R3-B: One-phase handler ordering could lose the source-tag guarantee

**Problem:** A future handler could change routing/session state and only afterward attempt to tag the triggering message. A persistence failure would leave durable state changed but replay protection absent.  
**Remediation:** The final local-turn contract is two-phase. Match is pure; core validates and tags claimed source messages; only then does Handle run.

### Finding R3-C: Reply tagging after output is too late

**Problem:** Even a very small timing window violates the only property that makes later one-snapshot enforcement causally safe.  
**Remediation:** Requirement 3 requires reply tag commit before any client-visible reply event.

## Requirements Review Round 4

### Finding R4-A: Whole-message granularity needed to be explicit

**Problem:** Generalizing too aggressively to arbitrary part/substrings would reintroduce parser semantics, multipart surgery, and invalid tool/item graphs.  
**Remediation:** Requirements 1.8–1.10 and scope explicitly restrict v1 to whole message units. Future producers must emit local-only information as a standalone message.

### Finding R4-B: Ordered item references can become dangling

**Problem:** Removing a tagged concrete item while retaining an in-call `item_reference` to its ID can invalidate the canonical call.  
**Remediation:** Requirement 4.6 removes dependent in-call references and Requirement 4.7 revalidates the projection.

### Finding R4-C: Continuation storage should not become a second filtering owner

**Problem:** Teaching every continuation/frontend store to physically erase local turns duplicates policy and loses A-leg truth.  
**Remediation:** Requirement 8 keeps A-leg/continuation history intact and enforces after materialization/before B-leg projection.

## Implementation Approach Options

### Option A: Proxy-only metadata flag on `lipapi.Message`

**Advantages**
- Extremely small immediate change.
- Easy local filtering in one request.

**Disadvantages**
- Metadata is not client-round-tripped.
- Cannot recognize future replays.
- Encourages frontend-specific markers/fallbacks.

**Assessment:** Rejected.

### Option B: Regex/text-marker scrub on every request

**Advantages**
- Similar to the oldest Python implementation.
- No persistence schema.

**Disadvantages**
- Command-specific and text-only.
- False positives/negatives in normal content.
- Cannot robustly identify synthetic replies.
- Duplicates parser concerns and conflicts with structured Items.

**Assessment:** Rejected.

### Option C: Process-local identity registry + final backend filter

**Advantages**
- Easy to implement.
- Backend leakage reduced within one process lifetime.

**Disadvantages**
- Loses protection across durable-session restart.
- Shared PostgreSQL/processes diverge.
- Final-only filter leaves routing/economics wrong.

**Assessment:** Rejected.

### Option D: Durable A-leg semantic registry + early projection + final guard + local-turn seam

**Shape**

- Core-owned versioned semantic message identity.
- Optional focused A-leg registry implemented by MemoryStore/Bun.
- One bounded tag snapshot per normal turn.
- Early effective-call projection before backend-oriented processing.
- Final shared candidate guard before PTB/open.
- Dedicated generic local-turn extension with source-tag-before-handle and reply-tag-before-release.
- Existing frontends encode canonical local EventStream.

**Advantages**
- Satisfies replay/durability guarantee.
- One policy owner across every frontend/backend.
- Correct economics/context/routing semantics.
- Strong final safety invariant.
- No base continuity API break.
- Future interactive commands become a feature handler rather than another Executor refactor.
- Future proxy-local message producers can reuse the same registrar/registry/enforcer.

**Disadvantages**
- Adds one new focused core capability, SDK extension point, and schema migration.
- Requires careful runtime sequencing and contract coverage.

**Assessment:** Preferred.

## Complexity and Risk

- **Effort: L (approximately 1–2 weeks of focused implementation)** — identity/store/runtime/SDK/continuation/TCK coverage crosses several established boundaries, but does not require provider-specific work.
- **Risk: Medium** — the main risk is ordering: filtering too late affects economics; tagging too late loses replay safety; local-turn fallback after claim leaks; item-authority filtering can invalidate dependencies. The final requirements make all four orderings explicit.

## Requirements Gap Analysis Result

**Decision: PASS after remediation.**

The final requirements close every P0 gap without broadening into interactive-command implementation. They preserve existing frontend/backend boundaries, use A-leg continuity as authority, retain client/audit truth, and add only the minimum extension surface required to prove backend-free local replies can safely participate in future replayable sessions.