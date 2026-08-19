# Brownfield Requirements Gap Analysis

## Scope

This analysis compares `requirements.md` against current `main` as of 2026-08-19. The repository already implements most of the difficult lifecycle and infrastructure around issue #369. The remaining gap is not a new generic compression subsystem; it is a narrow bridge between the existing reasoning-preservation artifact lifecycle and the generic auxiliary/background execution capability.

## Existing Authorities That Must Be Reused

### 1. Reasoning preservation already owns the correct surfaced-winner lifecycle

Current package: `internal/plugins/features/reasoningpreservation`.

Important landed behavior:

- `StreamObserverFactory.Open` resolves capture eligibility for the surfaced candidate.
- The observer consumes defensive canonical stream events and distinguishes text reasoning, reasoning signatures, opaque reasoning, exact reasoning parts, tools, text, images, and files.
- `Finish` commits only for `response.OutcomeSuccessReleased`.
- Original reasoning is stored as `TurnArtifact.Reasoning []PlacedReasoning` with exact placement relative to non-reasoning parts.
- The artifact is scoped through the authoritative session partition and matched later by the existing `AttemptTransform`.
- Existing state is bounded by TTL, artifacts/session, bytes/turn, and bytes/session.
- Existing telemetry intentionally avoids reasoning contents.

**Conclusion:** do not create another observer, reasoning transcript, matching engine, or replay owner. Compression must extend this feature after original artifact commit.

### 2. Exact/native replay is already a first-class contract

The completed OpenAI Responses and direct Codex work already requires exact item/opaque/encrypted continuity. Direct Codex additionally owns native `/responses/compact` checkpoint behavior.

The standard composition also installs a bounded Codex companion rule/continuity marker around `reasoning-output-preservation`, but native connector policy remains separate.

**Conclusion:** semantic compression is not a replacement for existing native compaction. Exactness must be represented as an explicit semantic classification and take precedence even when an exact artifact contains readable text.

### 3. Generic auxiliary/background infrastructure already exists

Current packages:

- `pkg/lipsdk/auxiliary`;
- `internal/core/auxreq`;
- runtime composition that binds generation-local auxiliary clients.

Landed capabilities include:

- normal Executor/routing/B2BUA execution;
- detached private child calls;
- originating principal scope propagation;
- normal admission/billing/metering path;
- bounded worker pool and queue;
- coalescing;
- generation pin retention;
- bounded job timeout;
- bounded retained results and TTL;
- explicit `Forget`;
- feature self-disable patterns from `compaction-continuity`.

**Conclusion:** do not add a provider client, compressor-specific worker pool, goroutine-per-artifact scheduler, second billing seam, or dependency on the `compactioncontinuity` feature package.

## Requirement-by-Requirement Gap Map

| Requirement | Current state | Gap / action |
|---|---|---|
| R1 exact/native baseline | Strong existing exact replay/native compaction | Add explicit non-compressible semantic classification and architecture tests preventing exact payload egress to compressor |
| R2 replay semantics | Replay dialect support exists, but no lossy/semantic replay class | Add one typed classification/profile authority used at capture and destination replay |
| R3 opt-in config | Reasoning config has no `compression` block | Add strictly decoded nested config; disabled default and shadow/active modes |
| R4 original-before-compression | Existing observer already has correct `success_released` append point | Hook optional submission only after successful original `Append`; no pre-commit job |
| R5 non-destructive store | `TurnArtifact` contains original only | Add bounded pending/surrogate state plus CAS/update operations without replacing original |
| R6 generic auxiliary | Infrastructure exists | Bind narrow generic auxiliary capability into reasoning feature; create feature-specific compressor request/validator only |
| R7 billing | Aux execution already traverses ordinary economic path | Add compressor workload role and tests for originating principal/admission/settlement |
| R8 output validation | No reasoning compressor exists | Add ordinary-text result validator, hard bounds, no-tools validation, savings policy |
| R9 non-blocking adoption | `BackgroundClient` has `SubmitCollect`, blocking `Await`, `Forget` only | Add a generic non-blocking result inspection operation; avoid timing hacks/callback goroutines |
| R10 target revalidation | AttemptTransform already validates replay dialects | Extend with semantic replay permission and surrogate selection; original remains fallback |
| R11 shadow evidence | No compression telemetry | Add content-free compression outcome/size/savings metrics and shadow-only behavior |
| R12 architecture | Existing ownership mostly correct | Avoid widening response observer services if a constructor/composition port suffices; add dependency/lifecycle gates |
| R13 release evidence | Strong reasoning E2E and compaction/aux tests exist | Extend existing suites instead of building new Cartesian matrices |

