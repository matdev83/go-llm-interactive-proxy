# Research & Design Decisions

## Summary

- **Feature**: `backend-connector-resource-reconciliation`
- **Discovery Scope**: brownfield runtime optimization / connector-scale lifecycle refactor
- **Key Findings**:
  - Go-LIP already solves the difficult correctness problem with immutable generations, request/async generation leases, `ResourceLedger`, and manager-owned retirement. Those mechanisms should remain authoritative.
  - Catalog cardinality is already cheap: executable connector discovery is manifest-only and lazy with respect to process launch. The scaling pressure is the number of **enabled configured executable connector instances** rebuilt by a material generation reload.
  - Current `buildBackends` constructs every enabled backend in every generation. Discovered `per_instance` factories deliberately mint unique host activation IDs so candidate and active generations can overlap, which also guarantees unchanged physical connectors are duplicated during candidate construction.
  - The highest-ROI Cordis-v4 idea for this specific seam is provider identity + reconciliation + dependent lifetime retention: exact unchanged configured connector resources can be retained by more than one immutable generation and closed after the final generation releases them.
  - A generic Cordis runtime remains a poor fit. The selected design is one private connector-specific process owner that returns per-generation leases and delegates all physical process supervision to existing `processhost.Host`.
  - Physical reuse needs a stronger identity than `BackendStateIdentity`: exact artifact, opaque configure payload, runtime policy, secret fingerprint, process model, logical instance/factory identity, and any future configure-affecting input must be represented or reuse must fail closed.

## Research Log

### Cordis v4: the transferable principle

- **Context**: Re-evaluate the user-supplied paper *A Programming Paradigm for Spatiotemporal Composability* (Yifan Shi, Wei Zhang, Tianyi Cui) against the current backend connector architecture.
- **Findings**:
  - Cordis models components/providers by identity rather than only current values and reconciles a desired component graph against current runtime state.
  - Dependents retain access to a withdrawing provider while their teardown runs; the provider is physically removed only after dependent cleanup.
  - Revertible effects make rollback/teardown explicit and reverse effects when a component is withdrawn.
  - Cordis's generic reactive coeffect graph, fibers, HMR, and dynamic service context solve a broader class of application-platform problems than Go-LIP needs here.
- **Implications**:
  - Borrow semantic provider identity, physical incarnation identity, reconciliation, and reference-retained teardown.
  - Map a Go-LIP generation to a dependent/lease holder, not to a Cordis component graph.
  - Keep generation publication/retirement as the consistency boundary and avoid live dependency replacement under requests.

### Prior Cordis-derived ownership work

- **Context**: Determine whether `.kiro/specs/archive/atomic-owned-resource-lifecycle` already addressed this problem.
- **Sources Consulted**:
  - archived `atomic-owned-resource-lifecycle` research/design
  - `internal/infra/runtimebundle/process_owner.go`
  - `internal/infra/runtimebundle/resource_ledger.go`
- **Findings**:
  - The previous spec addressed acquisition/cleanup locality and worker ownership.
  - It deliberately left backend lifecycle unchanged because `BackendBuildResult` already paired a backend with cleanup and `buildBackends` immediately transferred that cleanup to `ResourceLedger`.
  - Main now contains `processResourceOwner` and `acquireOwnedProcess`, confirming that process cleanup locality is already hardened.
- **Implications**:
  - This spec is not a correction to the prior work. It addresses a different dimension: **one expensive physical backend resource being useful to several overlapping generations**.
  - Reuse existing ownership authorities; do not invent another general effect/owner framework.

### Current generation construction and backend rebuild behavior

- **Context**: Identify whether material reload reconstructs unchanged backend resources.
- **Sources Consulted**:
  - `internal/infra/runtimebundle/build_model.go`
  - `internal/pluginreg/lifecycle.go`
  - `internal/infra/runtimebundle/reload_backend_recomposition_test.go`
- **Findings**:
  - `buildModelRuntime` calls `buildBackends` for every candidate generation.
  - `buildBackends` iterates every enabled backend row and calls `BuildBackendWithLifecycle`.
  - A changed same-ID backend is intentionally constructed separately so old pinned generations retain old behavior while new generations use the replacement.
  - This same whole-set construction happens for unchanged rows on unrelated material generation changes.
- **Implications**:
  - Do not change changed/remove semantics.
  - Introduce reuse only at the physical connector construction seam for exact unchanged identities.
  - Continue rebuilding generation-local executor maps, inventories, model registry, routing projections, and policy views.

### Discovered executable connector activation

