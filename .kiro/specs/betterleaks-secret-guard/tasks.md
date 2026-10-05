# Implementation Plan

- [x] 1. Establish detector configuration and brownfield characterization
  - [x] 1.1 Add failing configuration/default tests for detector selection
    - Characterize existing top-level feature activation, required action, single-user exact defaults, multi-user request-credential behavior, and reload failure semantics before changing implementation.
    - Add matrix tests for absent/true/false local auto-discovery across both access modes and absent/true/false BetterLeaks enablement.
    - Prove explicit local auto-discovery true in multi-user is rejected without any environment call.
    - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 1.9, 10.2, 10.8_
    - _Boundary: secret-guard feature configuration and composition_
    - _Depends: none_
    - _Validation: focused config/compose tests with panic environment_

  - [x] 1.2 Implement presence-aware detector policy resolution
    - Add the local-auto-discovery and BetterLeaks configuration subtrees without changing the existing action/audit/redaction contracts.
    - Resolve access-mode-dependent defaults only at composition time and keep the effective runtime config immutable.
    - Validate confidence, decode depth, workers, mutually exclusive rule selectors, and canonicalized rule lists before candidate publication.
    - Preserve existing host binding and single-user catalog overrides where their semantics remain applicable.
    - _Requirements: 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 7.1, 7.2, 7.3, 7.4, 7.7, 7.9, 7.10_
    - _Boundary: secret-guard config resolver / standard featurehost composition_
    - _Depends: 1.1_
    - _Validation: config decode + candidate generation tests_

- [x] 2. Build the private BetterLeaks generation adapter
  - [x] 2.1 Add the pinned v2 dependency and detection-only adapter boundary
    - Pin BetterLeaks v2 to the exact reviewed version and import only the minimum detection/config surface.
    - Construct one reusable generation scanner with stdlib regex, explicit AIProxer worker count, confidence threshold, decode depth, precompile, and zero allow signatures.
    - Keep BetterLeaks logging disabled and expose only a feature-private scanner handle.
    - RED-test `betterleaks:allow` and `gitleaks:allow` bypass attempts before adapter implementation.
    - _Requirements: 3.1, 3.2, 3.4, 3.5, 3.6, 3.7, 3.8, 8.1, 8.3, 9.6_
    - _Boundary: private BetterLeaks detector adapter_
    - _Depends: 1.2_
    - _Validation: adapter unit tests + dependency/import architecture checks_

  - [x] 2.2 (P) Add architecture ratchets for forbidden integration modes
    - Reject BetterLeaks CLI/subprocess integration under the secret-guard feature tree.
    - Reject BetterLeaks analysis/pipeline/provider-validation/revocation and source-orchestration imports on the request path.
    - Reject BetterLeaks concrete types outside the private adapter and reject any allow-signature configuration surface.
    - _Requirements: 3.2, 3.3, 3.4, 3.5, 3.6, 7.8, 10.7_
    - _Boundary: internal architecture tests_
    - _Depends: 2.1 interface boundary_
    - _Validation: architecture test suite_

  - [x] 2.3 Configure rule selection and frozen policy identity
    - Apply validated selectors to the pinned in-memory default configuration. `disable_rules` rejects unknown IDs and removes only explicitly selected known IDs; reject candidate publication with a bounded configuration error if any retained rule requires a selected ID. Permit component removal when every rule that requires it is explicitly selected for removal. Do not cascade-remove dependents, weaken or remove retained required references, or substitute suppression for removal.
    - For `isolate_rules`, retain selected roots and their transitive required component closure, prune optional references to excluded components, and retain an explicitly selected optional component with its pinned matching/reporting behavior and required closure.
    - Capture BetterLeaks version, config hash, active rule count, confidence, decode depth, and worker count as safe generation facts.
    - Add a review ratchet so an upstream dependency/rule change cannot alter the default resolved policy unnoticed.
    - _Requirements: 7.5, 7.6, 7.7, 7.9, 7.10, 9.1, 9.2, 9.3_
    - _Boundary: BetterLeaks scanner builder / policy ratchet_
    - _Depends: 2.1_
    - _Validation: deterministic config hash/rule inventory goldens; required-only `aws-secret-access-key` disable rejection; component-plus-dependent `aws-secret-access-key` plus `aws-access-token` disable acceptance; shared component rejection while any required dependent remains; optional removal and isolation behavior; unchanged retained AWS multipart matching; failed candidate rejection retains the previously published generation_

