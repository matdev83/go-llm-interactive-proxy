# Implementation Plan

## Execution Rules

- Execute against implementation-time current main, not the specification baseline blindly. Revalidate the session-preparation order, feature-plane census, large-body proof shape, and featurehost ownership before Task 1.
- Use TDD: create/refresh RED characterization tests before production behavior in each workstream.
- Keep this feature behavior-neutral for existing coding-oriented consumers. Do not gate #637/#467/#468/#469/#475 in this implementation.
- Do not add coding-client branches to generic core/runtime.
- Do not turn the new MetadataOnly plane into a hidden full-Call/content dependency.
- Remote TypeSafe/Jev tests are hermetic by default; live external tests are explicit opt-in only.
- Tasks marked (P) may proceed in parallel after their listed dependencies.

- [ ] 1. Rebaseline current-main invariants and freeze the acceptance matrix
  - [ ] 1.1 Characterize session-authority and stage ordering before production changes
    - Add/refresh tests proving the current order through BeginTurn, A-leg fetch, secret guard, frontend ingress, submit, tool catalog, request shaping, pre-request, and route hint.
    - Characterize SessionView construction/cloning and prove ClientSessionHint is not proxy authority.
    - Record implementation-time paths/symbols in test comments only where useful; preserve semantic order if files moved.
    - _Requirements: 2.1-2.3,4.1-4.7,11.4-11.8_
    - _Boundary: existing runtime/session/execctx tests; no production behavior_
    - _Depends: none_
    - _Validation: focused runtime preparation/order tests remain GREEN before feature edits_

  - [ ] 1.2 (P) Freeze positive and adversarial client/tool evidence fixtures
    - Reuse current codexclientcompat, tool-call-classification, compaction-event-detection, and explicit-completion research fixtures.
    - Add a table describing which harness identities are high-confidence, ambiguous, or unsupported by UA alone at implementation time.
    - Include Codex/Roo stable identity positives, Cline generic-SDK-UA negative, OpenCode/Pi/Droid/Hermes existing root matcher cases, distinctive coding-tool clusters, weaker tool+project-marker cases, and technical-chat negatives.
    - Do not make every named harness positive merely because it was researched; require actual supported evidence.
    - _Requirements: 3.1-3.10,12.1-12.3_
    - _Boundary: feature/shared-facts test fixtures only_
    - _Depends: none_
    - _Validation: one data-driven fixture matrix with explicit expected evidence source/reason_

  - [ ] 1.3 (P) Rebaseline feature-plane and large-body wire eligibility
    - Pin the implementation-time standard plane count/order, request-access classes, generated-plane currency, and static disposition for an occupied MetadataOnly plane.
    - Characterize canonical versus wire proof inputs available for ClientUserAgent/tool names without introducing new behavior yet.
    - Prove the pre-change wire path has no classification evidence and no hidden shadow Call.
    - _Requirements: 5.1-5.7,11.4-11.5,12.7,12.11_
    - _Boundary: pkg/lipsdk/feature, internal/core/largebody, frontendpipe tests_
    - _Depends: none_
    - _Validation: plane census + large-body static/differential characterization tests_

