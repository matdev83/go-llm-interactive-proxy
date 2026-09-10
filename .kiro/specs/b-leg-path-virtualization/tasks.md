# Implementation Plan

## Execution Rules

- Follow TDD: characterization/RED first for every runtime/SDK semantic change.
- Do not add feature-specific imports or branches under `internal/core`.
- Do not broaden scope into dynamic multi-root discovery, prose rewriting, or generic compression.
- Keep all observability path-content-free.
- When a task changes an upstream contract, update generated plane/architecture parity artifacts in the same task if required.
- Suggested parallel work is marked `(P)` only when it can proceed after its stated dependencies.

- [ ] 1. Characterize current tool-call assembly and request ordering
- [ ] 1.1 Add RED characterization for mandatory finalizer bypass above 64 KiB
  - Construct a completed tool call with arguments larger than the current default finalization cap and a test finalizer that declares expansion as required.
  - Prove current behavior would pass original fragments and therefore violate Requirement 4.5/4.6.
  - Do not change production behavior in this sub-task.
  - Observable completion: focused test fails for the intended current-behavior reason.
  - _Requirements: 4.5, 4.6, 8.4_
  - _Boundary: tests / SDK-runtime characterization_
  - _Depends: none_
  - _Validation: `go test -count=1 ./internal/core/runtime/...` focused test_

- [ ] 1.2 Add RED ordering characterization for path expansion before tool policy
  - Use a test finalizer and tool policy/reactor observer to prove the desired observer path is real/expanded before policy evaluation.
  - Include streaming fragments split through the virtual alias and JSON tokens.
  - Observable completion: test captures current metadata/ordering gap without raw-delta rewriting.
  - _Requirements: 4.1, 4.2, 5.1_
  - _Boundary: tests / core runtime_
  - _Depends: none_
  - _Validation: focused `internal/core/runtime` tests_

- [ ] 1.3 Add RED backend-bound ordering characterization
  - Prove attempt transforms precede request-part hooks and that PTB/`Backend.Open` occurs after request-part hooks and conversation-view reassertion.
  - Build a fixture where an early path rewrite would be detectable if lost later.
  - Observable completion: stable test defines the two-pass invariant without coupling to private call-graph trivia beyond the semantic checkpoints.
  - _Requirements: 5.2, 5.3, 5.4_
  - _Boundary: tests / core runtime_
  - _Depends: none_
  - _Validation: focused executor runtime tests_

- [ ] 2. Implement the pure cross-platform path virtualization kernel (P)
- [ ] 2.1 Implement host-independent path flavor parsing
  - Add POSIX, Windows drive, UNC, extended drive, and extended UNC recognition.
  - Reject relative paths, malformed roots, and Windows device paths.
  - Do not use host `filepath.Clean/Rel` as authority for foreign path flavors.
  - Observable completion: full table passes on every host OS.
  - _Requirements: 1.1, 1.2, 1.3, 1.6, 1.7, 1.8, 1.9_
  - _Boundary: feature plugin / domain policy_
  - _Depends: none_
  - _Validation: `go test -count=1 ./internal/plugins/features/pathvirtualization/...`_

