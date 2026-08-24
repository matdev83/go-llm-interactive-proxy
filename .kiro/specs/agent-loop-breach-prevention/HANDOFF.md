# HANDOFF — kiro-impl run: agent-loop-breach-prevention

Read this fully before resuming `/kiro-impl agent-loop-breach-prevention`. It encodes every binding decision, known issue, and process mechanic from the implementation session.

## Mission

Autonomous kiro-impl execution of the approved spec `.kiro/specs/agent-loop-breach-prevention/` (requirements/design/tasks all approved; phase `tasks-generated` with remediation pending). Branch: `feat/agent-loop-breach-prevention` (dedicated worktree, do NOT create another). Spec artifacts live beside this file; steering is `.kiro/steering/*` plus root/`.kiro/AGENTS.md`.

## Progress ledger

- Completed (marked `[x]` in tasks.md): **Tasks 1.1 through 10.3** (all foundational policy, progress tracking, stream-recovery continuation, auxiliary verification, continuation safety, request orchestration, post-output transport continuation, explicit completion, observability, and regression suites).
- Pending remediation: **Task 11** (Remediate canonical PR435 conversation-view steering integration):
  - 11.1 (P) Add failing unit & contract tests for steering writer registration, anchor resolution, and snapshot isolation.
  - 11.2 Implement steering writer integration in continuation orchestration and eliminate duplicate authority.
  - 11.3 (P) Add failing tests and implementation for exact-once reassertion, overlay lifecycle, and stale cleanup.
  - 11.4 Add failing tests and implementation for candidate capability rejection and transcript isolation.
  - 11.5 Converge full regression, race, and architecture ratchets.

## Binding architecture decisions (do not relitigate)

1. **Package map**:
   - `internal/core/stopguard`: PURE policy (cause/verdict/action vocab, NormalizeVerdict, Decide/DecideWithVerdict, ProgressTracker; imports only lipapi/stdlib; NO I/O ever — task 10.2 archtest enforces).
   - `internal/core/stopgate`: request-level Gate composing policy+tracker+continuationsafety; runtime consults it via `Executor.LoopGuard *loopguardRuntime` (nil = disabled fast path).
   - `internal/core/stopguardverify`: auxreq-backed Verifier adapter + evidence projector + six-rule instruction + strict JSON verdict parser (stays OUTSIDE stopguard because of the no-aux-I/O ratchet).
   - `internal/core/continuationsafety`: pure continuation-safety Evaluate + BuildRecoveryInstruction/BoundRecoveryText.
   - `pkg/lipsdk/steering` & `internal/core/conversationview`: canonical merged PR #435 infrastructure for hidden steering overlays, anchor resolution, turn snapshot isolation, projection, and reassertion.
2. **Canonical PR435 Steering Integration (SUPERSEDES outdated "future non-forwardable" note)**:
   - Direct appending to `Call.Messages` or `Call.Items` in `internal/core/runtime/agent_loop_guard_continuation.go` and ad-hoc hidden fields (`turnTerminal.guardHidden`) are temporary implementation artifacts that MUST be removed.
   - `conversationview` steering overlays are the single authoritative mechanism for hidden recovery instructions.
   - Construction: `sdkadapter.NewWriter(store, aLegID, resolver)` binds authoritative A-leg scope and trajectory resolver. The resolver must return the accepted user ingress call (`identityBoundTurn.ingressCall` / preserved ingress trajectory) plus current committed snapshot, preserving the terminal forwardable user message boundary required by `ResolveAfterIngressTailAnchor`.
   - Lifecycle: On actionable `CONTINUE`, runtime settles B1, formats instruction via `continuationsafety.BuildRecoveryInstruction`, registers overlay via `steering.Writer.Put` with fixed `OverlayID("alg-rec")` within the authoritative A-leg scope, `RoleDeveloper`, `AfterIngressTail`, `FailClosed`, and reason `loop_guard_recovery`. Runtime authority guarantees single active logical request per A-leg, preventing concurrent active ALG overlays.
   - Anchor resolution: `AfterIngressTail` resolves to fixed `MessageAnchor` on the terminal forwardable user message from accepted ingress trajectory; fails closed if user anchor is missing or excluded.
   - Snapshot sequencing: freeze Snapshot N+1 for hidden model turn B2 after overlay registration; all candidate arms/attempts of turn B2 share this snapshot. Never mutate already frozen turn snapshots.
   - Late transform reassertion: `conversationview.Reassert` using `OverlayProvenance` and `FilteredBaseline` guarantees exact-once steering injection before backend `Open`.
   - Deactivation: `steering.Writer.Deactivate(ctx, "alg-rec")` is called on final A terminal publication, cancellation, budget exhaustion, or leg open failure.
   - Stale cleanup: on subsequent external turn ingress, deterministically call `Deactivate(ctx, "alg-rec")` before taking the turn's initial snapshot; `ErrOverlayNotFound` or already inactive is treated as no-op success, while real persistence error fails closed.
