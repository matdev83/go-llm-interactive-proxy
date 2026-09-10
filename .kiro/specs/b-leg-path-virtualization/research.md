# Research & Design Decisions

## Summary
- **Feature**: B-Leg Path Virtualization
- **Discovery Scope**: Brownfield / Complex Integration
- **Baseline**: `main` at `96803666590d47a3ede11b93db286db290839f84` (2026-09-09)
- **Key Findings**:
  - The proxy already has the canonical A-leg/B-leg split, canonical tool representations, workspace metadata, attempt/request shaping stages, and complete tool-call assembly needed for a reversible path namespace.
  - The initial idea of replacing absolute paths anywhere inside tool output is unsafe because opaque tool output can contain source/config data whose literal paths are semantically meaningful.
  - The existing complete-tool-call finalizer path is the correct direction for model→client expansion, but its metadata lacks workspace context and its shared default 64 KiB assembly behavior can bypass required expansion.
  - V1 should avoid a durable dynamic alias table: deterministic `Workspace.ProjectRoot` virtualization makes restart, reload, retries, and provider-side continuation reconstructable.

## Brownfield Gap Analysis

### Current runtime facts

1. Frontends decode wire requests into `pkg/lipapi.Call`; backends consume canonical calls and emit canonical event streams.
2. Feature plugins contribute typed extension planes. Existing relevant planes/stages include:
   - request-wide transforms;
   - candidate attempt transforms;
   - request-part hooks;
   - tool-call finalizers;
   - tool reactors;
   - response-part hooks.
3. `request.AttemptTransform` receives candidate metadata including A-leg/session/workspace plus `request.Services{State, Aux}`.
4. `RequestPartHook` runs during attempt opening before final backend translation.
5. Runtime ordering is materially:
   - clone baseline / interleaved shaping;
   - candidate attempt transforms;
   - candidate admission / preliminary context and token preflight;
   - request-part hooks;
   - post-hook rederivation;
   - final conversation-view reassertion;
   - clamps / backend-ingress accounting / final authorization;
   - candidate adaptation;
   - PTB capture;
   - `Backend.Open`.
6. `toolCallAssembler` reconstructs complete tool arguments, runs `toolcall.Finalizer`s, and synthesizes a rewritten lifecycle.
7. The assembler currently uses one shared maximum argument size, defaulting to 64 KiB; overflow falls back to the original lifecycle.
8. `toolcall.Meta` currently contains trace/A-leg/B-leg/attempt identifiers but not workspace/session/scope.
9. `hooks.ToolMeta` does contain workspace/session/scope, but tool reactors observe incremental lifecycle events and therefore are a poor place to perform arbitrary-length JSON alias expansion safely.
10. `WorkspaceView.ProjectRoot` already exists as the authoritative feature-facing workspace root.
11. The default extension state store is process-memory state. Session-scoped state exists, but using it for the primary path mapping would make restart semantics weaker unless a durable feature-state design were added.
12. Conversation-view final reassertion operates on message visibility/steering semantics; a final idempotent path rewrite is still required because late request shaping exists after initial attempt transforms.

### Gaps discovered

| Gap | Severity | Consequence | Required repair |
|---|---|---|---|
| No path-virtualization feature | Expected | No savings | Add feature-owned implementation |
| Opaque tool result payloads are not reliably distinguishable from source/file content | P0 semantic | Blind replacement can alter model-visible file contents | Restrict rewriting to explicit/safely inferred path-bearing surfaces; opaque result rewrite opt-in/profiled |
| `toolcall.Meta` lacks workspace context | P0 integration | Complete finalizer cannot deterministically expand workspace alias | Add read-only workspace/session/scope views to finalizer metadata or an equivalent narrow request context |
| Shared 64 KiB assembler limit can bypass finalizers | P0 correctness/security | Virtual alias could reach client unresolved | Add mandatory buffering/completeness contract so path expansion cannot silently pass through |
| Attempt transform is not final PTB boundary | P1 correctness | Later hooks could introduce/reintroduce unvirtualized tool surfaces | Reapply idempotently at request-part stage and assert final backend-effective state |
| No durable general alias dictionary | P1 continuity | Dynamic aliases would be lost on restart | Keep V1 deterministic and workspace-root-only |
| No cross-OS path parser independent of host OS | P1 portability | Windows paths on Linux proxy or POSIX paths on Windows proxy mis-handle | Implement feature-owned lexical parser |
| No measurements of actual savings | P1 product | Optimization value unknown | Add audit mode and content-free savings metrics |

### Requirements repair caused by gap analysis

The initial product idea was amended in the requirements in four ways:

1. **Blind tool-output replacement was removed.** Only structured/configured/safely inferred path-bearing surfaces may be rewritten.
2. **Transparency was narrowed to tool-boundary transparency.** Ordinary assistant prose remains untouched in V1; a model may therefore mention a virtual alias in prose.
3. **Primary mapping became deterministic workspace-root-only.** Dynamic discovered roots are explicitly deferred.
4. **Reverse expansion became mandatory/fail-closed once aliases are model-visible.** The existing 64 KiB pass-through behavior is not acceptable for this feature.

The requirements gate was re-run after these changes and is internally consistent.

## Research Log

### Canonical representation and extension seams
- **Context**: Determine whether the feature can remain provider-neutral.
- **Sources Consulted**:
  - `docs/architecture.md`
  - `docs/extension-platform-authoring.md`
  - `pkg/lipapi/items.go`
  - `pkg/lipapi/parts.go`
  - `pkg/lipsdk/request/transform.go`
  - `pkg/lipsdk/request/attempt_transform.go`
  - `pkg/lipsdk/hooks/parts.go`
- **Findings**:
  - Tool calls/results are represented canonically for both item-authoritative and legacy message flows.
  - Attempt transforms have workspace context and state services.
  - Request-part hooks are a late canonical mutation point before provider translation.
- **Implications**:
  - No frontend×backend path translators are needed.
  - Outbound virtualization should be a feature plugin over canonical calls.

### Tool-call reverse expansion
- **Context**: Avoid rewriting incremental JSON fragments.
- **Sources Consulted**:
  - `internal/core/runtime/tool_call_assembler.go`
  - `pkg/lipsdk/toolcall/finalizer.go`
  - `internal/core/runtime/response_pipeline_observations.go`
  - `pkg/lipsdk/feature/plane_manifest.go`
- **Findings**:
  - The assembler already reconstructs complete JSON before tool policies/reactors.
  - Finalized tool calls are rewritten before downstream tool policy, which is the correct security order.
  - Workspace metadata is missing from `toolcall.Meta`.
  - One global 64 KiB assembly cap can cause original fragments to pass through.
- **Implications**:
  - Reuse the assembler/finalizer mechanism, but extend its generic metadata/completeness contract rather than introducing raw-stream regex rewriting.

### Conversation view and final backend boundary
- **Context**: Determine whether attempt-transform output is the final model-visible call.
- **Sources Consulted**:
  - `internal/core/runtime/executor_open_attempt.go`
  - `internal/core/runtime/executor_attempt_transform.go`
  - `internal/core/conversationprojection/reassert.go`
  - `docs/conversation-view.md`
- **Findings**:
  - Candidate attempt transforms are followed by request-part hooks and final conversation-view reassertion.
  - Candidate adaptation and PTB capture happen still later.
- **Implications**:
  - Use attempt transformation early enough to affect context/preflight, then reapply idempotently through the request-part hook.
  - Add focused runtime tests proving PTB/backend ingress remains virtualized.

### State and continuity
- **Context**: Decide where the translation table should live.
- **Sources Consulted**:
  - `pkg/lipsdk/state/store.go`
  - `internal/core/state/mem.go`
  - `pkg/lipsdk/workspace/view.go`
- **Findings**:
  - Session-scoped feature state exists but the default store is in-memory.
  - `Workspace.ProjectRoot` is already available on every request.
- **Implications**:
  - V1 does not need mutable mapping state. The alias is a pure function of path flavor, workspace root, and feature configuration.
  - A later multi-root feature can introduce durable A-leg-scoped mapping semantics explicitly.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|---|---|---|---|---|
| Raw wire proxy replacement | Rewrite provider/client JSON bodies directly | Near network boundary | Protocol-specific, escaping-sensitive, Cartesian complexity | Reject |
| Global substring replacement in canonical tool payloads | Replace root everywhere inside tool calls/results | Simple, high compression | Corrupts source/content/patch semantics | Reject |
| Tool-reactor delta rewriting | Rewrite streamed args incrementally | Existing ToolMeta has workspace | Fragment-boundary complexity, stateful JSON rewrite | Reject |
| Canonical feature + completed-call expansion | Attempt/request shaping outbound; complete finalizer inbound | Protocol-neutral, security ordering correct | Requires narrow SDK/assembler enhancement | Select |
| Dynamic session dictionary | Learn arbitrary prefixes and assign aliases | Higher potential savings | Restart/provider-continuation persistence problem | Defer |
| Deterministic workspace-root alias | Pure mapping from Workspace.ProjectRoot | Restart-safe, simple, no store | Only compresses one root | Select for V1 |

## Design Decisions

