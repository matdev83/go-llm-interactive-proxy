# Implementation Plan

## Execution Rules

- This is a **brownfield, test-first** implementation plan for issue #431.
- Baseline is merged PR #428 / `main` merge `7a6c7532`; preserve `readyAttempt` unpublished ownership and `attemptSession.TerminalizeAttempt` as the sole production attempt terminal owner.
- Do not combine phases into a repository-wide rewrite. Keep each phase green before proceeding.
- Tasks marked **(P)** may run in parallel once their declared dependencies are satisfied.
- Do not weaken existing architecture ratchets, billing invariants, no-retry-after-output behavior, or connector ABI compatibility to make tests pass.
- Every production change starts with or is preceded by a discriminating RED test from Phase 1.

## Phase 1 — Characterize the Merged Baseline and Lock the Gaps

- [ ] 1.1 Revalidate the merged #428 ownership baseline at implementation start
  - Confirm `main` still has one production attempt terminal entry, ready-owned unpublished cancellation, ready-before-publication, raw-stream fencing, positive B2BUA attempt sequence, and existing cancellation billing behavior.
  - Record the exact baseline SHA and update this spec only if those contracts materially changed.
  - _Requirements: 1.1–1.6_
  - _Boundary: characterization only; no production behavior change_
  - _Depends: none_
  - _Validation: focused runtime/archtest ownership suites; document the baseline SHA in the implementation PR_

- [ ] 1.2 (P) Add RED tests for cancel-vs-provider-launch linearization
  - Reproduce cancel-before-delayed-parallel-Open, cancel-vs-serial-Open, cancel-during-blocked-Open, and a late Open returning after cancellation.
  - Prove current code can cross `Backend.Open` after explicit A-leg cancellation or cannot cancel the in-flight Open through A-leg authority.
  - Include initial, failover/replacement, delayed parallel/hedged, and interleaved-continuation launch shapes where applicable.
  - _Requirements: 2.1–2.7, 10.1–10.2_
  - _Boundary: runtime/core tests only_
  - _Depends: 1.1_
  - _Validation: tests fail for the intended missing launch barrier and not for unrelated setup reasons_

- [ ] 1.3 (P) Add RED tests for sibling fan-out and physical CancelResult fidelity
  - Build a multi-B-leg test where one child's cancellation blocks and prove siblings are currently delayed.
  - Add physical streams returning provider, transport and close-only modes/errors and prove ready/session lifecycle handles currently fabricate provider mode.
  - Pin exact physical Cancel/Close invocation counts under cancellation races.
  - _Requirements: 3.1–3.7, 4.1–4.8, 10.3–10.4_
  - _Boundary: core lifecycle/runtime tests only_
  - _Depends: 1.1_
  - _Validation: RED failures distinguish fan-out latency from result-fidelity defects_

- [ ] 1.4 (P) Add RED backend-plugin tests proving CANCEL is not an active-Execute handshake
  - Demonstrate that post-START CANCEL is not consumed by current `ForwardExecute`, cancel deadline is not enforced end-to-end, `CancelOutcome` is discarded, and host cancellation can return after local enqueue without upstream cancellation.
  - Characterize current legacy connector behavior so compatibility can be proven later.
  - _Requirements: 5.1–5.9, 10.5–10.6_
  - _Boundary: SDK/host/adapter test code only; no production wire change yet_
  - _Depends: 1.1_
  - _Validation: RED test uses one active Execute/upstream stream and cannot pass merely because a frame was transmitted_

- [ ] 1.5 (P) Add RED tests for terminal provider-sideband billing evidence
  - Cover canceled and parallel-loser attempts where provider-only sideband evidence exists but canonical usage does not.
  - Include `FinalizeBilling` unsupported, failure, success, duplicate dedupe keys, and no-usage cases.
  - Prove finalizer failure/absence can currently lose otherwise buffered provider evidence on terminal paths.
  - _Requirements: 6.1–6.7, 7.1–7.9, 10.7_
  - _Boundary: runtime/backendplugin billing-evidence tests only_
  - _Depends: 1.1_
  - _Validation: RED cases preserve exact B-leg ID/AttemptSeq and fail only on missing evidence behavior_