- [ ] 2. Add the bounded SDK classification contract and legal extension plane
  - [ ] 2.1 Define scalar SessionView classification and bounded evidence types
    - RED-test zero-value unknown semantics, positive validation, IsCodingAgent, source/confidence/evidence/revision bounds, and absence of variable content collections.
    - Add session Classification to SessionView and verify all SDK/core view clone/projection helpers preserve it without new aliasing.
    - Add sessionclassification Evidence and fixed ToolCategorySet plus pure tool-name accumulation using lipapi.ClassifyToolName.
    - Keep Call, ToolDef slices, raw header maps, provider DTOs, SQL types, and vendor response types out of the SDK contract.
    - _Requirements: 1.1-1.8,3.9,5.2-5.5,7.3-7.4,11.1-11.5_
    - _Boundary: pkg/lipsdk/session, pkg/lipsdk/sessionclassification, affected view-copy tests_
    - _Depends: 1.1,1.2_
    - _Validation: public SDK unit tests + external compile/architecture guards_

  - [ ] 2.2 Add StageIDSessionClassification and exclusive PlaneSessionClassifier
    - RED-test legal stage order after secret_guard and before submit_request.
    - Declare one exclusive sessionclassification.Classifier plane with RequestBodyMetadataOnly access and bounded diagnostics.
    - Update/generate the closed plane surface, snapshot accessors, descriptors, inventory and generator fixtures; do not hand-edit generated outputs outside the repository generator workflow.
    - Duplicate effective classifier contributors must fail generation composition.
    - _Requirements: 4.1-4.7,5.1,5.6,8.5-8.6,11.4-11.7,12.11_
    - _Boundary: pkg/lipsdk/feature, plane generator/generated outputs, architecture tests_
    - _Depends: 1.3,2.1_
    - _Validation: generator currency + plane diagnostics + stage-order + duplicate-exclusive tests_

  - [ ] 2.3 Add the generic fail-open classification stage runner
    - RED-test no-plane no-op, valid positive projection, invalid classifier output, classifier error, context cancellation, and preservation of an already-positive SessionView value.
    - Implement feature-neutral execution that calls only the SDK Classifier and never interprets client families, modes, persistence, or Jev.
    - Ensure runtime classifier failure cannot reject an otherwise valid inference request.
    - _Requirements: 1.2-1.7,4.3-4.6,7.6,11.4-11.8_
    - _Boundary: internal/core/extensions narrow stage runner + focused tests_
    - _Depends: 2.1,2.2_
    - _Validation: stage unit tests; core package has zero concrete sessionclassification feature imports_

- [ ] 3. Implement shared client identity facts and the deterministic local heuristic
  - [ ] 3.1 (P) Extract a bounded root client-family matcher and migrate compatible brownfield use
    - Create internal/agentfacts as a pure trim/case-fold/exact-prefix/token matcher over stable client identity strings.
    - RED-test high-confidence families and ambiguous generic SDK values; no regex/fuzzy/prompt matching.
    - Reuse it from codexclientcompat agent-string matching where semantics are identical while leaving feature-specific prompt signatures in codexclientcompat.
    - Do not couple the external Codex connector module to root-internal matcher code if that breaks connector isolation; document/characterize that boundary instead.
    - _Requirements: 3.1-3.2,3.10,11.3-11.5,12.1-12.3_
    - _Boundary: new internal/agentfacts + internal/plugins/features/codexclientcompat agent-string paths_
    - _Depends: 1.2_
    - _Validation: shared matcher matrix + unchanged codexclientcompat behavior fixtures_

  - [ ] 3.2 Implement feature-owned config and local heuristic rules
    - Decode canonical plugins.features payload for session-classification; outer Registration.Enabled remains authoritative.
    - Support modes heuristic, jev, hybrid with heuristic as omitted-mode default; define bounded ignored_user_agent_prefixes and remote config shape.
    - RED-test Rule A known client identity; Rule B read/search + edit/remove + OS-command cluster; Rule C weaker read/search + mutation plus recognized project marker.
    - RED-test filename/code-fence/language/model/one-tool/web/generic-SDK-UA negatives and exclusions.
    - Keep recognized project/build markers bounded and ignore generic .git as decisive corroboration.
    - _Requirements: 3.1-3.8,6.1-6.4,8.1-8.4,11.1-11.3_
    - _Boundary: internal/plugins/features/sessionclassification config/heuristic package_
    - _Depends: 2.1,3.1_
    - _Validation: config table tests + local policy acceptance matrix_

  - [ ] 3.3 Harden evidence bounds and prove zero transcript/path retention
    - Add property/fuzz tests for oversized UA/exclusion/marker values, odd casing, unicode/control input already rejected by identity capture, tool-name explosions, and unknown tool aliases.
    - Assert feature state/evaluation structs contain no messages, prompt strings, tool arguments, raw paths, or raw header maps.
    - Prove weak evidence never accumulates into an unbounded per-session history.
    - _Requirements: 3.5-3.10,7.1-7.5,10.2,10.5,11.5_
    - _Boundary: sessionclassification/agentfacts tests and architecture reflection guards_
    - _Depends: 3.2_
    - _Validation: fuzz/property tests + no-content architecture assertions_