- **Context**: Determine where physical duplication originates and where reconciliation should sit.
- **Sources Consulted**:
  - `internal/infra/runtimebundle/discovered_factories.go`
  - `internal/infra/backendplugins/processhost/host.go`
  - `internal/infra/backendplugins/processhost/model.go`
  - `internal/infra/runtimebundle/reload_discovered_overlap_test.go`
- **Findings**:
  - `InstallDiscoveredExports` registers one generic lifecycle factory per validated manifest export; there is no provider-specific switch.
  - `buildDiscoveredBackend` encodes opaque YAML, activates/configures the host instance, builds an adapter backend, and returns physical cleanup.
  - For `per_instance`, the runtime deliberately mints a distinct host activation handle for each candidate construction so old/new generations never collide in `Host.instances`.
  - `processhost.Host` already has correct lazy launch, singleflight process slot creation, peer authentication, instance tracking, generation invalidation, and cleanup.
  - `shared_artifact` has different isolation/concurrency and overlap policy semantics.
- **Implications**:
  - Put reconciliation **above** `processhost`, not inside it.
  - Preserve the unique host activation handle for each newly created physical incarnation.
  - Reuse prevents calling physical construction at all for unchanged identity; it does not teach `processhost` about LIP config generations.
  - Exclude `shared_artifact` initially.

### Discovery cardinality versus live cardinality

- **Context**: Validate whether hundreds of installed connectors themselves justify runtime reconciliation.
- **Sources Consulted**:
  - `internal/infra/backendplugins/discovery/hundred_test.go`
  - `docs/adr/0008-hybrid-backend-connector-plugins.md`
  - `docs/backend-plugins/authoring.md`
- **Findings**:
  - Discovery has explicit coverage for 100 synthetic manifests without launching them.
  - Optional connectors are separate executable modules; installed but unconfigured plugins remain inactive.
  - Activation is lazy and trusted artifacts are exact-digest bound.
- **Implications**:
  - Do not optimize the manifest catalog or add dynamic discovery.
  - The evidence harness must model many **enabled process-backed instances**, not merely many installed manifests.

### Existing backend state identity precedent

- **Context**: Check whether Go-LIP already distinguishes semantic backend identity across generations.
- **Sources Consulted**:
  - `internal/infra/runtimebundle/backend_state_identity.go`
  - `internal/infra/runtimebundle/shared_mutable.go`
- **Findings**:
  - `BackendStateIdentity` namespaces process-owned affinity/health observations by instance ID, factory kind, and config digest.
  - Compatible identities allow process-owned observation continuity across generation replacement; changed identities hide stale state from the new generation.
  - This is a strong local precedent for identity-sensitive reuse but is purposefully narrower than physical connector interchangeability.
- **Implications**:
  - Keep `BackendStateIdentity` unchanged.
  - Introduce a separate private physical resource identity and avoid conflating observation-state compatibility with connector/session reuse compatibility.

### Physical identity inputs

- **Context**: Determine what makes two configured executable connector resources interchangeable.
- **Sources Consulted**:
  - `pkg/lipsdk/backendplugin/types.go`
  - `internal/infra/backendplugins/trust/artifact.go`
  - `internal/infra/runtimebundle/discovered_factories.go`
- **Findings**:
  - A connector is configured with logical `InstanceID`, `FactoryKind`, opaque YAML, secrets, `RuntimePolicy`, negotiation metadata, and an exact verified executable artifact/process model.
  - `VerifiedArtifact.DigestHex` is the exact launch identity that must distinguish binary upgrades.
  - `RuntimePolicy` contains execution bounds/timeouts/locality/environment policy that can affect configured behavior.
  - Secret rotation can change configured credentials without changing public YAML.
- **Implications**:
  - Physical identity must fingerprint every generation-varying construction/configure input.
  - Secret values are hashed locally with stable sorted framing and never surfaced.
  - Prefer one explicit identity builder at the same seam where effective configure input is assembled.
  - Add a contract test that makes future configure DTO/input additions require deliberate identity review.
  - When completeness cannot be proven, fall back to non-shared construction.

### Adapter/backend cleanup shape

- **Context**: Check whether the resulting backend value itself can safely be shared across executor maps.
- **Sources Consulted**:
  - `internal/infra/backendplugins/adapter/backend.go`
  - `internal/infra/backendplugins/processhost/build_result.go`
  - `internal/infra/runtimebundle/build_model.go`