## Phase 2 — Add the Atomic A-Leg Launch/Cancellation Boundary

- [ ] 2.1 Implement the A-leg single-use B-leg launch permit
  - Add private launch state to `leglifecycle.ALeg` without storing request contexts.
  - `BeginBLegLaunch` and `Cancel` must linearize under the same mutex; a granted launch stores only a derived Open cancel function.
  - Implement race-safe single-use Commit/Abort so launch entries become ready/session lifecycle handles atomically or observe sticky cancellation.
  - Preserve late registration behavior and idempotent A-leg cancellation.
  - _Requirements: 2.1–2.7, 3.1, 3.7_
  - _Boundary: `internal/core/leglifecycle`; no provider SDK, billing, routing or frontend imports_
  - _Depends: 1.2, 1.3_
  - _Validation: permit state-machine tests cover cancel-before-begin, begin-before-cancel, cancel-vs-commit, abort and repeated calls_

- [ ] 2.2 Integrate the permit into every provider activation path and transfer successful Open ownership early
  - Acquire the permit immediately before the actual `Backend.Open` call and use the permit-derived context for Open.
  - After successful Open, construct the attempt/session and transfer it immediately into an **unprepared `readyAttempt`**; commit the permit to `ready.lifecycleHandle()` before later fallible post-Open persistence/readiness work.
  - On commit cancellation/failure, terminalize/dispose through the ready owner; never raw-close the returned stream.
  - Refactor the serial path so interleaved persistence/memo work cannot leave a post-Open/pre-ready ownership gap; adapt parallel arms to the same transition without changing reducer authority.
  - _Requirements: 1.1–1.5, 2.1–2.7, 8.1, 8.6–8.7_
  - _Boundary: core runtime attempt-open composition; preserve `readyAttempt`/`TerminalizeAttempt` ownership_
  - _Depends: 2.1_
  - _Validation: Phase 1 launch RED tests turn green; no raw stream cleanup or new terminal owner appears_

- [ ] 2.3 Replace serial active-child teardown with bounded concurrent A-leg fan-out
  - Under the A-leg lock, mark cancellation sticky and snapshot launch cancel functions plus active lifecycle handles.
  - Unlock before any I/O; cancel in-flight launch contexts promptly and cancel active children concurrently with per-child bounded cleanup contexts.
  - Aggregate child errors after bounded completion while ensuring one poisoned child cannot delay sibling signaling.
  - Explicitly own/join any introduced goroutines and update the goroutine allowlist only for intentional load-bearing concurrency.
  - _Requirements: 3.1–3.7, 4.8, 9.1–9.2_
  - _Boundary: core `leglifecycle`; calls attempt lifecycle handles only_
  - _Depends: 2.1_
  - _Validation: sibling timing test proves concurrent signaling; race/goleak coverage has no orphan child cleanup_

- [ ] 2.4 Propagate the physical stream `CancelResult` through the #428 terminal owner
  - Extend the attempt terminal result/effects narrowly so the winning `TerminalizeAttempt` path captures the exact result returned by physical `ManagedEventStream.Cancel`.
  - Make both `readyAttempt` and `attemptSession` lifecycle handles return that result instead of unconditional provider mode.
  - Preserve terminal cleanup even when physical cancel returns an error, and define/test idempotent behavior for callers that lose the terminal CAS.
  - _Requirements: 1.1–1.3, 4.1–4.8_
  - _Boundary: core runtime attempt terminal/lifecycle seam; no second physical teardown path_
  - _Depends: 1.3, 2.2_
  - _Validation: provider/transport/close-only/error fixtures round-trip truthfully and physical Cancel/Close remain at-most-once_

