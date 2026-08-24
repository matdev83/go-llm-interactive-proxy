# Implementation Plan

## Task Summary

Implement Agent Loop Guard as a narrow request-terminal policy integrated with existing Go-LIP recovery/continuation/auxiliary/terminal owners. Follow repository TDD discipline: each behavior-changing task begins with failing tests, then the smallest implementation, then refactor/architecture ratchets. Do not introduce a second retry engine or weaken post-output commitment rules.

Parallel marker `(P)` is used only where the task owns distinct files/interfaces and does not require an unfinished upstream contract.

---

- [x] 1. Add guard configuration and pure policy domain with failing unit tests
  - [x] 1.1 (P) Add failing configuration tests for the opt-in Agent Loop Guard surface
    - Cover default disabled behavior, verifier role, 4s-equivalent timeout default, semantic continuation cap, no-progress limit, and explicit-completion policy. Prove that enabling the guard verifies every eligible clean normal stop unless a stronger canonical exclusion applies.
    - Reject invalid enum values and non-positive enabled bounds using current config validation conventions.
    - Prove existing `stream_recovery_*` defaults/settings are unchanged and no duplicate transport retry knobs are introduced.
    - Likely files: `internal/core/config/*agent_loop_guard*_test.go` plus focused existing config tests.
    - _Requirements: 1.1, 1.5, 3.1, 8.1, 8.2, 12.8_
    - _Depends on: none_
    - _Validation: `go test ./internal/core/config/...`_
  - [x] 1.2 (P) Create `internal/core/stopguard` policy tests and domain types
    - Table-test canonical causes, verifier verdict normalization, pure action decisions, explicit-completion policy, and conservative unknown/uncertain handling before implementation.
    - Require non-empty concrete `RemainingObjective` for actionable `CONTINUE`; normalize verifier error/malformed/unknown verdict to allowed stop.
    - Keep package free of backend/provider/auxiliary/runtime I/O dependencies.
    - _Requirements: 2.1–2.5, 5.1–5.7, 6.6–6.7, 7.1–7.6_
    - _Depends on: none_
    - _Validation: `go test ./internal/core/stopguard/...`_
  - [x] 1.3 Implement minimal Agent Loop Guard config and pure decision policy
    - Add flat snake_case config fields/accessors/defaults consistent with existing config package.
    - Implement cause/verdict/action vocabulary and decision invariants proven by 1.1/1.2.
    - Keep `UNCERTAIN -> ALLOW_STOP` fixed in v1 rather than configurable.
    - _Requirements: 1.1–1.5, 2.1–2.5, 5.2–5.7, 6.6–6.7_
    - _Depends on: 1.1, 1.2_
    - _Validation: `go test ./internal/core/config/... ./internal/core/stopguard/...`_

- [x] 2. Add bounded semantic progress tracking
  - [x] 2.1 (P) Write failing no-progress/budget tests in `internal/core/stopguard`
    - Cover materially equivalent final answer, same tool+normalized args+same error/result cycle, same verdict/objective without new canonical progress, new-progress behavior, immutable max-attempt budget, and cancellation/exhaustion terminal action.
    - Ensure volatile IDs/timestamps alone cannot defeat repetition detection.
    - _Requirements: 8.1, 8.3, 8.4, 8.6, 12.9_
    - _Depends on: 1.2 domain vocabulary_
    - _Validation: `go test ./internal/core/stopguard/...`_
  - [x] 2.2 Implement bounded progress fingerprint/tracker
    - Build deterministic bounded digests from canonical output/tool/result/continuation/verdict facts.
    - Enforce no-progress threshold and total semantic continuation cap separately.
    - Do not retain raw prompt/tool payloads solely for guard fingerprinting when stable hashes/facts suffice.
    - _Requirements: 8.1, 8.3, 8.4, 8.6, 11.4_
    - _Depends on: 2.1_
    - _Validation: `go test ./internal/core/stopguard/...`_

