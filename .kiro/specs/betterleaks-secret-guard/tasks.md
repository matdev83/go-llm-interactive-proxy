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

- [ ] 2. Build the private BetterLeaks generation adapter
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

  - [ ] 2.3 Configure rule selection and frozen policy identity
    - Apply validated disable/isolate selectors to the pinned in-memory default configuration, preserving required component closure for isolated multipart rules.
    - Capture BetterLeaks version, config hash, active rule count, confidence, decode depth, and worker count as safe generation facts.
    - Add a review ratchet so an upstream dependency/rule change cannot alter the default resolved policy unnoticed.
    - _Requirements: 7.5, 7.6, 7.7, 7.9, 9.1, 9.2, 9.3_
    - _Boundary: BetterLeaks scanner builder / policy ratchet_
    - _Depends: 2.1_
    - _Validation: deterministic config hash/rule inventory goldens_

- [ ] 3. Add context-preserving logical-fragment discovery
  - [ ] 3.1 Define and RED-test the logical fragment traversal
    - Characterize current canonical locations and JSON raw representations used by secret guard.
    - Produce one bounded fragment per logical text/JSON unit without concatenating the complete request or exposing fragments outside the feature.
    - Prove contextual JSON detection remains possible for generic key/value rules and tool schema/result content.
    - Share the existing request-level byte budget instead of creating a second independent scan allowance.
    - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.6, 10.3, 10.4_
    - _Boundary: secret-guard canonical traversal_
    - _Depends: 2.1_
    - _Validation: traversal tables + JSON context fixtures + scan-limit regression_

  - [ ] 3.2 Implement error-returning BetterLeaks fragment scans
    - Use the pinned SDK's error-returning scan path with request context/cancellation and a tiny in-memory fragment source where required.
    - Reuse the generation scanner concurrently; do not construct scanners per fragment/request.
    - Enforce the 256 projected-finding request cap and return bounded failure classification when exceeded.
    - _Requirements: 4.5, 8.1, 8.2, 8.4, 8.5, 8.6, 8.7_
    - _Boundary: BetterLeaks adapter request path_
    - _Depends: 3.1_
    - _Validation: cancellation/error/cap/concurrency tests_

- [ ] 4. Project findings safely and merge hybrid detector results
  - [ ] 4.1 Extend safe finding provenance without leaking BetterLeaks internals
    - Add only bounded detector/rule/confidence metadata needed by audit and diagnostics.
    - Keep raw matches, captures, components, context, fingerprints, and fragments private.
    - Validate/bound projected strings and preserve compatibility for existing exact findings.
    - _Requirements: 2.5, 5.1, 5.2, 5.5, 5.6, 9.4_
    - _Boundary: secretguard SDK safe metadata + feature projector_
    - _Depends: 3.2_
    - _Validation: serialization/leak tests + existing SDK consumers_

  - [ ] 4.2 Implement private deduplication and deterministic merge
    - Deduplicate exact and BetterLeaks reports using private concrete occurrence/span/value identity before public projection.
    - Preserve meaningful exact source/reference attribution and attach BetterLeaks provenance without double-counting one occurrence.
    - Sort merged findings deterministically independent of worker scheduling.
    - _Requirements: 4.5, 5.3, 5.4, 5.5_
    - _Boundary: feature-private hybrid merger_
    - _Depends: 4.1_
    - _Validation: overlapping/repeated/randomized-order fixtures_

- [ ] 5. Preserve exact protection and wire hybrid composition by access mode
  - [ ] 5.1 Restore/retain single-user exact local protection behind its new switch
    - Preserve existing include/exclude/popular environment inventory, minimum-secret-length, known-prefix, and mask behavior when enabled.
    - Ensure disabling local discovery performs no environment catalog enumeration and does not disable BetterLeaks.
    - _Requirements: 1.3, 1.6, 2.1, 2.3_
    - _Boundary: exact local source/composition_
    - _Depends: 1.2, 4.2_
    - _Validation: existing catalog characterization + switch matrix_

  - [ ] 5.2 (P) Preserve shared request-credential exact protection
    - Keep the current accepted request credential private in ingress context and exactly detectable regardless of BetterLeaks state/rule coverage.
    - Prove no process environment is consulted in multi-user composition or request execution.
    - _Requirements: 1.4, 2.2, 2.4, 2.5, 10.2_
    - _Boundary: stdhttp auth matcher + multi-user resolver_
    - _Depends: 1.2, 4.1_
    - _Validation: arbitrary opaque request-credential tests with panic environment_

  - [ ] 5.3 Integrate hybrid detector services into the frozen generation plane
    - Compose exact and BetterLeaks detector capabilities without moving BetterLeaks imports into generic runtime or core.
    - Preserve disabled behavior, action/audit configuration, immutable generation semantics, and existing request stage ordering.
    - Expose bounded detector posture through the existing diagnostics inventory path.
    - _Requirements: 1.1, 1.7, 1.8, 3.2, 9.1, 10.1, 10.8_
    - _Boundary: standard featurehost secret-guard composition / frozen plane_
    - _Depends: 2.3, 5.1, 5.2_
    - _Validation: generation compose/reload/inventory tests_