- **Findings**:
  - `adapter.Build` creates an `execbackend.Backend` whose functional fields close over the configured `ExecuteSession` and resolved profile.
  - Physical session cleanup is retained in `processhost.BuildResult` rather than embedded as an ordinary request-time operation.
  - `buildDiscoveredBackend` combines adapter cleanup with `ActivateResult.Cleanup`, then returns it as `pluginreg.BackendBuildResult.Cleanup`.
  - `buildBackends` currently registers that cleanup into the generation `ResourceLedger`.
- **Implications**:
  - A reconciliation entry can own the physical `BackendBuildResult` and physical cleanup.
  - Each generation must receive the same immutable backend functional value plus a **new lease-release cleanup**, never the underlying physical cleanup.
  - Tests must prove no generation-local `Close`/lifecycle path can bypass the lease for pooled external resources.

### Process ownership transfer and teardown order

- **Context**: The discovered host exists before `ProcessServices`; determine how a pool can be captured by factories while still being process-owned.
- **Sources Consulted**:
  - `internal/infra/runtimebundle/plugin_catalog.go`
  - `internal/infra/runtimebundle/composition_root.go`
  - `internal/infra/runtimebundle/host_build.go`
  - `internal/infra/runtimebundle/process_services.go`
- **Findings**:
  - `prepareDiscoveredPluginInstall` creates staging/trust artifacts and `processhost.Host` before factory registration.
  - `InstallDiscoveredExports` registers factory closures before `NewProcessServices` freezes discovery.
  - The host/artifacts/staging ownership bundle then transfers into `ProcessServices`.
  - Existing reverse teardown intentionally closes host before artifacts and artifacts before staging.
- **Implications**:
  - Create the private backend resource pool beside the discovered host during install preparation so closures capture it directly.
  - Transfer the pool with the host into `ProcessServices` using a package-private construction field/seam.
  - Register pool close after host close registration so reverse teardown runs pool → host → artifacts → staging.
  - Error paths before transfer use the same dependency order.

### Failure and incarnation semantics

- **Context**: A desired identity can remain unchanged while its current process/session fails.
- **Findings**:
  - Existing adapter invalidation can invalidate a `processhost` process generation.
  - A semantic-key-only cache would risk returning the known-dead entry to later candidates.
  - The same semantic identity must be able to acquire a fresh physical process/session after failure.
- **Implications**:
  - Track physical incarnation identity independently from semantic identity.
  - Invalidation detaches the exact current incarnation before/with delegating to existing host invalidation.
  - New acquisition may build a fresh incarnation under the same semantic key.
  - Old leases stay bound to the old incarnation; no published generation is live-mutated.
  - Incarnation token comparison prevents a delayed stale invalidation from removing a newer entry.

### ROI measurement strategy

- **Context**: Candidate compilation is already fast on ordinary synthetic fixtures, while real connector startup is platform/provider dependent.
- **Findings**:
  - Existing reload benchmarks are useful but do not model hundreds of enabled external process-backed connectors.
  - Wall-clock startup varies with OS process creation, IPC, connector implementation, machine load, and CI environment.
  - The architectural cost being removed has deterministic work-count semantics independent of timing noise.
- **Implications**:
  - Primary gates are counts: builds, activations/launches, configure calls, physical cleanup.
  - Add a 100-enabled-connector benchmark/evidence fixture and record wall time/allocations as supporting data.
  - Expected unchanged reload changes from O(N) physical construction to O(N) lightweight lease acquisitions with zero physical reconstruction.
  - Mixed reload with K changed connectors performs physical construction proportional to K, not N.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|---|---|---|---|---|
| Full Cordis runtime | Generic provider graph, fibers, effects/coeffects, reactive reconciliation | broad composability | fights Go-LIP generation model; duplicates lifecycle machinery; high maintenance cost | Reject |
| Put generation reconciliation in `processhost` | Make host understand config-generation identity and reuse | central process knowledge | conflates physical supervision with semantic LIP generation composition | Reject |
| Reuse `BackendStateIdentity` directly | Treat existing observation-state key as physical resource key | smallest apparent change | unsafe: misses artifact, policy, secrets and future configure inputs | Reject |
| Cache `BackendBuildResult` per generation-independent key | Return same cleanup/value to multiple generations | simple lookup | double/early cleanup; no incarnation-safe invalidation | Reject |
| Process-owned connector resource leases | Private semantic identity → current physical incarnation → generation leases; host remains physical supervisor | focused ROI, preserves generations, scales with unchanged enabled connectors | identity and concurrency correctness require strong tests | **Select** |
| No change | Rebuild every enabled connector each material generation | simplest correctness model | O(N) physical activation wave and overlap resource spike at high enabled cardinality | Keep as fallback for non-shareable paths |