- [x] 3. Add context-preserving logical-fragment discovery
  - [x] 3.1 Define and RED-test the logical fragment traversal
    - Characterize current canonical locations and JSON raw representations used by secret guard.
    - Produce one bounded fragment per logical text/JSON unit without concatenating the complete request or exposing fragments outside the feature.
    - Prove contextual JSON detection remains possible for generic key/value rules in user prompts and tool results; tool definitions and model-generated arguments are excluded under 4.7.
    - Share the existing request-level byte budget instead of creating a second independent scan allowance.
    - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.6, 10.3, 10.4_
    - _Boundary: secret-guard canonical traversal_
    - _Depends: 2.1_
    - _Validation: traversal tables + JSON context fixtures + scan-limit regression_

  - [x] 3.2 Implement error-returning BetterLeaks fragment scans
    - Use the pinned SDK's error-returning scan path with request context/cancellation and a tiny in-memory fragment source where required.
    - Reuse the generation scanner concurrently; do not construct scanners per fragment/request.
    - Enforce the 256 projected-finding request cap and return bounded failure classification when exceeded.
    - _Requirements: 4.5, 8.1, 8.2, 8.4, 8.5, 8.6, 8.7_
    - _Boundary: BetterLeaks adapter request path_
    - _Depends: 3.1_
    - _Validation: cancellation/error/cap/concurrency tests_

- [x] 4. Project findings safely and merge hybrid detector results
  - [x] 4.1 Extend safe finding provenance without leaking BetterLeaks internals
    - Add only bounded detector/rule/confidence metadata needed by audit and diagnostics.
    - Keep raw matches, captures, components, context, fingerprints, and fragments private.
    - Validate/bound projected strings and preserve compatibility for existing exact findings.
    - _Requirements: 2.5, 5.1, 5.2, 5.5, 5.6, 9.4_
    - _Boundary: secretguard SDK safe metadata + feature projector_
    - _Depends: 3.2_
    - _Validation: serialization/leak tests + existing SDK consumers_

  - [x] 4.2 Implement private deduplication and deterministic merge
    - Deduplicate exact and BetterLeaks reports using private concrete occurrence/span/value identity before public projection.
    - Preserve meaningful exact source/reference attribution and attach BetterLeaks provenance without double-counting one occurrence.
    - Sort merged findings deterministically independent of worker scheduling.
    - _Requirements: 4.5, 5.3, 5.4, 5.5_
    - _Boundary: feature-private hybrid merger_
    - _Depends: 4.1_
    - _Validation: overlapping/repeated/randomized-order fixtures_

- [x] 5. Preserve exact protection and wire hybrid composition by access mode
  - [x] 5.1 Restore/retain single-user exact local protection behind its new switch
    - Preserve existing include/exclude/popular environment inventory, minimum-secret-length, known-prefix, and mask behavior when enabled.
    - Ensure disabling local discovery performs no environment catalog enumeration and does not disable BetterLeaks.
    - _Requirements: 1.3, 1.6, 2.1, 2.3_
    - _Boundary: exact local source/composition_
    - _Depends: 1.2, 4.2_
    - _Validation: existing catalog characterization + switch matrix_

  - [x] 5.2 (P) Preserve shared request-credential exact protection
    - Keep the current accepted request credential private in ingress context and exactly detectable regardless of BetterLeaks state/rule coverage.
    - Prove no process environment is consulted in multi-user composition or request execution.
    - _Requirements: 1.4, 2.2, 2.4, 2.5, 10.2_
    - _Boundary: stdhttp auth matcher + multi-user resolver_
    - _Depends: 1.2, 4.1_
    - _Validation: arbitrary opaque request-credential tests with panic environment_

  - [x] 5.3 Integrate hybrid detector services into the frozen generation plane
    - Compose exact and BetterLeaks detector capabilities without moving BetterLeaks imports into generic runtime or core.
    - Preserve disabled behavior, action/audit configuration, immutable generation semantics, and existing request stage ordering.
    - Expose bounded detector posture through the existing diagnostics inventory path.
    - Verify selector dependency rejection through actual candidate composition retains the previously published generation and its request behavior (Requirement 7.10).
    - _Requirements: 1.1, 1.7, 1.8, 3.2, 9.1, 10.1, 10.8_
    - _Boundary: standard featurehost secret-guard composition / frozen plane_
    - _Depends: 2.3, 5.1, 5.2_
    - _Validation: generation compose/reload/inventory tests_