- [x] 3. Extend stream-recovery decisions for guard-owned post-output continuation
  - [x] 3.1 Add failing `streamrecovery` policy tests for continuation-eligible post-output interruptions
    - Preserve current behavior when the Agent Loop Guard path is not requested: post-output EOF/idle/error still follows existing configured `finish` behavior.
    - Add a typed policy outcome or higher-level mode that reports post-output interruption as continuation-eligible without producing a synthetic final response.
    - Prove pre-output recovery, cancellation, already-finished responses, warning behavior, and output-commit tracking remain unchanged.
    - Prove no post-output decision is returned as retry/replacement.
    - _Requirements: 1.1–1.5, 2.2, 3.1–3.5, 4.1, 9.5, 12.5–12.7_
    - _Depends on: 1.3_
    - _Validation: `go test ./internal/core/streamrecovery/...`_
  - [x] 3.2 Implement the narrow post-output continuation signal
    - Extend `streamrecovery` only enough for runtime to distinguish “finish post-output” from “higher-level guard may continue from committed trajectory.”
    - Do not add semantic verifier, continuation store, provider routing, or terminal mutation to `streamrecovery`.
    - Keep current default/config compatibility when guard disabled.
    - _Requirements: 3.1–3.5, 4.1–4.2, 9.5_
    - _Depends on: 3.1_
    - _Validation: `go test ./internal/core/streamrecovery/...`_

- [x] 4. Build the auxiliary semantic completion verifier
  - [x] 4.1 (P) Write verifier-adapter contract tests with a fake auxiliary client
    - Assert internal/detached request, parent trace/A-leg/B-leg/branch lineage, dedicated role, and Agent Loop Guard recursion suppression.
    - Assert bounded deadline and strict structured parsing for `ALLOW_STOP`, `CONTINUE`, `NEEDS_USER`, `BLOCKED`, `UNCERTAIN`.
    - Timeout, transport error, malformed output, unknown verdict, and `CONTINUE` without concrete remaining objective must normalize conservatively.
    - Ensure no tools are needed/exposed for the verifier path.
    - _Requirements: 5.1–5.7, 7.1, 8.2, 8.5, 11.2–11.5, 12.8_
    - _Depends on: 1.3_
    - _Validation: focused verifier adapter tests_
  - [x] 4.2 Define the bounded evidence projector and conservative verifier prompt
    - Project current/recent user objective, candidate assistant output, relevant canonical tool/action state, explicit completion fact, continuation lineage, and attempt count without creating a second transcript store.
    - Encode negative examples/rules for completed answers, optional “I can also…”, user-owned “Next steps”, direct questions, and quoted future-action language.
    - Encode positive rule for first-person immediate in-scope commitments not evidenced as executed.
    - _Requirements: 5.1–5.7, 7.1–7.6, 12.1–12.4_
    - _Depends on: 4.1_
    - _Validation: verifier prompt/evidence unit tests including realistic fixed fixtures_
  - [x] 4.3 Implement auxiliary verifier execution and structured result parsing
    - Use existing `auxreq` client/runtime path and current internal scope/lineage/accounting behavior.
    - Propagate only bounded verifier reason/remaining objective to recovery; do not surface verifier chain-of-thought.
    - Record verifier usage/latency through existing observability/accounting seams.
    - _Requirements: 5.1–5.7, 7.1, 8.2, 8.5, 11.2–11.5_
    - _Depends on: 4.2_
    - _Validation: focused verifier tests plus auxiliary package tests_