- [ ] 6. Route BetterLeaks discoveries through existing enforcement and redaction
  - [ ] 6.1 RED-test action semantics for BetterLeaks findings
    - Cover block before dispatch/quarantine, log without mutation, literal redact, decoded/unrewritable redact, scan error, finding-cap error, and existing scan-limit behavior.
    - Assert block/log never mutate input and failed redaction never partially commits the working clone.
    - _Requirements: 6.1, 6.2, 6.5, 6.6, 6.7, 8.4, 8.6_
    - _Boundary: secret-guard Guard action tests_
    - _Depends: 5.3_
    - _Validation: focused Guard/runtime no-dispatch tests_

  - [ ] 6.2 Implement the transient exact rewrite bridge
    - Extract only literal rewrite candidates that are provably present in the original fragment.
    - Feed those candidates into existing byte-length-preserving text and parsed-JSON mutation semantics.
    - Re-run canonical validation after mutation and retain existing unsupported-token fail-closed behavior.
    - Zero/release transient secret bytes where practical after rewrite planning/execution.
    - _Requirements: 6.3, 6.4, 6.6_
    - _Boundary: feature-private rewrite bridge + existing rewriter_
    - _Depends: 6.1_
    - _Validation: text/JSON redaction differential tests_

  - [ ] 6.3 Implement decoded/unrewritable fail-closed behavior
    - Map non-literal BetterLeaks positives to safe findings while withholding unsafe rewrite candidates.
    - Block under redact with bounded `unrewritable_detected_secret`; preserve normal block/log semantics.
    - Prove no encoded secret is reported as redacted unless its original representation was actually sanitized.
    - _Requirements: 6.5, 6.7_
    - _Boundary: BetterLeaks discovery-to-enforcement bridge_
    - _Depends: 6.2_
    - _Validation: depth-1 encoded fixtures under all actions_

- [ ] 7. Harden observability and failure privacy
  - [ ] 7.1 Add bounded detector diagnostics and audit provenance
    - Project detector posture and BetterLeaks policy facts into existing secret-guard diagnostics.
    - Add detector/rule/confidence only where bounded audit consumers need explanation; keep metrics low-cardinality.
    - Sanitize scanner/construction errors before generic logging/client mapping.
    - _Requirements: 5.6, 9.1, 9.2, 9.4, 9.6_
    - _Boundary: diagnostics/audit/metrics_
    - _Depends: 4.1, 5.3_
    - _Validation: inventory and structured audit tests_

  - [ ] 7.2 (P) Add end-to-end anti-secret observability canaries
    - Feed unique synthetic credentials through exact, BetterLeaks, overlapping, decoded, scanner-error, and redaction-failure paths.
    - Capture ordinary logs, structured audit, metrics text/labels, diagnostics, errors, and decision DTOs and assert the raw canary/fingerprint/context never appears.
    - _Requirements: 5.1, 5.2, 5.5, 9.4, 9.5_
    - _Boundary: secret-guard leak regression suite_
    - _Depends: 4.1, 6.3_
    - _Validation: canary absence assertions across all observability sinks_

- [ ] 8. Certify protocol parity, concurrency, fuzz safety, and performance
  - [ ] 8.1 Build the synthetic detector corpus and frontend parity matrix
    - Cover OpenAI, Anthropic, GitHub, Slack, Stripe, AWS multipart, generic API key/password/credential URI, private key, public/non-secret negatives, JSON key context, tool schemas/results, repeated overlaps, allow markers, and decoded forms.
    - Run equivalent canonical payload cases through every bundled frontend flavor that can represent them.
    - Use synthetic credentials only and avoid printing fixture values in test failures.
    - _Requirements: 10.3, 10.4_
    - _Boundary: testkit + frontend/secret-guard integration tests_
    - _Depends: 6.3_
    - _Validation: parity matrix_

  - [ ] 8.2 (P) Add race and fuzz/adversarial certification
    - Race one shared generation scanner under realistic concurrent request scans.
    - Fuzz logical fragment mapping, malformed JSON, exact/discovery overlaps, redaction invariants, high finding counts, cancellation, and observability sanitization.
    - Assert no per-request unbounded goroutine growth and stable deterministic finding ordering.
    - _Requirements: 8.2, 8.6, 8.7, 10.5_
    - _Boundary: race/fuzz test suites_
    - _Depends: 6.3_
    - _Validation: targeted race + fuzz smoke + adversarial unit tests_

  - [ ] 8.3 Benchmark BetterLeaks-only and hybrid hot-path cost
    - Benchmark 1 KiB/10 KiB/100 KiB/1 MiB/2 MiB no-hit and positive cases, JSON, generic-heavy adversarial input, and realistic concurrency.
    - Compare exact-only, BetterLeaks-only, and hybrid latency/allocations; record p50/p95/p99 where the benchmark harness supports it.
    - Record scanner build/precompile cost, goroutine behavior, binary size, and dependency-size delta.
    - Treat a material regression as a design/performance review trigger rather than hiding it with weaker detector defaults.
    - _Requirements: 8.8, 10.6_
    - _Boundary: secret-guard benchmarks / build-size evidence_
    - _Depends: 8.1, 8.2_
    - _Validation: reproducible benchmark report attached to implementation PR_

- [ ] 9. Run final cross-boundary security certification
  - [ ] 9.1 Verify all architecture, config, security, and regression gates together
    - Re-run zero-env-read multi-user tests with BetterLeaks on/off, local-discovery invalid/absent, reload failures, and normal request traffic.
    - Re-run allow-marker, no-network, no-CLI, no-raw-finding, scan-failure, decoded-redact, quarantine/no-dispatch, deterministic merge, and default-policy-hash ratchets.
    - Run the repository's applicable quality, unit, race, parity, and QA gates for a wide security-sensitive feature change.
    - Confirm no implementation task changed routing/failover/B2BUA/billing/protocol semantics outside the specified secret-guard boundary.
    - _Requirements: 3.3, 3.4, 3.6, 6.7, 9.3, 9.5, 10.1, 10.2, 10.7, 10.8_
    - _Boundary: whole-feature certification_
    - _Depends: 7.1, 7.2, 8.3_
    - _Validation: make test-unit; make quality-checks; applicable parity/qa/race gates_