- [x] 6. Route BetterLeaks discoveries through existing enforcement and redaction
  - [x] 6.1 RED-test action semantics for BetterLeaks findings
    - Cover block before dispatch/quarantine, log without mutation, literal redact, decoded/unrewritable redact, scan error, finding-cap error, and existing scan-limit behavior.
    - Assert block/log never mutate input and failed redaction never partially commits the working clone.
    - _Requirements: 6.1, 6.2, 6.5, 6.6, 6.7, 8.4, 8.6_
    - _Boundary: secret-guard Guard action tests_
    - _Depends: 5.3_
    - _Validation: focused Guard/runtime no-dispatch tests_

  - [x] 6.2 Implement the transient exact rewrite bridge
    - Extract only literal rewrite candidates that are provably present in the original fragment.
    - Feed those candidates into existing byte-length-preserving text and parsed-JSON mutation semantics.
    - Re-run canonical validation after mutation and retain existing unsupported-token fail-closed behavior.
    - Zero/release transient secret bytes where practical after rewrite planning/execution.
    - _Requirements: 6.3, 6.4, 6.6_
    - _Boundary: feature-private rewrite bridge + existing rewriter_
    - _Depends: 6.1_
    - _Validation: text/JSON redaction differential tests_

  - [x] 6.3 Implement decoded/unrewritable fail-closed behavior
    - Map non-literal BetterLeaks positives to safe findings while withholding unsafe rewrite candidates.
    - Block under redact with bounded `unrewritable_detected_secret`; preserve normal block/log semantics.
    - Prove no encoded secret is reported as redacted unless its original representation was actually sanitized.
    - _Requirements: 6.5, 6.7_
    - _Boundary: BetterLeaks discovery-to-enforcement bridge_
    - _Depends: 6.2_
    - _Validation: depth-1 encoded fixtures under all actions_

- [x] 7. Harden observability and failure privacy
  - [x] 7.1 Add bounded detector diagnostics and audit provenance
    - Project detector posture and BetterLeaks policy facts into existing secret-guard diagnostics.
    - Add detector/rule/confidence only where bounded audit consumers need explanation; keep metrics low-cardinality.
    - Sanitize scanner/construction errors before generic logging/client mapping.
    - _Requirements: 5.6, 9.1, 9.2, 9.4, 9.6_
    - _Boundary: diagnostics/audit/metrics_
    - _Depends: 4.1, 5.3_
    - _Validation: inventory and structured audit tests_

  - [x] 7.2 (P) Add end-to-end anti-secret observability canaries
    - Feed unique synthetic credentials through exact, BetterLeaks, overlapping, decoded, scanner-error, and redaction-failure paths.
    - Capture ordinary logs, structured audit, metrics text/labels, diagnostics, errors, and decision DTOs and assert the raw canary/fingerprint/context never appears.
    - _Requirements: 5.1, 5.2, 5.5, 9.4, 9.5_
    - _Boundary: secret-guard leak regression suite_
    - _Depends: 4.1, 6.3_
    - _Validation: canary absence assertions across all observability sinks_