## Design Decisions

### Decision: Reconcile only configured physical external connector resources

- Keep executor maps, model registry/catalog, routes, feature surfaces, generation lifecycle and policies generation-local.
- Share only the configured adapter/backend session resource for exact eligible identities.
- Rationale: this removes expensive reconstruction without weakening the generation consistency boundary.

### Decision: Create the pool during discovered install, own it through ProcessServices

- Factory closures need the pool before `ProcessServices` exists.
- Construct pool next to `processhost.Host`, capture directly, then transfer process lifetime into `ProcessServices`.
- Do not use a global map or setter/locator.

### Decision: One semantic current entry plus explicit physical incarnation

- Semantic identity expresses desired configured resource equality.
- Incarnation identifies the concrete live process/session created for that desire.
- Invalidation detaches an incarnation without banning the semantic identity forever.

### Decision: Physical cleanup lives in the entry; ResourceLedger owns a lease release

- Underlying `BackendBuildResult.Cleanup` is consumed by the pool and never copied to multiple generations.
- Each Acquire returns an independent idempotent release closure.
- Final release performs physical cleanup exactly once.

### Decision: No idle cache

- Reuse exists only across overlapping retained generations.
- Final lease release closes immediately.
- This avoids cache sizing, TTLs, memory/process retention, eviction races, and another operational knob.

### Decision: No public opt-in flag

- Exact-identity reuse is an internal semantic-preserving optimization.
- Unsafe/incomplete identities fall back automatically to current construction.
- A public feature flag would expose internal lifecycle structure and create configuration/test matrix cost without adding user capability.

### Decision: Preserve unique host activation IDs for newly built incarnations

- The pool prevents unnecessary build calls for unchanged resources.
- When a build is actually required, current host-instance uniqueness remains intact, preserving overlap safety and current `processhost` assumptions.

## Risks & Mitigations

- **Identity omission causes unsafe reuse** — one construction-input identity choke point, fail-closed fallback, DTO-shape/identity contract tests.
- **One generation closes another generation's resource** — pool owns physical cleanup; generation ledgers own only idempotent lease release; no bypass closer.
- **Dead resource reused** — exact-incarnation invalidation detaches before future acquisition.
- **Stale invalidation kills new resource** — compare incarnation token before detaching current entry.
- **Pool becomes a service locator** — package-private connector-specific API, construction-only use, architecture tests forbid generic `Get`/`Resolve` and request-path access.
- **Pool duplicates processhost** — no launch/IPC/process tree/peer auth logic in pool; physical build and invalidation delegate to existing host.
- **Shutdown ordering regression** — explicit pool → host → artifacts → staging characterization tests on success and bootstrap error.
- **Resource retained after last generation** — no idle cache; refcount zero synchronously detaches and closes.
- **Race between Acquire, Release, Invalidate, Close** — small state machine with no external cleanup under mutex; race/goleak tests and per-key build serialization.
- **Optimization hides generation-specific derived state** — explicit tests prove model/routing/policy views are rebuilt while physical activation counts remain zero for unchanged resources.
- **Speculative complexity** — deterministic 100-connector operation-count gate and final simplification review; revert/re-scope if architecture cost exceeds demonstrated gain.

## References

- User-supplied paper: *A Programming Paradigm for Spatiotemporal Composability* — provider identity, reconciliation, dependent teardown, revertible-effect concepts.
- `.kiro/specs/archive/atomic-owned-resource-lifecycle/` — prior focused Cordis-derived ownership hardening.
- `.kiro/specs/archive/runtime-architecture-convergence-and-shrinkage/` — canonical one-process/one-generation/one-host architecture and anti-container guardrails.
- `docs/runtime-config-reload.md` — transactional last-good generation publication/retirement contract.
- `docs/adr/0008-hybrid-backend-connector-plugins.md` — executable connector process model and lazy discovery/activation boundary.
- `internal/infra/runtimebundle/build_model.go` — all-enabled-backend candidate construction and generation-local model runtime.
- `internal/infra/runtimebundle/discovered_factories.go` — discovered lifecycle factory activation/configure/cleanup seam.
- `internal/infra/backendplugins/processhost/` — physical executable process/IPC ownership authority.
- `internal/infra/runtimebundle/backend_state_identity.go` and `shared_mutable.go` — existing semantic identity precedent for affinity/health state.
- `internal/infra/runtimebundle/process_owner.go` — current process-owned acquisition/cleanup authority facade.
- `pkg/lipsdk/backendplugin/types.go` — configure-time DTO and runtime policy identity surface.
