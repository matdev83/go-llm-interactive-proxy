# Implementation Plan

## Execution Constraints

Baseline is current `main` tip `290831e2` (merged #692), code-identical to merged #666 at `6e97e0d9`. All tasks use TDD: first independently constructed failing evidence, then the smallest implementation, then focused regression checks. The money policy is not selectable: unknown intersection and reporting exhaustion cannot waive customer charges.

Task 1.1 is an explicit architecture-footprint gate. Approved spec metadata is not approval to weaken production or test-cost budgets. Core implementation waits for measured capacity or explicit maintainer-approved allowance. No deployment, new service, new dependency, or historical backfill is required.

Parallel markers describe only the named disjoint boundaries. Tasks without `(P)` follow prior work sequentially; the two marked tasks in group 3 may run together after group 2. PostgreSQL certification uses the registered parity topology, not per-test ad hoc database setup.

- [x] 1. Establish footprint and independently pinned compatibility evidence
- [x] 1.1 Establish a permitted production footprint before adding core behavior
  - Measure the core, SDK, and existing billing-growth overlay with the repository harness, retaining the pinned audited-plus-headroom and excess-negative checks.
  - Baseline live core is 143700/143720 and growth overlay 57665/57726; new production files also require pinned manifest rows. Produce a scoped allowance proposal if needed; obtain explicit maintainer authorization, or demonstrate a genuine same-scope simplification that fits all existing total, per-file, and allowlist limits.
  - Only the design's Validation footprint files own approved manifest rows/count/credits/cap, audit history, and exact line-budget updates. Do not lower a threshold, delete unrelated prose, or relocate code merely to evade a cap. Tasks 3.1/3.2/3.3/4.x wait for this gate.
  - Done: the affected production boundaries have a measured admissible footprint and the existing budget checks still reject excess.
  - _Boundary: Validation footprint gate_
  - _Requirements: 6.4_
  - _Validation: go test -run 'TestPhase20RefreshedBudgetHeadroomExact|TestBillingEconomicsGrowthAllowanceLive' ./internal/archtest_
- [x] 1.2 Pin baseline replay and the nonblocking money controls
  - Capture independent pre-feature snapshot/view canonical bytes, content hash, valuation context preimage, and valuation fingerprint fixtures; preserve the six existing #666 pins.
  - Pin the 100/20/30 subset-sibling vector at complete 50 with no error under legacy material and assert baseline known-conflict controls retain their typed errors.
  - Construct and pin the historical branch-root advisory-text case before any v1 bypass is introduced.
  - Done: baseline fixtures pass without using the implementation being tested to generate their expected values, and mutations to identity/typed errors fail the controls.
  - _Boundary: SDK Contracts and Support Assessor characterization integration_
  - _Requirements: 3.1, 3.2, 4.1, 4.2, 4.3, 7.2, 7.5_
  - _Validation: make dev-test PKGS='./pkg/lipsdk/economics/... ./internal/core/billing/...'_

- [x] 2. Establish the frozen public reporting contract
- [x] 2.1 Add immutable reporting-version material and lossless catalog-view conversion
  - Add the optional empty/v1 version to snapshot and view contracts, with clone, canonical body, validation, and content hashing.
  - Reject unsupported versions; retain exact old bytes for omitted version and require new publication identity for changed material.
  - Start with failing round-trip/hash/unknown-version cases and verify the new field cannot silently disappear in view-to-tariff conversion.
  - Done: empty material matches baseline goldens, v1 material hashes differently, and unknown reporting versions are unusable.
  - _Boundary: SDK Contracts_
  - _Requirements: 7.1, 7.2, 7.5_
  - _Depends: 1.1, 1.2_
  - _Validation: make dev-test PKGS='./pkg/lipsdk/economics/...'_
- [x] 2.2 Add canonical advisory context and bounded non-monetary result contracts
  - Define context, pair, incomplete reason, and report value objects; support all four incomplete reasons and published limits.
  - Implement deep clone, validation, orientation, sorting, exact deduplication, nil-report encoding, and omission-compatible valuation JSON.
  - Reject self-pairs, invalid keys, dangling context references, unsupported versions/reasons, and forged oversized external results without introducing any amount field.
  - Done: canonical report bytes are order-independent, invalid input is rejected, and an empty legacy valuation serializes exactly as before.
  - _Boundary: SDK Contracts_
  - _Requirements: 2.3, 5.1, 5.2, 5.3, 5.4, 6.1, 6.2, 6.4, 6.5, 7.2, 7.3, 7.5_
  - _Validation: make dev-test PKGS='./pkg/lipsdk/economics/...'_
- [x] 2.3 Bind source reporting contexts into the existing valuation interpretation identity
  - Append optional context metadata to the existing context preimage and extend the established field-mutation test.
  - Prove different route-source reporting versions/content produce different context hashes even with identical monetary fields.
  - Keep advisory pairs out of context identity but inside the canonical result fingerprint, so report divergence conflicts under one frozen interpretation.
  - Done: legacy IDs/hashes are unchanged; changed source context changes identity; changed report changes only result payload/fingerprint under that identity.
  - _Boundary: SDK Contracts_
  - _Requirements: 6.2, 7.1, 7.2, 7.4, 7.5_
  - _Validation: make dev-test PKGS='./pkg/lipsdk/economics/...'_

- [x] 3. Implement independently owned assessor and catalog integration
- [x] 3.1 (P) Preserve reporting semantics through existing catalog publication
  - Add the catalog-owned atomic card-plus-advisory-tariff publication method for customer default/route binding; tariff-only hosts can use PutTariff or RatingCatalogView. Leave PricingSnapshot and PricingSnapshotToTariff untouched. Copy reporting version through default/route sources and snapshot reconstruction, with independently pinned same-ref/different-content rejection.
  - Test mixed historical and enabled snapshots without automatically upgrading old material or creating a strict policy selector.
  - Prove source views retain schemas, reporting version, and content identity through publication and retrieval.
  - Done: the existing catalog exposes v1 losslessly, old snapshots retain exact content, and conflicting same-version publication fails.
  - _Boundary: Catalog Integration_
  - _Requirements: 7.1, 7.2, 7.5_
  - _Depends: 2.1, 2.2, 2.3_
  - _Validation: make dev-test PKGS='./internal/infra/billingcompose/... ./pkg/lipsdk/economics/...'_
- [x] 3.2 (P) Discover uncertain contributor candidates with deterministic finite work
  - Build the private assessor over the existing compiled support relation and supplied per-scope coverage; do not add another topology or cover authority.
  - Enumerate common-ancestor candidates without materializing a full Cartesian report, with deterministic deduplication and per-group candidate/graph counters.
  - Add budget-aware reachability/relation queries that produce no verdict on exhaustion; never certify separation from partial closures.
  - Cover branch roots, separate trees, resolved and unresolved covers with an independent bounded structural oracle.
  - Done: reference siblings report unknown, proven-separated pairs do not, all limits yield valid incomplete reports, and disconnected populations avoid a global pair sweep.
  - _Boundary: Support Assessor_
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 2.3, 3.4, 5.2, 6.1, 6.2, 6.3, 6.4, 6.5_
  - _Depends: 2.1, 2.2, 2.3_
  - _Validation: make dev-test PKGS='./internal/core/billing/...'_
- [x] 3.3 Integrate assessments after existing monetary decisions without recomputing coverage
  - Hand the existing conflict resolver's per-scope coverage map through its private result; do not perform a third resolution.
  - Select retained positive exact-charge contributors after suppression; test rounding-to-zero positives, minimum charges at zero quantity, free/zero/unpriced lines, intentional additions, and cross-scope isolation.
  - Initialize one context before assessment even for no schema, no contributors, or fixed-only evaluations; P receives no locally inferred advice.
  - For v1 do not execute or join the legacy quantity-positive unknown reporter; preserve quantity contradictions and typed errors, with exact legacy bypass behavior when version is empty.
  - Done: E/Q/R money matches controls; successful complete results expose advice; legacy fingerprints/errors remain exact; missing coverage produces evidence-unavailable advice without withholding charges.
  - _Boundary: Support Assessor integration with existing rating owner_
  - _Requirements: 1.5, 2.1, 2.2, 2.4, 3.1, 3.2, 3.3, 3.4, 4.1, 4.2, 4.3, 4.4, 5.1, 5.3, 5.4, 6.3, 7.1, 7.2_
  - _Depends: 3.1, 3.2_
  - _Validation: make dev-test PKGS='./internal/core/billing/... ./internal/infra/billingcompose/...'_

- [x] 4. Preserve report provenance through complete customer valuation composition
- [x] 4.1 Merge independent source contexts without inventing cross-group pairs
  - Carry contexts and reports through inference grouping, input narrowing, line-ID renaming, fixed/proxy combination, and final base-tariff overwrite.
  - Test two route tariffs with equal amounts/line IDs but different reporting semantics; their interpretation identities must differ without changing the debit.
  - Prove the 1027-context bound from pre-narrowing nonempty observation groups plus the three ancillary groups; retain existing final validation/error precedence and preserve mixed legacy/v1 results.
  - Done: final complete R valuations retain correct source tariff/scope pairs, context mutation changes identity, and no cross-tariff pair is fabricated.
  - _Boundary: Retail Integration_
  - _Requirements: 3.1, 3.2, 3.3, 4.4, 5.1, 5.3, 6.2, 6.3, 7.1, 7.4_
  - _Depends: 3.3_
  - _Validation: make dev-test PKGS='./internal/core/billing/... ./internal/infra/billingcompose/...'_
- [x] 4.2 Enforce deterministic composed report limits and truthful incompleteness
  - Merge at most 128 retained and 128 incoming entries before reducing to the canonical lowest prefix; preserve all incomplete contexts/reasons.
  - Test differently ordered group merges, pair 128/129, duplicated unknown pairs, and contexts whose entries are omitted by the result limit.
  - Show exact-limit complete versus over-limit incomplete outcomes without turning truncation into settlement denial.
  - Done: equivalent permutations produce identical output and every lost reporting context is explicitly marked incomplete.
  - _Boundary: Retail Integration_
  - _Requirements: 2.3, 3.4, 5.2, 6.1, 6.2, 6.4, 6.5_
  - _Validation: make dev-test PKGS='./internal/core/billing/...'_

- [x] 5. Certify durable storage and existing operator retrieval
- [x] 5.1 Preserve canonical reports through existing append, query, and replay paths
  - Use the existing canonical payload transaction; test append/get/list/detail with complete, failing, clean-enabled, and incomplete-assessment results.
  - Verify clone/query consumers preserve optional fields and retrieve stored data without rerating; do not add a new table, sidecar worker, endpoint, or backfill.
  - Test old-row byte-exact re-append, identical enabled replay, and changed advice under the same identity yielding an integrity conflict.
  - Done: operators can inspect stored advice independently of errors, historical rows remain unchanged, and no report overwrite is accepted.
  - _Boundary: Durable Integration_
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 7.2, 7.3, 7.4, 7.5_
  - _Depends: 4.1, 4.2_
  - _Validation: make dev-test PKGS='./internal/infra/billingstore/...'_
- [x] 5.2 Prove advice cannot change settlement and run the shared dual-dialect contract
  - Extend the existing registered billingstore parity cases with report round-trip, idempotency, changed-report conflict, rollback, and existing query exposure.
  - Verify unknown and limited assessments settle complete customer money at the same debit as the equivalent historical tariff; known-conflict controls still refuse complete settlement.
  - Exercise the same contract through SQLite and direct PostgreSQL, using existing hermetic/tagged fixture conventions and avoiding a second expensive matrix.
  - Done: both dialects preserve identical report and financial semantics, including retry and transaction boundaries.
  - _Boundary: Durable Integration with existing settlement owner_
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 4.1, 4.2, 4.4, 5.5, 7.3, 7.4, 7.6_
  - _Validation: make test-db-parity-sqlite; make test-db-parity-postgres-direct_

- [ ] 6. Validate architecture, determinism, and practical cost as one coherent change
- [x] 6.1 Lock non-monetary ownership and bounded reporting regressions
  - Assert no advisory dependency enters stream/admission money seams and no SQL/provider import enters the core assessor.
  - Run the existing agreement, exhaustive/model, and six-fingerprint suites without rewriting historical expected output.
  - Exercise independent oracle prefix tests and deterministic budget counters; remove each key binding/budget guard in scratch mutations and require the corresponding test to fail.
  - Done: architecture gates pass within the approved footprint and reports cannot alter monetary classifications or silently claim completeness from partial graph work.
  - _Boundary: Validation_
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 2.1, 2.2, 2.3, 2.4, 3.1, 3.4, 4.1, 4.2, 4.3, 4.4, 6.1, 6.2, 6.3, 6.4, 6.5, 7.2, 7.4_
  - _Depends: 5.1, 5.2_
  - _Validation: go test ./internal/archtest ./internal/qa; make dev-test PKGS='./internal/core/billing/...'_
- [ ] 6.2 Run final affected-consumer certification without relaxing budgets
  - Run quality, unit, catalog/external-host compatibility, both registered database parity gates, and targeted fuzzing for the new bounded JSON contract.
  - Run Windows make test-cost once on the coherent implementation; report any cost regression without increasing the budget to hide it.
  - Verify reader-first compatibility through current SDK/source consumers and explicitly check old material needs no publication/migration.
  - Done: all required gates pass with direct output evidence and no unresolved report, monetary, replay, or footprint issue remains.
  - _Boundary: Validation integration_
  - _Requirements: 3.1, 3.2, 3.3, 4.1, 4.2, 5.5, 6.4, 6.5, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6_
  - _Validation: make quality-checks; make test-unit; make test-db-parity-sqlite; make test-db-parity-postgres-direct; make test-cost_

## Implementation Notes

- 1.1: Maintainer authorized prospective LOC reserves: 1500 core and 250 catalog lines; assessor paths `component_rater_advisory.go` and `component_rater_advisory_graph.go` allow 450 lines each plus existing headroom. Historical audit pins remain unchanged; SDK economics has no aggregate cap.
- 1.2: Independent literal preimages/hash fixtures and historical diagnostic controls pass; scratch identity/error-tree mutations fail. Baseline billing exceeded the 10-minute runner deadline but passed unfiltered with a 30-minute invocation deadline. Repeated task-local hooks omit existing generated, schema-model sweep/order, metamorphic matrices, and the exhaustive support-agreement parent; its small acceptance/regression populations run separately. Final coherent certification must run all suites unfiltered without changing cost budgets.
- Model-cost guidance: exact task-local exclusion is `^Test(GeneratedSchema(StructureSweep|CommercialSweep|OrderInvariance|TransformIsNotContainment|DirectionUnitIsolation)|SchemaModel(StructureSweep|CommercialSweep|OrderInvariance)|Metamorphic(PricingMetamorphism|StructuralVerdictAgreesWithModel)|SupportAgreementShadowPredicate)$`; retain `TestSupportAgreementShadowPredicate/(acceptance_vectors|regression_schemas)` with a separate unfiltered selector.
- Baseline race repair: `e776283a` synchronizes the existing seam-equivalence test's shared tallies; focused repeated race checks and the retained full-package race gate pass with all 28 cases and parallel execution preserved.
- Baseline child-limit repair: `caa70439` isolates the existing test's package-wide cap override by removing its contradictory parallel marker; repeated focused and retained full-package race checks pass.

- 2.2: Advisory ScopeKey uses a conservative 64 KiB opaque envelope because validated reduction keys embed escaped lineage JSON; canonical context tuples omit publication timestamps while clone preserves them.
- 2.3: Identity mutation controls compare each source field against a valid enabled baseline; independent overlays flattening source fields and restoring pre-task behavior fail the corresponding tests.
- 3.2: The shared relation guard now permits exactly one monetary overlap owner and the named bounded advisory consumer; an independent third-consumer mutation is rejected. The assessor remains unwired until 3.3.
- 4.1: The 1027 bound counts pre-narrowing source groups; fixed scopes share a context and deduplicate. Proxy report-only changes may alter the composite result ID, but unchanged input-set/context identity still triggers the store's integrity conflict; pin this path in durable replay tests.

- Baseline filesystem fixture repair: `ade340bf` separates recovery-file creation from the original filesystem birth-time tick, retaining inode-reuse and in-place rewrite controls. Host `/tmp` exhausted; local verification uses transient `TMPDIR` and `GOTMPDIR` in a private root-filesystem directory without changing caches or persistent configuration.

- 5.1: Existing canonical payload and query decoders preserve all advisory fields without production or SQL changes. Literal historical replay remains byte-exact; report divergence conflicts under both matching and distinct primary IDs sharing one input/context interpretation.

- Main integration: Rebased onto `09f93c10`; the upstream fixed core ceiling of 216000 remains unchanged, with the approved feature path reserves and billing overlay of 59476 retained. Upstream moved exhaustive schema checks to `make test-billing-schema`; final certification must execute its full 12-test population without exclusions. Independent budget review and fresh SDK, metering, core billing, catalog, and store regression checks passed.

- 5.2: One shared registered SQLite/direct PostgreSQL runner certifies stored reports, replay conflicts, rollback/retry, and real-rate settlement at identical 50 USD and 18 USD debits across legacy/v1 publications; known paid containment remains fenced with unchanged financial state. Both catalog gates and scoped lint passed. Fresh reviewer dispatch hit the service thread limit twice; the documented manual review fallback independently reproduced RED and both dialect gates before acceptance.

- Baseline durable fixture repair: `e62588d4` gives the sink-failure restart test the same bounded 20-second test cleanup window as its adjacent real-store fixture; production deadlines remain unchanged. A scratch delayed append reproduced the original missing leg, passed after alignment, and repeated focused plus full runtime race and root hooks passed.

- 6.1: AST boundary checks and independent negative fixtures keep advisory interpretation out of runtime/stream/admission owners and SQL/provider/infra imports out of the assessor. Nine scratch binding/budget mutations trigger runtime failures. Fresh SDK/core controls, architecture/QA, scoped lint, and the complete 12-test schema certification passed (535.443 seconds, no exclusions); historical expected outputs remain unchanged. The service thread limit required the documented manual implementation/review fallback.

- 6.2: Fresh unit, quality, protocol parity, both registered database parity gates, external public-host compile/process smoke, built CLI configuration validation, and bounded JSON fuzzing passed (30 seconds, 353237 executions). The lossy-wire mutation fails the enabled/clean/incomplete seeds. Full Windows cost run `36802600816` at `224ee892` failed twice without overrides: first runtime 71.992s/62.519s allowed; retry failures are recorded above. Overall unit cost metrics and quality cost pass; tagged QA cost passes only the first attempt. All 141 runtime tests added since the cost anchor already exist on main. Diagnostic run `36806082530` compares main `09f93c10` (43.995s/47.144s) against code-identical feature `f0268715` (40.848s/45.642s); all four unfiltered runtime controls pass, but they do not replace whole-suite process-accounted certification. Underlying timing cause remains unestablished; do not claim feature GO or relax budgets. Both attempts and control artifacts are retained. Temporary CI jobs and the owned trigger label are removed; the recurring watchdog remains disabled and unchanged. Manual review fallback records 6.2 as rejected/not verified pending the cost finding.

- 6.2 cost remediation in progress: integrated main #696 and shared dependency/selector scans clear runtime (29.253s), QA (13.506s), and tagged release gates (16.862s) in run `36827411334` without budget overrides. Billing alone exceeds its 15s floor at 15.164s. CPU profiling identifies repeated context-key JSON encoding in the retail merge; cache keys per merge without changing canonical identities, source contexts, reports, or money. A full-size allocation guard fails the old code (20593 allocations versus 8200 allowed) and passes the repair.

- 6.2 QA cost remediation: run `36833220871` clears billing, runtime, release and overall budgets without overrides; QA alone takes 24.241s against its unchanged 15s floor. Reuse real Git fixtures and omit redundant predecessor-policy commits, retaining every workflow script and all 74 test/subtest identities. Track fixture paths once and use the actual `HEAD^` predecessor for each isolated diff; Git launches fall from 311 to 133. Full QA, focused race and lint pass; an accumulated-diff mutation is rejected. Original Windows budgets require fresh certification.

- 6.2 cancellation fixture repair: run `36839199397` records QA 10.387s (original floor 15s), billing 9.002s, runtime 20.962s, and release 14.904s. The complete cost command aborts on an existing OpenResponses fixture that races handler launch against cancellation while asserting no execution. A forced handler-first schedule reproduces the failure; cancel before launch and additionally require zero executor calls, retaining stream and timeout assertions. Fifty repeated lifecycle race runs pass; no production behavior or budget changes. Fresh full cost certification remains required.

- 6.2 pool handoff and QA baseline repair: run `36843803003` aborts on an existing pool test that confuses internal Acquire completion with its caller observing the return; its early failure leaves physical cleanup blocked and contaminates the generation leak gate. A forced delayed caller observation reproduces the false failure. Observe released internal claims after the cleanup-entry signal and always unblock/join fixture workers; an actual retained-claim mutation fails while the following generation leak check passes. One hundred repeated pool/generation race runs and full runtimebundle/QA plus lint pass. Shared immutable Git baseline copies reduce scope fixtures from 133 to 106 launches, retaining all 74 original test identities. No production or original cost policy changes; fresh full certification remains required.
