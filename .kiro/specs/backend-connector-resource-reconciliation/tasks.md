# Implementation Plan

## Execution Rules

- Follow TDD: characterization/RED tests and contract gates precede production behavior changes.
- Keep every task independently reviewable and limited to at most five concrete actions.
- Preserve existing public APIs, backend-plugin ABI, configuration schema, immutable generation model, `ResourceLedger`, and `processhost` ownership.
- Prefer deletion/replacement of old per-generation physical-cleanup plumbing at the touched seam over layering a second cleanup path.
- Do not broaden scope to built-in backends, `shared_artifact`, generic resource management, dynamic discovery, or request-time lookup.

## Phase 1 — Establish Scale, Identity, and Lifetime RED Gates

### Task 1.1 — Build the high-cardinality executable-connector reload harness

- Add a deterministic runtimebundle test fixture with at least 100 enabled synthetic discovered `per_instance` connector instances, unique logical IDs/prefixes, and no external credentials.
- Instrument physical factory/build, processhost activation/launch, Configure, physical cleanup, and later lease acquire/release counts without changing production behavior.
- Characterize the current unrelated-material-reload baseline: unchanged enabled connector rows are physically reconstructed for the candidate while the old generation remains retained.
- Add RED target assertions for unchanged reload (`0` new physical builds/activations/configures) and mixed reload (`K` changed identities -> `K` physical replacements).
- Add/extend a focused benchmark fixture for candidate compilation at representative connector cardinalities without using wall-clock thresholds as correctness assertions.

_Requirements: 1.1–1.7, 2.2, 9.1, 9.3_

_Validation: current production behavior is characterized deterministically; target reuse assertions are RED before implementation._

_Design: Scale and ROI Evidence Design; Deterministic 100-connector harness._

### Task 1.2 — Lock physical resource identity requirements with RED tests

- Add table-driven RED tests proving identity equality only when logical instance, factory, artifact digest, process model, opaque Configure bytes, effective runtime policy, and secret fingerprint are compatible.
- Prove config, artifact, credential/secret, and runtime-policy changes produce distinct identities while secret plaintext never appears in identity string/debug/error output.
- Add a contract gate that forces deliberate identity treatment when the external configure-time physical input/`RuntimePolicy` surface changes.
- Prove `BackendStateIdentity` compatibility alone cannot authorize physical connector reuse.
- Add fallback tests for an input explicitly marked non-shareable/incompletely representable.

_Requirements: 3.1–3.11, 8.1, 8.7, 9.1, 9.4_

_Validation: identity tests fail before the new private identity builder exists and protect DTO drift._

_Design: Physical Resource Identity; DTO Drift Gate; Fail-Closed Eligibility._

### Task 1.3 — Specify the lease/refcount/incarnation state machine with RED tests

- Add RED tests for first Acquire, reuse Acquire, independent idempotent releases, and physical cleanup only on final release with no idle retention.
- Add concurrent absent-Acquire tests proving one physical build and one current incarnation with multiple leases; include waiter cancellation without canceling another caller's build.
- Add invalidation tests proving exact-incarnation detach, fresh same-semantic-key rebuild, and stale old-incarnation invalidation cannot evict the replacement.
- Add races for final Release versus new Acquire and pool Close versus a pending physical build; no caller may receive a closing/invalid entry.
- Add failed-build tests proving no permanent negative cache, no leaked ref/entry, and successful later retry.

_Requirements: 4.1–4.10, 6.1–6.8, 7.5, 7.8, 9.1–9.2_

_Validation: state-machine tests are RED against the absent pool and define exactly-once cleanup/concurrency behavior._

_Design: Resource Pool State Machine; Concurrency Design; Invalidation and Incarnations._

### Task 1.4 — Lock candidate isolation and process teardown ordering with RED tests

- Add a candidate-rollback test where active and candidate generations share one synthetic physical resource; rollback must release only the candidate lease and leave active execution/query behavior available.
- Add an external-adapter characterization gate proving a pooled backend has no generation-owned physical `Close`/`Start`/`Stop` bypass around lease cleanup.
- Add overlapping-generation `Resolve`/`ListModels` metadata access coverage under race instrumentation while keeping model-registry/runtime projections generation-local.
- Add ProcessServices/bootstrap ownership-order tests requiring pool close before processhost, artifacts, and staging on success and partial startup failure.
- Add a non-shareable fallback test for any candidate path that requires mutating Configure/Start/Stop/Close/preflight preparation on an existing physical connector.

