# Brownfield Design Validation

## Verdict

**GO after correction loop.** The design fits current Go-LIP ownership boundaries and reuses existing routing, auxiliary execution, generation pins, B2BUA, secure-session, extension and billing authorities. Initial design review identified four issues that must be corrected before task generation: final-release detector ordering, private child A-leg semantics, useful late-result retention, and explicit prerequisite/fail-closed composition when the #312 detector surface is unavailable.

No provider-specific implementation, second transcript database, second billing path, or generic workflow engine is required.

## Validation Inputs

Reviewed against current `main` and steering constraints, including:

- `.kiro/specs/compaction-event-detection/{requirements,research,design,tasks}.md`;
- `pkg/lipsdk/auxiliary/client.go`;
- `internal/core/auxreq/client.go`;
- `pkg/lipsdk/genpin/pin.go`;
- `pkg/lipapi/{call,items}.go`;
- `pkg/lipsdk/feature/bundle.go` and `internal/featurebundle/merge_surface.go`;
- `internal/core/extensions` / `internal/infra/runtimebundle/build_extension.go`;
- `internal/infra/runtimebundle/{process_services,shared_mutable}.go`;
- `pkg/lipsdk/session/{view,opener}.go`;
- `internal/core/runtime/executor_prepare_secure.go`;
- `internal/core/state/{partition,mem}.go`;
- `internal/infra/billingcompose/identity.go`;
- `.kiro/steering/{structure,testing,routing-and-orchestration}.md`.

## Finding V1 — Final detector observation must still see the actually released event

### Initial concern

The first design ordering said:

```text
selected event
 -> detector derives/commits completion
 -> preservation mutates verified plaintext carrier
 -> observer dispatch
 -> client
```

The #312 invariant is stronger: `ResponseReleased` represents the canonical event actually released after final mutation/gating. Calling committed detector observation before preservation means it technically observes a pre-final form.

### Correction

Add a pure `PreviewResponse` (or equivalent shared matcher preview) analogous to request preview:

```text
selected event
 -> detector PreviewResponse (pure, no state/event emission)
 -> preservation BeforeResponseRelease (bounded join/mutation)
 -> detector ResponseReleased(final event) (commits completion)
 -> metadata compaction observer dispatch
 -> client release
```

`PreviewResponse` may expose the active transaction/rule candidate required to find the preservation job, but it cannot mark completion. This keeps the detector non-mutating and truthful to the final-release contract.

### Status

**Accepted; requirements/design must be corrected.**

## Finding V2 — Detached child needs its own private A-leg, not no A-leg and not the parent A-leg

### Initial concern

Normal runtime authority/billing/attempt mechanics assume a logical A-leg. Reusing the parent A-leg would risk route-override/session-state coupling. Running completely without an A-leg would require a second execution path and weaken current B2BUA/request-authority assumptions.

### Correction

Detached auxiliary execution creates/touches a **private child A-leg** for the child canonical call using existing B2BUA lifecycle/store semantics. Parent IDs stay explicit lineage only:

```text
primary SessionID / A-leg A123
   |
   +-- auxiliary lineage parent_a_leg=A123
          |
          +-- child A-leg AUX456
                 +-- B-leg 1 extractor backend/model
                 +-- B-leg 2 failover if needed
```

Rules:

- child A-leg gets no primary secure-session ownership/resume semantics;
- parent route override is never read as child authority;
- child gets normal request authority/BillingCallID/B-leg accounting;
- child A-leg/attempts remain distinguishable as internal auxiliary workload;
- client-visible primary transcript/turn/activity remains unchanged.

### Status

**Accepted; requirements/design must be corrected.**

## Finding V3 — A short raw-result TTL can lose a valid background extraction before the next turn

### Initial concern

If the strict-compaction response barrier times out, the semantic job may finish later. If its raw result expires after only a short scheduler TTL and the next user turn arrives much later, the capsule never receives that valid result.

### Correction

Pending-job result retention must be useful for the branch, while remaining bounded:

- raw output is still size/count bounded and never logged;
- while a BranchState references `PendingJobID`, the scheduler result may be retained up to the configured pending-continuity TTL, not an arbitrary tiny default;
- first eligible `Await` parses/validates/merges and immediately `Forget`s raw output;
- branch/job expiry clears both sides coherently;
- no result lives longer than the configured bounded continuity/source retention window;
- implementation may later add a typed completion-consumer optimization, but v1 does not need arbitrary callbacks in the scheduler.

### Status

**Accepted; config/result-lifetime design must be corrected.**

## Finding V4 — Feature composition must fail clearly when prerequisite preservation/detector services are absent

### Initial concern

#312 is not runtime code on current `main`. A feature plugin silently receiving disabled compaction services would appear enabled while doing nothing, which is misleading and hard to operate.

### Correction

When `compaction-continuity` is enabled:

- generation/process composition verifies the prerequisite detector preview/commit capability, branch coordinator and BackgroundAux client exist;
- missing prerequisite service is a startup/generation compile error with a clear message;
- feature disabled keeps zero/no-op behavior and does not require semantic extractor configuration;
- implementation tasks are chronologically ordered so #312 runtime lands first.

This is configuration fail-closed, not request traffic fail-closed.

### Status

**Accepted; design/config tasks must pin it.**

## Finding V5 — Process-owned branch coordination is justified despite feature-state guidance

### Concern

The extension platform generally prefers plugin state through `lipsdk/state.Store` rather than per-feature core state. However current Store `Get`/`Put` has no atomic CAS, and a per-generation plugin mutex cannot protect overlapping old/new generation callbacks and background workers.

### Decision

Keep the process-owned **narrow BranchCoordinator** as a synchronization facade and use process `ExtensionState` as its serialized backing where practical. The coordinator owns only:

- branch-key lock/serialization;
- revision/high-watermark/job/injection metadata;
- bounded entry TTL/eviction;
- opaque capsule/source blobs.

It does **not** own plan semantics, extractor prompts, provider clients, billing, generic state transactions, or arbitrary tasks.

This is smaller and safer than adding a generic transactional plugin-store API solely for this feature.

### Status

**Accepted as designed.**

## Finding V6 — Background auxiliary scheduler is a reusable primitive but must remain narrow

### Concern

Adding an async service can become a generic workflow engine.

### Decision

The scheduler accepts only canonical auxiliary model collection requests and returns bounded collection jobs. It cannot schedule arbitrary functions, timers, cron, durable tasks, webhooks, or user-defined callbacks. ProcessServices owns it; generation snapshots receive non-owning adapters that capture the current runner/pin at submission.

### Status

**Accepted with architecture tests.**

## Finding V7 — Detached mode must not become a client bypass around secure session

### Concern

An exported/internal session mode that suppresses secure-session BeginTurn could be abused if a frontend could set it.

### Decision

Detached mode is carried only on the trusted auxiliary SDK/internal request object after frontend decode; no `lipapi.Call` JSON/wire field or header maps to it. Architecture/tests prove frontend inputs cannot request detached mode. Feature plugins are already trusted in-process extensions; the standard continuity feature is the initial consumer.

### Status

**Accepted with security tests.**

## Finding V8 — Billing requirements align with current money architecture

### Validation

Current billing account identity is principal-scope-derived; detached child can preserve this scope. Normal Executor submission then naturally receives independent BillingCallID/exposure/terminal usage/provider COGS. No stream-time financial mutation is required.

The only additive need is a content-free auxiliary workload/role projection for reports/diagnostics. That metadata must not influence pricing implicitly.

### Status

**PASS.**

## Finding V9 — Primary protocol usage separation is achievable

The child is an independent internal call and is never encoded into the primary frontend response. Existing protocol usage remains scoped to primary canonical stream. Account-level durable usage/billing includes child records separately.

### Status

**PASS; regression tests required.**

## Finding V10 — Opaque compaction fallback is mandatory, not optional polish

`CompactionItem` exposes encrypted/opaque state rather than universal plaintext summary. The result-augmentation path therefore cannot be the only preservation mechanism.

Reinjection on the first eligible post-compaction request is the universal safe fallback and must be implemented/tested even if one or more plaintext carriers are supported.

### Status

**PASS after design explicitly treats reinjection as mandatory fallback.**

## Validation Checklist