- [x] 5. Build safe continuation materialization and conditional internal recovery instruction
  - [x] 5.1 (P) Add failing continuation-safety tests
    - Preserve already committed assistant output without duplication.
    - Preserve completed tool call + matching result exactly once and prohibit re-execution solely due to transport interruption.
    - Reject incomplete tool arguments and unsupported opaque/provider state unless a normalized native-resume capability proves safe.
    - Preserve continuation lineage, chain-depth/materialization limits, and accurate prior attempt status.
    - _Requirements: 4.1–4.6, 9.1–9.6, 10.2–10.4, 12.6–12.7_
    - _Depends on: 1.3_
    - _Validation: `go test ./internal/core/continuation/... ./pkg/lipsdk/continuation/...` plus focused runtime helper tests_
  - [x] 5.2 Add failing tests for the conditional hidden recovery instruction
    - Assert the instruction says it is automated, not a new user request/approval/scope expansion.
    - Assert it tells a completed worker to end, an unfinished worker to resume only existing work, and a user-dependent worker not to infer an answer/permission.
    - Assert it carries bounded recovery reason/objective/attempt facts and is absent from A-side user-authored transcript/output.
    - _Requirements: 6.1–6.7, 7.2–7.4, 12.2–12.4_
    - _Depends on: 4.2 verifier contract_
    - _Validation: focused prompt/control-content tests_
  - [x] 5.3 Implement safe canonical continuation construction
    - Reuse continuation records/materialization and existing lineage rather than reconstructing from frontend bytes.
    - Build a new B-side continuation leg from the last safe canonical point; do not classify it as retry/replacement.
    - Use strongest legal internal/control role available; if a backend requires user-role content, retain internal/non-forwarded provenance and non-authorizing wording.
    - Keep compatibility with future non-forwardable steering without depending on it now.
    - _Requirements: 4.1–4.6, 6.1–6.7, 9.2, 9.5–9.6, 10.2–10.4_
    - _Depends on: 5.1, 5.2_
    - _Validation: continuation/runtime helper tests_

- [x] 6. Integrate the provisional terminal gate into logical request orchestration
  - [x] 6.1 Add failing runtime tests for terminal holdback and exactly-once ownership
    - Candidate clean terminal must not reach A-side before guard decision.
    - Swallowed B-attempt settles exactly once while A-side request stays open.
    - Final `ALLOW_STOP`, `NEEDS_USER`, `BLOCKED`, `UNCERTAIN`, cancellation, and exhaustion terminalize A-side exactly once.
    - Race cancel/close/normal finish/verifier completion and assert existing owner is authoritative.
    - _Requirements: 1.2–1.4, 5.1–5.6, 8.4–8.5, 9.1–9.5, 12.8–12.10_
    - _Depends on: 1.3, 3.2, 4.3, 5.3_
    - _Validation: focused runtime tests and targeted `go test -race`_
  - [x] 6.2 Implement request-level guard orchestration before logical normal-finish claim
    - On candidate terminal, normalize cause/evidence and select: existing pre-output recovery, safe continuation, semantic verify, or final terminal/failure.
    - Settle the current B-side attempt independently before/while opening a continuation according to existing lifecycle rules; never undo a logical terminal claim.
    - Make cancellation/close authoritative and cancel in-flight verifier/continuation work promptly.
    - _Requirements: 1.2–1.5, 2.1–2.5, 3.1–3.5, 5.1–5.6, 9.1–9.5_
    - _Depends on: 6.1_
    - _Validation: focused runtime tests_
  - [x] 6.3 Wire semantic continuation with immutable per-request budget/progress state
    - On actionable `CONTINUE`, invoke safe continuation materialization, add conditional hidden instruction, open new B-leg through normal runtime admission, and keep logical A response open.
    - Preserve max semantic continuation count across new progress; only reset justified no-progress state.
    - Suppress guard recursion for verifier operation and avoid accidental nested autonomous recovery.
    - _Requirements: 5.3, 6.1–6.7, 8.1–8.6, 9.2, 9.6_
    - _Depends on: 2.2, 6.2_
    - _Validation: focused runtime/stopguard integration tests_

- [x] 7. Implement post-output transport continuation without replay or side-effect duplication
  - [x] 7.1 Add failing runtime integration tests for post-output EOF/idle cases
    - Visible text then EOF: never reopen as a retry of the original attempt; no duplicate text.
    - Completed tool call + matching result then interruption: continue after retained result; assert tool side effect executes once.
    - Incomplete tool args or unsafe opaque state: no execution/replay; one controlled final outcome.
    - Client cancellation prevents continuation.
    - _Requirements: 4.1–4.6, 9.5–9.6, 10.1–10.4, 12.6–12.7_
    - _Depends on: 3.2, 5.3, 6.3_
    - _Validation: focused runtime + tool correlation tests_
  - [x] 7.2 Wire post-output interruption into safe continuation path
    - Consume the new `streamrecovery` continuation-eligible signal.
    - Preserve committed canonical trajectory and settle interrupted B-leg with truthful typed outcome.
    - Open continuation B-leg only if canonical continuation builder proves state safe; otherwise finalize without replay.
    - Keep existing no-silent-failover-after-output-commit ratchets passing.
    - _Requirements: 3.1–3.5, 4.1–4.6, 9.1–9.6_
    - _Depends on: 7.1_
    - _Validation: focused runtime/streamrecovery tests_

