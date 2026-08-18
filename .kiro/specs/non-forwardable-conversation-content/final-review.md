# Final Spec Review

## Review Scope

This final pass checks the complete `non-forwardable-conversation-content` SDD after requirements gap analysis, design validation, and task generation. It verifies internal consistency, implementation completeness, brownfield alignment, and the explicit scope guard requested by the maintainer.

Repository baseline: `main` at `b54982384840ba85c0af2a019ccc35becdd63f10`.

## Cross-Artifact Consistency

**Decision: PASS**

- `requirements.md` defines one feature: durable A-leg whole-message never-forward classification plus canonical enforcement/local-turn plumbing.
- `gap-analysis.md` maps every required capability to current assets/gaps and rejects metadata-only, regex-only, process-local, and single-boundary designs.
- `design.md` assigns each responsibility to a concrete existing/new package boundary and preserves client truth separately from backend projection.
- `design-review.md` records the NO-GO findings that forced semantic identity, durable continuity, early+final enforcement, and source-tag-before-handle ordering.
- `tasks.md` schedules implementation/tests/docs for every final requirement and contains no production interactive command or quota-notification work.

## Scope Guard Review

**Decision: PASS**

The final spec DOES schedule:

- generic replay-stable non-forwardable identity;
- A-leg-scoped registry and persistence;
- early backend call projection;
- final backend wire enforcement;
- trusted registrar plumbing;
- generic local-turn Match/Handle extension point;
- generic local assistant text response stream;
- frontend/continuation/reload certification;
- observability/security/performance/docs.

The final spec DOES NOT schedule:

- `!/` detection/parser;
- `set`, `unset`, `help`, model/route commands;
- interactive command state or handlers;
- session routing mutation for a command;
- quota thresholds/usage policy;
- quota notification generation/scheduling;
- asynchronous notification delivery;
- partial-message regex/substring stripping.

A future interactive-command implementation should only need to contribute a `localturn.Handler` and its own command-owned state/service ports. It should not need to change frontend/backend adapters, message identity, registry persistence, backend projection, or the PTB/open guard.

A future standalone quota/status message producer can reuse the same trusted non-forwardable Registrar/tag-before-release invariant; producer-specific response-injection policy remains outside this spec.

## Requirements Coverage Review

| Requirement | Implementation tasks | Status |
|---|---|---|
| 1 Canonical identity | 1, 5, 12 | Covered |
| 2 A-leg registry | 2-4, 13 | Covered |
| 3 Tag-before-release | 2, 6, 9 | Covered |
| 4 Early backend projection | 5, 10 | Covered |
| 5 Final backend guard | 6, 11 | Covered |
| 6 Local-turn seam | 7-9 | Covered |
| 7 Local EventStream | 8-9, 12 | Covered |
| 8 Replay/continuation | 10, 12-13 | Covered |
| 9 Observability/security | 6, 9-11, 14 | Covered |
| 10 Performance/lifecycle/compatibility | 2-4, 7, 10-11, 13, 15 | Covered |
| 11 TDD/docs/quality | 1-15 | Covered |

No final acceptance criterion is intentionally deferred to a follow-up spec.

## Brownfield Ordering Review

**Decision: PASS**

The final runtime sequence is intentionally asymmetric:

1. preserve/authenticate A-leg client truth;
2. run secret/submit acceptance and CTP evidence;
3. allow a generic local-turn Match;
4. if claimed, tag source before Handle and tag reply before release;
5. otherwise load one A-leg tag snapshot and derive the early B-leg projection;
6. run backend-oriented transforms/routing/billing from the filtered call;
7. apply late candidate shaping/transforms/adaptation;
8. re-enforce at final `wireCall`;
9. only then emit PTB and invoke backend.

This ordering closes both leakage and cost/context distortion without making persistence mutable per B-leg.

## SOLID / Hexagonal Review

**Decision: PASS**

- Core policy is provider-neutral and does not depend on Bun/frontends/backends.
- Persistence implements a focused core port.
- Feature plugins depend on SDK application ports.
- Base continuity and canonical wire contracts stay narrow.
- Runtime owns only sequencing/orchestration.
- Local-turn is a dedicated use-case seam rather than an overloaded SubmitHook error path.
- No DI container, reflection registry, global mutable authority cache, or pairwise translator is introduced.

## Safety Review

**Decision: PASS**

The spec has explicit fail-closed behavior for:

- registry snapshot uncertainty;
- tag persistence/capacity failure;
- invalid local-turn source selection;
- local handler failure after claim;
- reply tag failure;
- invalid/no-content early projection;
- late final-guard validation failure.

The two strongest invariants are testable:

1. local-only output cannot be released before its tag commit;
2. PTB/backend open cannot occur before final enforcement succeeds.

## Performance Review

**Decision: PASS**

- one bounded registry snapshot per normal logical turn;
- maximum 4096 identities per A-leg;
- no per-B-leg durable read;
- no polling/watcher/invalidation cache;
- in-memory final guard reused by failover/race/interleaved attempts;
- explicit benchmark/race tasks are scheduled.

## Final Polish Applied

The final artifacts were tightened to:

- use “conversation message”/“whole-message” consistently instead of command-specific terminology;
- make `never_forward` the only v1 disposition and avoid speculative conditional scopes;
- separate future producer policy from reusable registrar/enforcement infrastructure;
- require a two-phase local-turn handler so claimed source tags precede local execution;
- state that continuation history remains A-leg truth rather than becoming a second enforcement store;
- explicitly cover item-reference cleanup and no-forwardable-content failure;
- preserve FeatureBundle schema v1 and base continuity interfaces;
- include implementation tasks for both memory and durable PostgreSQL/SQLite paths, frontend replay, generation reload, observability, race/performance certification, and documentation.

## Final Decision

**GO FOR SPEC REVIEW / IMPLEMENTATION READINESS AFTER MAINTAINER APPROVAL.**

The SDD is complete and internally consistent. `spec.json` intentionally keeps artifact approvals and `ready_for_implementation` false because this is a spec-only PR and normal maintainer review remains the implementation gate.