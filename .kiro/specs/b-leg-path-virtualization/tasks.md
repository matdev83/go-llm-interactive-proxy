# Implementation Plan

## Execution Rules

- Follow TDD: characterization/RED first for every runtime/SDK semantic change.
- Do not add feature-specific imports or branches under `internal/core`.
- Do not broaden scope into dynamic multi-root discovery, prose rewriting, or generic compression.
- Keep all observability path-content-free.
- When a task changes an upstream contract, update generated plane/architecture parity artifacts in the same task if required.
- Suggested parallel work is marked `(P)` only when it can proceed after its stated dependencies.

- [ ] 1. Characterize current tool-call assembly and request ordering
- [x] 1.1 Add RED characterization for mandatory finalizer bypass above 64 KiB
  - Construct a completed tool call with arguments larger than the current default finalization cap and a test finalizer that declares expansion as required.
  - Prove current behavior would pass original fragments and therefore violate Requirement 4.5/4.6.
  - Do not change production behavior in this sub-task.
  - Observable completion: focused test fails for the intended current-behavior reason.
  - _Requirements: 4.5, 4.6, 8.4_
  - _Boundary: tests / SDK-runtime characterization_
  - _Depends: none_
  - _Validation: `go test -count=1 ./internal/core/runtime/...` focused test_

- [x] 1.2 Add RED ordering characterization for path expansion before tool policy
  - Use a test finalizer and tool policy/reactor observer to prove the desired observer path is real/expanded before policy evaluation.
  - Include streaming fragments split through the virtual alias and JSON tokens.
  - Observable completion: test captures current metadata/ordering gap without raw-delta rewriting.
  - _Requirements: 4.1, 4.2, 5.1_
  - _Boundary: tests / core runtime_
  - _Depends: none_
  - _Validation: focused `internal/core/runtime` tests_

- [x] 1.3 Add RED backend-bound ordering characterization
  - Prove attempt transforms precede request-part hooks and that PTB/`Backend.Open` occurs after request-part hooks and conversation-view reassertion.
  - Build a fixture where an early path rewrite would be detectable if lost later.
  - Observable completion: stable test defines the two-pass invariant without coupling to private call-graph trivia beyond the semantic checkpoints.
  - _Requirements: 5.2, 5.3, 5.4_
  - _Boundary: tests / core runtime_
  - _Depends: none_
  - _Validation: focused executor runtime tests_

- [ ] 2. Implement the pure cross-platform path virtualization kernel (P)
- [x] 2.1 Implement host-independent path flavor parsing
  - Add POSIX, Windows drive, UNC, extended drive, and extended UNC recognition.
  - Reject relative paths, malformed roots, and Windows device paths.
  - Do not use host `filepath.Clean/Rel` as authority for foreign path flavors.
  - Observable completion: full table passes on every host OS.
  - _Requirements: 1.1, 1.2, 1.3, 1.6, 1.7, 1.8, 1.9_
  - _Boundary: feature plugin / domain policy_
  - _Depends: none_
  - _Validation: `go test -count=1 ./internal/plugins/features/pathvirtualization/...`_

