# Final Spec Review

## Scope Reviewed

Final pass covers:

- `spec.json`
- `research.md`
- `requirements.md`
- `gap-analysis.md`
- `design.md`
- `design-review.md`
- `tasks.md`

The review was repeated after adding the persistent backend-only steering requirement.

## Final Scope Statement

The spec now defines one reusable **A-leg conversation-view projection** with two visibility directions:

1. **client-visible / backend-hidden (`never_backend`)**
   - client/agent sees and can replay the message;
   - proxy stores a semantic identity;
   - every B-leg projection removes it.

2. **backend-visible / client-hidden persistent steering**
   - client/agent never sees or persists the message;
   - proxy stores the complete bounded canonical message and stable placement state;
   - every later B-leg projection reconstructs it.

The spec does **not** implement any producer-specific application logic:

- no interactive command grammar/handlers;
- no `!/set`;
- no routing-setting command;
- no Quality Verifier logic/model call/scheduler;
- no quota-notification thresholds/policy/scheduler;
- no generic async notification service.

## Cache-Friendliness Review

**Decision: PASS**

The revised design treats placement as durable state and explicitly rejects moving-tail reinjection.

For a mid-session overlay activated after `U_N`:

```text
activation:
... U_N, STEERING

subsequent:
... U_N, STEERING, A_N, U_N+1
```

This makes unchanged steering part of append-only model-visible history.

The spec also supports `stable_prefix` for guidance that belongs near the session's static instructions.

Hard cache rules are explicit:

- unchanged overlay revision → same role/text/anchor/order;
- no per-turn timestamps/nonces/trace IDs in model-visible steering;
- no current-tail fallback;
- create/replace/move/deactivate is an explicit cache discontinuity;
- after that discontinuity, unchanged turns must stabilize again;
- anchor loss after compaction uses deterministic stable-prefix fallback or fail-closed;
- core does not manipulate provider `PromptCacheKey`, TTL, `cache_control`, or explicit cache resources.

The requirements and tasks include exact-prefix canonical regression tests across at least three growing turns plus bounded OpenAI/Anthropic/Gemini-family translation sentinels.

## Requirements Completeness Review

**Decision: PASS**

Coverage includes:

- representation-neutral semantic identity;
- coherent A-leg state snapshot;
- exclusion tag persistence;
- tag-before-release/source-side-effect ordering;
- early backend-effective projection;
- final shared reassertion;
- local successful turns;
- canonical local streams;
- persistent steering payload/anchor/slot/revision;
- stable-prefix and fixed activation-boundary placement;
- cache-prefix invariants and explicit discontinuities;
- continuation/full-history replay;
- client/PTB evidence separation;
- shared PostgreSQL/restart/reload behavior;
- observability/security/bounds;
- performance/race/architecture/quality gates.

No requirement relies on a future follow-up to make the infrastructure correct.

## Brownfield Gap Review

**Decision: PASS**

Every P0 gap has a disposition:

- no identity/registry → conversation-view identity/store;
- no early projection → runtime base projection;
- no final safety boundary → candidate-open reassertion;
- no local success → two-phase local-turn stage;
- no hidden steering state → full bounded overlay persistence;
- no producer writer → narrow steering writer;
- moving-tail cache break → fixed placement;
- no stable anchor → semantic identity + occurrence ordinal;
- anchor loss → explicit fallback/fail policy;
- late transform corruption → final reassertion;
- no cache tests → prefix invariants + adapter sentinels;
- memory/Bun missing state → standard optional capability;
- shared-process stale state → per-logical-turn snapshot.

## Architecture Review

### SOLID

**PASS**

- SRP: identity, state, projection, local application behavior, writer, runtime sequencing and persistence remain distinct.
- OCP: new producers use SDK seams; new frontends/backends inherit canonical projection.
- LSP: Memory/Bun state contracts and local EventStream substitutions are testable.
- ISP: Reader, Tagger, SteeringWriter and local Handler are narrow.
- DIP: runtime/producers depend on ports; core imports no provider SDK.

### Hexagonal

**PASS**

- core owns A-leg/B-leg policy;
- client frontends/trusted producers drive it;
- persistence is a driven adapter;
- provider adapters remain translation/cache-policy owners;
- construction is explicit;
- no DI container/global service locator.

### Cache ownership

**PASS**

Canonical history stability is core-owned; provider cache implementation/TTL/breakpoint/residency remains provider/backend-owned.

## Task Plan Review

**Decision: PASS**

The plan is TDD-first and contains 20 bounded tasks across five phases:

1. RED contracts.
2. Domain + memory/Bun persistence + producer services.
3. Base runtime projection + local success/frontend certification.
4. Final B-leg reassertion + adversarial path/cache/translation tests.
5. Continuation/reload/observability/performance/docs/final gates.

Each task contains no more than five concrete actions and includes boundary, dependencies, validation and requirement traceability.

No task implements a concrete interactive command, verifier, quota notification, or provider cache policy.

## Implementation Surface Review

Expected implementation zones are bounded to:

- `internal/core/conversationview`
- `internal/core/runtime`
- focused additions to `internal/core/b2bua`
- `internal/core/continuity/bunstore`
- `pkg/lipsdk/nonforwardable`
- `pkg/lipsdk/localturn`
- `pkg/lipsdk/steering`
- FeatureBundle/runtime composition
- focused frontend/backend-family contract tests
- docs/architecture tests

No pairwise translator or provider-specific state store is planned.

## Final Decision

**GO FOR DESIGN READINESS**

The added steering requirement is now first-class and cache-aware. The spec is final for the infrastructure it claims to deliver.

The implementation gate remains maintainer approval. `spec.json` intentionally keeps all approvals and `ready_for_implementation` false.