- [ ] 2.5 Certify the core cancellation race matrix before crossing the process boundary
  - Run deterministic schedules for explicit cancel vs initial Open, replacement Open, delayed parallel arm, ready Prepare/Consume, publication, Recv and Close.
  - Verify no cancellation winner is reclassified into recoverable failover and no provider activation/retry occurs after cancel wins.
  - Verify allocated-but-never-started and provider-attempted legs keep correct lineage/outcome semantics.
  - _Requirements: 2.1–2.7, 3.1–3.7, 4.4–4.8, 8.1–8.7, 10.1–10.4_
  - _Boundary: core runtime/leglifecycle certification only_
  - _Depends: 2.2, 2.3, 2.4_
  - _Validation: focused packages green repeatedly; targeted race/goleak passes on supported platform_

## Phase 3 — Turn Backend-Plugin CANCEL into a Negotiated Same-Execute Contract

- [ ] 3.1 (P) Add the next additive backend-plugin cancellation feature/minor and wire mode
  - Allocate the next available v1 protocol minor at implementation time (8 only if still free) and add optional `cancellation_handshake_v1` negotiation.
  - Add wire cancellation mode compatible with `none/provider/transport/close_only` and an additive mode field on `CancelOutcome`.
  - Preserve existing CANCEL reason/deadline fields, old peer compatibility, unknown-required-feature rejection, and disabled automatic retries.
  - Update conversion/shape/size/ABI structural tests and baselines deliberately.
  - _Requirements: 1.6, 4.1–4.7, 5.2–5.8_
  - _Boundary: `api/backendplugin/v1` + `pkg/lipsdk/backendplugin`; additive versioned contract only_
  - _Depends: 1.4_
  - _Validation: old-minor negotiation remains green; new feature round-trips mode/deadline/outcome exactly_

- [ ] 3.2 Refactor `ForwardExecute` into an active-Execute control/upstream coordinator
  - Continue reading host client frames after START with one control reader while one upstream reader owns `ManagedEventStream.Recv`.
  - Process negotiated CANCEL against the already-open upstream stream using the bounded effective deadline; keep CLOSE_INPUT semantically distinct.
  - Use one server-frame sequencer/sender for events, accounting, diagnostics, cancel outcome and terminal frames.
  - Ensure all reader/watcher goroutines stop and join when Execute ends.
  - _Requirements: 3.3–3.5, 5.1–5.5, 5.8–5.9, 8.4_
  - _Boundary: shared backend-plugin SDK helper; provider-neutral `ManagedEventStream` only_
  - _Depends: 3.1_
  - _Validation: negotiated CANCEL invokes the real active upstream stream and frame validator sees monotonic accepted→outcome/evidence→terminal ordering_

- [ ] 3.3 Strengthen the connector contract TCK around active cancellation
  - Replace/augment cancellation certification so one active Execute attempt is opened, then canceled in-band, and the test observes actual upstream cancellation rather than frame transmission.
  - Add success, negative/error outcome, missing outcome, late terminal, deadline expiry, forced transport cancellation and legacy-minor scenarios.
  - Validate exactly one terminal and no event-after-terminal behavior.
  - _Requirements: 5.1–5.9, 10.5–10.6, 10.8–10.9_
  - _Boundary: reusable backend-plugin connector contract tests; no product-specific assumptions_
  - _Depends: 3.2_
  - _Validation: standard fake connector plus at least one real standard connector passes both negotiated and legacy compatibility cells_

## Phase 4 — Make the Host Adapter Graceful-Then-Force and Backward Compatible

- [ ] 4.1 Implement attempt-local cancellation progress state in the backend-plugin adapter
  - Track cancel requested, outcome seen, terminal seen and forced-abort state with bounded synchronization.
  - `onPluginFrame` must signal/store negotiated `CancelOutcome` instead of discarding it while continuing to buffer accounting frames.
  - Populate cancel deadline with the earliest applicable caller/A-leg/runtime-policy bound; never widen deadlines.
  - _Requirements: 4.4–4.8, 5.2–5.7, 9.1, 9.5_
  - _Boundary: `internal/infra/backendplugins/adapter`; no core policy or provider-specific implementation_
  - _Depends: 1.4, 3.1_
  - _Validation: state-machine tests cover outcome-before-terminal, terminal-before-outcome, duplicate/late frames and deadline composition_