- [ ] 4. Build monotonic feature state, durable persistence, and remote leases
  - [ ] 4.1 (P) Define the authoritative key/state contract and bounded memory store
    - RED-test secure SessionID preference, A-leg fallback, rejection of empty authority, and refusal to use ClientSessionHint as a key.
    - Implement Record, Promote, ClaimRemote, CompleteRemote semantics with unknown -> coding_agent as the only V1 classification transition.
    - First accepted positive wins; repeats are idempotent; later weak/negative proposals cannot rewrite it.
    - Implement memory store with bounded entry capacity/idle cleanup and no unrelated-session long-held lock.
    - _Requirements: 2.1-2.6,2.9,6.5-6.11,7.3-7.4,10.3-10.7_
    - _Boundary: internal/plugins/features/sessionclassification state contract + featurehost sessionclassification memory adapter_
    - _Depends: 2.1_
    - _Validation: deterministic fake-clock concurrency/store contract tests_

  - [ ] 4.2 Implement Bun Load/Promote persistence and restart restoration
    - RED-test SQLite reopen before production SQL.
    - Add the feature-owned logical table with proxy-authority key, scalar classification, remote-control metadata and timestamps only.
    - Implement indexed Load and atomic compare-and-promote; no raw UA/path/prompt/vendor payload columns.
    - Use featurehost-borrowed Bun DB ownership and never close the shared DB from the feature store.
    - _Requirements: 2.1-2.10,7.3-7.4,8.7-8.8,10.6,12.6,12.10_
    - _Boundary: internal/standardplugins/featurehost/sessionclassification Bun store/schema_
    - _Depends: 4.1_
    - _Validation: SQLite store contract + reopen/restart tests_

  - [ ] 4.3 Implement atomic remote claim/lease parity on SQLite and PostgreSQL
    - RED-test two concurrent claimers, abandoned lease expiry, finite attempts, backoff, positive classification racing remote completion, and stale lease token rejection.
    - Keep HTTP/network work outside transactions.
    - Register the store in dbparity if required by the current repository persistence catalog and prove equivalent logical schema/behavior on direct PostgreSQL.
    - _Requirements: 2.5,6.5-6.11,10.3-10.6,12.5,12.8,12.10_
    - _Boundary: featurehost sessionclassification Bun store + dbparity contract_
    - _Depends: 4.2_
    - _Validation: memory/SQLite/PostgreSQL shared store contract; race-safe claim tests_

  - [ ] 4.4 Add the process cache/coordinator
    - Cache stable positive classifications so later turns require no DB/network work.
    - Coalesce concurrent first loads/promotions for one key while allowing unrelated keys to proceed independently.
    - For durable unknown sessions, preserve authoritative visibility across replicas rather than hiding a remote promotion behind an unbounded negative cache.
    - Enforce capacity/idle eviction; no per-session goroutine/timer.
    - _Requirements: 2.5-2.7,6.5-6.11,10.1-10.7_
    - _Boundary: internal/standardplugins/featurehost/sessionclassification coordinator/cache_
    - _Depends: 4.1,4.3_
    - _Validation: fake-clock cache capacity/eviction + concurrent load/promotion/claim tests + race suite_

