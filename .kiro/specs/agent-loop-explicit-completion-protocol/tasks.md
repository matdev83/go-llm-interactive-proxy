# Implementation Plan

Implement the explicit-completion ALG strategy in strict RED -> minimal implementation -> GREEN -> refactor slices. Shared runtime work must remain provider-neutral; concrete `attempt_completion` policy stays under `internal/plugins/features/agentloopguard`. Do not implement a hybrid verifier fallback. Preserve old enabled-without-strategy behavior until explicit migration.

## Task Ordering Principles

- Characterize current main before changing shared candidate/tool/terminal seams.
- Build and certify the generic `controltool` contract before wiring ALG into it.
- Put request projection/accounting correctness before response interception.
- Put response interception before preferred terminal policy so tests never rely on a fake completion fact unavailable in production.
- Refactor current ALG into strategy dispatch only after the new isolated path has focused tests.
- Run legacy parity and architecture ratchets before broad acceptance/QA.

---

- [x] 1. Rebaseline brownfield seams and freeze characterization
  - [x] 1.1 Revalidate current candidate-open and tool-response ordering against `main`
    - Inspect current `executor` candidate shaping, request hooks, post-hook rederivation, conversation-view final reassertion, PTB/adaptation/open order, tool assembler/finalizers, tool policy/reactors, and terminal-decision chokepoint.
    - Reconcile any landed `b-leg-path-virtualization`, large-payload, or concurrency changes with this design before production edits.
    - Record focused characterization tests for every ordering invariant the implementation will depend on; do not create a permanent extra report unless repository conventions require one.
    - _Requirements: 4.7,5.1,5.4,8.7,12.6_
    - _Boundary: `internal/core/runtime`, `internal/core/extensions`, relevant active-spec surfaces; test-only in this task_
    - _Depends: none_
    - _Validation: focused runtime ordering tests; `git diff` must contain no production behavior change_

  - [x] 1.2 Characterize existing ALG configuration and verifier decisions
    - Add/refresh fixtures proving current `enabled: true` configuration, explicit-completion trust/verify behavior, verifier error/timeout/malformed behavior, progress caps, and provider-removal behavior.
    - Capture a reusable legacy acceptance matrix to compare after strategy dispatch is introduced.
    - _Requirements: 1.5,9.1-9.6,10.3-10.5_
    - _Boundary: `internal/plugins/features/agentloopguard`, `internal/standardplugins`; test-only in this task_
    - _Depends: none_
    - _Validation: focused ALG config/provider/standard-plugin tests pass before refactor_

- [x] 2. Add the generic proxy control-tool SDK and feature plane
  - [x] 2.1 Define bounded `pkg/lipsdk/controltool` contracts with RED validation tests
    - Add provider/spec/instruction/completed-call/meta/outcome types equivalent to the approved design.
    - Validate provider/tool/instruction IDs, JSON Schema, args bounds, role bounds, result/reason bounds, typed nils, and outcome invariants.
    - Keep the package free of ALG, `attempt_completion`, terminal, backend SDK, and frontend knowledge.
    - _Requirements: 2.6,3.2,5.2,5.5,12.1-12.2_
    - _Boundary: new `pkg/lipsdk/controltool` only_
    - _Depends: 1.1_
    - _Validation: `go test -count=1 ./pkg/lipsdk/controltool/...`_

  - [x] 2.2 Add the exclusive `PlaneControlToolProvider` contribution
    - Add the generated/declared feature plane with exclusive merge semantics, defensive typed-nil validation, snapshot accessor, and provider-removal behavior.
    - Update plane manifests/generators/architecture tables through normal repository tooling; do not hand-edit generated outputs inconsistently.
    - Prove a second provider is rejected deterministically and an absent provider is a zero-work no-op.
    - _Requirements: 1.4,10.5,12.2-12.3_
    - _Boundary: `pkg/lipsdk/feature`, `internal/featurebundle`, `internal/core/extensions` snapshot access, generated plane metadata_
    - _Depends: 2.1_
    - _Validation: focused feature/featurebundle/snapshot generated-plane tests; plane generator/check target_

  - [x] 2.3 Implement pure authority-neutral control projection and ToolChoice eligibility
    - RED-test message-authority and item-authority calls, stable instruction placement, tool append, idempotence/reassertion, collision, backend-tool capability, default/auto eligibility, and `none`/`any`/required/allowed-tools rejection.
    - Projection must clone/mutate only the B-leg candidate call and preserve original ToolChoice unchanged.
    - Return bounded inactive reason codes instead of converting an optional control feature into candidate rejection for ordinary ineligibility.
    - _Requirements: 2.7,3.1-3.4,4.1-4.7,10.6_
    - _Boundary: `pkg/lipsdk/controltool` pure projection helpers and generic tests_
    - _Depends: 2.1_
    - _Validation: controltool projection matrix, canonical `Call.Validate`, item/message parity tests_