- [ ] 4.2 Implement graceful-to-force `Cancel` and deterministic `Close`
  - For negotiated connectors, send CANCEL on the active Execute stream, wait for terminal or the bounded grace deadline while still accepting accounting/outcome frames, then force-cancel only the active Execute context if required.
  - Return the actual final mode/error: provider/transport/close-only according to what occurred; if graceful provider cancellation was attempted but transport force-abort was required, diagnostics record both phases while the final stop mode remains truthful.
  - For legacy connectors, skip unsupported handshake claims and cancel only the active Execute transport/context.
  - Make `Close` idempotently join/clean the attempt after cancellation instead of racing ahead and destroying the handshake.
  - _Requirements: 3.3–3.6, 4.1–4.7, 5.2–5.9, 8.5–8.6_
  - _Boundary: host adapter/Execute transport only; never `CloseInstance` as per-B-leg force fallback_
  - _Depends: 2.4, 3.2, 4.1_
  - _Validation: poison connector cannot block past grace; unrelated Execute attempts on same connector instance remain alive_

- [ ] 4.3 (P) Upgrade standard connector feature advertisements and backend-family cancellation-mode contracts
  - Advertise `cancellation_handshake_v1` from standard connectors using the upgraded common helper.
  - For each backend family, certify the strongest real physical mode it uses; do not invent provider-native cancel APIs.
  - Add connector-specific provider cancellation only where an existing official protocol operation is actually supported and safe.
  - _Requirements: 4.1–4.3, 5.6–5.9, 10.8_
  - _Boundary: connector descriptors/adapters; provider-specific code stays outside core_
  - _Depends: 3.2_
  - _Validation: capability/mode matrix passes; close-only/transport backends are not mislabeled provider mode_

## Phase 5 — Preserve Provider Sideband Evidence Through Attempt Terminalization

- [ ] 5.1 Introduce/refactor the bounded attempt-owned provider-evidence accumulator
  - Reuse the existing per-attempt dedupe authority (`internalUsageKeys` or a focused successor) rather than creating a second dedupe source.
  - Retain unique provider-billable sideband events on the producing attempt with existing bounds/authority metadata.
  - Provide a snapshot/aggregate operation using existing authoritative/scoped usage aggregation semantics.
  - _Requirements: 6.1–6.5, 7.2–7.4_
  - _Boundary: attempt-local runtime evidence only; no client-visible event emission or money policy_
  - _Depends: 1.5, 2.4_
  - _Validation: multiple unique charges aggregate; duplicate dedupe keys count once; bounds fail safely_

- [ ] 5.2 Route normal sideband drain through the attempt-owned accumulator
  - Refactor response-pipeline sideband consumption so newly accepted evidence is first owned/deduped by the explicit attempt and then observed by existing internal response/usage observers.
  - Preserve current pre/post-Recv drain behavior and provider-only invisibility to clients.
  - _Requirements: 6.1–6.5_
  - _Boundary: response observation uses explicit attempt; never re-snapshot current slot for source attribution_
  - _Depends: 5.1_
  - _Validation: existing usage-sideband tests remain green plus replacement-source attribution tests_

- [ ] 5.3 Drain remaining sideband evidence inside the single attempt terminal owner
  - Before/during physical cancellation and before the billing fallback is frozen, drain any available `UsageEvidenceSource` evidence from the detached physical stream into the attempt accumulator.
  - Preserve evidence received before force-abort and run bounded terminal evidence work on detached cleanup context when the client context is canceled.
  - Do not let evidence-drain failure prevent attempt terminalization.
  - _Requirements: 6.2–6.7, 7.2–7.5_
  - _Boundary: `TerminalizeAttempt` remains sole physical owner; evidence drain is an effect inside that winner_
  - _Depends: 2.4, 5.1_
  - _Validation: cancellation/loser/late-open tests retain evidence without double physical teardown_