- [ ] 5. Compose the standard feature across process and immutable generations
  - [ ] 5.1 Wire the lightweight process holder and enabled-generation store lifecycle
    - Construct only a lightweight StateHolder/coordinator shell at process featurehost startup; do not ensure a classification table or contact a remote classifier while the feature has never been enabled.
    - For enabled generations, add an overlap-safe feature lifecycle whose Start initializes/ensures the memory or Bun store once through the shared holder before publication; Stop must not destroy shared state used by overlapping generations.
    - Ensure candidate failure publishes no plane/classification records and final process Close owns holder/cache disposal exactly once.
    - Register feature metrics collector through the generic metrics registry without exposing concrete feature types to runtimebundle.
    - _Requirements: 1.1,2.7-2.10,8.5-8.8,9.1-9.6,10.5-10.8,11.4-11.7_
    - _Boundary: internal/standardplugins/featurehost process ownership + sessionclassification child + ordinary feature lifecycle_
    - _Depends: 4.4_
    - _Validation: never-enabled no-schema/no-network test; lifecycle overlap/rollback/constructor-count/close ownership tests; memory-vs-Bun composition tests_

  - [ ] 5.2 Bind one generation classifier from feature registration/config
    - Register the session-classification standard feature factory.
    - Decode/validate mode-specific config at candidate generation, build the concrete feature classifier with the process coordinator and optional remote adapter, and contribute it through PlaneSessionClassifier.
    - Prove absent/disabled feature publishes no classifier plane.
    - Prove invalid candidate or duplicate classifier leaves last-good generation/process state untouched.
    - _Requirements: 1.1,6.1-6.4,8.1-8.8,11.7_
    - _Boundary: internal/plugins/features/sessionclassification bundle/config + internal/standardplugins feature registration/featurehost generation_
    - _Depends: 2.2,3.2,5.1_
    - _Validation: standard bundle/config/reload/candidate rollback tests_

  - [ ] 5.3 Add bounded feature observability
    - Implement evaluation/transition/remote/store metrics with closed enum labels only.
    - Emit one transition observation for first positive promotion and bounded outcome diagnostics for unknown/excluded/remote failures.
    - Prove no session ID, A-leg, UA, path, prompt or arbitrary remote value can become a metric label/log payload.
    - _Requirements: 7.5,9.1-9.6,10.8_
    - _Boundary: sessionclassification featurehost collector/observer + metrics registration tests_
    - _Depends: 4.4,5.1_
    - _Validation: collector gather tests + label-cardinality/redaction assertions_

- [ ] 6. Integrate same-turn classification into canonical execution
  - [ ] 6.1 Build canonical Evidence and run classification after secret guard
    - RED-test exact ordering: BeginTurn/A-leg -> secret guard -> session classification -> frontend ingress/request authority -> submit.
    - Derive Evidence from accepted Invocation.ClientUserAgent, Operation and tool-name category bits only; do not scan Messages/Items/Instructions.
    - Pass bound SessionView and WorkspaceView; update ibt.preSession only with validated classification result.
    - Runtime/store/remote classifier errors preserve the request and prior positive state.
    - _Requirements: 1.1-1.7,4.1-4.7,7.1-7.7,11.1-11.8_
    - _Boundary: internal/core/runtime secure preparation + generic extension runner_
    - _Depends: 2.3,3.2,5.2_
    - _Validation: focused canonical preparation tests with spy classifier/order barriers_

  - [ ] 6.2 Propagate classification through all later same-turn SessionView consumers
    - Prove submit/tool-catalog/request/pre-request/route-hint and later execctx views receive the classification without independent store/classifier calls.
    - Update view-copy/context/policy projection helpers only where required by the new scalar field.
    - Add a fake opt-in consumer test proving first-turn positive gating is possible while leaving real existing consumers behavior-neutral.
    - _Requirements: 1.6,1.8,4.2,9.5,11.7,12.6_
    - _Boundary: runtime meta construction + pkg/lipsdk/core view projection tests_
    - _Depends: 6.1_
    - _Validation: same-turn metadata matrix; classifier invocation count exactly once per admitted client turn_

  - [ ] 6.3 Prove auxiliary and failure isolation
    - Ensure detached proxy-owned auxiliary calls do not create classification rows under their private child A-legs.
    - Cover canceled classification context, state failure, remote failure and invalid result without changing routing/retry/output commitment semantics.
    - Prove no post-commit replay/failover rule changes.
    - _Requirements: 4.4-4.6,7.6-7.7,11.7-11.8_
    - _Boundary: detached/aux/runtime failure tests_
    - _Depends: 6.1_
    - _Validation: detached child state-count assertions + recovery/commit regression suite_

