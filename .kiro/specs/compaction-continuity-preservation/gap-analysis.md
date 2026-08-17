# Brownfield Requirements Gap Analysis

## Result

**PASS after requirements and design-validation corrections.** The feature can be implemented without a new provider client, second transcript database, second billing path, or general workflow runtime. The correction loop made the real brownfield constraints explicit: #312 is a prerequisite; preservation mutation is separate from metadata observation; background auxiliary work needs submit-time generation ownership and ProcessServices lifetime; detached extraction needs a private child A-leg; late results must remain useful within a bounded branch window; and opaque/encrypted compaction state is reinjection-only.

## Existing Brownfield Facts

- `compaction-event-detection` is currently a merged **specification**, not runtime code on `main`. It defines process-owned A-leg detector state, one versioned rule matrix, metadata-only `compaction.Observer`, start only after successful upstream `Open`, and committed response observation at the final release seam.
- Current `pkg/lipsdk/auxiliary.Client` is synchronous. `Collect` delegates to `Stream` and the normal runtime Executor.
- `internal/core/auxreq.Client` already clones authenticated principal/scope, marks internal origin, supports plugin suppression and retains `genpin.KindAsync` when auxiliary execution starts.
- `genpin.Retainer` makes spawn rights explicit; attempting to retain after the request lease ends fails closed.
- Current auxiliary `Role`/`Visibility` are lineage metadata only. They do not suppress secure-session BeginTurn/turn transcript/activity or parent route-override authority.
- `lipapi.Call.Route.Selector` already gives a child canonical independent route that normal core routing can parse/execute.
- Billing account identity is principal-scope-derived. Preserving principal scope lets an independent child reuse current usage/billing authorities and obtain its own BillingCallID.
- `ProcessServices` owns process lifetime. `ExtensionState` survives immutable generation reload, while feature lifecycles are generation-composed.
- `ExtensionState` is process-local/in-memory by default and `ScopeSession` partitions by authoritative SessionID when available; A-leg/branch must remain an additional continuity key.
- Secure-session transcript storage already exists when explicitly enabled; it is the only appropriate durable historical recovery source in v1.
- `lipapi.CompactionItem` carries `EncryptedContent` and opaque provider data rather than a universal plaintext summary field.
- Current `FeatureBundle` has no content-bearing compaction-preservation surface; #312 proposes only non-mutating compaction observers.

## Gaps and Corrections

### G1 — #312 runtime capability is absent

**Gap:** #344 could otherwise silently duplicate compaction signatures or pretend lifecycle events already exist.

**Correction:** implementation order makes `compaction-event-detection` runtime a hard prerequisite. Enabled continuity composition fails clearly if preview/commit capability is absent. Disabled continuity remains compatible/no-op.

### G2 — `compaction.Observer` cannot carry preservation content/mutation

**Gap:** observer events intentionally contain no canonical request/response body and return no replacement decision.

**Correction:** add a distinct `compaction.Preserver`-style slice, separately merged/frozen in FeatureBundle/runtime snapshot. Observer remains unchanged.

### G3 — pre-open protection must not commit detector truth

**Gap:** first post-compaction request may need continuity before B-leg Open, while detector start/completion commitment is intentionally tied to successful Open/final release.

**Correction:** factor pure request preview from the same matcher/fingerprint authority. Preview can guide a barrier but cannot mutate detector state, emit lifecycle events, or start a strict billable extraction merely from an unopened signature.

### G4 — response preservation must not make `ResponseReleased` observe a pre-final event

**Gap:** an initial sequence of committed detector completion -> preservation mutation would violate #312's “event actually released” meaning.

**Correction:** add pure response preview and use final ordering:

```text
selected event
 -> PreviewResponse (pure)
 -> preservation finalization / safe plaintext mutation
 -> ResponseReleased(final event) (commit)
 -> metadata observers
 -> client
```

No ordinary response hook runs after preservation finalization.

### G5 — synchronous `Aux.Collect` is not a safe background worker

**Gap:** `go Aux.Collect(parentCtx)` can start after spawn authority is gone, inherit cancellation, leak across shutdown, and lacks bounded queue/result ownership.

**Correction:** add a narrow process-owned BackgroundAux collector. `SubmitCollect` synchronously resolves the current runner, retains `KindAsync`, clones safe attribution and transfers ownership to a bounded scheduler. Workers use scheduler-rooted deadlines. Await/Forget operate on bounded job IDs/results. No arbitrary callbacks/functions/tasks are accepted.

### G6 — worker lifetime cannot be generation-only

**Gap:** generation-scoped feature lifecycle may retire while a job remains in flight.

**Correction:** ProcessServices owns scheduler and BranchCoordinator. Generation snapshots hold non-owning adapters. Each job retains exactly the generation it needs.

### G7 — detached child cannot reuse primary secure-session turn semantics

**Gap:** normal Executor preparation would otherwise enter primary BeginTurn/transcript/activity/route-override paths.

**Correction:** add trusted internal detached auxiliary mode. It preserves principal/scope but suppresses primary secure-session turn effects and parent route authority.

### G8 — detached child still needs normal B2BUA/request authority

**Gap:** making the child completely session/A-leg-less would force a second execution/billing path; reusing the parent A-leg would contaminate primary authority.

**Correction:** detached child creates/touches a **private child A-leg** via existing B2BUA semantics. Parent A-leg is lineage only. The child then gets ordinary request authority, private B-legs, separate BillingCallID, usage and provider COGS.