- [x] 3. Integrate capability-aware control projection into candidate execution
  - [x] 3.1 Add RED runtime tests for post-hook projection and final reassertion
    - Prove control content is present before authoritative post-hook capability/context/accounting preflight and remains byte/semantics-identical after final conversation-view reassertion.
    - Prove CTP/A-leg baseline remains unchanged while PTB/backend-effective call contains the active tool/instruction.
    - Prove ineligible candidates remain otherwise usable and do not gain tool requirements.
    - _Requirements: 3.1,4.1-4.7,7.6,11.3,12.4,12.6_
    - _Boundary: `internal/core/runtime` tests plus generic fake control provider_
    - _Depends: 1.1,2.2,2.3_
    - _Validation: focused candidate-open/rederive/conversation-view/runtime tests RED before implementation, GREEN after 3.2_

  - [x] 3.2 Implement one generic bounded control-projection stage and attempt activation owner
    - Invoke the generic provider after ordinary request mutation and before authoritative post-hook rederivation using resolved candidate capabilities.
    - Store trusted activation/provenance in attempt-local runtime state, not in client-writable canonical extensions or serialized backend metadata.
    - Reassert only the already-approved byte-identical projection after final conversation-view reassertion and fail/exclude before `Backend.Open` if safe equivalence cannot be maintained.
    - Keep candidate/race attempt activations independent and generation-pinned.
    - _Requirements: 3.1-3.3,4.1-4.7,10.3-10.6,12.1-12.2_
    - _Boundary: generic `internal/core/extensions` + `internal/core/runtime`; no concrete ALG import/name_
    - _Depends: 3.1_
    - _Validation: 3.1 tests GREEN; candidate race/failover capability tests; `go test` on touched runtime/extension packages_

- [x] 4. Add streaming-preserving private control-call interception
  - [x] 4.1 Build a bounded attempt-local control-call capture state machine
    - RED-test start/args/finish correlation, name-less fragments by call ID, strict maximum args, duplicate/multiple call handling, incomplete close, cancellation, and reset/discard on attempt loss.
    - Capture only the tool prepared by trusted activation; ordinary tool events must be a pass-through.
    - _Requirements: 3.2,3.5,5.1-5.6,8.5,10.3_
    - _Boundary: generic runtime/SDK adapter state; no client tool execution_
    - _Depends: 2.1,3.2_
    - _Validation: focused capture/state-machine tests including malformed sequences and race cleanup_

  - [x] 4.2 Intercept claimed control calls before ordinary finalizers/policy/reactors
    - Wire capture at the backend-event boundary before existing ordinary tool-call assembler/finalizers and before tool policy/reactors.
    - Preserve BTP/provider usage observation and ordinary non-control streaming while preventing claimed lifecycle events from PTC/client release.
    - Call the pinned generic control provider only when a full bounded control call is complete; normalize provider panic/error through existing extension-safety conventions without falling through to client execution.
    - _Requirements: 3.5,5.1-5.7,11.1-11.3,12.3-12.4_
    - _Boundary: generic `internal/core/runtime`/`internal/core/extensions` response path_
    - _Depends: 4.1_
    - _Validation: event-order tests prove client tool policy/reactor fake sees zero claimed events and does see ordinary tools; streaming timing/order assertions_

  - [x] 4.3 Certify parallel/race/failover isolation and cleanup
    - Prove losing attempts cannot publish completion evidence/result into the winning logical response.
    - Prove replacement attempts get independent activation/capture and no stale call ID or args buffer survives transition.
    - Prove cancellation/Close deterministically releases bounded state with no goroutine/global-map ownership.
    - _Requirements: 4.7,8.2-8.7,10.3,12.5_
    - _Boundary: runtime attempt/response lifecycle tests and minimal generic cleanup code_
    - _Depends: 4.2_
    - _Validation: focused retry/race/TTFT/cancel tests; race detector where supported_