- [ ] 7. Preserve classification semantics on the large-payload wire path
  - [ ] 7.1 Add bounded classification evidence to large-body proof/session facts
    - RED-test proof validation/aggregate-fact accounting before adding fields.
    - Carry sessionclassification.Evidence only; no raw header map/tool list/Call.
    - Extend no-smuggling/reflection tests and semantic-fact bounds.
    - _Requirements: 5.2-5.5,7.1-7.4,12.7_
    - _Boundary: internal/core/largebody Proof/WireSessionFacts + SDK evidence type_
    - _Depends: 1.3,2.1_
    - _Validation: proof Validate/AggregateFactBytes + no-shadow-Call/no-unbounded-field tests_

  - [ ] 7.2 (P) Compile canonical-equivalent evidence in certified frontend profiles
    - Reuse the same User-Agent acceptance policy through the frontend identity helper.
    - While profiles already scan/validate tool definitions, feed names into the shared fixed-bit accumulator without retaining ToolDef lists in proof.
    - Cover OpenAI Responses, OpenAI Chat and OpenResponses certified lanes that expose tools/UA.
    - RED-test field order, absent UA, invalid UA, unknown tools and large streamed bodies.
    - _Requirements: 3.9,5.2-5.5,11.1-11.2,12.7_
    - _Boundary: certified frontend profile proof compilers + frontendpipe differential fixtures_
    - _Depends: 7.1_
    - _Validation: canonical Evidence versus wire Proof Evidence bit-for-bit differential tests_

  - [ ] 7.3 Execute the metadata-only classifier after wire authority binding
    - Insert the same generic classification stage into ExecuteLargeBody after secure-session/A-leg/workspace binding and before route/request consumers.
    - Use proof Evidence, never reconstruct lipapi.Call.
    - Unknown/error continues the one committed wire execution; no post-commit canonical fallback.
    - Extend the fixed plane census/count/order for PlaneSessionClassifier and prove occupied MetadataOnly classifier does not set a canonical static blocker.
    - _Requirements: 4.1-4.7,5.1-5.7,10.1-10.2,12.7,12.11_
    - _Boundary: internal/core/runtime ExecuteLargeBody + internal/core/largebody eligibility/authority gates_
    - _Depends: 2.2,2.3,5.2,7.2_
    - _Validation: real wire executor classification tests + static disposition + canonical/wire outcome parity_

- [ ] 8. Implement the optional Jev adapter behind the remote-decision contract
  - [ ] 8.1 Define and test the provider-neutral RemoteDecider boundary
    - Keep RemoteInput derived/content-free: normalized client family/ambiguity, tool bits, workspace class, operation and local evidence outcome.
    - Define bounded RemoteDecision probability/confidence fields used by feature threshold policy.
    - Validate remote mode requires explicit provider, credential reference, timeout, attempt/lease/backoff values and positive threshold within safe finite bounds.
    - _Requirements: 6.1-6.4,6.7-6.10,7.1-7.7,8.3-8.4_
    - _Boundary: internal/plugins/features/sessionclassification remote contract/config_
    - _Depends: 3.2,4.4_
    - _Validation: config/DTO bounds tests; architecture assertion that vendor DTOs cannot cross this boundary_

  - [ ] 8.2 Implement the current TypeSafe/Jev HTTP adapter without widening SDK contracts
    - Reconfirm current early-access TypeSafe documentation immediately before coding; map current endpoint/auth/request/response into RemoteDecider.
    - Use strict request/response size bounds, timeout/cancellation, safe redirect/origin behavior and credential redaction.
    - Never send raw UA, headers, prompt/transcript, reasoning, tool args, paths, resume tokens, provider credentials or secret findings.
    - Keep vendor-specific types under the featurehost-owned adapter package.
    - _Requirements: 6.6-6.11,7.1-7.5,11.5,12.8-12.9_
    - _Boundary: internal/standardplugins/featurehost/sessionclassification Jev adapter_
    - _Depends: 8.1_
    - _Validation: httptest success/429/5xx/timeout/redirect/malformed/oversize/cancel/redaction matrix_

  - [ ] 8.3 Integrate heuristic/jev/hybrid decision flow with durable leases
    - heuristic never constructs/calls RemoteDecider.
    - jev constructs local evidence but requires remote positive result to promote.
    - hybrid promotes on decisive local evidence first and claims remote only while still unknown.
    - Claim before network, finish after network, never hold store transaction/lock across HTTP.
    - Prove positive cached sessions issue zero remote calls and concurrent ambiguous turns have one active lease in the store scope.
    - _Requirements: 6.1-6.11,10.1,10.3-10.7,12.5,12.8_
    - _Boundary: concrete sessionclassification classifier + state coordinator/remote adapter_
    - _Depends: 5.2,8.2_
    - _Validation: fake decider call-count + concurrent/multi-store lease acceptance suite_