- [x] 8. Certify protocol parity, concurrency, fuzz safety, and performance
  - [x] 8.1 Build the synthetic detector corpus and frontend parity matrix
    - Cover OpenAI, Anthropic, GitHub, Slack, Stripe, AWS multipart, generic API key/password/credential URI, private key, public/non-secret negatives, JSON key context, schema snippets within user prompts/tool outputs, repeated overlaps, allow markers, and decoded forms.
    - Run equivalent canonical payload cases through every bundled frontend flavor that can represent them.
    - Consume authoritative `Call.Items` within the feature-private logical fragment walker, including eligible user/tool message text/JSON and tool-result output/parts. Exclude model-generated tool-call arguments under 4.7. Preserve shared byte-budget accounting, stable locations, and clone-only replacement closures without projecting duplicate legacy messages or changing frontend/API contracts.
    - Exercise real BetterLeaks under the unchanged resolved default policy for positive and negative frontend cases; assert detector/rule provenance, JSON/canonical validity after literal redaction, and hybrid repeat/overlap counts. Do not skip representable item-authoritative payloads.
    - Use synthetic credentials only and avoid printing fixture values in test failures.
    - _Requirements: 10.3, 10.4_
    - _Boundary: testkit + frontend/secret-guard integration tests; feature-private authoritative-item traversal and replacement closures_
    - _Depends: 6.3_
    - _Validation: parity matrix_

  - [x] 8.2 (P) Add race and fuzz/adversarial certification
    - Race one shared generation scanner under realistic concurrent request scans.
    - Fuzz logical fragment mapping, malformed JSON, exact/discovery overlaps, redaction invariants, high finding counts, cancellation, and observability sanitization.
    - Assert no per-request unbounded goroutine growth and stable deterministic finding ordering.
    - _Requirements: 8.2, 8.6, 8.7, 10.5_
    - _Boundary: race/fuzz test suites_
    - _Depends: 6.3_
    - _Validation: targeted race + fuzz smoke + adversarial unit tests_

  - [x] 8.3 Benchmark BetterLeaks-only and hybrid hot-path cost
    - Benchmark 1 KiB/10 KiB/100 KiB/1 MiB/2 MiB no-hit and positive cases, JSON, generic-heavy adversarial input, and realistic concurrency.
    - Compare exact-only, BetterLeaks-only, and hybrid latency/allocations; record p50/p95/p99 where the benchmark harness supports it.
    - Record scanner build/precompile cost, goroutine behavior, binary size, and dependency-size delta.
    - Treat a material regression as a design/performance review trigger rather than hiding it with weaker detector defaults.
    - _Requirements: 8.8, 10.6_
    - _Boundary: secret-guard benchmarks / build-size evidence_
    - _Depends: 8.1, 8.2_
    - _Validation: reproducible benchmark report attached to implementation PR_

  - [x] 8.4 Resolve branch-local quality findings
    - Apply the four verified formatter, redundant assignment, and redundant type fixes without changing behavior.
    - _Requirements: 10.1_
    - _Boundary: BetterLeaks adapter; scan state declaration; race/fuzz test formatting; runtime observability canary test declaration_
    - _Depends: 8.3_
    - _Validation: focused feature/runtime tests; scoped lint; git diff --check_

  - [x] 8.5 Restore architecture convergence through genuine simplification
    - Simplify feature-owned credential acceptance and diagnostic/composition duplication until the unchanged architecture ratchet passes. Main passes at -801; this feature adds 55 measured production lines and currently measures -746.
    - Preserve accepted-credential attribution, request isolation, no-environment guarantees, frozen generation diagnostics, and zero-valued policy facts.
    - Do not change budgets, broaden exclusions, compress formatting, or relocate unchanged logic to alter the measurement.
    - _Requirements: 2.2, 2.4, 9.1, 10.1, 10.2; archived runtime-architecture-convergence-and-shrinkage 11.5, 11.6_
    - _Boundary: feature-owned changes in standard HTTP auth adapter, diagnostics mount, runtimebundle secret-guard plane and their focused tests_
    - _Depends: 8.4_
    - _Validation: strict TDD; exact unchanged shrinkage ratchet; full architecture suite; affected auth/composition/diagnostics regressions_

  - [x] 8.6 Reduce measured feature-private fragment allocation costs
    - Preserve immutable text as strings and reuse one admitted fragment representation; materialize byte buffers only when occurrence mapping or mutation needs them. Avoid unsafe string aliases.
    - Preserve whole JSON context, distinct-field byte accounting, clone-only mutation, cancellation, deterministic merge, and decoded fail-closed behavior.
    - Repeat comparable exact-only, BetterLeaks-only, and hybrid no-hit/hit benchmarks and allocation profiles. Record the performance assessment and remaining upstream matching cost without weakening detector defaults.
    - _Requirements: 4.1, 4.2, 4.6, 8.2, 8.8, 10.1, 10.6_
    - _Boundary: feature-private logical fragments, adapter source, scan/merge/rewrite consumers and their tests; benchmark evidence_
    - _Depends: 8.5_
    - _Validation: strict TDD; corpus/budget/redaction regressions; targeted race/fuzz; comparable benchmarks and profiles_

