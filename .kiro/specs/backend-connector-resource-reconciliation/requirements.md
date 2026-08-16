# Requirements Document

## Introduction

Go-LIP shall reduce unnecessary reconstruction of expensive executable backend connector resources across immutable runtime generations when a material configuration reload does not change those connectors. The optimization shall preserve the existing generation publication model: every request remains bound to one immutable `GenerationRuntime`; changed resources are constructed before publication; old generations and their resources remain valid until their existing work drains; candidate failure leaves the last-good generation untouched.

This specification borrows only the Cordis-v4 ideas that fit this problem: semantic provider identity, reconciliation of unchanged desired resources, and lifetime retention while dependents still hold the provider. It does **not** introduce a Cordis component runtime, reactive dependency graph, fibers, dependency injection, service location, HMR, or generic effect system.

The first implementation is intentionally narrow. It targets discovered executable backend connectors whose declared process model is `per_instance` and whose current reload policy permits candidate/active overlap. Installed-but-disabled connectors, statically linked backends, and `shared_artifact` connectors are not justification for this refactor and remain outside the first implementation.

## Boundary Context

- In scope: deterministic scale evidence, private physical connector-resource identity, process-lifetime reconciliation, per-generation leases, candidate rollback, invalidation/incarnation behavior, shutdown ordering, and focused executable-connector integration.
- Out of scope: general component reconciliation, frontend/feature reconciliation, dynamic plugin install/uninstall, discovery watchers, new backend-plugin ABI fields, new public configuration knobs, built-in backend pooling, `shared_artifact` behavior changes, request migration between generations, or live mutation of a published backend instance.
- Existing authorities remain: `ProcessServices` for process-owned resources, `ResourceLedger` for generation-owned cleanup, `processhost.Host` for executable process/IPC supervision, runtimehost generation leases for request/async lifetime, and the existing backend-plugin ABI for connector behavior.
- Performance intent: eliminate redundant connector construction work during material reloads. This is not a request-hot-path optimization.

## Requirement 1: Evidence-First Scale Justification

1.1. Before production reuse is enabled, add a deterministic high-cardinality characterization harness that compiles overlapping generations with at least 100 enabled synthetic host-backed `per_instance` connector instances.
1.2. The harness shall count at minimum physical connector builds, `processhost` activations or launches, configure operations, and physical cleanup operations; timing/allocation benchmarks may supplement but shall not replace these deterministic counters.
1.3. The baseline shall demonstrate the current reconstruction behavior for an unrelated material reload in which connector-defining inputs are unchanged.
1.4. After implementation, an unrelated material reload with `N` unchanged eligible live connectors shall perform **zero** new physical connector builds, activations/launches, and configure operations for those `N` connectors; it may perform `N` lightweight lease acquisitions.
1.5. For a candidate with `K` changed eligible connector identities and all remaining eligible connectors unchanged, the target is exactly the necessary replacement construction for the changed set; unchanged identities shall be leased rather than rebuilt.
1.6. Test acceptance shall not depend on fixed wall-clock thresholds that are vulnerable to CI host variance. Benchstat or equivalent measurements may be recorded as supporting evidence.
1.7. If the implementation cannot achieve the count-based reduction without changing request semantics or introducing a general runtime/container abstraction, the implementation shall be stopped or re-scoped rather than preserving the abstraction for speculative future value.

## Requirement 2: Narrow Eligibility and Ownership Boundary

2.1. Reconciliation shall be private to runtime composition and shall not become a public SDK capability or a request-time service locator.
2.2. The initial eligible set shall be discovered executable connectors using `ProcessModelPerInstance` and an overlap-safe reload policy.
2.3. Built-in/in-process backend factories shall retain current generation ownership unless a later evidence-backed specification separately justifies reuse.
2.4. `ProcessModelSharedArtifact` connectors shall retain their current explicit sharing/restart-required semantics; this specification shall not weaken their isolation or overlap gates.
2.5. Plugin discovery/trust shall remain startup-fixed. Connector installation, removal, directory rescanning, executable upgrade discovery, and automatic file watching are not added by this specification.
2.6. `processhost.Host` shall remain the sole executable process/IPC supervisor; the reconciliation layer shall not duplicate launch, peer authentication, process-tree cleanup, slot management, or transport supervision.
2.7. No new public YAML field, manifest field, CLI flag, environment variable, or backend-plugin ABI field shall be required merely to turn this optimization on.