- [ ] 9. Certify persistence, reload, concurrency, and performance behavior
  - [ ] 9.1 Prove durable resume and generation reload semantics
    - New session -> promote -> close/reopen durable store -> resume authoritative SessionID -> classification available before downstream consumer.
    - Reload heuristic/remote rules without erasing process state; disable withdraws plane without deleting durable row; re-enable restores.
    - Verify in-flight request remains bound to old generation policy.
    - _Requirements: 2.4-2.10,8.5-8.8,12.6_
    - _Boundary: featurehost/runtimebundle composed tests with SQLite and generation reload_
    - _Depends: 5.2,6.2,7.3_
    - _Validation: restart/reload composed acceptance tests_

  - [ ] 9.2 (P) Prove authority isolation and concurrent promotion
    - Same ClientSessionHint under different authoritative sessions/A-legs must never share rows/cache.
    - Concurrent local and remote positives converge to one stored coding_agent revision without source/evidence flapping.
    - Route/failover/compaction/backend switches preserve the same authoritative classification.
    - _Requirements: 2.1-2.6,2.9,12.4-12.6_
    - _Boundary: store/coordinator + runtime lineage tests_
    - _Depends: 4.3,6.1_
    - _Validation: concurrency barriers + race detector where supported_

  - [ ] 9.3 (P) Add hot-path allocation/latency and store-call ratchets
    - Warm coding_agent benchmark asserts zero remote calls, zero DB writes and zero transcript scan; count durable store calls explicitly.
    - Unknown local path benchmark covers bounded UA/tool/marker evaluation.
    - Remote benchmark/fake latency proves no lock/transaction held during decision I/O and unrelated session progress.
    - Verify classifier-disabled path performs zero classifier/store/remote work.
    - _Requirements: 10.1-10.8,5.1,11.7_
    - _Boundary: feature/runtime benchmarks and instrumented store/decider fakes_
    - _Depends: 4.4,6.1,7.3,8.3_
    - _Validation: focused benchmarks + allocation/store-call assertions + make test-cost if harness cost changes_

- [ ] 10. Run the cross-harness and false-positive acceptance matrix
  - [ ] 10.1 Certify supported coding-agent positives
    - Run the evidence matrix from Task 1.2 through canonical and, where applicable, wire paths.
    - Cover stable identity positives and tool-structure positives independently so generic/absent UA coding harnesses remain classifiable.
    - Record evidence source per fixture; do not invent identity for indistinguishable harnesses.
    - _Requirements: 3.1-3.4,12.1,12.3,12.7_
    - _Boundary: feature/runtime/testkit acceptance fixtures_
    - _Depends: 6.2,7.3,8.3_
    - _Validation: data-driven cross-harness acceptance suite_

  - [ ] 10.2 Certify technical-chat and ambiguous-client negatives
    - Cover programming prose, source filenames, code fences, one shell/web/read tool, model names, generic browser/API SDK UAs, ignored identity prefixes and weak tool clusters without project marker.
    - Prove unknown stays generic behavior and creates no durable negative classification.
    - _Requirements: 1.2,3.2,3.4-3.8,6.8-6.9,12.2-12.3_
    - _Boundary: feature/runtime negative fixtures_
    - _Depends: 6.2,7.3,8.3_
    - _Validation: false-positive matrix; persisted kind remains unknown/absent_

  - [ ] 10.3 Prove consumer neutrality and opt-in capability
    - Use a test-only downstream consumer to gate on SessionView.Classification and prove first-turn visibility.
    - Assert existing bundled coding-oriented features have no new dependency/gate and preserve pre-feature behavior when #645 lands.
    - _Requirements: 1.6-1.8,4.2,11.7_
    - _Boundary: integration/architecture tests only; no production consumer migrations_
    - _Depends: 6.2_
    - _Validation: bundled feature behavior characterization + test-only opt-in consumer_