- [x] 9. Run final cross-boundary security certification
  - [x] 9.1 Verify all architecture, config, security, and regression gates together
    - Re-run zero-env-read multi-user tests with BetterLeaks on/off, local-discovery invalid/absent, reload failures, and normal request traffic.
    - Re-run allow-marker, no-network, no-CLI, no-raw-finding, scan-failure, decoded-redact, quarantine/no-dispatch, deterministic merge, and default-policy-hash ratchets.
    - Run the repository's applicable quality, unit, race, parity, and QA gates for a wide security-sensitive feature change.
    - Confirm no implementation task changed routing/failover/B2BUA/billing/protocol semantics outside the specified secret-guard boundary.
    - _Requirements: 3.3, 3.4, 3.6, 6.7, 9.3, 9.5, 10.1, 10.2, 10.7, 10.8_
    - _Boundary: whole-feature certification_
    - _Depends: 7.1, 7.2, 8.3, 8.4, 8.5, 8.6, 9.2, 9.3, 9.4, 9.5_
    - _Validation: make test-unit; make quality-checks; applicable parity/qa/race gates_

  - [x] 9.2 Repair upstream billing boundedness measurement
    - Establish the cause of the intermittent process-wide stack-growth measurement and repair it at the billing test boundary. Preserve maximum chain depths, pinned fingerprints, solver coverage, and meaningful detection of depth-dependent recursion.
    - Add independent controls proving that iterative traversal passes and depth-growing recursion fails, including unrelated runtime activity where applicable. Do not increase bounds, skip checks, or select passing samples.
    - _Requirements: 10.1; upstream extensible-usage-economics-reconciliation non-recursion/certification contract_
    - _Boundary: billing boundedness integration test and directly related test helpers; no downstream detector workaround_
    - _Depends: 8.6_
    - _Validation: strict TDD; independent negative controls; focused maximum-depth integration probe; scoped lint/vet and affected regression checks_

  - [x] 9.3 Preserve billing certification population after harness repair
    - Move the four new harness regression tests into the existing integration-tagged stack-probe helper file. Keep their behavior and coverage, the existing certification population assertion, and the canonical sweep selector unchanged.
    - _Requirements: 10.1; upstream billing certification-tier contract_
    - _Boundary: billing stress integration test and stack-probe test helper file only_
    - _Depends: 9.2_
    - _Validation: failing billing-schema QA preflight before relocation; passing preflight and all relocated integration regressions afterward; scoped vet and diff checks_

  - [x] 9.4 Isolate billing-spool worker semantics from disk synchronization
    - Use the existing injected database seam to give the two failing worker-delivery tests private in-memory SQLite fixtures, consistent with the testing steering. Preserve their one-second/five-second observation limits, assertions, repeated Start calls, claim configuration, and caller-owned database cleanup.
    - Keep file-backed durability/restart tests and production spool behavior unchanged. Diagnostic passes do not establish the cause of the original full-suite failures; final certification must establish the resulting suite status.
    - _Requirements: 10.1; upstream billing-spool worker/certification contract_
    - _Boundary: the two worker-delivery tests and a directly related test fixture helper in internal/infra/billingspool/spool_test.go_
    - _Depends: 9.3_
    - _Validation: equivalent pre-change fixture check; focused worker and complete spool tests; unchanged file-backed durability tests; scoped vet and Linux race; comprehensive certification in 9.1_

  - [x] 9.5 Remove the reasoning-preservation poller fixture data race
    - Make shared test-poller job-ID recording safe for concurrent Poll calls. Keep the concurrent attach/clear scenario, recorded-ID assertions, and production feature behavior unchanged.
    - _Requirements: 10.1; upstream reasoning-preservation concurrency/certification contract_
    - _Boundary: reasoning-preservation poller test double and directly related test assertions only_
    - _Depends: 9.4_
    - _Validation: reproduce the concurrent Poll race before repair; focused concurrent adoption and complete package Linux race after repair; scoped lint/vet and diff checks_