- [ ] 5. Project trusted completion evidence and publish pending result through the existing terminal owner
  - [x] 5.1 Extend terminal-decision evidence with completion expectation
    - Add additive `ExplicitCompletionExpected` (or repository-consistent equivalent) to generic evidence and validation/contract fixtures.
    - Set expectation only from a successful request-local control activation; set observed `ExplicitCompletion` from either existing native completion facts or a valid completed proxy control outcome.
    - Keep internal control calls out of ordinary client action/tool facts.
    - _Requirements: 3.2-3.4,6.1-6.2,7.1,7.6,9.3_
    - _Boundary: `pkg/lipsdk/terminaldecision`, generic runtime evidence projection_
    - _Depends: 4.2_
    - _Validation: terminaldecision contract + runtime evidence tests for active/inactive/native/proxy cases_

  - [ ] 5.2 Add pending completion-result publication at accepted terminal
    - RED-test completion-only response, prior visible text, invalid completion, continuation path, recording failure, and terminal sequencing.
    - For a valid proxy completion with no prior meaningful assistant text, release bounded `result` as canonical assistant text through existing response recording/traffic/usage ownership before accepted terminal publication.
    - When assistant text is already committed, do not duplicate the result; never emit result on invalid completion or an unaccepted terminal.
    - _Requirements: 6.3-6.7,11.3,11.5,12.5_
    - _Boundary: generic response/terminal drain path; no frontend-specific writes_
    - _Depends: 5.1_
    - _Validation: canonical event-sequence tests across streaming/non-streaming frontend fixtures; secure-recording/traffic/usage focused tests_
    - _Blocked: debug attempted twice, still failing — a timed-out real Close leaves an armed interrupted memo uncaptured when the admitted Observe callback later returns an error; fresh independent review rejected final checkpoint remediation. Task 5.2 WIP is preserved and uncommitted._

- [ ] 6. Add mutually exclusive ALG strategy configuration
  - [x] 6.1 Implement strategy-aware config decoding/normalization with RED tests
    - Add `attempt_completion|semantic_verifier` selector and bounded `max_protocol_reprompts` (default 1, V1 max 3).
    - Preserve enabled+omitted-strategy as semantic verifier.
    - Reject mixed strategy-specific fields using YAML key presence, not merely post-default values; apply equivalent validation for programmatic construction.
    - Keep `no_progress_limit` shared and existing legacy defaults unchanged.
    - _Requirements: 1.1-1.7,9.1,10.1-10.5_
    - _Boundary: `internal/plugins/features/agentloopguard/config.go` + tests_
    - _Depends: 1.2_
    - _Validation: config table tests for old/new/disabled/mixed/unknown/bounds cases_

  - [ ] 6.2 Compose exactly the planes required by the selected strategy
    - Preferred mode contributes the ALG terminal provider plus one control-tool provider and does not require/build verifier auxiliary machinery.
    - Legacy mode contributes only the current terminal provider path and no control provider.
    - Disabled mode contributes neither.
    - Prove generation reload/withdrawal and provider-removal behavior with immutable snapshots.
    - _Requirements: 1.1-1.4,3.6,9.4-9.6,10.3-10.5,12.3_
    - _Boundary: `internal/plugins/features/agentloopguard` feature root / `internal/standardplugins` feature-host composition_
    - _Depends: 2.2,6.1_
    - _Validation: standard-plugin feature bundle tests; reload/no-provider/removal fixtures_