- [x] 8. Preserve frontend/protocol legality and explicit completion semantics
  - [x] 8.1 (P) Add normalized explicit-completion evidence tests for known frontend capability paths
    - Consume a canonical capability/fact rather than hard-code provider/frontend tool names into `stopguard`.
    - `trust` skips semantic continuation for clean explicit completion; `verify` passes strong evidence to verifier.
    - Malformed/absent explicit signal falls back to normal semantic policy.
    - _Requirements: 5.7, 7.1, 10.3_
    - _Depends on: 1.3_
    - _Validation: relevant frontend/canonical capability tests_
  - [x] 8.2 Add end-to-end protocol tests for one logical A-side stream spanning hidden B-legs
    - Assert no intermediate terminal leaks, canonical item ordering/tool correlation remains legal, and exactly one final terminal renders for supported streaming frontends.
    - Cover non-streaming collection over the same canonical stream.
    - Cover unsupported continuation capability with a clean final fallback rather than raw-frame concatenation.
    - _Requirements: 1.3–1.4, 10.1–10.5, 12.10_
    - _Depends on: 6.3, 7.2_
    - _Validation: relevant frontend/backend conformance and runtime E2E tests_
  - [x] 8.3 Implement any minimal canonical identity/capability plumbing exposed by 8.1/8.2
    - Keep provider/frontend-specific translation at adapters; core consumes normalized facts only.
    - Do not add raw SSE/provider-frame stitching.
    - Prefer existing canonical response identity/item-index mapping; add narrow capability only where current contract cannot express continuation legality.
    - _Requirements: 10.1–10.5_
    - _Depends on: 8.1, 8.2 failing tests_
    - _Validation: focused protocol/conformance tests_

- [x] 9. Add bounded observability and accounting assertions
  - [x] 9.1 (P) Add telemetry tests for guard cause/verdict/action/continuation/no-progress paths
    - Use bounded enums/codes only; prohibit prompt text, assistant text, tool arguments, verifier reason/objective, or recovery prompt as metric labels.
    - Preserve existing trace/A-leg/B-leg lineage and auxiliary verifier usage accounting.
    - _Requirements: 9.6, 11.1–11.5_
    - _Depends on: 1.3 domain enums_
    - _Validation: focused metrics/tracing/accounting tests_
  - [x] 9.2 Wire guard telemetry through existing observability paths
    - Record candidate cause, verifier outcome/latency, action/outcome, no-progress breaker, replay suppression, and final result.
    - Ensure hidden verifier and continuation B-legs remain operator-visible/internal rather than being billed/recorded as fabricated A-side user turns.
    - _Requirements: 9.6, 11.1–11.5_
    - _Depends on: 4.3, 6.3, 7.2, 9.1_
    - _Validation: focused observability + billing/B2BUA tests_

