# Design Review: billing-host-composition

## Design Review Summary

The design is an extension of the merged billing engine, not a second money path. Injection-only fences, a single catalog for admit and rate, a narrow provisioner port, and stock identity from scope context plus authoritative session match the gap analysis and the locked product decision. The one material hole found during review — `PostTurnWorker` requiring `RatingInput.Authorization` from the resolver — was closed with `AuthorizationLookup` and `JoinRatingResolver` before this GO.

## Critical Issues

None remaining after the hold-lookup join was added to `design.md`.

## Design Strengths

- Dependency direction is explicit: `billingcompose` does not import `runtimebundle`; `lipstd` does not call `ComposeBilling`; public Options stay non-money.
- Ports stay narrow (`AccountProvisioner`, `AuthorizationLookup`) instead of bloating `AuthoritativeBilling`.

## Final Assessment

**GO**

Rationale: Requirements 1–8 map to concrete files and contracts; worker internals and stream/TUR/rating formulas stay frozen; remaining risk is implementation size of the BuildHost host-loop test, which is acceptable with SQLite and a stub backend.

Next steps: generate `tasks.md` from this validated design.