- [ ] 5.4 Feed aggregated provider evidence into existing exactly-once terminal B-leg billing precedence
  - Keep `FinalizeBilling` at-most-once and authoritative when available.
  - Use attempt-owned sideband/stream evidence as fallback/augmentation under existing presence/cost merge rules; preserve estimates/unavailable semantics when stronger evidence is absent.
  - Preserve positive B2BUA AttemptSeq, expected-B-leg call closure, customer/provider financial separation, and no stream-time money mutation.
  - _Requirements: 7.1–7.9, 10.7_
  - _Boundary: terminal usage-record evidence only; rating/journal/account balance remain post-usage billing owners_
  - _Depends: 5.2, 5.3_
  - _Validation: billing matrix covers finalizer success/failure/unsupported, sideband-only, duplicate keys, no evidence and parallel/cancel outcomes_

## Phase 6 — Preserve Recovery Semantics and Add Secret-Safe Observability

- [ ] 6.1 Reconcile intentional cancellation with recovery/error classification
  - Ensure explicit/context/force cancellation cannot be converted into recoverable pre-output failover or provider retry.
  - Preserve post-commit no-retry behavior, winner output ordering, affinity/interleaved state, secure session, completion gates, prompt-cache and compaction semantics.
  - Classify legacy Execute transport death caused by intentional cancellation as cancellation rather than an unrelated retryable provider failure.
  - _Requirements: 7.8, 8.1–8.7_
  - _Boundary: existing recovery/error classification only; no selector-policy redesign_
  - _Depends: 2.5, 4.2_
  - _Validation: serial/parallel committed/uncommitted cancellation matrix has no unexpected replacement or client-visible semantic drift_

- [ ] 6.2 (P) Add bounded cancellation diagnostics and metrics
  - Expose low-cardinality cause class, actual mode, requested/outcome/terminal/forced phase and timeout/error class using existing observability seams.
  - Never label/log prompt text, secrets, raw cancel detail, credentials or unbounded user/provider payloads.
  - Distinguish negotiated handshake from legacy fallback without treating fallback as a protocol violation.
  - _Requirements: 4.8, 9.1–9.5_
  - _Boundary: existing diagnostics/metrics ports; no new public admin API_
  - _Depends: 2.4, 4.2_
  - _Validation: metric label-cardinality/secret-safety tests plus representative log assertions_

## Phase 7 — Add Permanent Architecture Ratchets

- [ ] 7.1 Ratchet A-leg launch authority and #428 attempt ownership
  - Reject production provider-open paths that cross `Backend.Open` without the launch authority.
  - Reject raw backend-stream A-leg registration, alternate attempt terminal owners, post-publication raw teardown, and lock-held backend/billing I/O regressions.
  - Preserve ready-owned unpublished cancellation and one `TerminalizeAttempt` production entry.
  - _Requirements: 1.1–1.5, 2.1–2.7, 3.7, 10.10_
  - _Boundary: `internal/archtest`; semantic AST/rule checks preferred over brittle text counting_
  - _Depends: 2.5_
  - _Validation: ratchets fail against intentionally reintroduced anti-pattern fixtures and pass production tree_

- [ ] 7.2 (P) Ratchet cancellation ABI/result fidelity and evidence ownership
  - Protect the negotiated cancellation feature/minor, truthful mode conversion, active-Execute TCK surface, and legacy fallback contract.
  - Reject lifecycle wrappers that hard-code provider mode and terminal evidence paths that derive B-leg evidence from mutable current-slot identity.
  - Protect provider-only sideband from client-facing canonical emission.
  - _Requirements: 4.1–4.8, 5.1–5.9, 6.3–6.5, 10.10_
  - _Boundary: `internal/archtest` + backendplugin structural contract tests_
  - _Depends: 3.3, 5.4_
  - _Validation: ABI baselines deliberate; old-minor compatibility fixtures remain accepted_

