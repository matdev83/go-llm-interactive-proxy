# Brownfield Requirements Gap Analysis

## Result

**PASS after requirements corrections.** The feature can be implemented without a new provider client, second transcript database, second billing path, or general workflow runtime, but the initial requirements needed several brownfield constraints made more explicit. The most important gaps are lifecycle ownership for real background auxiliary work, strict isolation from the primary secure session, mutation ordering around the planned compaction detector, and the fact that canonical native compaction output is often opaque/encrypted rather than a mutable summary string.

The corrections below are normative and must be reflected in the final `requirements.md`, `design.md`, and `tasks.md`.

## Existing Brownfield Facts

- `compaction-event-detection` is currently a merged **specification**, not a runtime capability on `main`. It defines a process-owned detector, metadata-only `compaction.Observer`, and request start emission only after successful upstream `Open`.
- Its detector design already owns the versioned compaction signature/history matrix and authoritative A-leg transaction state. Duplicating that matrix in continuity preservation would create drift and false disagreement.
- Current `pkg/lipsdk/auxiliary.Client` is synchronous. `Collect` delegates to `Stream`, which delegates to the normal runtime `Executor`.
- Current `internal/core/auxreq.Client` preserves the parent principal/scope, marks derived origin internal, supports `DisablePlugins`, and retains `genpin.KindAsync` **when the auxiliary call actually starts**.
- `genpin.Retainer.Retain` is explicitly a spawn-right operation: post-request-lease spawn attempts fail closed. Therefore a delayed worker cannot simply keep a request context and call synchronous `Aux.Collect` later.
- The current auxiliary child executes through the normal `Executor`. Without an explicit detached-session mode, it can enter secure-session BeginTurn/session recording paths; `Visibility` and `Role` are currently lineage metadata, not session-isolation authority.
- `lipapi.Call.Route.Selector` already provides a canonical route authority for a child call. The normal core router can therefore resolve a completely independent extractor selector without a second provider client.
- The billing identity adapter resolves account identity from the authenticated principal in `scope`. Because auxiliary execution already clones that scope, originating-user billing can reuse the existing billing path if detached execution preserves principal scope.
- `ProcessServices` owns process-lifetime services; `ExtensionState` is process-owned and survives generation reload. Feature bundle lifecycles, in contrast, belong to compiled feature/generation composition and are the wrong sole owner for a worker intended to outlive one immutable generation.
- `ExtensionState` is in-memory by default. `ScopeSession` partitions by `SessionView.PartitionKey()` and therefore by authoritative SessionID when available, but branch/A-leg must remain part of the feature key to avoid cross-branch aliasing.
- Secure-session transcript recording already exists when `TranscriptEnabled` is active. Creating another durable full transcript solely for this feature would violate existing extension-state and secure-session boundaries.
- `lipapi.CompactionItem` carries `EncryptedContent` and opaque provider data; it does **not** provide a universal plaintext summary field that can safely be appended to.
- Current `FeatureBundle` has no content-bearing compaction-preservation extension point. The #312 spec proposes only non-mutating `CompactionObservers`.

## Gaps and Required Corrections

### 1. #312 is a prerequisite, not an implemented dependency

The initial feature request could be read as if compaction lifecycle events already exist in runtime.

**Correction:** implementation planning shall explicitly order the `compaction-event-detection` runtime capability first. This feature may extend/refactor that implementation, but shall not copy its rule catalog as a fallback. Tests for this spec may use a local fake/shared recognizer contract until the prerequisite lands.

### 2. Preservation mutation cannot be smuggled into `compaction.Observer`

The detector's observer contract is intentionally content-free and non-mutating. Preservation needs canonical content and, for verified paths, limited mutation.

**Correction:** add a distinct additive preservation/interception contract. Keep observer payload and dispatch semantics unchanged. FeatureBundle/snapshot merge must expose preservation interceptors separately and defensively freeze them like other extension slices.

### 3. Request preview and committed detection have different truth semantics

A first post-compaction request may need continuity before its B-leg opens, while #312 deliberately commits request detection only after successful `Open`.

