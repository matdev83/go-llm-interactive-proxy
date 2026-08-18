# Design Validation Review

## Review Method

The design was validated as a brownfield canonical/runtime/continuity/extension change against:

- root `AGENTS.md` and `.kiro/AGENTS.md`;
- `.kiro/steering/structure.md` and testing guidance;
- Go-LIP `main` at `b54982384840ba85c0af2a019ccc35becdd63f10`;
- `pkg/lipapi.Call`, legacy message authority, item authority, validation and walkers;
- shared frontend `frontendpipe` execution/encoding;
- secure-session/A-leg preparation;
- submit/request/pre-request/effective-baseline ordering;
- route planning, billing/context/capability preparation;
- initial/failover/parallel/interleaved candidate-open paths;
- CTP/PTB evidence boundaries;
- B2BUA MemoryStore and Bun SQLite/PostgreSQL continuity;
- runtime route-override optional-store precedent;
- FeatureBundle/runtime snapshot composition;
- OpenResponses continuation materialization/recording;
- the later Python LIP non-forwardable identity/registry/enforcer design;
- every acceptance criterion in `requirements.md` and gap in `gap-analysis.md`.

The review used four architecture rounds. Any design that depended on non-wire metadata, filtered too late for economics, lacked a final backend safety guard, mutated base/public Store contracts, exposed untagged local output, or scheduled command-specific implementation was treated as NO-GO and revised.

## Round 1: Replay Identity and Persistence

### Assessment

**Decision: NO-GO**

The initial concept of using proxy-owned message metadata plus a scrub pass was insufficient for client transcript replay.

### Critical Issue 1: `Message.Metadata` is not replay authority

**Concern:** `lipapi.Message.Metadata` is explicitly `json:"-"`. A client/agent is not required to return it.  
**Impact:** A local command/reply would be protected in the current request but could leak on the next full-history submission.  
**Resolution:** Add deterministic versioned semantic message identity plus server-owned A-leg registry. Metadata remains optional current-turn provenance only.  
**Traceability:** Requirements 1.1-1.10, 2.1-2.12; Design D1-D5.

### Critical Issue 2: Process-local tags do not match A-leg durability

**Concern:** Durable continuation/session history can survive proxy restart while an in-memory-only deny set would disappear.  
**Impact:** Previously local-only content could become backend-visible after restart or on another process sharing PostgreSQL.  
**Resolution:** Make tags an optional A-leg continuity capability implemented by both MemoryStore and Bun; load a fresh bounded snapshot per logical turn.  
**Traceability:** Requirements 2.5-2.9, 8.5-8.6, 10.2-10.6; Design D3-D7, D19.

### Critical Issue 3: Extending base Store is unnecessary public churn

**Concern:** `b2bua.Store` is mirrored by public continuity abstractions.  
**Impact:** A feature-specific replay policy would become a mandatory public persistence compatibility change.  
**Resolution:** Follow `routeoverride.Store`/interleaved-state precedent with a focused optional internal Store capability.  
**Traceability:** Requirement 10.5; Design D4.

## Round 2: Enforcement Placement

### Assessment

**Decision: NO-GO**

The registry model was sound, but a single filtering point could not simultaneously preserve correct backend economics and defend the final wire boundary.

### Critical Issue 1: Final-only filtering is too late

**Concern:** If local history is removed only before `be.Open`, request size, context limits, route selection, policy, and billing may all include bytes/tokens the provider never receives.  
**Impact:** Incorrect routing/cost/admission decisions and false context-limit failures.  
**Resolution:** Add early B-leg projection before backend request/pre-request transforms and all backend-oriented route/context/billing/capability work.  
**Traceability:** Requirement 4; Design D8-D9.

### Critical Issue 2: Early-only filtering is not a hard safety boundary

**Concern:** Per-attempt transforms and interleaved shaping occur after baseline construction.  
**Impact:** A tagged local message could be reintroduced after the early pass.  
**Resolution:** Add a second mandatory guard on the final backend-facing call before PTB capture and `be.Open`.  
**Traceability:** Requirement 5; Design D10.

