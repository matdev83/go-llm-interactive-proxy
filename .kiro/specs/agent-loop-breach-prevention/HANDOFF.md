# HANDOFF — kiro-impl run: agent-loop-breach-prevention

Read this fully before resuming `/kiro-impl agent-loop-breach-prevention`. It encodes every binding decision, known issue, and process mechanic from the implementation session that produced commits `201b6729..28a8aa58`.

## Mission

Autonomous kiro-impl execution of the approved spec `.kiro/specs/agent-loop-breach-prevention/` (requirements/design/tasks all approved; phase `ready-for-implementation`). Branch: `feat/agent-loop-breach-prevention` (dedicated worktree, do NOT create another). Spec artifacts live beside this file; steering is `.kiro/steering/*` plus root/`.kiro/AGENTS.md`.

## Progress ledger

Completed (marked `[x]` in tasks.md): **1.1, 1.2, 1.3, 2.1, 2.2, 3.1, 3.2, 4.1, 4.2, 4.3, 5.1, 5.2, 5.3, 6.1**.
In progress: **6.2** — phase 1 (`stopgate` package) committed; phase 2 (runtime wiring of finish chokepoints) committed; REMAINING for 6.2 completion: composition wiring (runtimebundle -> Executor.LoopGuard injection from `EffectiveAgentLoopGuard()`), end-to-end holdback assertions at runtime level, resolving the known issue below.
Pending: 6.3, 7.1, 7.2, 8.1, 8.2, 8.3, 9.1, 9.2, 10.1, 10.2, 10.3.

Commits (oldest first): `201b6729` config+stopguard policy; `00d04374` progress tracker; `a2aed482` streamrecovery continuation signal; `fcfb5e41` stopguardverify adapter/projector/instruction; `4b42eb6a` verifier observer seam; `6b1b3030` continuationsafety; `665859f3` stopgate package; `28a8aa58` runtime holdback wiring.

## KNOWN ISSUE — fix FIRST in 6.3 (Req 9.1)

On the guard-ENABLED path, withholding `finishResponse` can cause the same backend `response_finished` to be replayed through recovery drains and re-recorded via `recordAttemptLogged` (implementer observed 2 settlement logs on authority+dispatch paths). Requirement 9.1 demands exactly-once attempt settlement. Before opening any continuation leg: deduplicate by attempt terminal CAS state so a replayed finish cannot re-settle. Guard-DISABLED path verified unchanged (full `internal/core/runtime` suite green). Also recorded in tasks.md Implementation Notes.

## Binding architecture decisions (do not relitigate)

1. **Package map**: `internal/core/stopguard` = PURE policy (cause/verdict/action vocab, NormalizeVerdict, Decide/DecideWithVerdict, ProgressTracker; imports only lipapi/stdlib; NO I/O ever — task 10.2 archtest enforces). `internal/core/stopgate` = request-level Gate composing policy+tracker+continuationsafety; runtime consults it via `Executor.LoopGuard *loopguardRuntime` (nil = disabled fast path). `internal/core/stopguardverify` = auxreq-backed Verifier adapter + evidence projector + six-rule instruction + strict JSON verdict parser (must stay OUTSIDE stopguard because of the no-aux-I/O ratchet). `internal/core/continuationsafety` = pure continuation-safety Evaluate + BuildRecoveryInstruction/BoundRecoveryText.
2. **Config shape**: nested block `agent_loop_guard:` with snake_case leaves (mirrors `stream_recovery:`), NOT flat root keys despite design table wording; tasks.md 1.3 "consistent with existing config package" governs. New top-level sections MUST be registered in `internal/core/configreload/inventory.go` (+ typed comparator in `policy.go`, DispositionReloadable chosen) or `TestUnclassifiedTopLevelField_StructuralGuardFails` fails.
3. **Verifier request constants**: Visibility `"private"`, `auxiliary.SessionModeDetached`, `DisablePlugins: []string{PluginID}` with `PluginID="agent_loop_guard"`; lineage fields on `stopguard.Evidence{ParentTraceID, ParentALegID, ParentBLegID, ParentBranchBinding}` copied verbatim into auxiliary.Request, ctx fallback via `lineage.TraceID/ALegID`. Never derive B-leg from ToolState.PendingToolCallID (rejected as semantically wrong).
4. **Budget semantics (Req 8.1)**: config `MaxSemanticContinuations=N` means N hidden legs. Implemented as: FIRST actionable CONTINUE seeds tracker baseline AND opens leg #1 (baseline consumes tracker slot 1), every later CONTINUE consumes one slot; therefore `NewProgressTracker(N+1, noProgressLimit)` in gate.New. Two fixtures were corrected to this model (budget fixture: cont x3 then latched terminal at cfg=3); race/no-progress fixtures validate as-is. Tracker pins exhaustion AT its cap inclusive (Nth record terminals).
5. **Continuation lineage**: new leg's `PreviousID = prior.ID` (NOT prior.PreviousID) — repo precedent `pkg/lipsdk/continuation/materialize.go:190-193` walks `cur = rec.PreviousID`; linking to prior.PreviousID would orphan the interrupted leg from trajectory materialization (violates Req 4.2). Three RED fixtures were corrected accordingly; implementation comment cites the precedent.
6. **Telemetry honesty**: no fabricated values — an implementer's `Latency=1ns if 0` floor was removed; tests assert `>= 0`.
7. **streamrecovery extension**: `Config.AllowPostOutputContinuation bool` + `DecisionContinuePostOutput`; mode-gated conversion of post-commit EOF/generic-error/idle branches; cancellation still SurfaceFailure; default path byte-for-byte unchanged; sole production consumer (`runtimebundle/build_executor.go`) uses keyed literals, flag defaults false.