## Critical Gap 1: There Is No Semantic Compression Classification

Current reasoning preservation knows whether a candidate is eligible for replay and which dialects it can represent. It does not currently say whether a retained artifact may be lossily rewritten.

Treating `ReasoningPart.Text != ""` as compressibility would be unsafe because:

- exact Responses items may contain textual summary/content while still requiring exact item replay;
- Anthropic thinking may carry signatures whose validity is coupled to content;
- provider-native continuity/checkpoint artifacts may contain inspectable text but still be exact authority;
- future providers may introduce new signed/opaque mixed structures.

### Required remediation

Introduce one typed semantic authority, feature/canonical in nature, with an unknown/default state that fails closed. A possible shape is:

```go
type ReplaySemantics uint8

const (
    ReplaySemanticsUnknown ReplaySemantics = iota
    ReplaySemanticsExact
    ReplaySemanticsSemanticText
    ReplaySemanticsNotPersisted
)
```

The final naming/location is a design choice, but capture submission and destination replay must consult the same authority.

Do not create provider-name conditionals in core. Provider adapters/profile composition may contribute facts, while reasoning preservation resolves the effective semantic policy.

## Critical Gap 2: Current Artifact Storage Cannot Represent Safe Async Compression

Current `TurnArtifact` has an ID, anchor, source identity, original placements, creation time, and original reasoning byte count. There is no place for:

- a pending auxiliary job reference;
- source/policy digest needed to validate a late result;
- a validated surrogate;
- surrogate byte/token accounting;
- compression mode/profile metadata.

### Required remediation

Keep `Reasoning` unchanged as the authoritative original and add bounded optional state. Conceptually:

```text
TurnArtifact
  original reasoning placements  <-- authoritative
  pending compression?           <-- optional bounded reference
  validated surrogate?           <-- optional bounded replay optimization
```

The store needs atomic compare-and-set/equivalent operations such as:

- attach pending compression if artifact ID/anchor/revision still matches;
- attach validated surrogate only for the matching pending/source revision;
- clear/forget stale pending work;
- preserve original even when optional compression budget is exhausted.

A surrogate must never cause authoritative original eviction merely to fit an optimization. This suggests a dedicated bounded optional-compression byte budget or a store rule that rejects optional attachment rather than evicting the original.

## Critical Gap 3: Background Result Adoption Needs a Non-Blocking API

Current `pkg/lipsdk/auxiliary.BackgroundClient` is:

```go
SubmitCollect(...)
Await(...)
Forget(...)
```

`Await` is blocking until completion/error/context cancellation. Reasoning compression has no natural compaction barrier at which waiting is required, and waiting inside `StreamObserver.Finish` would make primary response release dependent on auxiliary inference.

A separate callback/maintenance goroutine would introduce a new lifecycle owner and allow late callbacks to mutate retired feature/store state.

### Required remediation

Add a small additive generic non-blocking inspection API. Exact naming is design-level, e.g.:

```go
type BackgroundResultState uint8

type PollResult struct {
    State BackgroundResultState // pending|completed|failed|not_found
    Collected lipapi.Collected
    Err error
}

Poll(id JobID) PollResult
```

Properties:

- no wait/sleep/timing race;
- defensive result copy just like `Await`;
- no feature-specific fields;
- bounded by existing scheduler retention;
- safe on disabled/closed scheduler;
- `Forget` remains explicit after terminal consumption.

Reasoning `AttemptTransform` can then poll once during a later matching replay. If still pending, it uses the original immediately. If complete, it validates and CAS-adopts the surrogate. This intentionally trades earliest-possible compression benefit for lower latency/lifecycle risk.

## Critical Gap 4: Final-Stream `response.Services` Is Deliberately Empty

`pkg/lipsdk/response.StreamObserver` currently documents final-stream observation as read-only and `response.Services` as an empty forward-compatible bag. Injecting generic state/Aux into that bag merely for #369 would broaden a clean SDK contract and make an optimization look like a generic observer authority.

### Required remediation

Prefer composition-time constructor injection into the reasoning-preservation feature:

```text
runtime/standard composition
    -> generation-bound auxiliary.BackgroundClient
    -> reasoning preservation InstanceParts / compressor port
    -> observer and AttemptTransform share the narrow port/store
```