## Phase 8 — Cross-Layer Certification and Release Gates

- [ ] 8.1 Run the complete runtime cancellation concurrency certification
  - Exercise explicit cancel, request context cancel, timeout, Close, Recv, replacement, late Open and race-loser schedules repeatedly.
  - Include cancellation-insensitive Open/Recv/Cancel fixtures and multiple active siblings.
  - Assert no deadlocks, data races, leaked goroutines, duplicate provider activation, duplicate Cancel/Close, duplicate terminal effects or duplicate B-leg records.
  - _Requirements: 10.1–10.4, 10.9_
  - _Boundary: runtime/leglifecycle certification_
  - _Depends: 2.5, 4.2, 5.4, 6.1_
  - _Validation: repeated deterministic runs + targeted `go test -race`/goleak where supported_

- [ ] 8.2 Run backend-plugin and connector cancellation certification
  - Run negotiated handshake and legacy fallback TCK across standard connectors/fakes.
  - Verify active upstream cancellation, actual mode, deadline expiry, force-abort scope, terminal sequencing, and no Execute-pump leaks.
  - Verify provider-mode is required only where the adapter really implements a provider-level operation.
  - _Requirements: 5.1–5.9, 10.5–10.6, 10.8–10.9_
  - _Boundary: backendplugin/host/adapter/connectors_
  - _Depends: 3.3, 4.3, 6.1_
  - _Validation: connector contract artifacts/matrix green for negotiated and downgraded peers_

- [ ] 8.3 Run terminal billing/evidence certification
  - Exercise winner, swallowed, canceled, timeout, late-open, parallel-loser and never-started legs with canonical usage, sideband-only usage, finalizer success/failure/unsupported, duplicate evidence and no evidence.
  - Assert one immutable terminal leg record per allocated B-leg, positive AttemptSeq, best-evidence precedence, explicit unavailable state, and correct call closure.
  - Verify customer settlement remains independent of provider-cost readiness and no stream-time money path appears.
  - _Requirements: 6.1–6.7, 7.1–7.9, 10.4, 10.7, 10.9_
  - _Boundary: runtime/billing-record integration; no financial architecture redesign_
  - _Depends: 5.4, 6.1_
  - _Validation: focused billing/runtime/store tests including replay/idempotency assertions_

- [ ] 8.4 Run final repository gates and produce implementation evidence
  - Run focused core/SDK/adapter/connector suites, `internal/archtest`, `make quality-checks`, `make test-unit`, `make parity-checks`, and targeted race/goleak/platform checks supported by CI.
  - Review final diff for scope creep, duplicate owners, widened deadlines, generated ABI artifacts, goroutine allowlist changes and accidental provider/core coupling.
  - Document any environment-only skips with equivalent CI evidence; do not weaken a gate to obtain green.
  - _Requirements: 9.1–9.5, 10.10–10.11_
  - _Boundary: whole-repository certification; no new feature work in this task_
  - _Depends: 6.2, 7.1, 7.2, 8.1, 8.2, 8.3_
  - _Validation: all required gates green and final implementation review verdict GO_

## Task Graph Review

- **Tasks**: 28 implementation tasks.
- **Parallel-capable tasks**: 8 (`1.2`, `1.3`, `1.4`, `1.5`, `3.1`, `4.3`, `6.2`, `7.2`).
- **Critical path**: baseline → launch RED → launch permit/ready integration → core certification → plugin handshake → adapter graceful/force → sideband terminal evidence → final certification.
- **No hidden prerequisite**: PR #428 is already merged on `main` and is revalidated by task 1.1 rather than treated as future work.
- **Dependency graph**: acyclic; no task depends on a later phase except explicitly parallel independent work after its prerequisites.
- **Scope boundary**: all production changes are attributable to requirements 2–9; Phase 1/7/8 exists to prove and preserve those contracts rather than invent functionality.