- [ ] 2.2 Implement deterministic alias derivation and inverse prefix mapping
  - Defaults: POSIX `/.__lip_v1__/0/`; Windows drive `<drive>:\.__lip_v1__\0\`; UNC/extended model alias `\\.__lip_v1__\0\`.
  - Require alias shorter than real root.
  - Implement Windows case-insensitive/separator-aware matching and POSIX case-sensitive matching.
  - Preserve original root spelling on expansion.
  - Add idempotence and segment-boundary tests.
  - Observable completion: round-trip property tests pass for supported path families.
  - _Requirements: 1.4, 1.5, 1.6, 1.7, 6.1, 6.2, 9.1_
  - _Boundary: feature plugin / domain policy_
  - _Depends: 2.1_
  - _Validation: feature unit + fuzz tests_

- [ ] 2.3 Add collision and reserved-marker handling
  - Validate marker as one bounded segment.
  - Detect workspace roots/observed selected paths that already occupy the reserved alias namespace.
  - Return bounded skip/reject reasons; never guess.
  - _Requirements: 1.8, 4.4, 7.5_
  - _Boundary: feature plugin / domain policy_
  - _Depends: 2.2_
  - _Validation: feature unit tests_

- [ ] 3. Implement path-bearing selector/profile policy (P)
- [ ] 3.1 Implement validated JSON Pointer selectors
  - Parse/canonicalize explicit argument and structured-result pointers at config compile time.
  - Support string and array-of-string leaves only.
  - Bound profile count, pointer count, pointer depth, and path-key count.
  - _Requirements: 3.1, 3.2, 3.5, 7.4, 7.5, 7.9_
  - _Boundary: feature plugin / domain policy_
  - _Depends: none_
  - _Validation: feature selector/config tests_

- [ ] 3.2 Implement conservative schema-assisted selector inference
  - Use actual `ToolDef.Parameters`.
  - Infer only bounded declared properties matching the path-key vocabulary with string/array-of-string shape.
  - Enforce payload denylist and do not infer through arbitrary `additionalProperties`.
  - Unknown/ambiguous schema means skip.
  - _Requirements: 3.3, 3.4, 3.5, 3.8_
  - _Boundary: feature plugin / domain policy_
  - _Depends: 3.1_
  - _Validation: table tests against representative tool schemas_

- [ ] 3.3 Implement exact-name built-in and operator profile precedence
  - No substring/prefix tool-name matching.
  - Operator exact profiles override/extend built-ins deterministically.
  - Keep opaque result rewrite disabled unless profile explicitly enables a bounded mode.
  - _Requirements: 2.5, 2.6, 3.6, 3.7, 3.8_
  - _Boundary: feature plugin / domain policy_
  - _Depends: 3.1_
  - _Validation: feature profile precedence tests_

- [ ] 4. Implement canonical outbound virtualization
- [ ] 4.1 Build one pure call rewriter for both canonical authorities
  - Handle item-authoritative `ToolCallItem.Arguments` / structured `ToolResultItem`.
  - Handle legacy tool-call `PartJSON` and `PartToolResult` representations.
  - Rewrite selected path values only; never recursively scan arbitrary strings.
  - Preserve IDs, names, ordering, unrelated values, and validation.
  - Return content-free rewrite statistics.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.8, 2.9, 3.1, 3.2_
  - _Boundary: feature plugin / app orchestration_
  - _Depends: 2.2, 3.3_
  - _Validation: feature canonical rewrite tests_

- [ ] 4.2 Implement conservative result handling
  - Structured JSON results may use explicit selectors.
  - Opaque result strings/text stay unchanged by default.
  - If a profile enables path-oriented opaque handling, implement only the specified bounded token/line recognizer and test source/content false positives.
  - Do not enable broad `read_file`/grep/source-content replacement.
  - _Requirements: 2.2, 2.5, 2.6, 3.8_
  - _Boundary: feature plugin / domain policy_
  - _Depends: 4.1_
  - _Validation: false-positive regression suite_

- [ ] 4.3 Add audit mode accounting
  - Run selector/mapping logic without mutation.
  - Count eligible occurrences and estimated bytes before/after/saved.
  - Ensure audit and rewrite share detection logic to avoid measurement drift.
  - _Requirements: 7.2, 7.3, 7.6, 9.5_
  - _Boundary: feature plugin_
  - _Depends: 4.1_
  - _Validation: audit-vs-rewrite parity tests_

- [ ] 5. Wire the two outbound feature passes
- [ ] 5.1 Add candidate attempt transform
  - Derive mapping from `AttemptMeta.Workspace.ProjectRoot`.
  - Run the pure call rewriter in audit/rewrite mode.
  - Fail open on unexpected outbound transformation errors before alias exposure.
  - Do not change route identity or candidate choice directly.
  - _Requirements: 5.3, 5.5, 5.6, 5.7, 8.2_
  - _Boundary: feature plugin / request attempt transform_
  - _Depends: 4.3_
  - _Validation: feature + candidate transform tests_

- [ ] 5.2 Add idempotent request-part hook
  - Reapply the same rewriter at the existing late request-part stage.
  - Confirm already-virtual paths fast-skip.
  - Add runtime regression proving final conversation-view reassertion and candidate adaptation preserve virtualized tool surfaces through PTB/`Backend.Open`.
  - _Requirements: 5.2, 5.4, 8.5_
  - _Boundary: feature plugin + core runtime tests_
  - _Depends: 5.1, 1.3_
  - _Validation: feature tests + focused executor PTB/backend tests_

- [ ] 6. Enrich complete tool-call finalizer context
- [ ] 6.1 Add read-only scope/session/workspace fields to `toolcall.Meta`
  - Match the authoritative view semantics already used by `hooks.ToolMeta`.
  - Preserve source compatibility for existing finalizers.
  - Add clone/isolation tests for maps/slices if metadata crosses ownership boundaries.
  - _Requirements: 4.1, 5.1, 6.1_
  - _Boundary: SDK/public contract_
  - _Depends: 1.2_
  - _Validation: `go test -count=1 ./pkg/lipsdk/toolcall/... ./internal/core/runtime/...`_

- [ ] 6.2 Populate enriched finalizer metadata from request facts
  - Plumb the existing authoritative request scope/session/workspace views into finalization.
  - Do not read client metadata directly and do not introduce a service locator.
  - _Requirements: 4.1, 5.1, 5.7_
  - _Boundary: core runtime / generic orchestration_
  - _Depends: 6.1_
  - _Validation: focused runtime metadata propagation tests_

- [ ] 7. Make required path expansion impossible to bypass
- [ ] 7.1 Introduce optional generic finalizer buffering/completeness capability
  - Do not add required methods to existing `toolcall.Finalizer`.
  - Define a generic optional contract equivalent to `BufferingRequirement{MaxArgsBytes, OverflowPolicy}`.
  - Preserve current behavior for finalizers that do not opt in.
  - Keep the contract feature-neutral; core must not import/path-check the concrete path feature.
  - _Requirements: 4.5, 4.6, 8.4_
  - _Boundary: SDK/public contract_
  - _Depends: 1.1_
  - _Validation: SDK contract tests_

- [ ] 7.2 Update assembler buffering semantics
  - Compute an effective bounded assembly requirement that can satisfy mandatory finalizers without globally raising unrelated finalizer policy limits.
  - Cap at `lipapi.MaxEventDeltaBytes`.
  - On `OverflowReject` for an applicable mandatory finalizer, return a typed rejection before any alias-bearing argument is released.
  - Preserve legacy pass-through for non-mandatory finalizers and calls with no applicable mandatory requirement.
  - Remove the RED condition from Task 1.1.
  - _Requirements: 4.2, 4.5, 4.6, 8.3, 8.4_
  - _Boundary: core runtime / generic tool assembly_
  - _Depends: 7.1_
  - _Validation: focused assembler tests including 64 KiB and >64 KiB cases_

- [ ] 7.3 Prove tool-call-repair compatibility
  - Characterize malformed JSON repaired before path expansion.
  - Ensure tool-call-repair may decline large calls without causing mandatory expansion to be skipped.
  - Ensure invalid finalizer rewrites retain existing validation/error semantics.
  - _Requirements: 8.4, 8.5_
  - _Boundary: feature integration tests_
  - _Depends: 7.2, 6.2_
  - _Validation: `go test -count=1 ./internal/plugins/features/toolcallrepair/... ./internal/core/runtime/...` focused composition_

- [ ] 8. Implement model→client path expansion
- [ ] 8.1 Add path-expansion finalizer
  - Derive mapping from `meta.Workspace.ProjectRoot`.
  - Resolve selectors against exact tool name/tool schema.
  - Expand only selected virtual-root-prefixed values.
  - Reject selected unresolved reserved aliases.
  - Preserve non-selected fields and JSON validity.
  - Declare mandatory buffering with default 1 MiB and configurable bounded value.
  - _Requirements: 4.1, 4.3, 4.4, 4.7, 4.8, 4.9_
  - _Boundary: feature plugin / tool-call finalizer_
  - _Depends: 2.3, 3.3, 6.2, 7.2_
  - _Validation: feature finalizer tests_

- [ ] 8.2 Prove expansion-before-policy and no alias release
  - Convert Task 1.2 RED test to green.
  - Add a policy/reactor observer that sees the full real path.
  - Add unresolved-alias and mandatory-overflow cases proving no client event contains the reserved alias in selected path fields.
  - _Requirements: 4.1, 4.4, 4.5, 5.1, 8.3_
  - _Boundary: core runtime + feature integration tests_
  - _Depends: 8.1_
  - _Validation: focused runtime stream tests_

- [ ] 9. Add typed feature configuration, registration, and diagnostics
- [ ] 9.1 Implement config decode/validation
  - Disabled by default.
  - Strict `audit|rewrite` mode.
  - Validate marker, bounds, path keys, exact tool names, JSON Pointers, duplicate/conflicting profiles.
  - No regex configuration in V1.
  - _Requirements: 7.1, 7.2, 7.4, 7.5_
  - _Boundary: feature plugin / config_
  - _Depends: 2.3, 3.3_
  - _Validation: feature config tests_

- [ ] 9.2 Build complete `FeatureBundle` and standard registration
  - Contribute attempt transform, request-part hook, and tool-call finalizer through existing planes.
  - Contribute any new generic plane only if Task 7 proves it necessary.
  - Register by existing standard feature conventions; no `internal/core` concrete feature import.
  - Update generated feature-plane/parity artifacts if applicable.
  - _Requirements: 5.8, 7.1, 8.1_
  - _Boundary: feature plugin + config/wiring_
  - _Depends: 5.2, 8.1, 9.1_
  - _Validation: feature bundle + standard registry + architecture tests_

- [ ] 9.3 Add content-free metrics/inventory projection
  - Expose enablement/mode and bounded profile counts.
  - Add rewrite/skip/reject/savings observations without paths, suffixes, IDs, or hashes.
  - Reuse existing metrics composition patterns and bounded cardinality.
  - _Requirements: 7.6, 7.7, 7.8, 9.5_
  - _Boundary: feature plugin + observability composition_
  - _Depends: 9.2_
  - _Validation: metrics/inventory tests_

- [ ] 10. Certify continuity, protocol neutrality, and failure behavior (P)
- [ ] 10.1 Add restart/reload and provider-continuation characterization
  - Prove same root+config derives the same alias without stored mapping.
  - Simulate a later turn after feature object/process recreation.
  - Cover provider-side continuation shape (`PreviousResponseID`) with consistent alias derivation.
  - Cover workspace-root change as an explicit discontinuity/no-old-root inference.
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6_
  - _Boundary: feature/runtime tests_
  - _Depends: 8.2, 9.2_
  - _Validation: focused continuation/reload tests_

- [ ] 10.2 Add canonical-family compatibility tests
  - Legacy chat history with tool call + tool result.
  - Item-authoritative/OpenResponses history.
  - One additional protocol-family sentinel if needed to prove adapters carry the canonical mutation unchanged.
  - Do not create frontend×backend Cartesian coverage.
  - _Requirements: 2.8, 5.8, 8.6, 8.7_
  - _Boundary: protocol/canonical tests_
  - _Depends: 9.2_
  - _Validation: relevant frontend/backend family tests + `make parity-checks` if touched_

- [ ] 10.3 Add disabled/fail-open/fail-closed regression matrix
  - Disabled feature is byte/semantic neutral.
  - Unsupported/malformed root skips outbound mutation.
  - Unexpected outbound transform error before alias exposure preserves real path.
  - Unresolved inbound alias fails closed.
  - Canonical validation holds after all rewrites.
  - _Requirements: 8.1, 8.2, 8.3, 8.5_
  - _Boundary: feature/runtime tests_
  - _Depends: 8.2, 9.2_
  - _Validation: focused feature + runtime tests_

- [ ] 11. Measure performance and realized savings (P)
- [ ] 11.1 Add microbenchmarks and representative fixtures
  - Benchmark path parsing/mapping, selector-guided argument mutation, idempotent second pass, and completed-call expansion.
  - Include long Windows worktree path and long POSIX monorepo/worktree path.
  - Include 10/100/1000 occurrence request fixtures.
  - _Requirements: 9.2, 9.3, 9.4, 9.6_
  - _Boundary: tests / performance_
  - _Depends: 4.1, 8.1_
  - _Validation: targeted `go test -bench` commands_

- [ ] 11.2 Add audit-mode savings assertions
  - Verify bytes-saved equals the same detector's rewrite-mode delta.
  - Verify no negative savings are applied.
  - Verify metrics remain content-free.
  - _Requirements: 7.3, 7.6, 7.7, 9.1, 9.5_
  - _Boundary: feature tests / observability_
  - _Depends: 9.3_
  - _Validation: feature audit/metrics tests_

- [ ] 12. Final integration and release-readiness review
- [ ] 12.1 Run focused architecture and quality gates
  - Run feature package tests, SDK/toolcall tests, focused runtime tests, architecture guards, and formatting/static checks.
  - Run `make quality-checks` and the smallest complete applicable parity suite.
  - Run `make test-cost` only if test/QA infrastructure cost changed materially.
  - Attribute unrelated baseline failures rather than broadening scope.
  - _Requirements: 5.5, 8.6, 9.7_
  - _Boundary: tests / repository QA_
  - _Depends: 10.1, 10.2, 10.3, 11.1, 11.2_
  - _Validation: `make quality-checks`; focused tests; applicable parity gate_

- [ ] 12.2 Perform final implementation review against SDD invariants
  - Confirm no concrete path feature import exists in core/runtime generic packages.
  - Confirm no broad substring replacement of opaque source/content exists.
  - Confirm no primary mutable mapping store was introduced.
  - Confirm no selected virtual alias can reach client tool execution on overflow/error.
  - Confirm PTB/backend history is virtualized after all late shaping.
  - Confirm retry/failover/continuation alias stability.
  - Confirm all logs/metrics are path-content-free.
  - _Requirements: 1.9, 2.3, 4.4, 4.5, 5.2, 5.6, 6.1, 7.7, 8.3_
  - _Boundary: cross-artifact implementation review_
  - _Depends: 12.1_
  - _Validation: code review + targeted regression reruns_