- [x] 10. Close the full regression, race, and architecture matrix
  - [x] 10.1 Add realistic semantic-stop regression fixtures
    - Positive unfinished cases: immediate promised action, cut-off actionable response, supported reasoning/thinking-only evidence where canonical.
    - Critical negative cases: complete answer, “Done; tests pass,” user-directed question, user-owned “Next steps,” optional improvements, “I can also…”, quoted “I’ll continue,” refusal/filter.
    - Exercise verifier error/timeout and semantic max-attempt/no-progress exhaustion.
    - _Requirements: 5.1–5.7, 6.1–6.7, 7.1–7.6, 8.1–8.6, 12.1–12.9_
    - _Depends on: 4.3, 6.3_
    - _Validation: focused stopguard/verifier/runtime regression suite_
  - [x] 10.2 Add architecture ratchets for ownership and dependency boundaries
    - Assert `internal/core/stopguard` has no provider adapter/SDK dependencies and no auxiliary/backend I/O.
    - Assert no post-output continuation path uses retry/replacement semantics.
    - Assert hidden recovery control content cannot be rendered/persisted as A-side user-authored content.
    - Assert every swallowed B-leg and final A-leg terminalize once under race te- [x] 10.3 Run whole-repository quality gates and repair only scope-related regressions
    - Execute `go test ./...`, relevant targeted race suite, `make quality-checks`, `make test`, and `make qa` according to current project steering/CI capabilities.
    - Confirm disabled-mode compatibility, no duplicate transport knobs, no provisional A terminal leakage, and no provider-specific guard policy in core.
    - _Requirements: 1.1–1.5, 9.1–9.6, 10.1–10.5, 12.10_
    - _Depends on: 10.1, 10.2, 9.2, 8.3_
    - _Validation: all listed repository quality gates_

- [ ] 11. Remediate canonical PR435 conversation-view steering integration
  - [x] 11.1 (P) Add failing unit & contract tests for steering writer registration, anchor resolution, and snapshot isolation
    - Test `steering.Writer` registration on actionable `CONTINUE` using fixed `OverlayID("alg-rec")` within authoritative A-leg scope, message role `RoleDeveloper`, placement `AfterIngressTail`, anchor missing policy `FailClosed`, and reason `loop_guard_recovery`.
    - Test that `TrajectoryResolver` returns the accepted user ingress request call (`identityBoundTurn.ingressCall` or equivalent preserved ingress trajectory) plus committed snapshot, allowing `ResolveAfterIngressTailAnchor` to resolve `AfterIngressTail` to a fixed `MessageAnchor` on the terminal user message; assert that passing post-B1 calls ending in assistant output fails with `ErrTerminalNotUser`.
    - Test that hidden continuation freezes a new turn snapshot (Snapshot N+1) and all candidate attempts of that turn share it without mutating already frozen snapshots.
    - Test single-active-request A-leg runtime authority serialization invariant preventing concurrent active ALG overlays on the same A-leg.
    - Test multi-backend persistence across Memory, SQLite, and PostgreSQL store implementations.
    - _Requirements: 6.8–6.10, 6.15, 12.11, 12.12, 12.15_
    - _Depends on: 10.3_
    - _Validation: `go test ./internal/core/runtime/... ./internal/core/conversationview/...`_
  - [x] 11.2 Implement steering writer integration in continuation orchestration and eliminate duplicate authority
    - Construct `steering.Writer` explicitly with authoritative A-leg ID and trajectory resolver (`sdkadapter.NewWriter`).
    - Implement `TrajectoryResolver` returning the accepted user ingress request call (`identityBoundTurn.ingressCall` / preserved ingress trajectory) plus current committed snapshot.
    - On actionable `CONTINUE`, register/update overlay via `Writer.Put` with fixed `OverlayID("alg-rec")` and freeze Snapshot N+1 for hidden model turn B2 before opening attempt.
    - Remove direct append to `Call.Messages` and `Call.Items` in `internal/core/runtime/agent_loop_guard_continuation.go`.
    - Eliminate `turnTerminal.guardHidden` and establish conversation-view steering as the single source of truth for hidden control content.
    - _Requirements: 6.8–6.10, 6.17, 12.11, 12.16_
    - _Depends on: 11.1_
    - _Validation: `go test ./internal/core/runtime/...`_
  - [x] 11.3 (P) Add failing tests and implementation for exact-once reassertion, overlay lifecycle, and stale cleanup
    - Test and wire `conversationview.Reassert` using `OverlayProvenance` and `FilteredBaseline` before backend `Open` to prevent duplicate or dropped steering after attempt shaping.
    - Test and implement explicit deactivation via `steering.Writer.Deactivate(ctx, "alg-rec")` on final A terminal publication, cancellation, budget exhaustion, or leg open failure.
    - Test and implement deterministic stale-overlay cleanup on external turn ingress via `Deactivate(ctx, "alg-rec")` before snapshot freeze, treating `ErrOverlayNotFound` or inactive as no-op success and failing closed if cleanup cannot commit.
    - _Requirements: 6.11, 6.13, 6.14, 12.12–12.14_
    - _Depends on: 11.2_
    - _Validation: `go test ./internal/core/runtime/... ./internal/core/conversationview/...`_
  - [ ] 11.4 Add failing tests and implementation for candidate capability rejection and transcript isolation
    - Test that backend candidates unable to represent required steering role or placement are rejected via standard candidate adaptation without silent dropping or relocation.
    - Test that registered steering overlays never appear in A-side client streams or frontend `ContinuationRecord` transcripts.
    - _Requirements: 6.12, 6.16, 10.1–10.4, 12.10, 12.12_
    - _Depends on: 11.3_
    - _Validation: `go test ./internal/core/runtime/... ./internal/plugins/frontends/...`_
  - [ ] 11.5 Converge full regression, race, and architecture ratchets
    - Architecture ratchet: assert zero direct append to `Call.Messages`/`Items` in continuation logic, zero reliance on `turnTerminal.guardHidden`, and single authority for hidden control content.
    - Run full regression suite, multi-store persistence tests (Memory, SQLite, PostgreSQL), deterministic race tests, and repository quality gates (`make quality-checks`, `make test`, `make qa`).
    - _Requirements: 1.1–1.5, 6.8–6.17, 9.1–9.6, 10.1–10.5, 12.1–12.16_
    - _Depends on: 11.1, 11.2, 11.3, 11.4_
    - _Validation: all listed quality gates and architecture ratchets_