**Correction:** if a pre-open preview is required, it must be a pure/non-committing view over the same matcher/fingerprint state. Preview may protect a preservation barrier but cannot emit lifecycle events, advance detector state, or establish a billable strict-start job. Successful Open remains the committed start boundary.

### 4. Final-release mutation ordering must remain explicit

The #312 design places `ResponseReleased` at the final canonical release seam and guarantees observation does not mutate the event. Continuity may need to add a validated capsule to a verified plaintext carrier before client delivery.

**Correction:** detector derivation, preservation finalization, metadata-observer dispatch, and client release require one explicit ordering. The detector itself remains pure/non-mutating; the preservation stage is separate, and no ordinary response hook runs after it. Opaque/native content remains byte-identical.

### 5. Synchronous `Aux.Collect` is insufficient for real background work

Launching `Aux.Collect` in a goroutine after the parent request returns is not safe: generation spawn rights can be gone, the parent context can be canceled, and shutdown cannot reliably own the goroutine.

**Correction:** introduce one narrow **background auxiliary collection** capability backed by a process-owned bounded scheduler. Submission synchronously captures the executor/generation right and retains `genpin.KindAsync` before returning. Worker execution uses a worker-owned context/deadline; Await/Forget use a bounded job ID/result registry. This is an auxiliary-execution primitive, not a generic task engine.

### 6. Worker lifetime belongs to `ProcessServices`, not only a feature lifecycle

A generation-scoped plugin lifecycle can disappear on reload while a compaction extraction is still in flight.

**Correction:** the scheduler is process-owned and closed through existing ProcessServices ownership/closer ordering. Generation snapshots receive non-owning clients. The job itself retains exactly the generation needed to execute its captured child call.

### 7. Normal auxiliary execution can accidentally create primary-session effects

Current auxiliary lineage is not sufficient to prevent secure-session BeginTurn/transcript/last-activity effects.

**Correction:** add a typed internal **detached auxiliary session mode** recognized by core execution. It preserves authenticated principal/scope and parent correlation but suppresses primary secure-session authority/turn recording, primary A-leg route overrides, and client-visible session history. This mode must be internal canonical execution metadata, not a provider-visible opaque extension.

### 8. Independent extractor routing must not inherit primary A-leg override implicitly

The main user's A-leg may have a runtime routing override. Applying it to the internal extractor would violate the requested independently configurable model.

**Correction:** the detached child uses its explicitly configured canonical `Call.Route.Selector` (or explicit `inherit` policy) and a private auxiliary execution lineage. Parent A-leg ID is correlation only and cannot become route authority for the child.

### 9. User billing can reuse current authority, but auxiliary workload identity must be projected

Principal scope inheritance is sufficient for account identity, and routing through the normal Executor gives the child its own BillingCallID. However operators/users must be able to separate primary inference from continuity overhead.

**Correction:** add a bounded content-free workload/origin classification (`compaction_continuity_extractor`) to the accounting/metering/diagnostic correlation path. It must not become a second money ledger or pricing engine. Primary protocol usage remains unchanged while account-level billing includes the child.

### 10. Provider-native compaction payloads are not universal summary text

Mechanical append to `CompactionItem.EncryptedContent` or opaque data could corrupt replay/provider state.

**Correction:** result-side merge is permitted only for a verified mutable plaintext carrier. Otherwise the capsule is persisted as proxy state and injected into the first eligible post-compaction canonical request. Encrypted/opaque fields are byte-for-byte immutable under this feature.

### 11. Process extension state is reload-safe but not restart-durable

It is appropriate for revisions, sanitized windows, pending job IDs and injection watermarks within one process. It does not justify claiming durable continuity after restart.

**Correction:** define first implementation durability honestly. Process state survives generation reload. Across process restart, reconstruction is allowed from the existing authorized secure-session transcript when available; otherwise continuity state may be absent and behavior fails open. No new durable full transcript or generic durable feature-state framework is introduced by this spec.

### 12. `ScopeSession` alone is not a branch key