- [x] 2.2 Implement deterministic workspace-bound alias derivation and inverse prefix mapping
  - Define the exact canonical root identity: flavor retained; POSIX case-sensitive with non-root trailing separator removal; Windows separator-normalized, ASCII-case-folded, non-volume trailing separator removal; no filesystem/dot-segment normalization.
  - Derive `workspaceTag = lower(base32-no-padding(SHA-256("lip:path-virtualization:v1\x00" + flavorID + "\x00" + canonicalRootIdentity)[0:12]))`, yielding exactly 20 characters / 96 bits.
  - Fixed aliases: POSIX `/.__lip_v1__/w_<tag>/`; drive `<UPPERCASE-DRIVE>:\.__lip_v1__\w_<tag>\`; UNC `\\.__lip_v1__\w_<tag>\`; extended drive `\\?\<UPPERCASE-DRIVE>:\.__lip_v1__\w_<tag>\`; extended UNC `\\?\UNC\.__lip_v1__\w_<tag>\`.
  - Require the complete alias to be shorter than the real root.
  - Implement Windows case-insensitive/separator-aware matching and POSIX case-sensitive matching.
  - Preserve original real-root spelling on expansion.
  - Add deterministic-tag, idempotence, segment-boundary, and round-trip tests.
  - Observable completion: same root/flavor always derives the same alias across process recreation and supported root changes derive different tags in fixtures.
  - _Requirements: 1.4, 1.5, 1.6, 1.7, 1.10, 6.1, 6.2, 9.1_
  - _Boundary: feature plugin / domain policy_
  - _Depends: 2.1_
  - _Validation: feature unit + fuzz tests_

- [x] 2.3 Add reserved-namespace and stale-workspace handling
  - Treat `.__lip_v1__` and its path-flavor forms as fixed V1 syntax, not operator configuration.
  - Parse a recognized reserved alias before ordinary prefix matching; validate the exact 20-character workspace tag plus flavor/drive form.
  - If a selected reserved alias tag does not equal the tag derived from the current authoritative root, return bounded `workspace_mismatch` and never expand it against the current root.
  - Detect malformed reserved aliases and real workspace roots that collide with the reserved alias namespace.
  - Add the critical regression: derive alias under root A, switch authoritative root to B, present A's alias, and assert rejection/no expansion to B.
  - Return bounded skip/reject reasons; never guess.
  - _Requirements: 1.8, 1.10, 1.11, 4.4, 6.5_
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
  - Derive the current workspace-bound mapping from `meta.Workspace.ProjectRoot`.
  - Resolve selectors against exact tool name/tool schema.
  - Recognize fixed V1 reserved alias forms before ordinary path matching.
  - Expand only selected aliases whose workspace tag/flavor matches the current mapping.
  - Reject malformed reserved aliases and selected aliases carrying a stale/different workspace tag.
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
  - Do not expose an alias-marker/version override in V1; `.__lip_v1__` and workspace-tag encoding are fixed compatibility contracts.
  - Validate bounds, path keys, exact tool names, JSON Pointers, duplicate/conflicting profiles.
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
- [ ] 10.1 Add restart/reload, stale-workspace, and provider-continuation characterization
  - Prove the same root derives the same fixed-V1 workspace tag/alias without stored mapping after feature object/process recreation.
  - Cover provider-side continuation shape (`PreviousResponseID`) with consistent alias derivation.
  - Derive alias A under root A, change the authoritative root to B, then inject a model tool call containing alias A and prove it is rejected as `workspace_mismatch`, never expanded to root B.
  - Cover same-drive Windows root changes, different-drive changes, POSIX changes, and at least one UNC/extended-path stale-alias case.
  - Assert that root B derives a different tag in the fixtures and that no mutable prior-root dictionary is consulted.
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
  - Unresolved or stale-workspace inbound alias fails closed.
  - Canonical validation holds after all rewrites.
  - _Requirements: 8.1, 8.2, 8.3, 8.5_
  - _Boundary: feature/runtime tests_
  - _Depends: 8.2, 9.2_
  - _Validation: focused feature + runtime tests_

- [ ] 11. Measure performance and realized savings (P)
- [ ] 11.1 Add microbenchmarks and representative fixtures
  - Benchmark path parsing/mapping including workspace-tag derivation, selector-guided argument mutation, idempotent second pass, and completed-call expansion.
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
  - Confirm no selected virtual alias can reach client tool execution on overflow/error, and no old workspace tag can expand against a changed `ProjectRoot`.
  - Confirm PTB/backend history is virtualized after all late shaping.
  - Confirm retry/failover/continuation alias stability.
  - Confirm all logs/metrics are path-content-free.
  - _Requirements: 1.9, 2.3, 4.4, 4.5, 5.2, 5.6, 6.1, 7.7, 8.3_
  - _Boundary: cross-artifact implementation review_
  - _Depends: 12.1_
  - _Validation: code review + targeted regression reruns_

## Implementation Notes

- Task 1.1: the RED test sizes its oversized fixture from the mutable assembler field `maxArgsBytes`; Task 7.2 should derive it from `defaultToolCallFinalizationMaxArgsBytes` so raising the effective mandatory bound cannot turn the fixture into a legitimate overflow-reject case.
- Task 1.1: `golangci-lint` is unavailable in this environment, so `make dev-lint` / `make quality-checks` cannot run locally; `gofmt -l` and `go vet` are the available static signals.
- Task 1.1: `internal/core/runtime/tool_call_mandatory_buffering_red_test.go` fails intentionally until Task 7.2 lands; coarse gates run before then will report this failure.
- Task 1.2: `orderingFinalizerProjectRoot` reaches the authoritative root via a reflection probe on `toolcall.Meta` field names `Workspace`/`ProjectRoot`; Tasks 6.1/8.2 must replace it with direct `meta.Workspace.ProjectRoot` access rather than relaxing the assertions.
- Task 1.2: the tool policy observer plane already receives the authoritative workspace view via `applyToolPolicies` (`response_pipeline_observations.go`); only the finalization metadata plane is blind, so no policy-plane change is needed for 5.1.
- Task 1.2: the test-local stand-in finalizer pairs `ActionRewrite` with `ReasonValidPassThrough`; Task 8.1's real finalizer should use a semantically correct reason code.
- Task 1.3: the backend-bound two-pass ordering (attempt transform -> candidate eligibility -> request-part hook -> conversation-view reassertion -> PTB -> `Backend.Open`) already holds in the current runtime, so the delivered `path_virtualization_two_pass_ordering_characterization_test.go` is an intentionally GREEN permanent regression guard, not a pending RED condition. A first review round rejected an earlier RED attempt whose failures came only from anchoring a conversation-view overlay on the very message the rewriter mutates.
- Task 1.3: conversation-view anchor identity is content-derived (`conversationprojection.MessageIdentityOf` hashes message content), so once this feature virtualizes a path-bearing tool-call message, a client steering overlay anchored on that message's pre-virtualization identity can no longer resolve and the executor denies the turn pre-backend (`ErrAnchorMissing` -> `AnchorFailClosed` -> `CommandPreBackendDenial`). This is a real functional gap, but it belongs to conversation-view anchor semantics which `design.md` "Out of Boundary" excludes and no task in 1.1-12.2 owns. Raise it as a separate spec item (suggested as a Requirement 6 continuity sidecar of Task 5.2); do not work around it inside this spec.
- Task 1.3: `EligibilityResolver.Check` is invoked twice per attempt (preliminary candidate evaluation, then `post_request_hooks` rederivation); only the preliminary invocation measures requirement 5.3 candidate sizing.
- Task 2.1: the new package exposes `ClassifyPath`/`ParsedPath{Flavor, Root, Rest}` plus four bounded `SkipReason` codes instead of the design's `Mapping`/`DeriveMapping`, because `WorkspaceTag`/`VirtualRoot` belong to Task 2.2. Task 2.2 should define `Mapping` over `ClassifyPath` (reuse `Flavor` and `RealRoot = Root+Rest`, reuse the `SkipReason` type) rather than re-implementing lexical parsing.
- Task 2.1: separator ownership is the main 2.2 hazard. For drive flavors the volume-boundary separator lives at the FRONT of `Rest` (`C:\Users` parses as `Root="C:"`, `Rest="\Users"`), which `Root+Rest == input` conceals. Task 2.2's `canonicalRootIdentity` must strip non-volume trailing separators from the concatenated form, never from `Root` alone.
- Task 2.1: `PathFlavor` constant order matches `design.md` 118-123 exactly and must not be renumbered; the SHA-256 tag algorithm depends on `flavorID` being stable.
- Task 2.1: `TestClassifyPathRejectionReasonsAreBounded` asserts `len(seen) == len(known)`; Task 2.3 must widen that `known` map when it adds `workspace_mismatch`/`malformed_reserved_alias`.
- Task 2.1: forward-slash-spelled Windows volumes (`//?/C:/x`, `//server/share`) classify as `FlavorPOSIX` because the design defines POSIX as "leading `/`". Design-literal, but 2.2/2.3 should know forward-slash Windows volumes take the POSIX branch.
- Task 2.1: `TestFlavorParsingHasNoHostAuthority` AST-scans the package's non-test sources and rejects `os`, `path`, `path/filepath`, `runtime`, `syscall`, and any repo-package import; keep it intact. Note `internal/plugins/features/...` is not exercised on the CI windows/macos runners today, so multi-OS evidence is host-independent constants plus `GOOS=windows`/`GOOS=darwin` compile+vet, not execution.
- Task 2.2: `flavorID` is serialized into the tag digest as DECIMAL DIGITS. This is an observable frozen V1 interop contract; changing it would change every derived tag.
- Task 2.2: activation requires `len(alias) < len(RealRoot)` AND `len(alias) < len(matchableRoot)`, both strict. The second strict comparison is what keeps requirement 9.1 ("replacement strictly shorter than the matched prefix") true for roots spelled with trailing-separator runs; a `<=` would leave a zero- or one-byte rewrite active.
- Task 2.2: `canonicalRootIdentity` treats a separator as volume-owned ONLY when the spelled volume alone is not a valid absolute root per Task 2.1, i.e. for `C:` and `\\?\C:` but NOT for `\\server\share` or `\\?\UNC\server\share`. So `\\srv\share` and `\\srv\share\` derive the same tag. The tag contract is frozen; changing this later would strand aliases a provider has already seen.
- Task 2.2: expansion is byte-exact except two documented separator-count-only shapes, both design-mandated: a path equal to the bare real root gains the alias's own trailing separator (the alias spelling is frozen and always ends with one), and a real root itself spelled with a trailing separator keeps its own spelling. No client byte is ever dropped.
- Task 2.2: an empty `VirtualRoot` with `SkipReasonNone` means "usable root whose alias is not beneficial". Tasks 4.3/9.3 must translate that state into a bounded reason for requirement 7.6 skip accounting; requirement 1.8's enumeration does not cover it.
- Task 2.2: `ExpandPath` returns `ExpandResultNotApplicable` for an alias it did not derive. Task 2.3 must REPLACE that fallthrough with reserved-alias pre-parsing, not merely add result codes — otherwise a stale alias would be passed through to the client.
- Task 2.2: reserved-namespace collision is still open and pinned by `TestReservedNamespaceCollisionStaysOwnedByTaskTwoThree`; Task 2.3 owns the detection.
- Task 2.3: reserved-alias recognition must NOT depend on the marker being the first segment below the path's OWN classified flavor. A first review round found that `/C:/.__lip_v1__/w_<tag>/...`, `//?/C:/...`, `//?/UNC/...` and `/?/C:/...` all classify as `FlavorPOSIX` (a supported absolute form) and were returned to the caller unchanged, breaking the fail-closed guarantee of requirements 4.4/6.5 and design step 6. Recognition now scans complete Windows volume spellings (`C:`, `?\C:`, `?\UNC`) independently of the classified flavor.
- Task 2.3: reserved recognition is deliberately separator-AGNOSTIC (a `\` splits segments on POSIX too), unlike ordinary prefix matching which keeps POSIX `/`-only semantics. Splitting on `/` only was measured to fail 74 tests. Requirement 1.7 is unaffected because ordinary matching is unchanged.
- Task 2.3: a real POSIX root shaped exactly `/C:/.__lip_v1__/...` (first directory literally named `C:` with a direct child named `.__lip_v1__`) is REJECTED as a reserved namespace rather than expanded. This is the required fail-closed trade; one level deeper (`/C:/projects/.__lip_v1__/...`) derives normally.
- Task 2.3: PRE-EXISTING 2.2 quirk, fail-closed, tracked for later tasks. A real root spelled with a DOUBLE or TRIPLE trailing separator round-trips lossily: `VirtualizePath` then `ExpandPath` gains one boundary byte per round trip (design.md 152 mandates the real root's own spelling). A single trailing separator round-trips byte-exactly for all five flavors. A real path always reaches the client, never an alias.
- Task 2.3: PRE-EXISTING 2.2 quirk, fail-closed, tracked for later tasks. `matchableRoot` trims a trailing `\` from a POSIX root even though POSIX treats `\` as an ordinary file-name byte (requirement 1.7), so `/home/.../proxy\` restores to `/home/.../proxy\sub/main.go`, which `stripRootPrefix` can no longer match. Consider trimming only `/` for POSIX in a later task.
- Task 2.3: `ExpandPath`'s ordinary-matcher success branch is a defensive guard for a FUTURE alias spelling this build cannot recognize (verified unreachable for every V1 spelling). It is exercised by `TestExpandPathResolvesAnAliasSpellingThisBuildCannotRecognize`; do not read its coverage as proof that ordinary matching wins for any V1 alias.