## Dependency Graph

```text
1.1 ─┐
     ├─> 1.3 ──> 3.1 -> 3.2 ───────────────┐
1.2 ─┘      │                              │
            ├─> 2.1 -> 2.2                 │
            ├─> 4.1 -> 4.2 -> 4.3          │
            ├─> 5.1 ─┐                     │
            │         ├-> 5.3 ─────────────┤
            │   5.2 ─┘                     │
            ├─> 8.1                        │
            └─> 9.1                        │
                                             v
                          6.1 -> 6.2 -> 6.3 -> 7.1 -> 7.2
                                      │          │       │
                                      │          │       ├-> 9.2
                                      │          │       └-> 8.2 -> 8.3
                                      │          └───────────────┘
                                      └-> 10.1

10.2 depends on 6.3, 7.2, 8.3
10.3 depends on 10.1, 10.2, 9.2, 8.3

10.3 ──> 11.1 ──> 11.2 ──> 11.3 ──> 11.4 ──> 11.5
```

## Parallel Execution Waves

- **Wave 1:** 1.1 config tests and 1.2 stopguard policy tests can run in parallel.
- **Wave 2:** after domain types stabilize, 2.1 progress tests, 4.1 verifier-adapter tests, 5.1 continuation-safety tests, 8.1 explicit-completion tests, and 9.1 telemetry tests can be developed in parallel because they own distinct packages/seams.
- **Wave 3:** 3.x transport policy, 4.x verifier, 5.x continuation builder, and 2.x progress tracker can proceed mostly in parallel after their local contracts exist.
- **Wave 4:** runtime integration 6.x is the convergence point; avoid parallel edits to the same runtime orchestration files.
- **Wave 5:** post-output recovery 7.x and protocol E2E 8.2/8.3 can split after 6.3, with coordination around any shared runtime/canonical files.
- **Wave 6:** observability wiring and final regression/architecture gates converge after functional paths stabilize.
- **Wave 7 (Remediation):** 11.1 (steering writer/snapshot tests) -> 11.2 (runtime writer wiring & direct append removal) -> 11.3 (reassertion, deactivation, stale cleanup) -> 11.4 (capability rejection & transcript isolation) -> 11.5 (full regression, race, and architecture convergence).

## Requirement Coverage Matrix