_Requirements: 2.8–2.9, 5.4–5.11, 7.1–7.9, 8.2, 8.8–8.9, 9.9_

_Validation: RED tests capture the last-good isolation boundary and exact pool → host → artifacts → staging dependency order._

_Design: Candidate Preparation and Last-Good Isolation; Process Shutdown; No cleanup bypass._

## Phase 2 — Implement the Minimal Private Identity and Reconciliation Owner

### Task 2.1 — Implement explicit fail-closed physical resource identity

- Add one package-private identity builder at the discovered physical construction/configure choke point using domain-separated, length-framed SHA-256 input.
- Canonically project every current `RuntimePolicy` field and deterministic opaque Configure bytes; fingerprint any non-empty `SecretBundle` with sorted, length-framed names/values without retaining plaintext.
- Include logical instance/factory identity, exact verified artifact digest, and process model; document any process-stable omitted input protected by tests.
- Return a shareability decision so incomplete/unsupported identity input falls back to current physical construction rather than producing an unsafe key.
- Make all Task 1.2 identity and privacy/DTO-drift tests green without adding production reflection or a public identity type.

_Requirements: 3.1–3.11, 8.1, 8.7_

_Validation: focused identity tests green; secret leak scans/errors contain no raw secret material._

_Design: Physical Resource Identity; Canonical Fingerprinting._

### Task 2.2 — Implement the connector-specific resource pool and lease state machine

- Add one package-private `backendResourcePool`/entry/lease implementation with pending/live/detached state, exact physical incarnation tokens, refcounts, and a closing state.
- Serialize same-identity physical construction without holding the pool mutex across build/configure; make canceled waiters independent of the builder/other dependents.
- Return the immutable backend value plus a fresh idempotent generation lease-release cleanup; retain physical cleanup only on the entry and run it exactly once after final release.
- Implement exact-incarnation invalidation/detach and no-negative-cache build failure semantics, with all physical cleanup outside the lock.
- Implement idempotent Close that rejects new Acquire, joins pending builders, and fail-safe cleans residual entries before returning.

_Requirements: 4.1–4.10, 6.1–6.8, 7.5, 7.8_

_Validation: Task 1.3 tests green under normal and race execution; no physical operation executes while pool mutex is held._

_Design: Private Types; Resource Pool State Machine; Concurrency Design._

### Task 2.3 — Transfer pool ownership through the existing process construction path

- Create the pool beside `processhost.Host` during discovered-install preparation and make eligible discovered factory closures capture it directly without global lookup/setter wiring.
- Extend the private discovered-install/process-build ownership bundle so pool lifetime transfers into `ProcessServices` while direct test/install paths can remain explicit and non-global.
- Register process cleanup so reverse shutdown orders pool before processhost, verified artifacts, and staging; preserve existing ordering of unrelated ProcessServices resources.
- Mirror the same dependency order on every pre-transfer/bootstrap failure path and prevent double cleanup after successful ownership transfer.
- Keep the pool connector-specific/package-private and make Task 1.4 process ownership-order tests green.

_Requirements: 2.1, 2.6–2.9, 7.2–7.6, 7.9_

_Validation: ownership tests prove exactly-once transfer and pool → host → artifacts → staging teardown on success/failure._

_Design: Construction and Ownership Timing; Process Shutdown._

## Phase 3 — Integrate Reuse Into Discovered `per_instance` Generation Construction

### Task 3.1 — Split physical construction from lease acquisition at the discovered factory seam

- Refactor the existing discovered backend builder into a narrow preparation/identity step plus the current physical Activate/Configure/adapter-build path without provider-specific branches.
- Preserve unique host activation IDs for every **new** `per_instance` physical incarnation; do not change `processhost.OwnershipKey` or host instance semantics.
- Route only eligible discovered overlap-safe `per_instance` resources through pool Acquire; leave builtins, `shared_artifact`, and non-shareable resources on current generation-local construction.
- Make pool hits return the existing backend functional value plus lease cleanup, while pool misses consume the underlying physical cleanup as entry ownership.
- Keep `buildBackends`/`ResourceLedger` as the generation cleanup transfer authority and make unchanged/mixed high-cardinality RED assertions green.