- [ ] 7. Implement the concrete `attempt_completion` control provider
  - [x] 7.1 Pin the familiar tool schema and stable base instruction
    - RED-test exact tool name, one required `result` property, `additionalProperties:false`, absence of `command`, stable description, stable base instruction, and size bounds.
    - Keep name/schema/instruction non-configurable in V1.
    - _Requirements: 2.1-2.7,11.5_
    - _Boundary: `internal/plugins/features/agentloopguard/completiontool.go` (or cohesive feature-local equivalent)_
    - _Depends: 2.1,6.1_
    - _Validation: exact snapshot/string/schema tests plus tool spec validation_

  - [ ] 7.2 Implement strict completion-argument handling
    - Parse exactly one JSON object with exactly `result`; reject missing/empty/wrong-type/unknown/duplicate/trailing/non-UTF8/oversized values.
    - Return `OutcomeComplete` only for valid result; expected model mistakes return bounded `OutcomeInvalid`; internal errors remain typed errors.
    - No command execution, filesystem action, approval, or external tool call is allowed.
    - _Requirements: 2.2,5.5-5.6,6.1-6.2,11.1-11.2_
    - _Boundary: feature-local completion control provider/parser_
    - _Depends: 7.1_
    - _Validation: parser/adversarial/fuzz tests; no raw result/args in error strings/labels_

- [ ] 8. Implement preferred missing-signal policy and protocol state
  - [ ] 8.1 Add feature-local protocol state/fingerprint/token with independent prefix
    - Use a distinct bounded state token such as `alg-proto-v1`, never decode it as legacy `alg-state-v1` state.
    - Track total reprompts, stable evidence fingerprint, consecutive no-progress, and terminal state; exclude volatile IDs/timestamps.
    - Enforce immutable total cap even when progress occurs and intersect with platform continuation cap.
    - _Requirements: 7.4-7.5,10.1-10.2,11.1_
    - _Boundary: `internal/plugins/features/agentloopguard/protocolstate` or cohesive feature-local pure package_
    - _Depends: 6.1_
    - _Validation: encode/decode corruption/bounds + fingerprint/no-progress/cap tests_

  - [ ] 8.2 Build the bounded missing-signal continuation intent
    - RED-test exact semantic clauses: no new user intent/approval, complete -> call tool, unfinished -> continue only existing work, user-input-needed -> ask normally and end, no invention/broadening.
    - Use existing objective/trajectory only; keep internal-control provenance and platform-owned placement/lifecycle.
    - Default first missing signal continues, second unmarked candidate stops when max reprompts is one.
    - _Requirements: 7.1-7.7,8.2-8.7_
    - _Boundary: feature-local preferred protocol policy; output is only `terminaldecision.Decision`/intent_
    - _Depends: 5.1,8.1_
    - _Validation: pure decision/intent tests for normal/post-output/user-input/no-progress/exhausted/inactive cases_

  - [ ] 8.3 Add preferred strategy provider dispatch with zero verifier calls
    - Integrate canonical cause/safety classification, explicit completion, protocol expectation, protocol state, and intent builder in the feature provider.
    - Authoritative cancellation/refusal/filter, unsafe action state, pre-output recovery ownership, inactive protocol, and exhausted budgets stop conservatively.
    - Instrument tests so any auxiliary verifier construction/call in preferred mode fails the test.
    - _Requirements: 1.2,4.5-4.6,6.5,7.1-7.7,8.1-8.7,9.4-9.5_
    - _Boundary: `internal/plugins/features/agentloopguard/provider.go` + preferred policy files_
    - _Depends: 7.2,8.2_
    - _Validation: preferred provider acceptance matrix; auxiliary collector call count always zero_

- [ ] 9. Preserve and certify legacy semantic-verifier behavior
  - [ ] 9.1 Refactor provider construction/dispatch without changing legacy policy
    - Isolate existing verifier path behind explicit/implicit legacy strategy while retaining current causepolicy, verifier, progress, and recovery behavior.
    - Avoid sharing new protocol state/wording into the legacy path except neutral helpers proven behavior-preserving.
    - _Requirements: 1.3,1.5,9.1-9.6_
    - _Boundary: existing `internal/plugins/features/agentloopguard` provider/verifier/progress packages_
    - _Depends: 6.1,8.3_
    - _Validation: pre-task 1.2 characterization matrix remains GREEN_

  - [ ] 9.2 Add explicit legacy-vs-old parity and strategy-isolation tests
    - Compare old-style enabled configuration with explicit `semantic_verifier` across complete/incomplete/user-directed/optional/transport/limit/unsafe/cancel/verifier-failure/no-progress/budget fixtures.
    - Prove legacy bundle has no control provider and preferred bundle cannot reach verifier code.
    - _Requirements: 1.2-1.5,9.1-9.6,12.3_
    - _Boundary: ALG + standard-plugin tests_
    - _Depends: 9.1,6.2_
    - _Validation: focused parity suite, provider-removal tests_