| Requirement | Tasks |
|---|---|
| 1 | 1.1, 1.3, 3.1, 6.1–6.2, 8.2, 10.3, 11.5 |
| 2 | 1.2–1.3, 6.2, 10.2, 11.5 |
| 3 | 1.1, 3.1–3.2, 6.2, 7.2, 11.5 |
| 4 | 3.1–3.2, 5.1, 5.3, 7.1–7.2, 10.2, 11.5 |
| 5 | 1.2–1.3, 4.1–4.3, 6.1–6.3, 8.1, 10.1, 11.5 |
| 6 | 1.2, 5.2–5.3, 6.3, 10.1–10.2, 11.1–11.5 |
| 7 | 1.2, 4.2, 5.2, 10.1, 11.5 |
| 8 | 2.1–2.2, 4.1–4.3, 6.1, 6.3, 10.1, 11.5 |
| 9 | 5.1, 5.3, 6.1–6.3, 7.1–7.2, 9.1–9.2, 10.2–10.3, 11.1–11.5 |
| 10 | 5.1, 5.3, 7.1, 8.1–8.3, 10.2–10.3, 11.4, 11.5 |
| 11 | 2.2, 4.1–4.3, 9.1–9.2, 11.5 |
| 12 | 2.1, 3.1, 4.2, 5.1–5.2, 6.1, 7.1, 8.2, 10.1–10.3, 11.1–11.5 |

## Task-Plan Review Verdict

**GO for Task 11 Implementation (Remediation Pending).** Every requirement has implementation and validation coverage. Group 11 specifies the remediation of PR #435 canonical conversation-view steering integration, replacing ad-hoc direct `Call` append and `guardHidden` with `pkg/lipsdk/steering.Writer` and `internal/core/conversationview`. Point 2 review findings (fixed `steering.OverlayID("alg-rec")` within A-leg scope, TrajectoryResolver user ingress call requirement, and deterministic stale overlay cleanup without prefix queries) have been resolved in the specification. Human approval for the integration architecture has been given, authorizing immediate execution of Task 11.

## Implementation Notes