_Requirements: 1.4–1.5, 2.2–2.7, 4.2–4.10, 5.1–5.8_

_Validation: unchanged eligible reload performs zero physical reconstruction; K changed identities produce exactly K new physical builds._

_Design: Physical Construction Integration; Current path retained as the builder; Pool return shape._

### Task 3.2 — Bind processhost failure invalidation to exact pooled incarnations

- Wrap the existing adapter invalidation callback for newly built pooled resources so it first detaches the exact pool incarnation from future reuse and then delegates to `processhost.InvalidateProcessGeneration`.
- Preserve existing processhost physical reap/recovery behavior and existing cleanup-error normalization after an invalidated process is already gone.
- Ensure invalidation does not decrement generation leases or live-substitute a replacement under an existing generation.
- Prove a later same-config candidate builds one fresh physical incarnation and that an old delayed callback cannot invalidate the new incarnation.
- Keep non-pooled and `shared_artifact` invalidation behavior byte-for-byte/semantically equivalent to current behavior.

_Requirements: 6.1–6.8, 7.7, 8.3, 8.5_

_Validation: invalidation/restart tests green; current processhost invalidation tests remain green._

_Design: Invalidation flow; No live substitution._

### Task 3.3 — Preserve generation-local model/routing/policy composition on reuse hits

- Keep building a new generation-local `BackendInventory` slice, model-registry runtime, model/catalog snapshot, executor/routing/policy views, HTTP handler, generation lifecycle context, and `ResourceLedger` from leased backends.
- Verify unchanged pooled resources can serve overlapping generations' metadata queries/executions without moving refresh loops or model state into the pool.
- Verify changed same-ID, remove/disable, retained old stream/async work, and candidate-failure rollback retain the exact current behavioral contracts.
- Ensure candidate reuse hits perform no Configure/Start/Stop/Close/mutating preflight on the physical resource; automatically bypass reuse if that condition is not satisfied.
- Keep all canonical request/event, streaming, failover, cancellation, accounting, billing and token-counting code paths unchanged.

_Requirements: 5.1–5.11, 8.2–8.9, 9.9_

_Validation: existing backend recomposition/no-drop tests plus new query-only/cross-generation tests green._

_Design: Generation-Local Derived State; Candidate Preparation and Last-Good Isolation._

### Task 3.4 — Add architecture fences against scope creep and cleanup bypass

- Add architecture tests proving the pool remains private to runtime composition and is not imported/reached from request execution, public SDKs, connectors, or provider-specific packages.
- Reject generic service/container vocabulary or a reusable keyed runtime registry API introduced to support this feature.
- Lock `processhost.Host` as the only physical process/IPC supervisor and reject duplicate launch/peer/process-tree logic in the pool.
- Lock pooled external backend lifecycle so generation cleanup is a lease release rather than an alternate physical `Close`/Start/Stop path.
- Assert no public YAML/manifest/ABI option is added for this internal optimization.

_Requirements: 2.1, 2.5–2.9, 4.9–4.10, 8.3–8.5, 9.6, 9.9_

_Validation: architecture gates fail on representative forbidden fixtures and pass the intended private design._

_Design: Boundary Commitments; Rejected Alternatives; No cleanup bypass._

## Phase 4 — Prove ROI, Correctness, and Final Simplicity

### Task 4.1 — Certify the high-cardinality reconciliation matrix

- Run the 100-enabled-connector harness for unchanged, one-changed, K-changed, remove/disable, candidate rollback, and one-invalidated-then-rebuild scenarios.
- Assert physical build/activation/Configure counts are `0` for unchanged reuse hits and proportional only to changed/unusable identities.
- Assert peak synthetic physical live-resource count follows active resources plus changed candidate replacements rather than duplicating every unchanged connector.
- Assert candidate rollback of reuse hits performs no physical cleanup of resources retained by the active generation and newly built candidate-only resources close at final release.
- Record the deterministic before/after operation-count evidence in implementation/PR evidence.