Compression-disabled construction should remain the current config-only path and should not require BackgroundAux.

Only widen `response.Services` if design validation proves constructor/composition injection cannot express the lifecycle safely. Current brownfield evidence suggests widening is unnecessary.

## Critical Gap 5: The Compaction Continuity Extractor Is Reusable as a Pattern, Not as a Dependency

`internal/plugins/features/compactioncontinuity/extractor` already demonstrates:

- fixed system policy;
- untrusted delimited input;
- independently routed detached no-tools child;
- plugin self-disable;
- strict input/output bounds;
- background submission;
- validated result parsing.

But it is semantically wrong to import it for #369 because it encodes continuity capsule schemas, source references, branch revisions, plan/decision extraction, and compaction-specific role names.

### Required remediation

Build a much smaller reasoning compressor package under the reasoning-preservation feature (or a narrowly reusable semantic-text helper only if another immediate caller exists). Its input is eligible reasoning text only; its output is bounded text only.

Avoid premature genericization: the reusable infrastructure is `auxiliary`, not the feature-specific prompt/schema.

## Critical Gap 6: Existing Store Bounds Need Explicit Optional-State Semantics

The current store bounds authoritative artifacts. Storing both original and surrogate increases memory even though the product goal is reduced reinjected context/cost.

Unsafe options include:

- counting surrogate bytes against the same cap and evicting the original;
- replacing original bytes with surrogate bytes;
- retaining a surrogate after original expiry;
- leaving pending result references indefinitely.

### Required remediation

Specify and test:

- original artifact bounds remain unchanged;
- optional compression state has its own bounded budget/count or is rejected before it can force original eviction;
- pending/surrogate lifetime is capped by original TTL;
- stale/expired/evicted original means result cannot be adopted;
- scheduler result is forgotten after terminal adoption/rejection when safely possible.

## Critical Gap 7: Source and Destination Safety Are Separate Checks

A source artifact may be semantically compressible while a later destination candidate requires exact replay or cannot represent semantic text.

### Required remediation

Use two gates:

1. **source gate** after original commit: is this artifact/profile safe to send to a semantic compressor?
2. **destination gate** during AttemptTransform: may this candidate legally receive the surrogate instead of the original?

The destination gate must re-evaluate the current candidate profile. It must not trust the source backend/model or compressor result as proof.

## Critical Gap 8: Shadow Mode Must Be a Real Behavioral State

A simple config flag that generates metrics while active substitution accidentally occurs would defeat staged rollout.

### Required remediation

Define replay selection such that:

- `disabled`: no job/state/telemetry;
- `shadow`: job + validation + optional surrogate storage; original always replayed;
- `active`: surrogate may be selected only after all source/result/destination checks.

Tests must assert actual backend-visible historical reasoning, not merely internal flags.

## Interaction with Concurrent Active Specs

Current active Kiro specs include pipeline/terminal ownership simplifications and other unrelated work. Implementation of this spec must rebase/revalidate if those active specs materially change:

- final stream observer lifecycle;
- request AttemptTransform ownership/order;
- runtime feature composition;
- auxiliary client/scheduler ownership;
- generation retirement.

The SDD should not freeze incidental function names that are expected to move under active simplification work; it should freeze semantic ownership and ordering instead.

## Requirements Corrections Applied After Gap Analysis

The initial requirements were hardened by this analysis in the following ways:

1. made non-blocking result inspection an explicit generic infrastructure requirement rather than hand-waving asynchronous CAS attachment;
2. prohibited completion callbacks/feature maintenance goroutines in v1;
3. required optional compression state to be unable to evict/destroy authoritative originals merely for memory budget;
4. required constructor/composition injection before considering `response.Services` widening;
5. separated source compressibility from destination surrogate replay permission;
6. clarified that generic aux infrastructure is a dependency but `compactioncontinuity` feature semantics and compaction detection are not;
7. made shadow mode backend-visible non-substitution testable;
8. required active pipeline simplification specs to trigger revalidation if they move lifecycle ownership.

## Gap Analysis Verdict

**GO to design, with the above remediations mandatory.**

The brownfield repository already owns the hardest machinery. The implementation should be relatively narrow if it resists duplicating infrastructure. The main new risk is not compressor prompting; it is safe asynchronous result adoption while retaining original exact state and respecting destination replay semantics.