## 10. PR review remediation

- [x] 10.1 Bound location projection for newline-dense fragments
  - Map upstream locations once per logical fragment and retain validated absolute byte offsets; eliminate per-finding whole-fragment splitting and repeated offset translation.
  - Add a single 2 MiB newline-dense fragment with near-cap findings to regression tests and benchmarks. Preserve byte-location semantics, literal verification, cancellation, and the 256-finding cap.
  - _Requirements: 4.4, 5.1, 6.3, 8.6, 8.7, 8.8_
  - _Boundary: private BetterLeaks adapter, location helpers, and directly related feature tests/benchmarks_
  - _Validation: strict TDD; bounded allocation regression; targeted benchmarks; full secretguard tests and vet_

- [x] 10.2 Apply immutable generation redaction policy in every access mode
  - Make resolved mask and prefix policy authoritative for exact, BetterLeaks, and hybrid redaction in single-user and multi-user modes; preserve explicit false and custom masks.
  - _Requirements: 6.3, 6.4, 7.10, 8.1, 10.2_
  - _Boundary: generation services, host composition, rewrite policy plumbing, request-credential redaction, and directly related tests_
  - _Depends: 10.3_
  - _Validation: strict TDD; single/multi-user detector-policy cross-product; affected composition/auth/feature consumers; vet_

- [x] 10.3 Deduplicate positional request-credential overlap
  - Add a neutral value-free positional capability for safe exact attribution, implement it for authenticated request credentials, and consume it privately for hybrid overlap deduplication. Do not import feature engine into auth or expose secret bytes/hashes.
  - _Requirements: 4.5, 5.2, 5.3, 5.5, 10.2_
  - _Boundary: SDK secretguard positional contract, auth credential matcher, private feature occurrence bridge, and related tests_
  - _Depends: 10.1_
  - _Validation: strict TDD; multi-user exact/BetterLeaks overlap and repeated occurrence tests; auth/SDK/feature regressions; architecture checks_

- [x] 10.4 Enforce complete post-redaction occurrence coverage
  - Require each BetterLeaks occurrence to be covered by actual rewrite or exact overlap; any missed occurrence blocks with unrewritable_detected_secret even if another mutation succeeds.
  - Update design and certification artifacts with current evidence for all four review areas; historical green checks do not certify these repairs.
  - _Requirements: 6.3, 6.4, 6.5, 6.6, 6.7, 10.1_
  - _Boundary: feature rewrite/evaluation and targeted tests; parent-owned spec artifacts_
  - _Depends: 10.2_
  - _Validation: strict TDD; partial/zero rewrite and JSON mapping negative controls; feature/integration/race checks; current remote CI; focused kiro-review fallback when delegation is unavailable; independent PR re-review before merge_

## 11. Content provenance and streaming contract correction

- [x] 11.1 Restrict canonical scanning and redaction by content provenance
  - Admit only user messages and genuine tool output across message/item authority. Preserve assistant history, model tool calls, instructions, unknown roles and tool definitions without findings or budget accounting. Apply uniformly to all detectors and actions; adapt older broad-scope fixtures to the corrected contract without removing detector/JSON assertions.
  - _Requirements: 4.1–4.7, 6.1–6.7_
  - _Boundary: feature logical-fragment traversal and directly affected feature fixtures/regressions; parent-owned approved spec correction_
  - _Validation: strict TDD; mixed history, excluded-only budget/enforcement, exact/BetterLeaks/hybrid text/JSON and message/item tests; complete affected suites and vet_

