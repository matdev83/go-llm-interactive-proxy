# Brownfield Design Validation

## Verdict

**GO after one requirements tightening.** The selected design fits the current Go-LIP runtime and connector boundaries and provides a focused path from O(N) physical connector reconstruction toward O(K changed/unusable physical reconstruction) while retaining immutable generations. The design itself already contains the necessary candidate-isolation treatment, but that safety property must also be promoted into normative requirements before task generation.

No broader Cordis runtime, processhost redesign, public ABI, or dynamic discovery work is justified.

## Validation Checklist

### Generation consistency boundary — PASS

The design preserves `GenerationRuntime` as the immutable request-plane unit. Reconciliation occurs only while constructing the backend resources used to build a new generation. Published generations are never reconfigured, rebound, or live-migrated.

Changed identity still creates a distinct physical resource before publication; removed/disabled identity is retained only by old generation leases. This preserves existing backend recomposition semantics.

### `ResourceLedger` authority — PASS

Each generation receives a fresh lease-release cleanup and existing `buildBackends` continues to transfer that cleanup into the generation `ResourceLedger`. No new generation cleanup engine exists.

The physical connector cleanup is pool-owned and therefore cannot be copied into two generation ledgers. This is the central correctness requirement for sharing.

### Process ownership — PASS

The pool is process-scoped but is not a second process supervisor. `processhost.Host` remains responsible for launch, process slots, authenticated IPC, peer identity, configured host instance ownership, generation invalidation and process-tree cleanup.

The pool only knows semantic identity, physical incarnation, backend value, physical cleanup, and dependent refcount.

### Pre-`ProcessServices` factory capture — PASS

Production discovered factories are installed before `ProcessServices` exists. The design correctly creates the pool beside `processhost.Host` during discovered-install preparation, captures it lexically in factory closures, then transfers pool lifetime into `ProcessServices`.

This avoids a service locator, global runtime registry, setter race, or post-publication dependency mutation.

### Teardown ordering — PASS

Current staging/artifact/host ownership has strict reverse-order requirements. Registering pool ownership after host ownership yields the needed relative shutdown order:

`pool -> processhost.Host -> verified artifacts -> staging`.

The design also requires equivalent bootstrap-failure ordering before ownership transfer.

### Physical identity completeness — PASS

The design rejects direct use of the narrower `BackendStateIdentity` and derives a separate key at the physical construction/configure choke point. Artifact digest, logical instance/factory identity, opaque Configure bytes, process model, runtime policy and secret fingerprint are covered.

The explicit DTO-drift gate is important: identity correctness must fail closed as connector configure inputs evolve.

### Secret handling — PASS

Secret values are only locally length-framed and hashed; no plaintext or secret-derived diagnostic value is exposed. Identity digests themselves should remain private because equality fingerprints can still be sensitive metadata.

### Incarnation-safe invalidation — PASS

The design correctly distinguishes desired semantic identity from a concrete process/session incarnation. Invalidation detaches the exact incarnation before/with existing processhost invalidation, future Acquire builds a new incarnation, and stale callbacks cannot remove a newer current entry.

Existing generations are not silently rebound to the replacement.

### `shared_artifact` exclusion — PASS

The design does not mix two independent sharing problems. Current `shared_artifact` process sharing and restart-required overlap gates remain unchanged. First-pass reconciliation is discovered `per_instance` only.

### Built-in backend exclusion — PASS

There is no evidence that cheap in-process builtins justify process-level lease machinery. Keeping them generation-owned limits complexity and avoids turning the selected connector optimization into a generic backend runtime.

### Request hot path — PASS

Resource lookup/refcounting occurs only during generation construction/retirement. Executor request calls use already-captured backend functions and introduce no pool mutex or identity hash on normal inference traffic.

### Dynamic inventory/model views — PASS with explicit constraint

The design correctly keeps `modelregistry.Runtime`, inventory projection, routing/model views and refresh-loop ownership generation-local. A reused physical session may therefore receive concurrent `Resolve`/`ListModels` calls from overlapping generations.

This is compatible with the existing configured-instance abstraction because one instance already serves runtime execution and metadata calls, but implementation tests must cover overlapping generation metadata access/race safety. Do **not** solve this by moving the model registry into the pool.

### Candidate last-good isolation — PASS after normative tightening

This is the most important design-validation finding.

Fresh physical processes currently give candidate preparation strong failure-domain isolation. Sharing intentionally reduces physical failure-domain independence for an **unchanged** connector. The design handles the acceptable boundary correctly:

- a candidate reuse hit performs no Configure/Start/Stop/Close on the shared resource;
- candidate rejection/rollback releases only its lease;
- generation-local model preparation may use query-shaped `Resolve`/`ListModels` calls;
- candidate rollback never invalidates the resource merely because the candidate failed;
- any future mutating generation-preparation lifecycle makes the resource non-shareable until separately proven safe.