_Requirements: 1.1–1.7, 4.5–4.7, 5.2–5.5, 6.3, 7.1_

_Validation: all count-based ROI/correctness gates green independent of wall-clock variance._

_Design: Required scenarios; Design Success Criteria 1–5._

### Task 4.2 — Run concurrency, leak, security, conformance, and reload regression gates

- Run focused `-race` coverage for pool Acquire/Release/Invalidate/Close plus overlapping generation metadata/execution paths and run goleak coverage for pending-builder/shutdown cases.
- Run processhost activation/cleanup/invalidation and executable backend-plugin security/conformance suites on supported local/CI profiles.
- Run runtimebundle ResourceLedger, backend recomposition, discovered overlap/restart-required, candidate rollback, generation retention, and reload no-drop/last-good tests.
- Run repository formatting, vet/lint/quality checks and existing architecture boundaries; fix root causes without weakening assertions or skips.
- Verify no secret/config digest/raw YAML leakage was added to logs, metrics, statuses, errors, or public DTOs.

_Requirements: 7.7–7.8, 8.3–8.9, 9.2–9.7, 9.9_

_Validation: race/goleak/security/conformance/reload/quality suites green; no weakened existing gate._

_Design: Testing Strategy; Risks & Mitigations._

### Task 4.3 — Record supporting benchmark evidence without overclaiming request performance

- Run the high-cardinality candidate compilation benchmark before/after with stable fixture inputs and use benchstat when comparable samples are available.
- Report candidate build time/allocations and available synthetic/native process-launch/resource observations, clearly separating deterministic counts from noisy platform metrics.
- Confirm normal request execution benchmarks show no new pool lookup/lock and no material regression attributable to the feature.
- Document that the claimed gain is reload physical-resource churn/peak overlap reduction, not inference throughput or token latency.
- Keep timing data informational unless a stable repository performance budget already governs the measured path.

_Requirements: 1.2, 1.6, 8.4_

_Validation: evidence supports O(N physical build) → O(K physical build) structural claim without unstable timing gates._

_Design: Supporting benchmark evidence; Scale and ROI Evidence Design._

### Task 4.4 — Perform the final simplification and scope gate

- Review production diff for duplicate ownership stacks, generic resource/container abstractions, unnecessary wrappers, public knobs, or request-path coupling and remove them.
- Confirm builtins, `shared_artifact`, discovery/watchers, model-registry sharing, and dynamic plugin lifecycle remain outside scope.
- Confirm `ProcessServices`, `ResourceLedger`, `processhost.Host`, runtimehost generation leases, and unique host activation semantics remain the same authorities after the refactor.
- Compare implementation complexity against deterministic scale evidence; if the count target is not met or the design requires broad runtime machinery, revert/re-scope instead of shipping speculative infrastructure.
- Update implementation evidence/docs only where needed to describe the internal optimization and its preserved operational semantics.

_Requirements: 1.7, 2.1–2.9, 5.6–5.7, 8.3–8.6, 9.6–9.8_

_Validation: final diff is narrowly connector-lifecycle focused, evidence-backed, and contains no new general runtime concept._

_Design: Simplification Review; Design Success Criteria._

## Requirement Coverage Matrix

| Requirement | Primary tasks |
|---|---|
| R1 Evidence-first scale justification | 1.1, 3.1, 4.1, 4.3, 4.4 |
| R2 Narrow eligibility/ownership boundary | 1.4, 2.3, 3.1, 3.4, 4.4 |
| R3 Physical resource identity | 1.2, 2.1 |
| R4 Acquire/lease contract | 1.3, 2.2, 3.1, 4.1 |
| R5 Immutable generation semantics | 1.4, 3.1, 3.3, 4.1 |
| R6 Invalidation/incarnations | 1.3, 2.2, 3.2, 4.1 |
| R7 Cleanup/shutdown/errors | 1.4, 2.2, 2.3, 4.2 |
| R8 Connector non-interference | 1.4, 2.1, 3.3, 3.4, 4.2–4.3 |
| R9 TDD/concurrency/architecture gates | Phase 1, 3.4, 4.2, 4.4 |