- [x] 11.2 Ratchet incremental response passthrough while request redaction is active
  - Use a controllable provider stream to prove unchanged secret-bearing response events are observed before completion, with BetterLeaks enabled and action:redact. Prove eligible request content is redacted before dispatch and preserve normal termination/cancellation cleanup.
  - _Requirements: 4.7, 10.8, 10.9_
  - _Boundary: runtime SecretGuard integration tests and scoped Linux CI certification; no response-path production changes_
  - _Depends: 11.1_
  - _Validation: meaningful buffering negative control; focused runtime test; feature/auth/composition/runtime Linux race; quality/unit/parity and fresh remote CI; focused independent review_

## 12. Full-head review remediation

- [x] 12.1 Fail closed on incomplete multipart projection and retain independent log findings
  - Recognize upstream `ComponentSetsTruncated` and local component/occurrence cap exhaustion; incomplete rewrite knowledge must never certify sanitization. Preserve bounded safe metadata and clone-only mutation.
  - A BetterLeaks failure in log mode must still collect exact/request-credential findings from the admitted fragments and retain `ScanLimitHit` without exposing raw scanner errors or mutating the call.
  - _Requirements: 5.1, 5.2, 6.2, 6.5, 6.7, 8.4, 8.6, 8.7_
  - _Boundary: betterleaks_adapter.go, scan.go, guard.go and directly related feature regression tests only_
  - _Validation: strict TDD for upstream/local truncation and partial detector failure; focused regressions, complete feature tests and vet; independent review_

- [ ] 12.2 Bound hybrid grouping and positional overlap work
  - Replace repeated exact/discovery group searches with keyed maps and index concrete positional overlap. Preserve safe attribution, deterministic ordering, occurrence deduplication and private value lifetimes.
  - Add many exact-only small fragments with BetterLeaks enabled and no corresponding discovery findings; record benchmark scaling and an allocation regression that rejects the quadratic implementation.
  - _Requirements: 4.5, 5.3, 5.4, 5.5, 8.7, 8.8_
  - _Boundary: hybrid_merge.go and directly related feature tests/benchmarks only_
  - _Validation: strict TDD allocation/semantic controls; focused and complete feature tests, benchmarks and vet; independent review_

- [ ] 12.3 Map validated JSON scalars without per-token decoders
  - Advance scalar spans to their delimiters after the outer decoder validates the first JSON value; preserve UseNumber, first-value consumption, duplicate-key semantics, depth handling, and exact raw-to-semantic occurrence mapping.
  - Add near-2 MiB scalar-dense JSON containing a detectable credential, allocation regressions, and adversarial benchmarks.
  - _Requirements: 4.2, 5.3, 6.3, 6.4, 6.6, 8.7, 8.8_
  - _Boundary: json_occurrences.go and directly related feature tests/benchmarks only_
  - _Validation: strict TDD allocation/semantic controls; focused and complete feature tests, targeted fuzzing, benchmarks and vet; independent review_

## Implementation Notes

- Tasks 12.1–12.3 reopen technical GO for the full-head review findings. Workers and independent reviewers use the maintainer-requested gpt-6.1-sol at medium reasoning. Prior scope, streaming, projection, policy, positional attribution and coverage fixes remain required and must not regress. PR delivery remains held for maintainer review, with merge and auto-merge unauthorized.

- Task 11.2 received independent APPROVED runtime and scoped-CI reviews. Canonical-valid secret-bearing response deltas arrive unchanged before completion with BetterLeaks/action:redact active; delayed-delivery controls, bounded execution, EOF/close and cancellation joining pass. Remote run 37296483084 at b31053f8 passes feature/auth/SDK/composition/runtime/runtime-bundle Linux race, canonical parity and CLI build/help. All executed same-head PR checks pass. Local quality passed; full local certification was interrupted by C: disk exhaustion and Windows-mounted temporary-file permission failures, retained as failed evidence rather than reported green.
- Task 11.1 received independent APPROVED review after canonical tool-result fixtures were corrected and validated before evaluation and after redaction. Exact/BetterLeaks/hybrid × message/item × block/log/redact and excluded-only budget matrices pass, along with the full feature suite and affected runtime/composition/auth consumers. Model tool-call JSON carriers are excluded by tool metadata even under an eligible message role. Linux race and final certification remain in 11.2.
- The maintainer's scope correction authorizes Requirements 4.7 and 10.9 and supersedes earlier broad request-field coverage, including tool definitions and model arguments. Tasks 11.1–11.2 are required before the current branch can claim feature GO. Response streaming remains outside SecretGuard ownership.