### Critical Issue 3: Two enforcement points must not become two mutable store reads per B-leg

**Concern:** Re-querying durable state on each race/failover attempt adds I/O and introduces changing policy within one logical turn.  
**Impact:** performance cost and inconsistent arms.  
**Resolution:** Load one bounded snapshot per normal logical turn and merge successful current-turn registrations into that request-local guard. All B-legs reuse it.  
**Traceability:** Requirements 2.10, 3.5, 4.2, 5.2, 10.2-10.3; Design D6-D7.

### Concurrency/cause check

**Decision: PASS after remediation.**

Tag-before-release establishes the causal ordering needed for snapshot semantics: a legitimately replayed proxy-local message cannot be received on a later client turn before its durable tag commit. Request-local additions cover messages registered after snapshot during the same turn. No watcher/cache invalidation protocol is needed.

## Round 3: Local Successful Turns

### Assessment

**Decision: NO-GO**

The generic registry/enforcer worked for historical filtering, but the original use case still lacked a canonical way to produce a successful proxy-local reply without B-leg work.

### Critical Issue 1: Submit/PreRequest rejection is not a local success response

**Concern:** Existing stages mutate or deny but cannot represent “handled locally; send a normal assistant response.”  
**Impact:** Future interactive commands would either fake errors, bypass Executor, or add frontend-specific response code.  
**Resolution:** Add a dedicated optional `localturn.Handler` FeatureBundle stage and keep it generic.  
**Traceability:** Requirement 6; Design D11-D13.

### Critical Issue 2: One-phase handler can execute before source protection

**Concern:** A future command handler could mutate server/session state and only afterward attempt to tag the triggering command message.  
**Impact:** tag-store failure leaves side effects applied while the command remains replayable/unprotected.  
**Resolution:** Make the stage two-phase: pure Match/claim identifies source message indexes; core persists those tags; only then does Handle run.  
**Traceability:** Requirements 3.3-3.4, 6.4-6.7; Design D11.

### Critical Issue 3: Reply output could race tag persistence

**Concern:** Returning an arbitrary handler EventStream lets output become visible before the core proves its reply identity was stored.  
**Impact:** immediate subsequent replay can leak.  
**Resolution:** Handler returns bounded text only; core constructs the assistant message, tags it, then constructs/releases the finite canonical stream from the same semantic content.  
**Traceability:** Requirements 3.1-3.2, 6.9, 7.1-7.7; Design D13.

### Scope validation

**Decision: PASS.**

The local-turn stage contains no command grammar or setting/routing state. Tests use fakes. A later interactive-command feature can be implemented as a consumer rather than reopening core architecture.

## Round 4: Canonical Items, Continuation, SOLID/Hexagonal, and Scope

### Requirements traceability

**Decision: PASS**

- complete-message granularity is explicit;
- semantic identity spans legacy/item authority;
- A-leg persistence is bounded/durable;
- source and reply tagging have pre-exposure order;
- early projection protects economics and context;
- final wire guard protects PTB/backend;
- local turns bypass inference routing/billing/B-legs;
- continuation remains A-leg truth and is filtered after materialization;
- existing frontends encode canonical local streams;
- no client tag authority, provider logic, regex fallback, or command implementation is introduced.

### Canonical item review

**Decision: PASS with explicit constraint.**

Removing only complete `ItemKindMessage` values avoids corrupting tool/reasoning/compaction semantics. In-call `item_reference` values targeting removed concrete IDs are removed as dependent references, and the projected call is revalidated. Opaque out-of-call references contain no message body and remain under existing item-reference/continuation rules.

### Continuation review

**Decision: PASS.**

OpenResponses resolves/materializes parents before executor entry. Correctness therefore remains core-owned: materialized local input/replies are ordinary concrete message items that the early projection removes. Continuation storage does not become a second policy store and requires no schema mutation for this feature.

### CTP/PTB audit review

**Decision: PASS.**