3. **Config shape**: nested block `agent_loop_guard:` with snake_case leaves (mirrors `stream_recovery:`). Registered in `internal/core/configreload/inventory.go` with typed comparator in `policy.go` (DispositionReloadable).
4. **Verifier request constants**: Visibility `"private"`, `auxiliary.SessionModeDetached`, `DisablePlugins: []string{PluginID}` with `PluginID="agent_loop_guard"`.
5. **Budget semantics (Req 8.1)**: config `MaxSemanticContinuations=N` means N hidden legs. Tracker pins exhaustion at its cap inclusive.
6. **Continuation lineage**: new leg's `PreviousID = prior.ID` (walks lineage correctly).
7. **Telemetry honesty**: no fabricated values; bounded metric labels only.
8. **streamrecovery extension**: `Config.AllowPostOutputContinuation bool` + `DecisionContinuePostOutput`.

## Process mechanics that worked (follow them)

- TDD cadence: capture CLI RED output BEFORE implementing; merged per-group RED->GREEN->single-commit cycles are REQUIRED because `make quality-checks` runs `go vet` which compiles test files.
- Archtest gates: `TestCorePackagesHaveDocGo` (every internal/core/* needs doc.go), `TestLineComplexityBudgets` (bump by appending a dated history comment line in `internal/archtest/budgets.go`).
- Race: Windows TSAN fails to allocate (`make test-race` skips on Windows); targeted `go test -race` may fail with error code 87 on Windows — Linux CI covers TSAN race. Mutex-guard shared state anyway.
- Human decision on record: 100-modified-Go-files gate authorized to be exceeded for this run (`git config lip.allowLargeChange true`); PR requires `allow-large-change` label.
- Human decision on record (2026-08-25): canonical conversation-view steering integration architecture is approved; Task 11 remediation authorized for immediate execution.

## Resume order

1. Execute **Task 11.1**: Add failing unit & contract tests for `steering.Writer` registration, `AfterIngressTail` anchor resolution to `MessageAnchor`, `FailClosed` policy, turn snapshot N+1 freeze, and multi-store persistence (Memory, SQLite, PostgreSQL).
2. Execute **Task 11.2**: Implement `steering.Writer` in continuation orchestration; eliminate direct `Call.Messages`/`Items` append and remove `turnTerminal.guardHidden`.
3. Execute **Task 11.3**: Add failing tests and implement `conversationview.Reassert` (with `OverlayProvenance` and `FilteredBaseline`), explicit deactivation on terminal/cancel/exhaustion/open-failure, and stale-overlay cleanup on external turn ingress.
4. Execute **Task 11.4**: Add failing tests and implement candidate capability rejection for unsupported role/placement and verify transcript isolation (absent from A stream and `ContinuationRecord`s).
5. Execute **Task 11.5**: Converge full regression suite, multi-store persistence, deterministic race tests, architecture ratchets, and repository quality gates (`make quality-checks`, `make test`, `make qa`).
6. Run `/kiro-validate-impl agent-loop-breach-prevention` as GO/NO-GO gate.