- [ ] 11. Add operator documentation and safe diagnostics guidance
  - [ ] 11.1 Document classification semantics, evidence, configuration and privacy
    - Add operator docs for unknown/coding_agent monotonic semantics, authoritative keying, local rules, Jev modes, fail-open behavior, durable/non-durable posture, reload behavior and large-body compatibility.
    - Provide canonical plugins.features examples for heuristic and explicit remote modes; do not publish real credentials.
    - State clearly that classification is not authorization and that existing features do not become gated automatically.
    - _Requirements: 1.1-1.8,6.1-6.11,7.1-7.7,8.1-8.8,11.7_
    - _Boundary: docs/session-classification.md plus config/example documentation_
    - _Depends: 5.2,8.3_
    - _Validation: documentation/config example parsing tests where supported_

  - [ ] 11.2 Document observability and future consumer contract
    - Describe bounded metrics/diagnostics and evidence-code semantics without raw identity/path examples that encourage high-cardinality logging.
    - Document how a future feature checks SessionView.Classification.IsCodingAgent rather than reimplementing detection.
    - Link #456 as a future explanation consumer without making it a prerequisite.
    - _Requirements: 1.6,9.1-9.6,11.3-11.7_
    - _Boundary: operator/plugin authoring docs_
    - _Depends: 5.3,6.2_
    - _Validation: doc review against actual metric/config names_

- [ ] 12. Run final architecture, parity, and release-grade certification
  - [ ] 12.1 Run focused feature/platform gates and generated-surface checks
    - Run feature/session/runtime/frontends/largebody focused suites, generator checks, architecture guards, fuzz/property targets and parity checks.
    - Prove the plane count/order/access manifest and generated outputs are current.
    - Re-run grep/arch assertions for no concrete coding-client or TypeSafe branching in generic core.
    - _Requirements: 5.6,11.4-11.8,12.1-12.9,12.11_
    - _Boundary: repository-wide certification only_
    - _Depends: 9.1,9.2,9.3,10.1,10.2,10.3,11.1,11.2_
    - _Validation: make quality-checks; make parity-checks; focused fuzz targets_

  - [ ] 12.2 Run persistence/concurrency/test-cost gates
    - Run make test-db-parity including direct PostgreSQL mandatory semantics when the gate requires it.
    - Run applicable race tests for store/coordinator/remote lease paths.
    - Run make test-cost before accepting any material default-suite cost increase; do not raise budgets as a normal escape hatch.
    - Record environment-gated failures truthfully and reproduce baseline attribution where necessary.
    - _Requirements: 10.3-10.8,12.5,12.8-12.10,12.12_
    - _Boundary: repository QA/persistence/concurrency gates_
    - _Depends: 12.1_
    - _Validation: make test-db-parity; make test-race where supported; make test-cost_

  - [ ] 12.3 Run wide QA and close out the SDD truthfully
    - Run make qa and any domain-specific Windows/large-body checks required by implementation-time main.
    - Verify no implementation task added consumer gating outside #645 scope.
    - Update spec metadata/tasks only after all implementation acceptance evidence passes; archive the SDD according to repository convention after merged-main closeout.
    - _Requirements: 1-12_
    - _Boundary: final merged-main certification/spec lifecycle_
    - _Depends: 12.2_
    - _Validation: make qa plus merged-main focused rerun; no unchecked applicable tasks before archive_
