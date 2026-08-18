# Brownfield Design Validation

## Verdict

**GO after correction loops.** The design fits current Go-LIP ownership boundaries and reuses existing routing, auxiliary execution, generation pins, B2BUA, secure-session, extension and billing authorities.

The initial brownfield validation identified four architectural corrections before task generation: final-release detector ordering, private child A-leg semantics, useful late-result retention, and explicit prerequisite/fail-closed composition when the #312 detector surface is unavailable. A later CodeRabbit review identified additional specification-level correctness gaps around field-level billing evidence, preservation callback failure handling, capsule self-validation, decision conflict identity, completion-only pre-open billing/coalescing, parent-branch ownership, reinjection commit identity, and the disabled-by-default example. Those gaps are now incorporated into the requirements, design, and task plan.

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
- `internal/core/runtime/billing_leg.go`;
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

**PASS after correction.**

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

**PASS after correction.**

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

**PASS after correction.**

## Finding V4 — Feature composition must fail clearly when prerequisite preservation/detector services are absent

### Initial concern

#312 is not runtime code on the original validation baseline. A feature plugin silently receiving disabled compaction services would appear enabled while doing nothing, which is misleading and hard to operate.

### Correction

When `compaction-continuity` is enabled:

- generation/process composition verifies the prerequisite detector preview/commit capability, branch coordinator and BackgroundAux client exist;
- missing prerequisite service is a startup/generation compile error with a clear message;
- feature disabled keeps zero/no-op behavior and does not require semantic extractor configuration;
- implementation tasks are chronologically ordered so #312 runtime lands first.

This is configuration fail-closed, not request traffic fail-closed.

### Status

**PASS after correction.**

## Finding V5 — Process-owned branch coordination is justified despite feature-state guidance

### Concern

The extension platform generally prefers plugin state through `lipsdk/state.Store` rather than per-feature core state. However current Store `Get`/`Put` has no atomic CAS, and a per-generation plugin mutex cannot protect overlapping old/new generation callbacks and background workers.

### Decision

Keep the process-owned **narrow BranchCoordinator** as a synchronization facade and use process `ExtensionState` as its serialized backing where practical. The coordinator owns only:

- parent branch-key lock/serialization;
- revision/high-watermark/job/preview-intent/injection metadata;
- bounded entry TTL/eviction;
- opaque capsule/source blobs.

It does **not** own plan semantics, extractor prompts, provider clients, billing, generic state transactions, or arbitrary tasks.

This is smaller and safer than adding a generic transactional plugin-store API solely for this feature.

### Status

**PASS as designed.**

## Finding V6 — Background auxiliary scheduler is a reusable primitive but must remain narrow

### Concern

Adding an async service can become a generic workflow engine.

### Decision

The scheduler accepts only canonical auxiliary model collection requests and returns bounded collection jobs. It cannot schedule arbitrary functions, timers, cron, durable tasks, webhooks, or user-defined callbacks. ProcessServices owns it; generation snapshots receive non-owning adapters that capture the current runner/pin at submission.

Completion-only pre-open identity is held as a non-billable BranchCoordinator preview intent, not as a scheduler job. Only a committed post-Open transaction may create a new billable BackgroundAux submission.

### Status

**PASS with architecture tests.**

## Finding V7 — Detached mode must not become a client bypass around secure session

### Concern

An exported/internal session mode that suppresses secure-session BeginTurn could be abused if a frontend could set it.

### Decision

Detached mode is carried only on the trusted auxiliary SDK/internal request object after frontend decode; no `lipapi.Call` JSON/wire field or header maps to it. Architecture/tests prove frontend inputs cannot request detached mode. Feature plugins are already trusted in-process extensions; the standard continuity feature is the initial consumer.

### Status

**PASS with security tests.**

## Finding V8 — Billing requirements must include the current independent-leg evidence contract

### Validation

Billing account identity is principal-scope-derived, so the detached child can preserve the originating account while receiving an independent BillingCallID and private child A-leg/B-legs. Normal Executor submission can therefore reuse existing exposure, terminal usage, settlement, and provider COGS authorities.

The runtime contract is stricter than merely assigning a distinct BillingCallID:

- independent B-leg persistence requires an authoritative positive `AttemptSeq`; records with `AttemptSeq <= 0` are rejected rather than assigned synthetic order;
- final billing evidence carries the existing token/usage quantities and presence bits, cost evidence/presence, `Source`, `Authority`, and `DedupeKey`;
- retry/failover legs must satisfy the same field-level contract independently.

The continuity spec therefore requires these invariants for every auxiliary/failover B-leg and requires RED coverage for rejected non-positive AttemptSeq and invalid accounting evidence. Workload/role classification remains content-free and must not implicitly change pricing.

### Status

**PASS after field-level billing correction; RED tests required.**

## Finding V9 — Primary protocol usage separation is achievable

The child is an independent internal call and is never encoded into the primary frontend response. Existing protocol usage remains scoped to the primary canonical stream. Account-level durable usage/billing includes child records separately.

### Status

**PASS; regression tests required.**

## Finding V10 — Opaque compaction fallback is mandatory, not optional polish

`CompactionItem` exposes encrypted/opaque state rather than universal plaintext summary. The result-augmentation path therefore cannot be the only preservation mechanism.

Reinjection on the first eligible post-compaction request is the universal safe fallback and must be implemented/tested even if one or more plaintext carriers are supported.

### Status

**PASS after design explicitly treats reinjection as mandatory fallback.**

## Finding V11 — Preserver callback errors must be explicitly fail-open and mutation-safe

### Review concern

`BeforeRequest`, `RequestOpened`, and `BeforeResponseRelease` return `error`. Without a composition rule, a preservation-only timeout/state/sanitizer error could escape into primary handling and make native compaction unusable. `BeforeRequest`/response-finalization can also mutate canonical objects before returning an error.

### Correction

- core dispatch isolates callback panic/error and records content-free preservation failure rather than propagating it as primary model/provider failure;
- `BeforeRequest` and `BeforeResponseRelease` run through a bounded transactional clone/undo helper;
- callback error, panic, or post-mutation canonical validation failure restores the exact pre-preservation `Call`/`Event`;
- `RequestOpened` failure cannot undo the already-opened primary request and only affects preservation-local state;
- preservation failure never changes routing, failover or no-retry-after-output authority.

This remains a focused mutation rollback seam, not a general transaction framework.

### Status

**PASS after correction; callback error/panic/rollback tests required.**

## Finding V12 — Serialized capsule must carry branch binding and canonical content digest

### Review concern

The requirement already called for branch identity and content digest, but the capsule JSON example kept them only outside the serialized envelope. Such bytes could not self-validate their scope/integrity after registry transfer, reinjection, recovery or reload.

### Correction

Capsule V1 now carries:

- `branch_binding`: a stable content-free digest derived from the authoritative **parent** BranchKey;
- `content_digest`: SHA-256 over the canonical versioned envelope excluding only the digest field itself.

The digest scope includes schema version, revision, source high-watermark, branch binding and semantic payload. Registry consume, merge, reinjection/projection, recovery and reload reuse validate binding + digest before use. Raw account/session identifiers need not be exported to the extractor model.

### Status

**PASS after correction; canonical encoding and mismatch tests required.**

## Finding V13 — Decision conflict identity cannot rely on extractor-generated fact IDs

### Review concern

A semantically conflicting decision can arrive under a fresh semantic fact ID, so ID reuse alone cannot ensure that later explicit corrections supersede older active choices.

### Correction

Decision facts now separate:

- stable fact `id` for provenance/history;
- normalized `conflict_key` for the semantic decision slot;
- optional validated `supersedes` references for explicit cross-slot corrections.

At most one decision may remain active per conflict key. A newer decision for an occupied conflict key deterministically supersedes the older active decision under the existing authority/revision precedence. Unknown/cross-branch `supersedes` references are rejected.

### Status

**PASS after correction; contradictory-active-decision tests required.**

## Finding V14 — Completion-only extraction cannot submit fresh billable work before primary Open

### Review concern

The earlier completion-only flow called `SubmitCollect` before the first post-compaction primary B-leg opened. If that Open failed, the detached child could still produce real provider usage, contradicting failed-Open zero-billing semantics. The earlier coalescing rule also required a transaction that did not yet exist.

### Correction

Before Open, completion-only flow may only:

1. use detector pure preview to derive a stable boundary/fingerprint;
2. derive/coalesce a **non-billable** preview intent from parent branch + preview boundary + target source revision;
3. merge deterministic state;
4. await a matching job that was already submitted by an earlier successfully opened request;
5. inject already-ready/deterministic continuity if needed.