- Tasks 10.1–10.4 repair PR 726 review findings. Task 10.1 received independent APPROVED review; subsequent delegation hit the host agent thread limit and used the kiro-review controller fallback. Current native Linux unit, quality, parity, targeted race, build, and CLI smoke pass at 424768c8. The PR remains open for independent focused re-review; technical verification is not merge authorization. See review-remediation.md for negative controls, performance scope, and raw evidence.

- Task 9.5 makes recorded poller job IDs atomic and preserves all three ID assertions and concurrent adoption coverage. Independent current Linux package race, focused repeated race, lint, and vet pass. Final strict broad race passes at daa38a0b; the other four lanes passed at 22d7b25b with unchanged source and configuration.

- Tasks 9.3 and 9.4 preserve the billing certification population and isolate two spool worker fixtures from disk synchronization. Independent package/durability checks, a fresh logged Linux worker race, and final unit/QA gates pass; observation limits and production billing remain unchanged. Original full-suite spool timeout attribution remains unproven.

- Final independent integration review returns GO for implementation-branch certification at daa38a0b: all 73 criteria across 10 requirement sections are covered, no new boundary violation exists, and the bounded performance assessment is accepted. Current unit, configured quality, parity, QA, broad race, build, and CLI smoke pass. Complete applicable race coverage includes the four unchanged lanes at 22d7b25b. Delivery, GitHub CI, merge, and archive verification remain separate.

- Task 9.2 measures a fresh retained Rate goroutine in an isolated process for each depth, disables both automatic GC triggers after fixture setup, and rejects collections or incomplete child evidence. Independent actual-Rate recursion mutations exceed the unchanged 65,536-byte spread bound. Focused integration, runtime controls, targeted race, vet, full maximum-depth billing race, and final QA pass.

- Task 8.6 removes avoidable no-hit text copies: independently measured 2 MiB scans allocate 1,221 B exact-only, 11,048 B BetterLeaks-only, and 17,816 B hybrid. The final performance assessment accepts remaining positive/JSON costs for the bounded opt-in profile; setup allocations are not steady-request costs.

- Task 8.5 removes 54 measured production lines through reviewed feature-owned deduplication; the unchanged architecture convergence ratchet now passes at -800 with accepted-credential and diagnostic-isolation regressions green.

- The original shrinkage failure was branch-local and was repaired in task 8.5 without changing its budget. Final Linux gates use an isolated native clone through UbuntuOld/ciuser, avoiding root execution and Windows worktree metadata.

- Initial request-sized no-hit copies were removed in task 8.6. Benchmark and profile artifacts retain remaining positive-match latency and JSON allocation costs; the accepted performance assessment does not establish a deployment latency SLO.

- Linux race certification uses GCC and Go 1.26.6. Final durable-runtime race passes; historical deadline failures do not replace the final recorded gate evidence.

- Enabled BetterLeaks zero decode depth and zero active rules remain present in diagnostics. Final architecture certification passes the unchanged -800 convergence requirement.

- Hybrid occurrence mapping must follow the canonical UseNumber decoder, first-value consumption, duplicate-key handling, and depth rejection; raw JSON escape syntax is not an additional semantic occurrence.

- BetterLeaks validates optional component references too; isolated rules prune unselected optional references and retain explicitly selected optional components with their pinned behavior. `disable_rules` removes only explicitly selected IDs and rejects removal of a required component while any rule requiring it remains. Dependency updates also require tidying the external-billing and enterprise fixture modules.