## Requirement 3: Semantic Physical Resource Identity

3.1. Reuse shall require an exact private identity representing the configured physical connector resource, not Go object equality and not only the logical backend instance ID.
3.2. At minimum, the identity shall distinguish logical instance ID, factory kind, exact verified executable artifact digest, process model, opaque connector configuration content, effective configure-time runtime policy, and configure-time secret material by a non-reversible fingerprint.
3.3. Identity construction shall be deterministic for semantically identical effective inputs within one process.
3.4. Secret plaintext shall never be retained in the identity, logged, emitted in diagnostics, or exposed through public status. Secret fingerprinting shall be local and deterministic for equality purposes only.
3.5. Executable artifact replacement shall produce a distinct resource identity even when logical instance ID and YAML configuration are unchanged.
3.6. Credential rotation or another configure-affecting secret change shall produce a distinct resource identity.
3.7. Runtime-policy changes that affect the configured connector shall produce a distinct resource identity.
3.8. `BackendStateIdentity` may provide precedent or low-level hashing helpers, but its current `{InstanceID, FactoryKind, ConfigDigest}` contract shall not be treated as sufficient proof that two physical connector resources are interchangeable.

## Requirement 4: Process-Scoped Acquire and Generation Lease Contract

4.1. An eligible physical connector resource shall have one process-scoped lifetime owner and zero or more generation leases.
4.2. Acquiring an exact live identity that is already current shall reuse the existing immutable configured resource and increment its dependent lease count rather than invoking the connector factory/process activation path again.
4.3. Concurrent acquisitions for the same absent identity shall construct at most one physical current resource; successful contenders shall receive leases to that same resource.
4.4. A generation shall own only its lease release through its existing `ResourceLedger`; generation rollback/retirement shall not directly own or close a shared physical connector resource.
4.5. Releasing one of several leases shall not close the physical resource. Releasing the final lease shall detach the resource and invoke its physical cleanup exactly once.
4.6. There shall be no idle TTL/cache-retention policy in the first implementation. A valid current physical resource exists only while at least one generation lease retains it.
4.7. A failed physical build/configure shall not be cached as a permanent negative result; a later independent acquisition may retry through the existing construction path.
4.8. Lease release shall be idempotent and safe under candidate rollback, generation retirement, and process shutdown races.

## Requirement 5: Preserve Immutable Generation Semantics

5.1. Published `GenerationRuntime` objects remain immutable; no connector resource shall be reconfigured or replaced underneath a published generation.
5.2. If connector identity changes, the candidate shall construct a distinct replacement resource before publication while the old generation retains its old resource until its leases drain.
5.3. If a connector is removed or disabled, the candidate shall acquire no replacement lease; old generations may continue using the removed connector until their existing leases drain.
5.4. A failed candidate that leased an existing resource shall release only its candidate lease and shall not disturb the active generation's lease/resource.
5.5. A failed candidate that created a new resource shall release it through rollback; if no other lease exists, physical cleanup shall run before rollback completes.
5.6. Existing generation-owned derived state—including executor maps/views, routing views, model-registry runtime, model catalog, feature composition, policy state, and generation lifecycle context—shall remain generation-owned and shall not be moved into the connector resource pool.
5.7. Existing no-drop, retained-generation, old-stream, and last-good reload guarantees shall remain unchanged.

## Requirement 6: Invalidation and Resource Incarnations