Only after successful primary Open does detector state commit, the preview intent bind to the committed transaction, and a fresh billable BackgroundAux job become eligible for submission. A failed Open creates no new child BillingCallID/B-leg/provider work.

This intentionally accepts that semantic information first discoverable at this boundary may improve later turns rather than justify premature provider billing.

### Status

**PASS after correction; failed-Open zero-child-work and preview-intent binding tests required.**

## Finding V15 — Continuity state belongs to the parent branch, never the detached child A-leg

### Review concern

The child must have a private A-leg for execution, but if a worker derives BranchCoordinator identity from that A-leg, late results update an auxiliary branch and cannot protect the primary conversation.

### Correction

The authoritative parent BranchKey is captured before auxiliary submission/child A-leg creation and stored as a content-free branch binding with pending job state. The parent key is used for:

- preview intent binding;
- `Await` result ownership checks;
- capsule merge/revision;
- pending injection/reinjection;
- late-result and reload coordination.

The child A-leg remains only child execution/routing/billing authority and lineage. Tests must use different parent and child A-leg IDs.

### Status

**PASS after correction; distinct-parent/child concurrency/reload tests required.**

## Finding V16 — Reinjection identity and commit point must be boundary-scoped

### Review concern

A revision-only `LastInjectedRevision` can wrongly suppress the same capsule revision after a second opaque compaction. Advancing a durable watermark at insertion time is also too early: canonical validation, primary Open, or final release can still fail, causing a later retry to skip required continuity.

### Correction

Reinjection now uses a compound identity:

```text
(parent branch binding, compaction boundary/transaction, capsule revision)
```

- the same revision may be injected again for a later distinct compaction boundary;
- a call-local ephemeral marker prevents duplicate insertion during one request's internal retry/failover lifecycle;
- callback/validation failure restores the pre-injection call and leaves pending state;
- failed primary Open leaves pending state;
- branch-level `LastReleasedInjection` advances and matching pending state clears only after successful final client release;
- an aborted/no-release turn therefore retries continuity on a later eligible request.

### Status

**PASS after correction; same-revision/two-boundary and failure-then-retry tests required.**

## Finding V17 — Configuration example must not contradict disabled-by-default behavior

### Review concern

The example had `enabled: true` while the security requirement says the new egress/billed feature is disabled by default. Copying the example could unintentionally enable remote extraction.

### Correction

The illustrative configuration is now `enabled: false` and explicitly described as disabled unless the operator opts in. The remote route remains illustrative only.

### Status

**PASS after correction.**

## Validation Checklist

| Check | Result |
|---|---|
| one compaction-recognition authority | PASS |
| detector observer remains content-free/non-mutating | PASS |
| actual final event observed by committed detector | PASS after V1 correction |
| strict extraction not billed before primary Open | PASS |
| completion-only fresh extraction not billed before primary Open | PASS after V14 correction |
| completion-only preview identity binds to committed transaction | PASS after V14 correction |
| real background execution with submit-time generation ownership | PASS |
| worker process-owned across generation reload | PASS |
| no unbounded goroutine/queue | PASS |
| off-primary-session semantics | PASS after V2 correction |
| private child A-leg never becomes continuity branch key | PASS after V15 correction |
| independent extractor selector | PASS |
| parent route override cannot hijack child | PASS |
| same user/account billed by default | PASS |
| separate auxiliary BillingCallID/B-legs | PASS |
| auxiliary/failover AttemptSeq > 0 | PASS after V8 correction |
| auxiliary usage/cost/Source/Authority/DedupeKey evidence pinned | PASS after V8 correction |
| primary protocol usage unchanged | PASS |
| preserver callback failure is fail-open | PASS after V11 correction |
| failed preservation mutation rolls back canonical object | PASS after V11 correction |
| capsule carries parent branch binding and canonical digest | PASS after V12 correction |
| decision conflicts use deterministic conflict identity | PASS after V13 correction |
| late job result remains useful | PASS after V3 correction |
| reinjection dedupe is branch/boundary/revision scoped | PASS after V16 correction |
| reinjection watermark commits only after final client release | PASS after V16 correction |
| encrypted/opaque content immutable | PASS |
| mandatory reinjection fallback | PASS |
| shipped example remains disabled by default | PASS after V17 correction |
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
| shared detector preview + prerequisite | 1.1–1.13, 4.10 |
| separate Preserver FeatureBundle surface + rollback | 1.2–1.4, 7.14, 7.16, 10.4–10.5, 11.12–11.13 |
| Continuity Capsule + branch digest + decision precedence | 2.1–2.17, 8.1, 8.4, 8.11–8.12 |
| deterministic carrier catalog | 3.1–3.7 |
| sanitizer/source window | 3.8–3.16, 9.3–9.10 |
| BackgroundAux scheduler + committed coalescing | 4.1–4.15, 11.2, 11.10–11.11 |
| detached child + parent branch ownership | 4.16, 5.5–5.15, 8.1–8.3, 11.4–11.5, 11.16 |
| explicit extractor route | 5.1–5.5, 10.1–10.2 |
| normal user billing + field-level B-leg evidence | 6.1–6.14, 10.7–10.8, 11.6–11.7 |
| strict and completion-only compaction flows | 7.1–7.16, 11.3 |
| boundary-scoped authority-aware reinjection | 7.7–7.16, 8.5, 11.17 |
| branch coordinator / preview intents / watermarks | 4.10, 4.16, 8.1–8.12, 11.8, 11.10–11.11, 11.16–11.17 |
| reload/restart behavior | 8.9–8.15, 10.8–10.10 |
| trusted policy/privacy | 9.1–9.13 |
| failure/observability | 10.1–10.13 |
| architecture/testing gates | 11.1–11.17 |