The current requirements imply this through immutability/non-interference but do not state it strongly enough. Add a normative clause requiring **query-only candidate preparation on a reused physical resource** and fallback to generation-local physical construction if a mutating lifecycle/preflight becomes necessary.

This is not a claim that an external connector process can never crash while queried. Existing process failure remains possible. The preservation requirement is that the candidate lifecycle itself cannot intentionally mutate/close/reconfigure the last-good shared resource.

### Adapter cleanup shape — PASS with regression lock

Current external adapter physical session cleanup lives in the returned BuildResult cleanup rather than an ordinary backend `Close` hook. This makes the selected lease model viable.

Implementation must add a characterization/architecture test so future external adapter lifecycle hooks cannot silently create a physical-cleanup bypass around the pool.

### Concurrency state machine — PASS

The proposed pending-entry protocol avoids a subtle refcount race that a naive `singleflight.Do` result handoff can create: the first caller cannot close a just-built resource before waiters have formally acquired it.

No physical build/cleanup under the mutex, exact-incarnation invalidation, cancellation-safe waiting, and close/pending-builder synchronization are appropriate.

### Failure caching — PASS

Failed physical build is not permanently cached. This preserves current retry-on-later-attempt behavior and avoids introducing hidden negative-cache policy.

### No idle cache — PASS

Closing on final generation lease keeps the scope tightly aligned with reload overlap. TTL/eviction/process-pressure policy would be speculative and materially increase operational complexity.

### Backend-plugin ABI/security — PASS

The design changes no public ABI and retains verified artifact binding, secure local IPC, peer auth before Configure/secrets, environment restrictions and process-tree cleanup. The pool does not parse provider-specific YAML.

### ROI gate — PASS

Deterministic operation counts are a stronger primary gate than wall-clock thresholds. The 100-enabled-connector harness directly proves whether the expensive O(N) activation/configure wave exists and whether implementation removes it. Benchstat remains supporting evidence.

## Required Requirements Correction

Add normative requirements equivalent to:

1. On a reuse hit, candidate preparation shall not invoke Configure, Start, Stop, Close, mutating preflight, or another generation-local mutation on the shared physical connector; query-shaped Resolve/ListModels access may remain generation-local.
2. If the candidate compiler or external adapter later requires such a mutating preparation action, the resource is non-shareable and must use current isolated physical construction until a separate design proves safe reuse.
3. Overlapping-generation metadata access on a reused physical configured instance requires race/conformance coverage.

The current `design.md` already states these constraints; only `requirements.md` needs tightening.

## Design-to-Requirement Trace

| Requirement | Design coverage |
|---|---|
| R1 scale evidence | 100-instance count harness, mixed-K matrix, supporting benchmarks |
| R2 narrow boundary | discovered per-instance only; private pool above processhost |
| R3 physical identity | explicit configure-input identity and fail-closed DTO drift gate |
| R4 lease contract | pending/live entries, per-generation lease release, final physical cleanup |
| R5 generation semantics | unchanged/changed/remove/rollback flows; derived state stays generation-local |
| R6 incarnation invalidation | exact entry token, detach, fresh same-key incarnation, no live substitution |
| R7 cleanup/shutdown | pool-before-host/artifacts/staging, one physical cleanup owner |
| R8 non-interference | no request path lookup, ABI/security/routing/billing unchanged, query-only candidate rule |
| R9 TDD/architecture | RED identity/pool/scale tests, race/goleak, anti-container gates |

## Simplification Review

The design deliberately rejects the following tempting expansions:

1. **No generic resource manager.** One connector-specific private pool is enough.
2. **No processhost generation awareness.** The host remains a physical supervisor.
3. **No `BackendStateIdentity` semantic overloading.** Observation-state compatibility remains independent.
4. **No idle cache.** Reuse exists only while generations overlap.
5. **No public feature flag.** Unsafe resources fall back rather than exposing lifecycle internals.
6. **No shared model registry.** Generation consistency remains coarse-grained where it provides value.
7. **No dynamic plugin reconciliation.** Startup-fixed trust/discovery remains unchanged.
8. **No Cordis requires/provides graph or fibers.** There is no current service-dependency problem requiring them.

## Implementation Risks to Pin With Tests

- a Configure-time DTO field added without identity treatment;
- two candidate builders constructing the same absent identity twice;
- a candidate rollback closing the active generation's resource;
- final release racing a new Acquire and handing out a closing entry;
- old incarnation invalidation deleting a newer incarnation;
- pool Close returning while a pending builder can still publish/use a closing host;
- external adapter gaining `Close`/Start/Stop semantics that bypass lease cleanup;
- dynamic inventory refresh races across overlapping generations;
- changed config/artifact/secret/policy incorrectly hitting reuse;
- removed backend closing before retained old generation drains;
- pool shutdown running after host/artifact teardown;
- request execution accidentally consulting the pool;
- architecture growing into a generic registry/container.

## Final Gate

Design validation is **GO** once the candidate query-only/fallback rule is copied into normative requirements. After that correction, task decomposition may proceed without further architecture changes.