- [ ] 10. Run the cross-layer acceptance matrix and harden observability
  - [ ] 10.1 Add end-to-end preferred-protocol fixtures across canonical authorities/frontends
    - Cover completion-only, streamed-text+completion, ordinary client tool then completion, missing signal, one-reprompt user-input case, malformed/multiple control calls, native completion collision, unsupported backend tools, and ToolChoice matrix.
    - Include message-authority and item-authority calls plus representative OpenAI/Anthropic/Gemini protocol adapters through existing testkit boundaries; no live billable calls required.
    - _Requirements: 2.1-2.7,3.1-3.6,4.1-4.7,5.1-5.7,6.1-6.7,7.1-7.7,12.5_
    - _Boundary: `internal/testkit`, runtime/frontends/backends contract fixtures; production changes only if a demonstrated generic bug exists_
    - _Depends: 4.3,5.2,6.2,8.3,9.2_
    - _Validation: focused E2E/contract suites with explicit early-stream observation assertion_

  - [ ] 10.2 Certify transport/cancellation/side-effect invariants
    - Test pre-output EOF/idle existing recovery, post-output interruption with active protocol, completed client tool/result retention, incomplete args, cancellation, refusal/filter, race losers, and continuation candidate reactivation.
    - Assert no replay/failover after client-visible commitment and no duplicate ordinary tool side effects.
    - _Requirements: 8.1-8.7,12.5_
    - _Boundary: runtime recovery/terminal integration tests_
    - _Depends: 10.1_
    - _Validation: streamrecovery/runtime/terminal focused suites; race where supported_

  - [ ] 10.3 Add bounded protocol observability and privacy tests
    - Emit strategy/activation/control/terminal reason telemetry through existing seams with bounded vocabularies.
    - Preserve upstream B-leg usage/cost and legacy auxiliary usage attribution; local control handling creates no fake provider usage.
    - Prove result/prompt/args/raw IDs never enter metric labels or bounded reason codes.
    - _Requirements: 11.1-11.5_
    - _Boundary: feature telemetry + existing generic extension/runtime observability seams_
    - _Depends: 8.3,10.1_
    - _Validation: metrics/trace/log privacy tests, usage/traffic attribution fixtures_

- [ ] 11. Document preferred usage and migration without changing old configs silently
  - [ ] 11.1 Update configuration/operator/plugin documentation and examples
    - Recommend explicit `strategy: attempt_completion` for new ALG deployments.
    - Document omitted-strategy legacy compatibility, explicit `semantic_verifier`, disabled behavior, mutual exclusion, ToolChoice/backend eligibility, one-reprompt default, self-attestation trade-off, and when to choose the independent verifier instead.
    - Document fixed tool schema/no-command rationale and client-owned `attempt_completion` collision behavior.
    - _Requirements: 1.5-1.7,2.1-2.6,4.1-4.6,7.4,9.1-9.6,10.1-10.2_
    - _Boundary: existing docs/example config/feature authoring references only_
    - _Depends: 6.1,8.3,9.2_
    - _Validation: docs/example-config/knowledge checks as applicable_