## Runtime integration facts (for 6.2 completion / 6.3 / 7.x)

- Finish chokepoints wrapped via `finishResponseGuarded` (file `internal/core/runtime/agent_loop_guard_gate.go`): dispatch_gated (~recv_loop:94), dispatch_nongated (:146), recovery_drain (:315), gate_drain (:373). Out-of-scope sites left untouched: finalizeResponseFinishedAuthority error fallbacks, handleEOF (:203), early ctx cancel (:292), interleaved_stream abort handoff, terminalizeTurn fallback.
- `turnTerminal` owns request truth: `commitment`/`completion` atomics, `markCommitted/markFinished/finishResponse/endALeg(aLegEndMode)`; attempts settle via `attempt.TerminalizeAttempt` / `terminalizeWithEvidence`; `terminalizeGateReplacement` is precedent for pre-request-final claims.
- Interim contract until 6.3: gate Action==continue_leg => finish withheld, client still sees controlled finish with reason `guard_continuation_pending_6_3` (conservative allow-stop fallback). 6.3 replaces this with real hidden B-leg opening through normal admission (route/billing/authority) using `continuationsafety.Evaluate` + `BuildRecoveryInstruction`, keeping A-side stream open per design "Protocol-Safe A-Side Stitching".
- Post-output interruption (7.x): consume `DecisionContinuePostOutput` from streamrecovery (set `AllowPostOutputContinuation=true` when guard enabled during composition), classify cause via stopguard (`CauseTransportEOFPostCommit`/`CauseIdlePostCommit` + `SafeCanonicalContinuation` fact), never replay committed output/side effects (Transport Recovery Matrix in design).

## Process mechanics that worked (follow them)

- TDD cadence: capture CLI RED output BEFORE implementing; merged per-group RED->GREEN->single-commit cycles are REQUIRED because `make quality-checks` runs `go vet` which compiles test files — compile-level RED tests cannot be committed standalone without bypassing hooks (forbidden).
- Subagent channel: flaky at session start (empty results twice -> manual mode for group 1), reliable afterwards. ALWAYS independently verify implementer claims; caught: budgets.go history-line REPLACEMENT (must be append-only), fabricated latency floor, contradictory fixtures papered over with conditional logic, wrong field mappings. Parse only exact `- STATUS:` / `- VERDICT:` lines.
- Archtest gates hit repeatedly: `TestCorePackagesHaveDocGo` (every internal/core/* needs doc.go), `TestLineComplexityBudgets` (internal/core ratchet in `internal/archtest/budgets.go` — bump by appending a dated history comment line `measured N, bump to N+25`; current Max 87122), configreload structural inventory (see decision 2).
- Race: Windows TSAN fails to allocate (`make test-race` skips on Windows); targeted `go test -race` may fail with error code 87 here — not a code failure; Linux CI covers race. Mutex-guard shared state anyway (ProgressTracker/Gate pattern).
- Transient `go clean -cache`/build-cache errors occurred once; retry clears them.
- Human decision on record (tasks.md Implementation Notes + local git config `lip.allowLargeChange true`): the 100-modified-Go-files gate is authorized to be exceeded for this run; CI/PR still needs the `allow-large-change` label.
- Verification bar per task: focused `-count=1` runs, full `internal/core/runtime` suite for runtime touches, `go test -run TestLineComplexityBudgets ./internal/archtest/`, gofmt/go vet, TODO grep. Final task 10.3 runs whole-repo gates (`make quality-checks`, `make test`, `make qa`).

## Environment

Windows/pwsh; Go toolchain 1.26.x pinned in go.mod; testify v1.11.1 + goleak available; forward-slash git pathspecs. Workdir `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-agent-loop-breach-prevention`. Only expected dirty file: `.kiro/specs/agent-loop-breach-prevention/spec.json` (pre-existing approval timestamps - preserve, never commit blindly alongside feature changes unless intended).

## Resume order

1. 6.2 completion: fix known issue (attempt-settlement dedupe) -> composition wiring (runtimebundle builds `loopguardRuntime` from `EffectiveAgentLoopGuard()`: Enabled, Policy mapping trust/verify, caps, role/timeout into stopguardverify.AdapterConfig + Observer) -> runtime-level tests proving held candidate never reaches A-side before decision (Req 12.10) -> mark 6.2 `[x]`.
2. 6.3: real continuation leg opening (hidden instruction injection per `BuildRecoveryInstruction`, admission through existing executor retry/open machinery, budget/progress state from Gate Outcome; recursion suppression via PluginID DisablePlugins + `SuppressVerification` fact for verifier legs).
3. 7.x post-output interruption; 8.x explicit-completion capability + E2E protocol matrix (one legal A-side stream across hidden B-legs, non-streaming parity, unsupported-continuation clean fallback); 9.x telemetry through existing observability (bounded enums only); 10.1 regression fixtures (design "Testing Strategy" lists 16 integration scenarios verbatim), 10.2 archtest ratchets (stopguard purity, no retry/replacement classification, no hidden instruction as A-side content, exactly-once under race), 10.3 full gates.
4. Then `/kiro-validate-impl agent-loop-breach-prevention` as GO/NO-GO gate; apply kiro-verify-completion before claiming feature success.