## Simplification Review

The chosen design adds two infrastructure capabilities only because existing seams cannot safely satisfy the requirements:

1. **BackgroundAux scheduler** — required because current Aux.Collect is synchronous and post-lease spawn is unsafe.
2. **BranchCoordinator** — required because process ExtensionState lacks atomic revision update and feature-instance locks do not span generations.

The CodeRabbit corrections do **not** justify further infrastructure. Preview intents, injection watermarks, parent-branch bindings and preservation rollback remain narrow state/contracts inside those existing seams; they do not require a generic transaction service, workflow scheduler or durable queue.

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
5. auxiliary `AttemptSeq <= 0` or invalid final evidence silently loses accounting -> field-level billing RED tests.
6. primary protocol usage includes child tokens -> frontend usage RED test.
7. detector commits completion before preservation final event -> ResponsePreview/ResponseReleased ordering test.
8. preserver error/panic aborts native traffic or leaves partial mutation -> rollback/fail-open RED tests.
9. capsule branch binding/digest mismatch is consumed -> transfer/reload/reinjection integrity RED tests.
10. semantic new ID allows contradictory active decision -> conflict-key/supersedes RED tests.
11. completion-only pre-open path submits provider work -> failed-Open zero-child-billing RED test.
12. preview intent fails to bind/coalesce after Open -> retry/failover duplicate-submission RED test.
13. child A-leg is used as continuity key -> distinct parent/child late-result RED test.
14. revision-only injection suppresses later opaque boundary -> same-revision/two-boundary RED test.
15. injection watermark advances before validation/Open/final release -> failure-then-retry RED test.
16. opaque `EncryptedContent` altered -> exact bytes RED test.
17. job result expires before next turn -> pending-result retention/expiry RED test.
18. stale worker overwrites user correction -> revision race RED test.
19. generation reload drops job/coordinator -> retained-generation RED test.
20. queue saturation creates goroutine fallback -> goroutine-count/goleak RED test.
21. disable/reload drops already-submitted billing settlement -> terminal accounting RED test.

## Final Gate

After the original V1–V4 correction loop and the later V8/V11–V17 CodeRabbit correction pass, the specification is **GO** for TDD implementation. Requirements, design, validation, and tasks now agree on the critical lifecycle rules: parent-branch state ownership, no fresh billable completion-only child before successful primary Open, current independent-leg billing evidence, fail-open transactional preservation callbacks, self-validating capsule bytes, deterministic decision conflict replacement, and boundary-scoped reinjection committed only after final client release.

The implementation should establish detector/preservation/background/detached/billing/branch contracts with RED tests before semantic extractor prompting and final compaction integration. If implementation requires pre-open billable child work, child-keyed continuity state, a second billing path, a generic workflow/transaction engine, or opaque provider mutation to pass, the design must be re-scoped rather than weakening these invariants.