The design intentionally does not sanitize A-leg truth. CTP shows what the client actually submitted according to existing capture/redaction policy. PTB is emitted only after final guard. This makes the feature explainable without misattributing proxy policy as client input.

### SOLID review

**Single Responsibility — PASS**

- identity answers message equivalence only;
- registry stores A-leg classifications only;
- projector/enforcer derives safe backend calls only;
- local-turn handlers decide local application behavior;
- runtime owns sequencing;
- continuity adapters own persistence;
- frontends/backends keep translation responsibilities.

**Open/Closed — PASS**

- new frontends/backends inherit enforcement through canonical Executor boundaries;
- future local features add handlers/producers rather than new filtering implementations;
- identity versioning provides an explicit evolution path.

**Liskov Substitution — PASS**

- Memory and Bun stores satisfy one registry semantic contract;
- a local EventStream is consumed by the same frontend encoding abstraction as a backend stream.

**Interface Segregation — PASS**

- base B2BUA/public continuity Store stays unchanged;
- persistence Reader/Tagger are focused;
- producer Registrar exposes no query/delete authority;
- local-turn contract exposes only Match/Handle needs.

**Dependency Inversion — PASS**

- runtime depends on core ports, not Bun;
- feature plugins depend on SDK contracts, not core store implementation;
- persistence adapters implement core-owned ports;
- provider/frontends do not own policy.

### Hexagonal boundary review

**Decision: PASS**

- core identity/registry has no provider/front-end imports;
- local-turn is a driving application extension;
- continuity is a driven adapter;
- process/generation composition is explicit and immutable;
- no DI container, reflection registry, global service locator, or reactive graph is introduced.

### Performance review

**Decision: PASS**

One bounded tag snapshot per logical backend turn is acceptable and much cheaper/clearer than per-B-leg lookups or a distributed invalidation cache. The 4096-tag cap bounds memory/DB work. Final enforcement is in-memory. No background worker is needed.

### Scope/generalization review

**Decision: PASS.**

The design is not command-specific: reason codes and semantic tags are generic, and a trusted future producer can use the Registrar. At the same time it does not speculate into arbitrary fragment surgery or asynchronous notification policy. A future quota/status feature must emit its local-only notice as a standalone message and can reuse the same registration/enforcement invariant.

## Final Brownfield Compatibility Review

**Decision: PASS**

No-tag/no-claim behavior preserves existing canonical/routing/streaming/accounting semantics with only bounded snapshot/projection overhead.

No implementation requires:

- a new `lipapi.Call`/Event wire field;
- frontend/backend-specific filtering;
- base Store expansion;
- new provider SDK dependencies;
- routing grammar changes;
- billing model changes;
- continuation record rewrite;
- retry after output;
- command syntax/handlers.

FeatureBundle receives one additive optional v1 contribution. Standard continuity adapters gain a focused optional capability. Stored tags remain enforceable even if a later generation removes all producers.

## Testing Review

**Decision: PASS**

The design requires RED contracts before implementation and covers the highest-risk failures:

- semantic identity drift across canonical/front-end representations;
- tag batch/capacity/persistence races;
- source-tag-before-handle and reply-tag-before-release;
- client truth vs backend projection;
- final late-transform reintroduction;
- all B-leg planning modes;
- OpenResponses materialized replay;
- durable restart/shared store;
- generation reload/producer removal;
- no handler fallback after claim;
- no base/public API drift.

## Final Assessment

**Decision: GO FOR DESIGN READINESS**

The final design creates a small reusable A-leg/B-leg separation primitive rather than reviving command-specific text rewriting. Its most important simplification is that **local-only history remains part of A-leg truth and is projected out, not destructively edited out of the session**. Durable semantic identity solves replay; the early pass preserves correct economics; the final guard provides a real exfiltration boundary; the two-phase local-turn seam makes future interactive commands an ordinary consumer.

The spec intentionally stops before implementing any interactive command or quota notification producer.

## Implementation Gate

This PR is spec-only. `spec.json` keeps approvals and `ready_for_implementation` false so implementation begins only after normal maintainer review/approval of the requirements, design, and task plan.