- [ ] 12. Architecture closure, simplification, and repository quality gates
  - [ ] 12.1 Add architecture ratchets for ownership and strategy isolation
    - Prove zero concrete ALG imports/branches in core, zero controltool package dependency on ALG, exclusive plane behavior, no A-leg direct append, no second terminal owner, no hidden `guardHidden` resurrection, no verifier reachability in preferred construction, and no control-provider reachability in legacy construction.
    - Add negative fixtures that demonstrate the ratchets fail on representative violations rather than grepping only happy paths.
    - _Requirements: 3.1-3.6,9.4-9.6,10.5,12.1-12.4_
    - _Boundary: `internal/archtest`, feature/SDK ownership fixtures_
    - _Depends: 6.2,9.2,10.1_
    - _Validation: `go test -count=1 ./internal/archtest/...` plus targeted negative-fixture proof_

  - [ ] 12.2 Revalidate adjacent landed specs and remove accidental complexity
    - Re-run the Task 1.1 census on final current main/branch integration, especially any landed path virtualization/large-payload/concurrency changes.
    - Prefer current canonical owners over compatibility shims; delete duplicated projection/capture/state helpers introduced during implementation; verify no unnecessary durable state, goroutine, map, or extra feature plane remains.
    - Confirm whole-response completion gates are not in the preferred normal path.
    - _Requirements: 5.7,10.6,12.1-12.6_
    - _Boundary: touched implementation surfaces; simplification only, no new feature scope_
    - _Depends: 10.2,10.3,11.1,12.1_
    - _Validation: focused suites after refactor; architecture line/plane budgets regenerated with rationale if changed_

  - [ ] 12.3 Run final repository gates and certify implementation readiness
    - Run formatting/build/vet, focused package tests, generated-plane checks, architecture/QA, docs/example-config checks, full repository tests, and race/fuzz targets required by touched parsers/concurrency surfaces and repository policy.
    - On Windows or other environments where a required gate cannot execute, report the limitation and rely on the repository's authoritative CI platform rather than claiming a local pass.
    - Record no implementation as complete while required CI is red or the legacy parity/streaming/ownership gates fail.
    - _Requirements: 12.7_
    - _Boundary: repository-wide validation only_
    - _Depends: 12.2_
    - _Validation: `make quality-checks`, `make test`, `make qa`, applicable race/fuzz/generated/docs gates and CI_

## Parallelization Summary

After Task 1 establishes the current baseline:

- Task 2.1/2.2/2.3 are mostly sequential because plane/projection types depend on the SDK contract.
- Task 6.1 (ALG config) may run in parallel with Tasks 2-5 because it is feature-local and protected by the Task 1.2 legacy characterization.
- Task 7.1/7.2 may begin after 2.1 and 6.1 while generic runtime Tasks 3-5 continue.
- Task 8.1 protocol state may run in parallel with runtime Tasks 3-5 after config bounds are frozen.
- Legacy refactor Task 9 intentionally waits for preferred policy shape so dispatch is introduced once rather than repeatedly.
- Acceptance/architecture/docs converge only after both strategies and generic runtime integration are stable.

This ordering minimizes shared-runtime churn while retaining real parallel work in feature-local config, tool contract, and protocol-state slices.

## Implementation Notes

- Task 1.1: `conversationprojection.Reassert` is a no-op unless NeverBackend/steering/provenance is non-empty; pin the live branch. Post-clamp preflight still sees message-authority `openCall` before `AdaptCallForCandidate`. Steering overlays can fail `VerifyAdaptation` if they land in Messages rather than Instructions.
- Task 1.2: `strategy` is still an unknown YAML field; `legacyEnabledConfigSources` currently has only omitted-strategy so task 9.2 can add explicit `semantic_verifier` without rewriting cases. Invert `TestLegacyEnabledYAMLRejectsStrategySelectorUntilConfigured` when the selector lands. Generation pin is two pluginreg `BuildFeatureBundle` snapshots with distinct frozen caps, not a full host reload.
- Task 2.1: `DefaultMaxArgsBytes` is 64 KiB to match the existing tool-call envelope. Schema validation is stdlib object-schema + no-`$ref`, not a draft compiler. Do not add `projection.go` until 2.3.
- Task 2.2: The control-tool plane belongs to Wave4_Tools and must remain non-negotiable canonical-required in both largebody eligibility compilation and authority assessment; provider removal publishes a new empty slot without mutating old snapshots.
- Task 2.3: Message authority includes both Instructions and Messages; exact client control copies in either container deactivate projection, and final reassertion rejects relocation/duplication while allowing ordinary history reconstruction. Pure projection owns byte slices and preserves nil/empty shapes.
- Task 3.1: The approved RED checkpoint uses temporary controltool_red test build tag, mandatory removal in 3.2. Preliminary post-hook eligibility resolution may see two ordinary tools; authoritative rederivation must see all three including the approved control append.
- Main rebase: Rebased onto 1fc49fe2, preserving the metadata-only session classifier and canonical-required control-tool provider with 28 planes; independent merge review and scoped checks pass.
- Dedicated billing/metering unblock: Insert-first journal identity resolution removes two reads for fresh observations while preserving atomicity, stored-envelope validation and source-identity collision priority; replay now attempts a conflict-suppressed insert. The private F4 durable-sink-failure fixture runs serial with package peers. Production budgets remain unchanged. Both repairs passed independent review, five repeated race runs for each durable F4 case, full tagged runtime and journalstore race checks, scoped direct PostgreSQL parity/replay, and complete pre-commit hooks.
- Task 3.1 accepted: Fresh tagged projection suite fails exactly the three intended behavior tests; ineligibility, absence and client-owned collision cases remain green. Independent review approved the RED checkpoint. Task 3.2 must remove controltool_red and make all assertions pass.
- Task 3.2: Added one post-hook, capability-aware generic projection stage and private attempt activation, with generation-frozen identity/suppression and final byte-stable reassertion. Removed controltool_red; all prior assertions pass unchanged. The canonical-required plane blocks wire execution when occupied. The clock is assigned before session publication rather than transported through the input, preserving constructor semantics and the existing ownership ratchet. Independent review, fresh focused projection/seam tests, scoped tests/lint/architecture guards and focused race verification pass.