6.1. Semantic resource identity and physical resource incarnation shall be distinct concepts: the same semantic identity may need a later fresh physical incarnation after failure.
6.2. When the existing connector/process invalidation path declares a physical resource generation unusable, that exact resource incarnation shall become non-acquirable for future candidates.
6.3. A future acquisition for the same semantic identity after invalidation shall create a fresh physical incarnation rather than returning the invalidated one.
6.4. Invalidation shall not live-swap a replacement resource into generations that already reference the failed incarnation. Their normal backend/process failure and recovery semantics remain authoritative.
6.5. An invalidation callback originating from an older/detached incarnation shall not evict or invalidate a newer current incarnation for the same semantic identity.
6.6. Existing `processhost` generation invalidation/process reap behavior shall remain authoritative for the physical process; reconciliation only controls future resource reuse eligibility and lease lifetime.

## Requirement 7: Cleanup, Shutdown, and Error Preservation

7.1. There shall be exactly one physical cleanup owner for each configured connector resource. A generation-specific closer shall never be capable of closing a physical resource still leased by another generation.
7.2. The process-scoped reconciliation owner shall close before `processhost.Host` during normal process teardown so remaining physical resource cleanup can still use the live host/session ownership path.
7.3. Process shutdown shall reject new resource acquisitions, be idempotent, and fail-safe close any residual physical resources after generation drain without creating a second process shutdown coordinator.
7.4. Verified artifacts and staging resources shall retain their existing later teardown ordering after all connector resources and `processhost.Host` are done with them.
7.5. Existing cleanup-error normalization and error-join behavior shall be preserved. Lease-triggered final physical cleanup errors shall surface through the existing generation rollback/close path rather than a new public error category.
7.6. Partial construction failure shall not leak sessions, host instances, processes, IPC connections, or leases.

## Requirement 8: Connector Semantics and Non-Interference

8.1. Reuse is valid only for a configured connector resource whose construction inputs are fully represented by the physical resource identity. If the implementation cannot prove identity completeness for an eligible path, that path shall fall back to current generation-local construction rather than risk unsafe reuse.
8.2. Dynamic provider facts that are already modeled through runtime calls such as `Resolve`, `ListModels`, health/readiness, or normal execution shall continue through those calls and shall not require connector reconstruction on unrelated Go-LIP generation changes.
8.3. The optimization shall not alter canonical requests/events, route selection, retries, failover, output-commit rules, streaming order, cancellation, billing finalization, accounting evidence, token counting, or provider-specific semantics.
8.4. No resource-pool lookup or lock shall be added to the normal request execution hot path; acquisition/release occurs at generation construction/retirement boundaries.
8.5. Existing backend-plugin security invariants—verified artifact binding, secure local IPC, peer authentication before secrets/configure, environment restrictions, and process-tree cleanup—shall remain unchanged.
8.6. Old and new generation behavior shall remain observationally equivalent to the current overlap model for unchanged resources except that unnecessary physical connector reconstruction is removed.

## Requirement 9: TDD, Concurrency, and Architecture Gates

9.1. Add RED tests for high-cardinality construction counts, identity discrimination, lease lifetime, rollback, invalidation/incarnation behavior, concurrency, and shutdown ordering before enabling production reuse.
9.2. Test concurrent acquire of one absent identity, concurrent release, release racing invalidation, candidate rollback while an old generation is retained, and a fresh acquire after invalidation.
9.3. Test unchanged reload, changed same-ID config, artifact change, secret change, policy change, remove/disable, candidate failure, and process shutdown.
9.4. Add race/goleak coverage for the private reconciliation owner and relevant executable connector lifecycle integration.
9.5. Architecture tests shall reject a generic service registry/container API, request-time lookup surface, public reusable-resource framework, provider-specific switch, or migration of `processhost` supervision responsibility.
9.6. Repository quality, focused executable-plugin conformance/security tests, and existing reload no-drop/rollback tests shall remain green.
9.7. Final implementation review shall remove unused abstraction layers and preserve the smallest private design that satisfies the deterministic scale and correctness gates.