| Check | Result |
|---|---|
| one compaction-recognition authority | PASS |
| detector observer remains content-free/non-mutating | PASS |
| actual final event observed by committed detector | PASS after V1 correction |
| strict extraction not billed before primary Open | PASS |
| real background execution with submit-time generation ownership | PASS |
| worker process-owned across generation reload | PASS |
| no unbounded goroutine/queue | PASS |
| off-primary-session semantics | PASS after V2 correction |
| independent extractor selector | PASS |
| parent route override cannot hijack child | PASS |
| same user/account billed by default | PASS |
| separate auxiliary BillingCallID/B-legs | PASS |
| primary protocol usage unchanged | PASS |
| late job result remains useful | PASS after V3 correction |
| encrypted/opaque content immutable | PASS |
| mandatory reinjection fallback | PASS |
| no second transcript DB | PASS |
| no provider-specific core branches | PASS |
| no generic workflow engine | PASS |
| prerequisite absence visible at startup | PASS after V4 correction |
| generation reload semantics explicit | PASS |
| restart durability honest | PASS |
| TDD/race/goleak coverage feasible offline | PASS |

## Design-to-Requirement Trace

| Design decision/component | Primary requirements |
|---|---|
| shared detector preview + prerequisite | 1.1–1.12 |
| separate Preserver FeatureBundle surface | 1.2–1.4, 7.14, 11.11–11.12 |
| Continuity Capsule + merge precedence | 2.1–2.15, 8.4, 8.11–8.12 |
| deterministic carrier catalog | 3.1–3.7 |
| sanitizer/source window | 3.8–3.16, 9.3–9.10 |
| BackgroundAux scheduler | 4.1–4.15, 11.2, 11.10–11.11 |
| detached child + private A-leg | 5.5–5.13, 11.4–11.5 |
| explicit extractor route | 5.1–5.5, 10.1–10.2 |
| normal user billing + workload class | 6.1–6.13, 10.7, 11.6–11.7 |
| remote strict-compaction flow | 7.1–7.8 |
| authority-aware reinjection | 7.7–7.15 |
| branch coordinator | 8.1–8.12, 11.8, 11.10 |
| reload/restart behavior | 8.9–8.15, 10.8–10.9 |
| trusted policy/privacy | 9.1–9.13 |
| failure/observability | 10.1–10.11 |
| architecture/testing gates | 11.1–11.14 |

## Simplification Review

The chosen design adds two infrastructure capabilities only because existing seams cannot safely satisfy the requirements:

1. **BackgroundAux scheduler** — required because current Aux.Collect is synchronous and post-lease spawn is unsafe.
2. **BranchCoordinator** — required because process ExtensionState lacks atomic revision update and feature-instance locks do not span generations.

Everything else is additive use/refactoring of existing authorities. In particular, do **not** add:

- a second Executor/provider client;
- generic async function/task APIs;
- durable queue/database;
- event bus for preservation;
- general memory/RAG subsystem;
- new per-provider compaction adapters;
- new billing journal/rater;
- a generic transactional state platform;
- a second summary-model pass.

## Implementation Risks to Pin With Tests

1. `KindAsync` retained too late -> deterministic post-lease submission RED test.
2. child accidentally resumes parent secure session -> transcript/turn/activity RED tests.
3. child inherits parent route override -> explicit different-route RED test.
4. parent/child BillingCallID collision -> billing RED test.
5. primary protocol usage includes child tokens -> frontend usage RED test.
6. detector commits completion before preservation final event -> ResponsePreview/ResponseReleased ordering test.
7. opaque `EncryptedContent` altered -> exact bytes RED test.
8. job result expires before next turn -> pending-result retention/expiry RED test.
9. stale worker overwrites user correction -> revision race RED test.
10. generation reload drops job/coordinator -> retained-generation RED test.
11. queue saturation creates goroutine fallback -> goroutine-count/goleak RED test.
12. disable/reload drops already-submitted billing settlement -> terminal accounting RED test.

## Final Gate

After applying V1–V4 corrections to requirements/design, the architecture is **GO** for TDD task generation. The implementation should be split into phases that establish background/detached/billing correctness before semantic extractor prompting and final compaction integration.