### G9 — extractor route must remain independent

**Gap:** parent A-leg route override could otherwise hijack the extractor model.

**Correction:** child uses explicit configured `Call.Route.Selector` (or explicit inherit policy). Detached private A-leg has no inherited parent override. Normal router remains authoritative; no provider client bypass.

### G10 — accounting needs workload distinction, not a new money path

**Gap:** principal inheritance solves account attribution but operators/users need to separate continuity overhead from primary inference.

**Correction:** project bounded content-free auxiliary workload/role (`compaction_continuity_extractor`) into existing metering/billing/report correlation. Child has separate BillingCallID/B-legs; primary protocol usage remains unchanged; account totals include child usage.

### G11 — `CompactionItem` cannot be treated as plaintext summary

**Gap:** mechanical append into `EncryptedContent`/opaque state can corrupt provider replay/continuation semantics.

**Correction:** result augmentation is allowlisted only for verified mutable plaintext carriers. Encrypted/opaque bytes are immutable. Mandatory safe fallback is proxy-owned first-post-compaction reinjection.

### G12 — process state is reload-safe, not restart-durable

**Gap:** ExtensionState is useful for v1 but cannot support a restart-survival claim.

**Correction:** v1 promises process/generation continuity only. Authorized existing secure-session transcript may be used through a narrow bounded reader to reconstruct after restart; otherwise missing capsule is fail-open. No second durable transcript/job/state platform.

### G13 — session partition alone is not a branch identity

**Gap:** `ScopeSession` can group multiple A-leg branches/forks.

**Correction:** continuity key is authoritative SessionID partition plus explicit A-leg/branch. Without secure SessionID, principal-isolated proxy-owned A-leg is authority. Client hints never select another branch.

### G14 — per-generation mutex cannot protect capsule revisions

**Gap:** Store Get/Put has no CAS and a feature-instance lock disappears on reload.

**Correction:** add a small process-owned BranchCoordinator that serializes revision/high-watermark/job/injection updates while using ExtensionState as serialized backing where practical. It treats capsule/source blobs opaquely and is not a generic transactional state framework.

### G15 — raw late result must remain useful without becoming durable memory

**Gap:** if the completion barrier times out and result TTL is too short, a valid extraction can disappear before the next turn.

**Correction:** while BranchState references a PendingJobID, bounded raw result retention remains useful up to the configured pending continuity window; first consumption parses/merges and Forget deletes raw output. Branch/job expiry clears both coherently. No result outlives bounded continuity/source retention.

### G16 — durable transcript access must stay behind authorization boundary

**Gap:** feature code importing secure-session Bun/store internals would break layering/security.

**Correction:** optional restart/historical recovery uses a narrow authorized read adapter. Ordinary compaction uses current canonical baseline/process sanitized window first.

### G17 — config reload/disable cannot erase submitted billing obligations

**Gap:** disabling feature after provider submission cannot retroactively make the child free or orphan its generation.

**Correction:** jobs use immutable submission-time config/generation and complete/cancel/settle through captured authorities. New config only affects future jobs; disable stops new submissions.

## Brownfield Compatibility Matrix

| Existing subsystem | Required treatment |
|---|---|
| #312 `compactiondetect` | prerequisite; shared request/response preview + committed state; no duplicate matrix |
| `compaction.Observer` | unchanged metadata-only/non-mutating contract |
| FeatureBundle/snapshot | additive separately frozen preservation-interceptor slice |
| synchronous `auxiliary.Client` | retained source-compatible |
| new BackgroundAux | additive narrow model-collection scheduler only |
| `genpin` | `KindAsync` retained synchronously at submit |
| ProcessServices | owns scheduler + BranchCoordinator |
| ExtensionState | process backing for bounded serializable branch state |
| secure session | primary turn/transcript/activity untouched by detached child |
| B2BUA | private child A-leg/B-legs; parent IDs lineage only |
| routing | explicit child selector; parent override not inherited implicitly |
| usage/billing | existing authorities; separate child BillingCallID and workload class |
| frontend protocol usage | remains primary-call-only |
| secure transcript | optional authorized recovery only; no second DB |
| compaction opaque/encrypted fields | exact byte preservation |
| provider/frontends | no continuity-specific core/adapter branches |
| generation reload | process state survives; jobs use captured generation/config |

## Corrected Invariants

1. One compaction recognition authority; previews never commit lifecycle truth.
2. `compaction.Observer` remains metadata-only and preservation mutation is separate.
3. Committed `ResponseReleased` sees the actual final event after permitted preservation mutation.
4. Strict semantic extraction is not submitted before successful primary Open.
5. Background jobs acquire generation ownership before request spawn authority ends.
6. ProcessServices owns workers/branch synchronization across reload.
7. Detached extractor uses a private child A-leg, not the parent A-leg and not a second provider path.
8. Same authenticated user/account is billed by default; primary protocol usage stays separate.
9. Parent route override cannot silently change extractor route.
10. Opaque/encrypted compaction state is immutable; reinjection is universal fallback.
11. Branch state is bounded/revisioned/SessionID+A-leg scoped and reload-safe, not falsely restart-durable.
12. No second transcript DB, financial ledger, generic task framework, or second summary LLM pass.

## Final Gate

All identified requirements gaps and design-validation corrections are reflected in final `requirements.md` and `design.md`. The brownfield gate is **PASS** for TDD task generation.