### Decision: Treat the feature as path virtualization, not compression
- **Context**: The transformation must be exactly reversible.
- **Selected Approach**: Maintain real-path A-leg truth and short alias B-leg truth.
- **Rationale**: Makes invariants and security ordering explicit.
- **Trade-offs**: Does not compress arbitrary non-path payloads.

### Decision: V1 maps only the authoritative project root
- **Alternatives Considered**:
  1. dynamic prefix discovery;
  2. session dictionary in extension state;
  3. deterministic project-root mapping.
- **Selected Approach**: deterministic project-root mapping.
- **Rationale**: eliminates persistence/restart ambiguity and substantially covers worktree/repository-prefix repetition.
- **Trade-offs**: misses secondary roots such as temp/cache/vendor directories.

### Decision: Use host-independent lexical path flavors
- **Selected Approach**: feature-owned parser for POSIX, Windows drive, UNC, and extended paths.
- **Rationale**: proxy host OS cannot be assumed to match client workspace OS.
- **Trade-offs**: lexical semantics only; no symlink/realpath resolution.

### Decision: Explicit path-bearing selectors
- **Selected Approach**: configured JSON Pointers plus conservative schema-assisted inference from a bounded key vocabulary.
- **Rationale**: prevents rewriting arbitrary source/content strings.
- **Trade-offs**: some tools receive no compression until profiled.

### Decision: Two outbound passes, one pure rewriter
- **Selected Approach**:
  - candidate attempt transform: early/idempotent virtualization so admission/sizing can see savings;
  - request-part hook: final idempotent canonical reapplication after later shaping.
- **Rationale**: matches current runtime ordering without adding feature-specific core branches.
- **Trade-offs**: same pure transform runs twice; implementation must be cheap and idempotent.

### Decision: Completed-call expansion is mandatory
- **Selected Approach**: extend generic finalizer metadata with workspace/session/scope views and introduce an optional finalizer buffering/completeness contract.
- **Rationale**: incremental raw-delta rewriting is error-prone, while silent pass-through of aliases is unacceptable.
- **Trade-offs**: requires small SDK/runtime-platform work beyond a pure feature package.

### Decision: Keep ordinary assistant prose untouched
- **Selected Approach**: no prose/reasoning scan in V1.
- **Rationale**: honors narrow tool-surface scope and avoids broad completion mutation.
- **Trade-offs**: model-written prose may expose the alias. Documentation must call this tool-boundary transparency, not full textual transparency.

## Risks & Mitigations

- **Alias collision with a legitimate real path** — reserved namespace validation; disable/fail safely on collision evidence.
- **Huge tool calls** — bounded mandatory expansion limit; fail closed if an applicable aliased path call exceeds it; do not silently pass through.
- **Path inside source content** — never recurse through arbitrary strings; selector/profile-only rewriting.
- **Cross-OS mismatch** — custom lexical parser independent of runtime OS.
- **Tool schema variance** — explicit JSON Pointer profiles and conservative inference; unknown means skip.
- **Late-stage reintroduction** — idempotent request-part reapplication plus PTB/backend-ingress regression tests.
- **Provider continuation after restart** — deterministic workspace-root alias, no ephemeral primary mapping.
- **Performance overhead** — audit mode, benchmarks, no regex-heavy scanning, bounded selectors.
- **Interaction with tool-call repair** — characterize order and make mandatory expansion independent of optional repair size policy.

## Design Validation Verdict

**GO after repairs.**

The draft design was revalidated against current `main`. Two local design defects were repaired before task generation:

1. The first draft assumed `AttemptTransform` was the final B-leg mutation boundary. Current runtime proves request-part hooks, conversation-view reassertion, accounting/admission, and candidate adaptation occur later. The design now specifies a second idempotent request-part pass and explicit PTB/backend-ingress invariants.
2. The first draft reused the existing tool-call finalizer without addressing the 64 KiB shared assembler fallback. The design now requires a generic mandatory completeness/buffering contract and fail-closed overflow semantics for path expansion.

No unresolved architecture blocker remains.

## References

Repository-local sources:
- `.kiro/steering/product.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/testing.md`
- `.kiro/steering/routing-and-orchestration.md`
- `docs/architecture.md`
- `docs/extension-platform-authoring.md`
- `docs/conversation-view.md`
- `pkg/lipapi/tool_classification.go`
- `pkg/lipsdk/workspace/view.go`
- `pkg/lipsdk/request/attempt_transform.go`
- `pkg/lipsdk/hooks/parts.go`
- `pkg/lipsdk/toolcall/finalizer.go`
- `internal/core/runtime/executor_open_attempt.go`
- `internal/core/runtime/executor_attempt_transform.go`
- `internal/core/runtime/tool_call_assembler.go`
- `internal/core/runtime/response_pipeline_observations.go`