- Dedicated staged-race unblock: Staged architecture scopes now run separately from ordinary scopes, preserving selected packages, race flags and production budgets. Independent review approved the repair; the real mixed staged gate and all repair commit hooks pass.

- Dedicated loader-fixture unblock: Replaced live HEAD/working-tree equality with an isolated committed Git fixture and dirty-tree independence checks; normal hooks passed in c1fab5cd.
- Task 4.1: Added unwired, attempt-local control capture with bounded owned arguments and 16 fixed-size correlation keys. Claimed malformed and mixed item payloads stay private, sticky invalidation revokes prior validity, and exhaustion is fatal. Item handoff uses exact-size copying. Behavioral RED regressions, focused tests/race/lint and final independent review pass. Integration must keep BTP/usage before capture, abort immediately on fatal, clear pending outcome on invalidation and respect single-owner Recv/Close synchronization.

- Task 4.2: Interception runs after BTP/usage and before ordinary tool processing. The pinned generic handler validates frozen inputs/outcomes and normalizes provider failures without exposing provider text. Provider metadata is deep-owned; outcomes stay private and later invalidation revokes them. Behavioral RED, scoped tests/lint/race, independent review and a fresh real-Recv/PTC/fatal regression pass. Task 4.3 must synchronize cancellation/loss cleanup without holding state locks across provider or backend I/O.

- Dedicated configsource unblock (`c1e25970`): live recycled-inode fixture now compares full identities, asserting fail-closed rejection when birth timestamps coincide; distinct-identity eligibility and in-place rewrite rejection remain. Original reproduction 19/20 failures; fresh 30-run regression, independent review, complete root pre-commit tests, quality/lint and scoped race passed. No production identity-policy change.

- Task 4.3: attempt-local controlMu protects observation, one handoff and outcome storage against one-way disposal; provider/backend/terminal I/O stays unlocked. Existing capture transfers owned arguments without another copy. Real cancel/Close, loser disposal and public-stream failover tests, scoped race/lint, independent review and fresh parent lifecycle/370-copy AST checks pass. Task 5.2 must own a bounded result snapshot before attempt cleanup and publish only through accepted request-terminal ownership.

- Task 5.1: trusted activation projects independent expectation; validated live proxy completion ORs with existing native completion. Disposal retains expectation and revokes proxy observation. Independent review, focused tests/race/lint, current AST guards and fresh parent evidence tests pass; task 5.2 owns result publication.
- Task 5.2: final debug cycle 2 exhausted; fresh review proves timed-out Close plus late Observe error strands an armed interrupted memo request. Preserve uncommitted runtime work; independent task 6.1 depends only on completed 1.2 and may continue. Remaining publication integration is unaccepted.

- Task 6.1: Strategy-aware YAML/programmatic normalization preserves legacy defaults and rejects inactive-field presence and malformed protocol caps with bounded errors. Independent review and fresh four-package tests pass. Preferred provider construction remains tasks 8.3/6.2; run 7.1/7.2 and 8.1/8.2/8.3 before composition so no placeholder provider is installed.

- Task 7.1: Frozen completion-tool schema and exact 688-byte LF instruction are independently pinned, bounded and copied per projection. Genuine assertion RED, independent review and fresh five-package tests pass. Task 7.2 must reuse completionToolSpec without changing its ABI.