Authoritative SessionID is a useful partition, but one session can contain/relate to multiple A-leg branches or forks.

**Correction:** capsule/source/job records use the authoritative session partition plus an explicit A-leg/branch key. When no secure SessionID exists, principal-isolated A-leg authority is used. Client hints never select another branch's capsule.

### 13. Durable transcript access must stay behind a narrow authorized seam

An official feature plugin should not import secure-session Bun/store internals merely to recover historical text.

**Correction:** optional historical recovery uses a narrow authorized transcript-source/read adapter supplied by composition/core. Normal extraction prefers the canonical compaction baseline and process-local sanitized window, so most executions need no durable transcript read.

### 14. Raw generic background results should not become another sensitive store

A process-owned scheduler may need to hold a completed auxiliary result until the preservation barrier consumes it.

**Correction:** background result storage is bounded/TTL-limited and never logged. Continuity parses/validates the small extractor response promptly and stores only the normalized capsule/delta afterward. Forget/expiry releases raw collected output.

### 15. Disabling/config reload must not orphan already-billed work

A generation reload can disable the feature or change extractor routing while a prior job has already submitted provider work.

**Correction:** in-flight jobs use a submission-time immutable configuration/execution snapshot and finish/settle or cancel through the normal captured generation. Disabling prevents new jobs but does not erase accounting obligations for submitted work.

## Brownfield Compatibility Matrix

| Existing subsystem | Required treatment |
|---|---|
| `compactiondetect` / #312 rule authority | prerequisite and shared matcher/state; not duplicated |
| `compaction.Observer` | unchanged metadata-only/non-mutating contract |
| `FeatureBundle` / request snapshot | additive separate preservation-interceptor slice |
| `internal/core/auxreq` | retain synchronous client; add narrow background collector/scheduler path |
| `genpin` | retain `KindAsync` synchronously at job submission; no post-lease spawn |
| `ProcessServices` | owns bounded background auxiliary scheduler lifetime |
| secure session | primary turn/session state untouched by detached extractor |
| B2BUA | parent IDs correlation only; child gets private auxiliary execution lineage |
| routing / route overrides | child selector parsed normally; parent A-leg override not inherited implicitly |
| billing / metering | existing authorities and separate child BillingCallID reused |
| protocol-visible usage | primary response usage unchanged; child usage separately recorded |
| `ExtensionState` | process-local capsule/source/job metadata; key additionally includes A-leg |
| secure-session transcript | optional authorized historical recovery; no second transcript DB |
| `CompactionItem` opaque/encrypted fields | immutable; never used as arbitrary text injection carrier |
| provider/frontends | no provider-specific continuity logic in core |
| generation reload | process state/scheduler survive; job uses submission-time generation/config |

## Corrected Required Invariants

1. Detection truth and preservation mutation are separate contracts sharing one recognition authority.
2. A strict compaction start is billable only after successful upstream Open; preview never emits/commits lifecycle truth.
3. Background extraction captures its execution generation and principal at submit time and is process-owned thereafter.
4. The extractor is detached from the primary secure-session turn while remaining financially attributable to the originating user.
5. Parent A-leg IDs are correlation, not extractor routing/session authority.
6. Every submitted extractor call uses normal Executor/BillingCallID/B-leg accounting; primary protocol usage stays separate.
7. Opaque/encrypted compaction state is never rewritten for continuity.
8. Capsule/source/job state survives generation reload but does not falsely claim process-restart durability.
9. Session partition plus explicit A-leg/branch identity prevents cross-branch capsule leakage.
10. No second provider client, transcript database, money ledger, or generic task framework is introduced.

## Requirements Correction Status

The final requirements must explicitly incorporate gaps 2–15, especially: additive preservation-interceptor merge/freeze semantics; process-owned background auxiliary scheduler; submit-time generation retention; detached auxiliary secure-session behavior; final-release stage ordering; workload classification for billing; branch keying; narrow transcript recovery; and honest restart durability.

With those corrections applied, the requirements quality gate is **PASS** and design may proceed.