- Group 1 (1.1-1.3) was executed as one RED->GREEN cycle with a single commit: make quality-checks runs go vet, which type-checks test files, so compile-level RED tests cannot be committed as their own revision without bypassing hooks. RED evidence (undefined stopguard/config symbols) was captured via CLI before implementation.
- ALG YAML uses the repo's nested-block convention ( gent_loop_guard: with snake_case leaves, mirroring stream_recovery:), not flat document-root keys; tasks.md 1.3's "consistent with existing config package" governs.
- New internal/core/stopguard requires doc.go (archtest TestCorePackagesHaveDocGo) and an internal/core line-budget ratchet bump in internal/archtest/budgets.go with justification comment; configreload inventory + typed comparator registration is mandatory for any new top-level config section.
- kiro-impl ran in manual mode: the sub-agent dispatch channel returned empty results twice, so implementation/review execute in main context per skill fallback.
- HUMAN DECISION (2026-08-24, repo owner): the 100-modified-Go-files source-change gate is authorized to be exceeded for this feature's implementation run given its broad cross-cutting scope. Local override enabled via git config lip.allowLargeChange true; CI/PR still requires the allow-large-change label per repo policy.
- 6.2-phase2 KNOWN ISSUE (fix first in 6.3): on the guarded path, withholding finishResponse can cause the same backend response_finished to be re-recorded via recordAttemptLogged on replay (implementer observed 2 settlement logs on authority+dispatch paths; >=1 accepted provisionally). Requirement 9.1 demands exactly-once attempt settlement - before opening continuation legs, deduplicate by attempt terminal CAS state. Guard-disabled path verified byte-for-byte unchanged (full runtime suite green).
- 6.2 completion resolves the replay issue through the existing per-attempt terminal owner, records held attempts truthfully as swallowed, injects the production guard through ExecutorConfig, and emits only the controlled interim fallback; full runtime/runtimebundle suites and scope-related architecture gates passed. Full archtest still has the pre-existing branch-wide shrinkage shortfall tracked for final convergence.
- 6.3 uses a per-logical-request LoopGuard factory, one immutable budget/progress gate across hidden legs, honest canonical prior evidence, Developer-role bounded recovery control, and a non-retry semantic continuation admission mode; production test stubs were removed and the focused/full runtime plus scope architecture gates passed.
- 7.1 adds compile-safe RED runtime coverage for post-output EOF/idle, completed tool/result retention, unsafe partial/opaque state, cancellation, and disabled compatibility; task 7.2 must harden idle-path, no-retry, and retained-tool assertions while turning these behavioral failures GREEN.
- 7.2 consumes guard-enabled post-output recovery signals for EOF, idle, and generic errors; safe state opens a non-retry continuation leg, unsafe state finalizes once without replay, and hardened tests prove retained tool pairs, cancellation authority, disabled compatibility, and one legal A-side terminal.
- 8.1 defines a conservative canonical explicit-completion fact from correlated completed `attempt_completion`/`attempt_complete` call-result pairs, proves the real OpenResponses decode path and stopgate trust/verify behavior, and leaves a compile-safe runtime RED seam for task 8.3.
- HUMAN DECISION (2026-08-24, repo owner): execute 8.2 and 8.3 as one pragmatic TDD cycle because unsupported-continuation RED needs the normalized capability type introduced by 8.3; retain separate completion checkboxes and verify both task boundaries before one combined commit.
- 8.2/8.3 use canonical Invocation operations to gate continuation legality conservatively, add Anthropic/Gemini operation identities, green the explicit-completion seam using real correlated tool results, and prove production B1-to-B2 stitching through live frontend wire handlers with one final terminal.
- 9.1 adds compile-safe behavioral RED contracts for bounded candidate/verdict/action/breaker/replay telemetry and GREEN assertions for verifier usage, private lineage, attributable B-leg accounting, privacy, and A-side turn integrity.
- 9.2 emits bounded candidate, verdict, action, breaker, and replay-suppression telemetry through runtime structured logs, carries honest verifier usage/latency and lineage, and keeps stopgate pure with no conversational payloads in telemetry attributes.
- 10.1 adds 18 deterministic semantic-stop fixtures and all 16 design integration scenarios across positive unfinished work, critical false-positive boundaries, verifier uncertainty, transport safety, cancellation, no-progress, budgets, explicit completion, and unsupported continuation.
- 10.2 ratchets stopguard purity and zero-I/O imports, semantic continuation non-retry call paths, hidden Developer-control isolation, and exactly-once B/A terminal ownership under deterministic repeated and race-enabled tests.
- 10.3 converges the architecture shrinkage gate with a locked Agent Loop Guard composition overlay plus behavior-preserving scope deduplication, restores cross-frontend OpenResponses compatibility, and passes `go test ./...`, `make quality-checks`, `make test`, and `make qa`; Windows race remains environment-limited while deterministic race tests pass.
- HUMAN DECISION (2026-08-25, repo owner): the canonical conversation-view steering integration architecture (merged PR #435 `b763a772`) is approved as the single authority for hidden control content. Direct append to `Call.Messages`/`Items` and `turnTerminal.guardHidden` must be migrated to `pkg/lipsdk/steering.Writer` and `internal/core/conversationview`. Remediation is tracked in Task 11 and authorized for immediate TDD execution.
- REVIEW RESOLUTION (2026-08-25, Point 2 findings): resolved all three implementation-readiness findings in spec: (1) bound fixed `steering.OverlayID("alg-rec")` within authoritative A-leg scope, relying on single-active-request A-leg authority for serialization; (2) specified that `TrajectoryResolver` must return the accepted user ingress request call (`identityBoundTurn.ingressCall` / preserved ingress trajectory) plus committed snapshot to satisfy `ResolveAfterIngressTailAnchor`'s terminal user message requirement; (3) specified deterministic stale-overlay cleanup on external turn ingress via `Deactivate(ctx, "alg-rec")` with `ErrOverlayNotFound`/inactive as no-op success and persistence error failing closed, avoiding unapproved pattern query APIs. Specification validation verdict updated to GO for Task 11 implementation.
