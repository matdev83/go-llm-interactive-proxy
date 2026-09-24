# Parent Phase 16 independent tenth-pass review

## Review Verdict

- VERDICT: APPROVED
- TASK: Parent 16, 16.1, 16.2 and 16.3.
- SUMMARY: The ninth-pass no-loader recovery gap is closed; fresh source review and affected-suite verification found no remaining Important correctness, security or idempotency finding in Phase 16.
- REMEDIATION: None required for Phase 16 acceptance. The test-strength suggestions and verification limitations below remain explicit.

Parent tasks 16/16.1–16.3 may be checked and Phase 17 may begin. This is task acceptance, not release certification or approval to skip Phase 17 migration/cutover work. Only this report was edited by the reviewer; no production, test, task/status, commit, merge or PR changes were made.

Scope: approved parent requirements/design/tasks and normative refinement; actual tracked diff and untracked Phase 16 files; constructor/recovery composition; durable output, dependency and head identity; earlier findings retained below; query/coverage/allocation readers; protected routes; evidence sanitization, retention and metrics. The worktree is `feat/b-leg-usage-economics`. CodeGraph reported stale changed files and did not resolve several untracked Phase 16 files; current source was read directly for those paths. The inventory contains 119 dirty/untracked Go files, within the user's 1,000-file authorization. The hardcoded 100-file QA ratchet is nonblocking.

## Ninth-pass finding resolved

`internal/core/billing/economic_revision_worker.go:69` is the common constructor for all four exported constructors. At `:84`, a non-nil provider poster on the provider queue, together with a processed-result probe, requires `EconomicRevisionValuationLoader`; otherwise construction returns an error wrapping `ErrInvalidEconomicRevision` before persistence or posting. Adding a reconciler does not bypass the guard. Nil-poster workers and customer-queue workers remain compatible with probe-only result stores. A result store without a probe takes the normal rating/append path, never the processed-result recovery branch; a conflicting rerated immutable result fails at append before posting.

Recovery at `economic_revision_worker.go:300` loads the exact persisted valuation, verifies its ID/version, observation hash, full allocation-aware hash and declared derivation, and passes that full valuation to provider posting. The defensive no-loader provider branch refuses to reconstruct an observation-only identity even if a package-local caller bypasses construction. Pure/no-post recovery retains its compatibility fallback. There is no accepted provider-posting + probe + no-loader path that silently guesses Hobs from empty DerivationHash.

The direct `DurableStore.LoadEconomicRevisionValuation` at `internal/infra/billingstore/economic_revision_store.go:336` loads the exact V2 valuation key, including legacy work whose output contains allocation refs despite empty work DerivationHash. Fresh normalization and durable append separately validate the observation fence and full allocation identity. Recovery therefore retains Hfull rather than replacing it with Hobs. Existing durable SQLite/PostgreSQL tests exercise interrupted posting, already-posted payable recovery, retained nonpayable exclusion recovery, ordinary no-allocation compatibility and distinct replacement derivations. They assert stable source keys and exactly one payable journal, or no journal for exclusions.

RED/GREEN: `%TEMP%/opencode/phase16-ninthpass-loader-red.txt` contains a real behavioral assertion failure in `TestPhase16NinthPassNoLoaderProviderPostingRejected`: construction unexpectedly returned nil error. Its matching GREEN log passes all four cases, and the fresh focused run passes. This is meaningful rejection-boundary RED, not a compile-only failure. The other three cases were already green in RED and serve as compatibility controls. The latest wrapper/result-store and poster fixtures are in-memory test doubles; they are not a composed real-DurableStore reproduction, despite the wrapper comment's wording. The guard itself makes the unsupported combination unreachable independently of the backing store.

## Phase 16 and historical-finding rechecks

- **16.1 / parent 6.1–6.6, 11.4, 16.3–16.4:** detail assembly retains source-separated E/Q/P/S/R evidence, native currencies, BYOK payer, known subtotal and explicit incomplete margin. The durable reader preserves exact frozen selected valuations separately from latest display. Selected coverage uses the chosen monetary result, original observation payload hashes, exact item refs, validated inclusive charge graphs, and exact allocation fingerprint/target ownership. Independent heads do not become an invented complete aggregate. Execution-only missing B-legs, provider-authoritative zero and accepted-evidence precedence retain their earlier fail-closed behavior.
- **Head/revision integrity / parent 10.1, 10.3, 10.6, 11.2, 11.5; refinement 4.2, 4.5:** same-plane head comparison uses the currently winning timestamp in `updated_at_unix`, updates that timestamp and derivation/dependency fields atomically, and uses full identity ordering for ties. Evidence containment still takes precedence for differing observation sets; PostgreSQL locks the head row. The additive migration backfills full derivation/dependency metadata in 256-row batches and rejects missing selected valuations or contradictory derivations. Immutable losing valuations remain retained.
- **Dependency and allocation identity:** `EconomicJobDependency` carries DerivationHash through construction, validation, equality/key, canonical dependency hashes and exact durable lookup. Dependency-output validation independently checks observation and full allocation hashes. Work and valuation identity distinguish allocation replacement without inventing observation revisions; declared allocation substitution fails. The composed rating-to-reconciliation and replacement tests pass in the affected and configured PostgreSQL suites.
- **Allocation semantics:** supersession/moved-target closure and global materialization budgets remain present. Exact zero target share is checked independently of nonzero source amount; a nonzero rounded residual prevents exemption, and positive share rounded to zero remains inclusion-required. Redacted source values do not certify free cost. Retained reconstructed zero-share RED contains behavioral core/durable failures and is sensitivity evidence, not newly recovered historical RED.
- **16.2 / parent 9.1, 12.5–12.6, 13.2, 13.5, 16.3–16.4:** protected report routes expose bounded typed discrepancy, allowance, statement and adjustment DTOs with separate comparison, selection and posting state. SQL/domain scope checks remain. HMAC cursor validation bounds input before decoding and binds kind, scope, filters, order and full-scope snapshot; stale continuation is explicit. Allowance history remains a gauge. The configured diagnostics secret protects the mount, empty secret mounts nothing, and statement import needs explicit opt-in authorization before body processing.
- **16.3 / parent 16.1–16.6, 5.6, 18.5:** finite typed economic-field allowlists and lexeme/path limits remain; sanitizer/mapping markers and safe hash semantics are retained. Retention preserves financial/adjustment/statement linkage. Metrics use finite queue/reason/status/currency vocabularies, preserve gross absolute discrepancies by native currency, and omit unsupported currency series without mixing them into an `other` monetary value. No provider SDK dependency, customer-balance lock in provider health reads, frontend supplier-economics leak or stream-time monetary writer was identified.
- **Boundary:** changes stay within Phase 16's query/security/observability seams and the required immutable identity/replay repairs exposed by this review sequence. Justified file/LOC growth is not a finding.

## Fresh mechanical verification

- `go test -count=1 -run Phase16NinthPass ./internal/core/billing` — PASS, exit 0, 0.041 s.
- `go test -count=1 -timeout=20m ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./internal/core/billing/... ./internal/core/metering/aggregate/... ./internal/infra/billingstore/... ./internal/infra/metrics/... ./internal/stdhttp/... ./pkg/lipsdk/controlplane/... ./internal/infra/runtimebundle/...` — all selected packages except runtimebundle PASS; billingstore 353.616 s. Combined exit 1 because runtimebundle failed at 254.809 s. Large fixture diagnostics exceeded tool output retention, losing the exact initial failure header; retained tail identifies the same-revision refinement fixture. No specific root cause is claimed.
- Isolated `go test -count=1 -timeout=10m ./internal/infra/runtimebundle/...` — PASS, exit 0, 45.338 s. Failure output was filtered to retain test/assertion lines without the large fixture dump.
- `go test -count=3 -timeout=5m -run '^TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay$' ./internal/infra/runtimebundle` — PASS, exit 0, 14.406 s. These successful reruns provide fresh affected-package evidence; they do not erase or establish the cause of the initial combined-run failure.
- `make test-db-parity-sqlite` — PASS, exit 0, complete registered SQLite catalog; billingstore 16.169 s.
- Configured PostgreSQL with `LIP_REQUIRE_POSTGRES=1`: `go test -tags integration -count=1 -timeout=20m -run 'EconomicDetail.*Postgres|Operator.*Postgres|EconomicHealth.*Postgres|Retention.*Postgres|Phase16.*Postgres|AllocationReplacement.*Postgres' ./internal/infra/billingstore` — PASS, exit 0, 621.380 s. This fresh complete selected run exceeds the old default ten-minute budget without timing out; it includes head ordering, allocation dependencies and provider replay. DSNs were not printed.
- Scoped `go vet` over metering/economics SDK, billing/aggregate, billingstore, metrics, stdhttp, controlplane SDK and runtimebundle — PASS, exit 0, no diagnostics.
- `go test -count=1 -run 'EconomicHealth|Economic.*Label|Billing.*Auth|Economic.*Security' ./internal/archtest` — PASS, exit 0, 2.585 s.
- `go test -run '^$' -fuzz '^FuzzSafeEvidenceValueTyping$' -fuzztime=30s ./pkg/lipsdk/metering` — PASS, exit 0, 945,377 executions, 31.265 s.
- `gofmt -l` over all 119 dirty/untracked Go files — clean; `git diff --check` — clean. Placeholder scan across those files and credential/private-key pattern scan across production paths — clean.
- `go test -count=1 ./internal/qa` — exit 1, 6.671 s, only the retained accepted exceptions: `TestRootHygiene_DirtyGoFiles` (119 versus hardcoded 100) and `TestQAFastPreflight_TestCost_ArchtestGoListCallsUseDedicatedCache` naming `internal/archtest/billing_binding_boundary_test.go`. `git diff --quiet HEAD -- internal/archtest/billing_binding_boundary_test.go` returns 0; this file remains unchanged. No baseline-wide QA claim is made.

## Nonblocking suggestions and residual limits

1. Add a real-DurableStore legacy allocation-bearing output case with empty work DerivationHash, including retained payable/exclusion replay. Current fresh evidence combines in-memory legacy controls with durable declared-allocation replay and direct source validation; it should not be described as one composed legacy durable test.
2. Add populated old-schema head migration/backfill fixtures, including retained legacy work, multiple batches and contradictory identity. Current migration review is source-backed plus fresh-schema execution, not a populated upgrade certification.
3. Strengthen equal-time ordering tests with explicitly reversed completion, processing each arrival before enqueueing the next, and a concurrent allocation-aware completion case. Reversing enqueue order before sorted queue processing alone does not prove both completion interleavings.

The earlier hand-built explicit Selection hardening suggestion remains nonblocking: no valid production durable producer path bypassing canonical selection was established. No new Critical or Important finding was found. The initial non-reproduced combined runtimebundle failure remains a verification limitation to watch during later integration gates. Full release QA, full PostgreSQL catalog, Windows race and opt-in test-cost were not run; Phase 17 and later certification retain those responsibilities.

---

# Historical ninth-pass review (superseded by tenth-pass verdict above)

## Review Verdict

- VERDICT: REJECTED
- TASK: Parent 16, 16.1, 16.2 and 16.3.
- SUMMARY: The direct durable-store head, dependency and posting paths are repaired, but the worker still permits a provider-posting recovery fallback that cannot reproduce a supported legacy allocation-bearing output.

Do not check 16/16.1–16.3 or proceed to Phase 17. Only this evidence report was edited. No source, test, task/status, Git or PR mutation was performed by this reviewer.

The user's 1,000-Go-file override applies. Fresh QA counted 117 dirty Go files; the hardcoded 100-file failure is explicitly nonblocking. The separately identified unchanged test-cost QA failure is also recorded as the controller's accepted exception, not a Phase 16 finding.

## Important finding

### 1. The no-loader provider-posting fallback still guesses a different replay identity

Source: `internal/core/billing/economic_revision_worker.go:283`, `:317–321`, `:345–355`; constructors at `:58–85`; `internal/core/billing/economic_revision.go:643`; `internal/core/billing/provider_cost_revision.go:684–697`.

The newly added loader fixes the direct `DurableStore` path. However, the loader remains an optional assertion and the constructor accepts a result store/probe that does not expose it. On that accepted path, recovery substitutes `identity.InputSetHash` whenever `DerivationHash` is empty. Empty derivation proves that the work declared no allocations; it does not prove that its persisted output contains no allocations. The normalizer explicitly permits allocation-bearing outputs for that legacy work, and `TestPhase16Finding1NormalizeRevisionValuationIncludesAllocationCoverage` continues to certify this behavior.

A concrete composition is a result-store decorator forwarding `AppendEconomicRevisionResult` and `HasEconomicRevisionResult` to `DurableStore`, exposing the existing result/probe ports but not the new optional loader. Use provider work without declared allocation refs and a rater returning a valid allocation-bearing output. Fresh processing persists and posts the full valuation hash Hfull. After processing is interrupted before work completion, the probe returns true and recovery posts Hobs under the same valuation/work identity. Hfull differs from Hobs by the canonical allocation extension. No invalid evidence, fabricated hash, changed rater result or competing writer is needed. The wrapper can use the actual durable store and actual provider posting adapter throughout.

This recreates eighth-pass finding 3 only on the accepted no-loader composition: source key and semantic fingerprint change across retry, and a retained nonpayable exclusion can reject recovery as a conflict. The stock direct `DurableStore` path is not affected. This review does not claim a duplicate nonzero debit. The optional-port documentation says this seam requires exact loading, but the executable fallback neither enforces that requirement nor rejects the unsupported combination.

Spec: parent 10.1, 10.6, 13.3, 14.4; design C5/C6. This is a source-derived counterexample, not a newly executed reproduction: reviewer ownership permits only this report to be edited.

Minimal remediation: enforce exact persisted-output recovery when provider posting and processed-result probing are enabled, or reject the unsupported legacy allocation-bearing combination before persistence/posting. Keep pure/no-post workers compatible. Do not silently infer an observation-only persisted output from an empty work derivation. Add a composed regression using a result/probe wrapper without the loader, plus a loader-backed legacy allocation-output retry; require either early explicit rejection or identical fresh/retry source keys, payable exactly-once behavior and successful nonpayable exclusion recovery. Existing declared-allocation and ordinary no-allocation compatibility tests must remain green.

## Remediation rechecks and remaining limits

- **Head ordering:** same-plane advancement compares incoming work `CreatedAt` with the current winner retained in `updated_at_unix`; the UPDATE persists timestamp, derivation and dependency fields together. Equal-time ordering uses full identity ordering. Evidence-superset/subset precedence is retained, PostgreSQL takes a row lock, and SQLite uses its existing transaction boundary. `GetEconomicValuationHead.UpdatedAt` returns the selected work timestamp, not the initial row creation timestamp. The t1/t3/delayed-t2 and zero/equal-time tests pass in the affected suite.
- **Migration:** the new additive SQLite/PostgreSQL migration is registered and required; it scans heads in finite ID batches, recovers full input identity from the selected valuation and dependency identity from retained work, and rejects missing selected valuations or contradictory derivations. Fresh-schema checks pass. No seeded old-schema/populated-head upgrade/backfill test was found among the new head tests; actual populated backfill is source-reviewed, not independently executed here. Add that coverage as a nonblocking test-strengthening suggestion, including retained legacy work and a multi-batch set.
- **Test-strength caveat:** equal-time tests reverse enqueue order and process after both enqueues; queue ordering can normalize their completion order. Their RED logs still demonstrate a wrong selected winner, but those tests alone do not prove opposite completion interleavings. Sequential per-arrival processing and an allocation-aware concurrent completion test would strengthen the claim.
- **Dependencies:** `NewEconomicJobDependency`, validation, equality, key construction, canonical dependency hashing, JSON work payloads, `OutputIdentity`, durable probes and exact loaders now retain DerivationHash. The runner independently validates observation-only and full allocation input hashes. The composed allocation-rating/dependent-reconciliation and replacement tests exercise the exact output rather than a historical observation-only output. Legacy empty-field JSON/preimages remain unchanged.
- **Direct durable provider replay:** the loader resolves exact V2 output and validates ID, version, observation hash, full canonical hash and declared derivation. Existing payable/nonpayable/replacement retry tests use this path and passed locally. They do not exercise the no-loader legacy allocation-output counterexample above.
- **Allocation identity and coverage:** the worker/result writer independently retain the observation fence and full allocation identity; declared-ref substitution fails. Replacement results remain immutable and distinct. Selected detail loads the exact frozen valuation separately from latest display. Coverage uses original observation payload hashes, exact charge item refs, allocation fingerprint and target ownership; zero-share classification preserves nonzero residual and positive-share/rounded-zero controls.
- **Earlier report findings:** selected monetary result/coverage coupling, independent-head ambiguity, execution-only missing legs, provider-authoritative zero, validated closed charge graphs, allocation supersession/moved-target closure, source redaction, global materialization limits, bounded authenticated cursor scope/kind/filter/snapshot binding, protected mounts and pre-body import authorization remain present in inspected source and covered by the passing affected suites. The previously recorded hand-built explicit Selection hardening note remains nonblocking.
- **Security/boundaries:** changed-source placeholder and credential-pattern scans are clean. Typed finite economic-field allowlists, sanitizer markers, bounded cursor decoding and finite currency/queue/status metric vocabularies remain. No new provider SDK dependency or stream-time money writer was identified. The patch stays within Phase 16 query/security/observability and the necessary identity/replay repairs it exposed.

## Fresh mechanical verification

- `go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./internal/core/billing/... ./internal/core/metering/aggregate/... ./internal/infra/billingstore/... ./internal/infra/metrics/... ./internal/stdhttp/... ./pkg/lipsdk/controlplane/... ./internal/infra/runtimebundle/...` — PASS, exit 0; billingstore 185.568 s, runtimebundle 85.362 s. No runtime timeout occurred in this pass.
- `make test-db-parity-sqlite` — PASS, exit 0, all registered components; billingstore 16.373 s.
- Configured PostgreSQL with `LIP_REQUIRE_POSTGRES=1`: `go test -tags integration -count=1 -run 'EconomicDetail.*Postgres|Operator.*Postgres|EconomicHealth.*Postgres|Retention.*Postgres|Phase16.*Postgres|AllocationReplacement.*Postgres' ./internal/infra/billingstore` — FAIL, exit 1, 600.213 s: package-wide 10-minute timeout. Active `TestLatestReconciliationRetentionPostgresPlanAndOrder` had run for five seconds and was inside `NewDurableStore` / legacy-retirement migration I/O. No assertion failure was printed. This is not a green full PostgreSQL gate. DSNs were not printed.
- Isolated configured PostgreSQL `go test -tags integration -count=1 -run '^TestLatestReconciliationRetentionPostgresPlanAndOrder$' ./internal/infra/billingstore` — PASS, exit 0, 10.719 s. The final unstarted case from the selected test list, `go test -tags integration -count=1 -run '^TestRetentionLinkagePostgresDirect$' ./internal/infra/billingstore` — PASS, exit 0, 13.845 s. These results do not erase the broad package timeout; a future complete combined run needs an adequate explicit package timeout or bounded shards.
- Scoped `go vet` over metering/economics SDK, billing/aggregate, billingstore, metrics, stdhttp and runtimebundle — PASS, exit 0, no diagnostics.
- `go test -count=1 -run 'EconomicHealth|Economic.*Label|Billing.*Auth|Economic.*Security' ./internal/archtest` — PASS, 1.128 s.
- `go test -run '^$' -fuzz '^FuzzSafeEvidenceValueTyping$' -fuzztime=30s ./pkg/lipsdk/metering` — PASS, 835,184 executions, 31.096 s.
- `gofmt -l` over all dirty/untracked Go files — no output; `git diff --check` — clean. Placeholder scan over those Go files — clean; production private-key/API-key pattern scan — clean.
- `go test -count=1 ./internal/qa` — exit 1, 7.058 s, only the two declared nonblocking exceptions: `TestRootHygiene_DirtyGoFiles` (117 versus hardcoded 100) and `TestQAFastPreflight_TestCost_ArchtestGoListCallsUseDedicatedCache` (`archtest/billing_binding_boundary_test.go`, unchanged from HEAD).

RED inspected: eighth-pass head-ordering log contains delayed-t2, equal-time and normalized-zero behavioral assertion failures; dependency behavioral RED contains exact allocation-output probe failure and replacement-key collision; provider-posting RED contains differing fresh/retry source hashes and replacement-key mismatch. The retained zero-share RED contains actual core and durable call/A-leg failures, and its matching GREEN log passes. This is reconstructed sensitivity evidence, not newly discovered historical RED. The current production coverage file's SHA-256 equals the retained pre-mutation backup. The earlier historical RED qualifications below remain historical.

Not run: full release QA, full PostgreSQL catalog, Windows race or opt-in test-cost. This is a Phase 16 task review, not release certification. Remediate the no-loader replay branch and obtain a fresh review before accepting parent Phase 16.

---

# Historical eighth-pass review (superseded by ninth-pass verdict above)

## Review Verdict

- VERDICT: REJECTED
- TASK: Parent 16, 16.1, 16.2 and 16.3.
- SUMMARY: The allocation-aware append fence is repaired, but current-head ordering, dependency identity and provider-posting replay still lose the new derivation identity; zero-share RED evidence remains unavailable.

Do not check 16/16.1–16.3 or proceed to Phase 17. Reviewer edits are confined to this report. No source, test, task, status, Git or PR changes were made.

The user explicitly raised the source-change limit to 1,000 Go files for this refactor. The fresh inventory is 104 dirty/untracked Go files, within that authorization. The hardcoded 100-file QA failure is waived for this review and is not a finding. Necessary file/LOC growth is accepted.

Scope: approved parent/refinement artifacts and Phase 16 boundaries; current dirty inventory and relevant production/test source; previous findings; worker/result/queue/dependency/provider-posting composition; exact frozen selection and allocation proof; bounded readers, cursor authentication, import authorization, sanitization and health metrics. CodeGraph reported stale indexed ranges for changed identity/store files; those files were read directly. Findings below are independently source-derived counterexamples, not claims of newly executed reproducers: reviewer-only ownership prohibited adding tests.

## Important findings

### 1. Allocation corrections can regress the durable current head

Anchors: `internal/infra/billingstore/economic_revision_store.go:607` and `:617`.

For equal evidence revision and observation hash, advancement compares `incoming.CreatedAt > existing.CreatedAt`. The UPDATE changes `updated_at_unix` but never `created_at_unix`. After V1 at t1 advances to V3 at t3, the row still has creation time t1. A delayed distinct V2 at t2, where t1 < t2 < t3, therefore passes the test and replaces V3. This is reachable through ordinary queued work arriving after the first two jobs were processed, or retry/out-of-order completion. Immutable valuations survive, but the authoritative current pointer regresses.

Equal timestamps have a related convergence gap: two distinct allocation derivations with the same CreatedAt use first-arrival-wins, with no tie policy or conflict. Zero timestamps normalize to the same epoch, making this reachable without timestamp manipulation. Rebuilding in another order can select another head. The new tests cover only two monotonically increasing timestamps.

Spec: parent 10.3, 10.6, 11.2, 11.5; D5/C6; refinement 2.6, 4.5.

Minimal remediation: compare against the currently selected work's retained ordering metadata, update that metadata atomically on every transition, and define equal-time handling that is deterministic or explicitly conflicting. Preserve evidence-containment precedence. Add SQLite/PostgreSQL tests for t1 -> t3 -> t2, restart/replay, reverse completion, equal timestamps and normalized-zero timestamps; assert the immutable history and final head.

### 2. Reconciliation dependencies cannot name allocation-aware rating outputs

Anchors: `internal/core/billing/economic_job_queue.go:182`, `:214`; `internal/core/billing/economic_job_runner.go:448`; `internal/infra/billingstore/economic_job_queue_store.go:403–425`; `internal/infra/billingstore/economic_job_runner_store.go:20`.

`EconomicJobDependency` retains only queue/head/evidence revision/observation InputSetHash. `OutputIdentity` reconstructs an identity with no DerivationHash. A declared-allocation rating now stores its valuation under a key that includes DerivationHash, so the dependency probe and loader look up a different valuation. Reconciliation stays pending; if a historical no-allocation output exists under the old key, it can instead bind that older output. The DTO cannot distinguish allocation V1 from V2. Supplying the full hash as dependency InputSetHash does not reconstruct the producer's two-part key.

Even after carrying the new identity through the dependency DTO, `validateEconomicJobDependencyOutput` currently compares the allocation-aware valuation InputSetHash with the observation-only dependency hash. That check must retain separate observation/full-input fences. The new job-runner test runs rating jobs only and does not exercise a dependent reconciliation.

Spec: parent 10.1, 10.3, 10.6, 11.2, 12.5; C4/C6 and Phase 16.2 discrepancy status; refinement 4.2, 4.5.

Minimal remediation: carry and validate the complete rating-output derivation identity through dependency canonicalization/equality/key/hash, durable dependency probes and exact loads, and output validation, preserving legacy empty-field bytes. Add composed rating -> dependent reconciliation tests for initial allocation and replacement under unchanged observations, old-output presence, exact replay and both dialects.

### 3. Provider-posting retry changes the same revision's source identity

Anchors: `internal/core/billing/economic_revision_worker.go:222`, `:254`; `internal/core/billing/provider_cost_revision.go:684–689`, `:924`; `internal/infra/billingstore/provider_cost_revision_store.go` exclusion replay and same-revision head handling.

The initial worker call passes the normalized valuation with its full allocation-aware InputSetHash to `BuildProviderCostRevisionInput`. After result persistence, a retry/restart takes the processed-result branch and passes only `Valuation{ID: identity.ValuationKey()}`. The builder then falls back to the observation-only work hash. Thus the same work, valuation ID and evidence produce different ProviderCostRevisionSourceKey and semantic fingerprint depending on whether execution follows the fresh or persisted-result branch.

A failure after provider posting commits but before work completion reproduces the split. For a durable nonpayable exclusion, same-revision replay rejects the changed hash/fingerprint as `ErrProviderCostRevisionConflict`, leaving work retrying. For a payable head, the changed hash enters the same-evidence lexical ordering path instead of exact replay and can create another source operation/head transition. This finding does not assert a duplicated nonzero debit: the concrete failures are broken replay identity and exclusion recovery. `BuildProviderCostRevisionInputFromWork` was updated to retain DerivationHash, but the worker's processed-result branch does not call it.

Spec: parent 10.1, 10.6, 13.3, 14.4; C5/C6.

Minimal remediation: use the same canonical full valuation identity on fresh and recovered provider-posting paths, including the supported legacy work/output case, and verify the existing exact selected valuation. Add fault/restart tests between valuation append, provider posting and work completion for payable and nonpayable inputs; assert stable source key/fingerprint, one posting operation and successful completion.

### 4. Zero-share behavioral RED evidence is still unavailable

The controller explicitly confirmed that the executor retained only failing test names, with no original RED output. `%TEMP%/opencode/phase16-seventhpass-zero-share-green.txt` is GREEN evidence only. Current target-zero code and regression tests appear correct, including nonzero rounded residual and positive exact-share/rounded-zero controls; that does not establish historical RED.

Acceptance: mandatory `kiro-review` Mechanical Check 5 and repository TDD policy. Supply original relevant RED output or a clearly labelled controlled pre-fix/mutation sensitivity check in isolation, without rewriting current shared source or describing reconstructed evidence as historical.

## Previous findings and boundary rechecks

- The prior append failure is repaired: the durable result writer independently verifies the observation-only work fence and the full allocation-aware canonical valuation hash. Stale caller hashes and observation drift fail; declared allocation substitution fails. Sorting/reordered replay and immutable replacement insertion are covered.
- RatingInput allocation refs are bounded, validated, store-scoped, cloned and canonicalized. Nonempty refs produce DerivationHash; Key, ValuationKey, ReconciliationKey and Less carry it. Empty refs omit the extension and preserve historical preimages. No additional feasible producer-key collision was identified; dependency propagation is finding 2.
- The supported no-declared-allocation seam remains explicitly lenient: an allocation-bearing output can persist once under the legacy work key, and a changed output conflicts rather than replacing it. It is not evidence that this old work identity can support allocation-only corrections.
- Exact frozen selected valuation/input hash, original observation payload hash, exact item/charge coverage, execution-only B-leg completeness, provider-zero authority and closed charge-graph handling remain present. The hand-built explicit Selection hardening note remains nonblocking; no valid durable producer bypass was established.
- Exact allocation ref/hash/target ownership, zero-share classification, supersession/moved-target closure, redaction and materialization bounds remain covered by current source and passing focused suites. No allocation amount is independently summed into selected money by the coverage query.
- Cursor length checks precede decoding/HMAC; domain, store, filter and kind binding remain enforced. Protected report mounting and explicit pre-body import authorization remain. Production secret-pattern and placeholder scans were clean; metrics remain finite-label projections, and no provider SDK or stream-time monetary writer was introduced.

## Fresh mechanical verification

- Broad affected command: `go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./internal/core/billing/... ./internal/core/metering/aggregate/... ./internal/infra/billingstore/... ./internal/infra/metrics/... ./internal/stdhttp/... ./pkg/lipsdk/controlplane/... ./internal/infra/runtimebundle/...` — FAIL. Runtimebundle reproduced `TestRefinement4StockObservationToEconomicSettlement`, line 184: `timed out waiting for observation outbox relay: pending=[] err=context deadline exceeded` (test 25.10 s; package 64.999 s). Cause is not established.
- Isolated `go test -count=3 -run '^TestRefinement4StockObservationToEconomicSettlement$' ./internal/infra/runtimebundle` — PASS, 15.458 s. This does not erase the broad failure.
- Fresh SDK/core/aggregate/metrics/HTTP/controlplane command excluding billingstore/runtimebundle — PASS, all listed packages.
- `go test -count=1 -run 'Phase16|EconomicDetail|Operator|EconomicHealth|Retention' ./internal/infra/billingstore` — PASS, 19.718 s.
- `make test-db-parity-sqlite` — PASS, all registered components; billingstore 16.244 s.
- Configured PostgreSQL with `LIP_REQUIRE_POSTGRES=1`: `go test -tags integration -count=1 -run 'EconomicDetail.*Postgres|Operator.*Postgres|EconomicHealth.*Postgres|Retention.*Postgres|Phase16.*Postgres|AllocationReplacement.*Postgres' ./internal/infra/billingstore` — PASS, 469.696 s, including the allocation-aware replacement path. DSNs were not printed.
- Scoped `go vet` over metering/economics SDK, billing/aggregate, billingstore, metrics, stdhttp and runtimebundle — PASS, no diagnostics.
- `go test -count=1 -run 'EconomicHealth|Economic.*Label|Billing.*Auth|Economic.*Security' ./internal/archtest` — PASS, 2.216 s.
- `go test -run '^$' -fuzz '^FuzzSafeEvidenceValueTyping$' -fuzztime=30s ./pkg/lipsdk/metering` — PASS, 1,125,094 executions, 31.073 s.
- Dirty/untracked Go `gofmt -l` and `git diff --check` — clean. Full changed-file placeholder scan and targeted production credential/private-key scan — clean.
- `go test -count=1 ./internal/qa` — FAIL, 12.300 s: expected/waived `TestRootHygiene_DirtyGoFiles` (104 vs hardcoded 100), and separately `TestQAFastPreflight_TestCost_ArchtestGoListCallsUseDedicatedCache`, naming `archtest/billing_binding_boundary_test.go`. That file is unchanged from HEAD and contains the named direct go-list call; this review did not reproduce the QA test in a separate baseline checkout and does not claim baseline-wide certification.

RED inspected: `phase16-seventhpass-worker-composed-red.txt`, `phase16-seventhpass-jobrunner-composed-red.txt`, `phase16-seventhpass-direct-dualfence-red.txt`, and `phase16-seventhpass-replacement-red.txt`. They contain relevant durable hash-mismatch, incomplete-job, replacement-work-conflict and allocation-claim-tamper failures. Earlier retained RED caveats are preserved in the historical reports below; zero-share historical RED remains missing.

Not run: full release QA, full PostgreSQL catalog, Windows race or opt-in test-cost. The newly identified ordering, dependency and provider-posting counterexamples still require executable RED/GREEN coverage. Remediate the four Important findings, resolve or establish the unrelated verification failures, and obtain a fresh independent review before accepting Phase 16.

---

# Historical seventh-pass review (superseded by eighth-pass verdict above)

## Current Review Verdict

- VERDICT: REJECTED
- TASK: Parent 16, 16.1, 16.2 and 16.3.
- SUMMARY: Direct allocation-aware append and target-zero classification are fixed; the revised rating normalizer still disagrees with its durable result writer, and latest zero-share RED evidence is unavailable.

Do not check 16/16.1–16.3 or proceed to Phase 17. Only this report was edited. No task/status, source, Git or PR changes are authorized. Justified file growth is accepted; the dirty Go inventory is 98, below the 100-file gate. The sixth-pass report retained below is historical, not the current verdict's finding list or fresh verification record.

### 1. Important — allocation-aware revision output fails at its durable result boundary

Anchors: `internal/core/billing/economic_revision_worker.go:281–317`, `internal/core/billing/economic_revision.go:379–390`, `internal/infra/billingstore/economic_revision_store.go:331–357`, `internal/core/billing/economic_job_runner.go:381–399`.

`normalizeRevisionValuation` now verifies the observation-only hash against frozen work, then assigns `CanonicalValuationInputSetHash`, including allocation refs, to the output. `EconomicRevisionWork.Normalize` still uses the observation-only hash. `AppendEconomicRevisionResult` recomputes the allocation-aware valuation hash, then at line 356 requires it to equal the observation-only work hash. A valid nonempty allocation input set therefore causes `ErrEconomicRevisionInputMismatch` before persistence.

Reachable composition: an injected `PostUsageRater` returns the work's exact observations and a valid allocation coverage ref. The new core regression expressly accepts that output at the normalizer. Both revision worker and job runner then call the incompatible durable result writer, preventing head advancement. The stock reference rater emits no allocation refs: this finding concerns the newly supported allocation-aware output through an existing injected internal seam, not ordinary reference-rater jobs or invalid hand-built explicit selections.

The new SQLite/PostgreSQL identity tests call `AppendValuation` directly; they do not compose this producer with its writer or advance the revision head. Removing the equality check alone is insufficient: valuation ID and processed-result identity still derive from observation-only work identity, so an allocation-only rerun can reuse its immutable key or be skipped as already processed.

Spec: parent requirements 10.1, 10.3, 11.2, 13.3; design D4/D5/C5/C6; refinement 2.5–2.6 and exact selected allocation inclusion. This is the remaining integration part of sixth-pass finding 1.

Minimal remediation: align the declared allocation-aware producer, immutable work/derivation identity and durable result boundary while preserving the observation fence, replay and no-allocation compatibility. If this seam is intentionally observation-only, enforce that consistently and demonstrate the actual allocation correction owner advancing the selected result; do not leave the normalizer claiming persistable allocation-only revisions that its writer rejects. Add composed RED/GREEN persistence tests for initial inclusion, replacement with unchanged observations, exact/conflicting replay and selected-head advancement, with PostgreSQL parity. Do not invent observation revisions or pricing-version changes to bypass uniqueness.

### 2. Important — zero-share remediation lacks verifiable RED evidence

The executor supplied failing test names but no retained RED output. Targeted `%TEMP%/opencode` text/log searches found no zero-share/rounded-zero/nonzero-share behavioral failure. Current core/durable GREEN tests pass and the implementation appears correct; that does not reconstruct historical RED.

Acceptance anchor: mandatory `kiro-review` Mechanical Checks 5 and strict TDD policy. Supply original relevant `RED_PHASE_OUTPUT`, or record a controlled pre-fix/mutation sensitivity check without disturbing the shared worktree. This is a verification gap, not a claim that target-zero behavior is still broken.

### Rechecks and non-blocking notes

- Direct-append collision is corrected: allocation refs enter canonical input identity; sorting, exact-duplicate collapse in the hash helper, conflict rejection and caller-hash validation are implemented. Valuation validation rejects duplicate refs. Empty refs preserve legacy bytes. Retail and revision producer recomputation were inspected; finding 1 concerns composition.
- Unchanged `ContextHash` is appropriate: pricing/subject context stays separate from allocation inputs, and durable direct-append uniqueness includes the updated input hash. No additional semantic collision was found there.
- Target-zero logic now checks exact share/source zero and refuses exemption for nonzero rounded residual. Positive share rounded zero remains inclusion-required; redacted lines fail closed. Core/durable call and A-leg controls pass.
- Prior selected-result coupling, separately loaded frozen selected valuation/input hash, original observation payload hash, exact allocation proof/target ownership, execution-only B-leg, provider-zero authority and closed charge-graph fixes remain intact on inspection and affected tests.
- Allocation supersession/moved-target closure, global materialization limits, cursor predecode bounds and HMAC scope/filter/kind/order/snapshot binding, durable keys, protected HTTP/import authorization, finite metric labels and typed evidence sanitization were rechecked. No new core provider dependency or stream-time money writer was found.
- Explicit hand-built same-basis Selection hardening remains a suggestion only: no valid approved durable producer path was found. It is not a blocker.

### Fresh seventh-pass mechanical evidence

- Affected command `go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./internal/core/billing/... ./internal/core/metering/aggregate/... ./internal/infra/billingstore/... ./internal/infra/metrics/... ./internal/stdhttp/... ./pkg/lipsdk/controlplane/... ./internal/infra/runtimebundle/...`: exit 1. All except runtimebundle passed; billingstore 129.871 s. Runtimebundle failed after 51.117 s; output truncation lost the exact assertion, so no cause is asserted.
- Isolated `go test -count=1 ./internal/infra/runtimebundle/...`: exit 0. `go test -count=3 -run '^TestRefinement4StockObservationToEconomicSettlement$' ./internal/infra/runtimebundle`: exit 0, 13.883 s. These reruns do not erase the initial failure; retain complete output if it recurs.
- `make test-db-parity-sqlite`: exit 0, registered catalog; billingstore 16.052 s.
- Configured PostgreSQL, `LIP_REQUIRE_POSTGRES=1`: `go test -tags integration -count=1 -run 'EconomicDetail.*Postgres|Operator.*Postgres|EconomicHealth.*Postgres|Retention.*Postgres' ./internal/infra/billingstore`: exit 0, 408.738 s. Dedicated `TestPhase16Finding1AllocationIdentityPostgresDirect`: exit 0, 14.484 s. DSNs were not printed.
- `go vet` over metering/economics SDK, core billing/aggregate, billingstore, metrics, stdhttp and runtimebundle: exit 0.
- `go test -count=1 -run 'EconomicHealth|Economic.*Label|Billing.*Auth|Economic.*Security' ./internal/archtest`: exit 0, 1.644 s.
- `go test -run '^$' -fuzz '^FuzzSafeEvidenceValueTyping$' -fuzztime=30s ./pkg/lipsdk/metering`: exit 0, 1,278,523 executions, 31.420 s.
- Dirty/untracked Go `gofmt -l`: clean; `git diff --check`: exit 0. New placeholder scan: zero. Targeted production credential/private-key scan: clean (synthetic rejection-test strings excluded).
- Inspected retained `phase16-finding1-sixthpass-core-red.txt`, `phase16-finding1-sixthpass-durable-red.txt`, `phase16-finding1-sixthpass-sdk-red.txt`: hash/collision behavioral failures and missing-API compilation scaffold respectively. Fifth-pass selected-proof RED was also inspected. Latest zero-share RED remains unavailable; prior historical evidence caveats are not represented as reconstructed.

Not run: full release QA, full PostgreSQL catalog, Windows race or opt-in test-cost. Scope included approved artifacts, current inventory, relevant dirty source/test changes, retained evidence and production call paths; no new reproducer was added under reviewer-only ownership. Both ranked findings require remediation and fresh verification before approval.

---

# Historical sixth-pass review (superseded by seventh-pass verdict above)

## Review Verdict

- VERDICT: REJECTED
- TASK: Parent 16, 16.1, 16.2 and 16.3.
- SUMMARY: The fifth-pass reproductions are remediated, but allocation inclusion has two Important correctness gaps in revision identity and exact-zero attribution.

Do not check tasks 16/16.1–16.3 or proceed to Phase 17. This review authorizes no task/status, Git or PR changes. Reviewer edits are confined to this report. File/LOC growth is accepted where justified; it is not a finding.

Scope: approved parent and refinement Phase 16 contracts, root and Kiro rules, current tracked diff and complete dirty/untracked inventory, previous report, selected-result/evidence identity, allocation persistence/rollup contracts, operator readers/cursors, HTTP composition, sanitization and metrics. The counterexamples below are source-derived; no new reproducer tests or production changes were made under reviewer-only ownership.

## Ranked findings

### 1. Important — allocation-only revisions cannot acquire a new durable valuation identity

Anchors: `pkg/lipsdk/economics/valuation.go:806`, `pkg/lipsdk/economics/input_identity.go:31`, `pkg/lipsdk/economics/valuation_context.go:33`, `pkg/lipsdk/economics/valuation_context.go:87`, `internal/infra/billingstore/v2_economics_store.go:548`, `internal/infra/billingstore/v2_economics_store.go:640`.

`AllocationCoverageRefs` changes canonical valuation JSON and `Fingerprint`, but participates in neither `CanonicalInputSetHash` nor `ContextHash`. Durable append deduplication uses those two hashes plus rater/policy/tariff/basis. A late or replacement allocation therefore cannot revise a valuation when its observation inputs and pricing context remain unchanged.

Concrete path: persist allocation-inclusive V1; append a conserved replacement allocation under the same policy with changed target weights; produce V2 with a fresh valuation ID, the replacement's exact allocation ref, and the correspondingly revised amount, retaining the same observation refs, subject, rater and pricing snapshots. Both input/context hashes equal V1. `AppendValuationInTx` finds V1 and returns `ErrIdentityConflict: valuation input set identity` before inserting V2. Reusing V1's ID instead conflicts with immutable replay. The passing `TestQueryEconomicDetailFinding3ProvenIncludedAllocationCompletes` appends only an initial valuation and does not exercise this legitimate allocation-only correction.

Spec anchors: parent requirements 10.1, 10.3, 11.2 and 13.3; parent design D4/D5/C5; refinement requirements 2.5–2.6 and OperatorCOGS allocation contract.

Minimal remediation: bind the economically significant allocation input set to a declared, versioned valuation derivation identity at its owning producer/store boundary, retaining old no-allocation identities and replay behavior. Do not fabricate observation revisions or change an unrelated rater/policy version to bypass uniqueness. Add RED/GREEN SQLite and PostgreSQL cases for initial valuation, late allocation inclusion, replacement allocation with unchanged observation inputs, exact replay and conflicting replay; prove the selected head can advance without double posting.

### 2. Important — exact-zero target shares are classified from the nonzero source aggregate

Anchors: `internal/core/billing/economic_detail_cost_coverage.go:514`, `internal/core/billing/economic_detail_cost_coverage.go:520`, `internal/core/billing/allocation.go:27`, `internal/core/billing/allocation.go:170`, `pkg/lipsdk/economics/allocation.go:906`.

The zero exemption calls `costCoverageAllocationIsZero(line.SourceAmount)`. `SourceAmount` deliberately retains the entire original resource amount; the target's attributable amount uses its exact `Share` and declared rounding treatment. Zero target weights are valid canonical conserved allocations.

Concrete path: a USD 10 allocation has two ordinary monetary targets with shares 1/1 and 0/1, no rounding residual, and exact rounded amounts USD 10 and USD 0. Query the otherwise complete call owning the zero-share target. Its line still has `SourceAmount = 10`, so the zero branch is false. Without an allocation-inclusion ref it increments `AllocationUnresolvedCount` and clears the margin with `allocation_coverage_unresolved`, although that target incurs exactly zero resource cost. Existing zero tests use a zero source aggregate and miss this shared-allocation case.

Spec anchors: parent requirements 6.3 and 6.5; parent design C4; refinement requirements 2.5–2.6 and the exact attributable-allocation OperatorCOGS formula.

Minimal remediation: classify a trusted target's exact monetary contribution, preserving fraction and rounding/residual semantics. Never infer zero from missing/redacted amounts or a rounded zero hiding a nonzero exact share. Keep amount aggregation out of the query. Add core and durable call/A-leg RED/GREEN tests with a nonzero source, zero-share target, nonzero-share sibling and rounding/residual controls.

## Non-blocking boundary hardening

- Suggestion: explicit `EconomicDetailInput.Selection` is less strictly validated than the canonical selector. `economic_detail_cost_coverage.go:352–371` resolves by ID and seeds every candidate having the selected basis, while `validateSelectionScope` (`economic_detail.go:1579`) checks only store/tenant/source-store. `SelectOperatorCost` rejects duplicate candidate bases (`operator_cost_selection.go:367,604`). A hand-built result can bypass that invariant and the input-hash check used by the persisted-head path. Existing explicit fixtures are hand-built; no production durable reader supplies explicit Selection. Preserve the canonical one-candidate-per-basis precondition at assembly, reject ambiguous valuation IDs across supplied sets, and verify the selected candidate's frozen identity before seeding. This review does not claim a valid canonical selector result can contain two same-basis candidates.
- FYI: durable V2 append fixes `Valuation.Version` to envelope version 2 and enforces store/ID/version uniqueness. Selected reference `Revision` is economic ordering, not envelope version. Exact older selected IDs are now loaded separately from latest-per-stream display; do not conflate these versions during remediation.

## Fifth-pass and earlier findings rechecked

- Independent retained heads no longer seed explicit selection coverage. Without explicit selection, only one authoritative selected head may seed; multiple selected heads remain ambiguous. Same/different B-leg provider-charge cases and inclusive controls pass.
- `SelectedValuations` carries the separately loaded frozen valuation with bounds and scope checks. The head path requires nonempty matching input identity; the durable writer independently verifies canonical observation input hashes. Original observation `ReplayFingerprint` values are captured before reduction. Line provenance requires store/ID/revision/payload hash and exact `ItemID`. Wrong/stale payload and latest-display-versus-frozen-selection regressions pass. Positive fixtures now refreeze refs after constructing the graph.
- Allocation lines retain `record.Fingerprint`; selected valuations carry bounded, cloned, sorted, JSON-round-tripped allocation refs. Exact store/ID/version/hash and target ownership gate inclusion. Positive unreferenced, incompatible and redacted allocations fail closed; no allocation money is summed into the selected result. Initial proven inclusion, source-zero, informational/unallocated and quantity-only controls pass. Findings 1–2 cover the remaining revision and target-zero gaps.
- Execution-only missing B-legs remain represented. Accepted observation evidence governs outcome exemptions. Provider-authoritative zero requires provider origin/acquisition and observed-claim authority. Local/estimated zero and accepted quantity/nonzero evidence do not become free work.
- Canonical reduction precedes closed-graph validation. Pending/unusable supersession, cycles, dangling edges, ambiguous inclusive parents, stale atoms and additive edges cannot certify complete coverage. Charge identity remains finer than shared B-leg ownership.
- Allocation supersession closure, moved-target retention, redaction and global materialization budgets retain passing coverage. Provider-charge/reconciliation identities remain separate scoped streams.
- Cursor size bounds precede decoding/hashing. HMAC binds scope/filter/kind/order/snapshot, with durable per-store keys and cross-kind/reopen regressions. Snapshot identity includes selected valuations and allocation proof.
- HTTP report/import protection and explicit import authorization remain intact. Typed allowlisted evidence rejects secret/content payloads. Metric currency labels are finite; backlog age excludes terminal history. No new frontend economics or stream-time monetary writer was introduced.

## Fresh mechanical verification

All commands below passed on `feat/b-leg-usage-economics` in the assigned worktree:

- `go test -count=1 ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./internal/core/billing/... ./internal/core/metering/aggregate/... ./internal/infra/billingstore/... ./internal/infra/metrics/... ./internal/stdhttp/... ./pkg/lipsdk/controlplane/... ./internal/infra/runtimebundle/...` — exit 0; billingstore 124.217 s, runtimebundle 46.065 s.
- `make test-db-parity-sqlite` — exit 0, registered SQLite catalog.
- Configured PostgreSQL with `LIP_REQUIRE_POSTGRES=1`: `go test -tags integration -count=1 -run 'EconomicDetail.*Postgres|Operator.*Postgres|EconomicHealth.*Postgres|Retention.*Postgres' ./internal/infra/billingstore` — exit 0, 394.679 s. DSN was not printed.
- `go vet ./pkg/lipsdk/metering/... ./pkg/lipsdk/economics/... ./internal/core/billing/... ./internal/core/metering/aggregate/... ./internal/infra/billingstore/... ./internal/infra/metrics/... ./internal/stdhttp/... ./internal/infra/runtimebundle/...` — no diagnostics.
- `go test -count=1 -run 'EconomicHealth|Economic.*Label|Billing.*Auth|Economic.*Security' ./internal/archtest` — exit 0.
- `go test -run '^$' -fuzz '^FuzzSafeEvidenceValueTyping$' -fuzztime=30s ./pkg/lipsdk/metering` — exit 0, 1,281,679 executions.
- `gofmt -l` over dirty/untracked Go files — clean; `git diff --check` — exit 0.
- Dirty Go inventory: 88 files, below the 100-file gate. No implementation placeholders; secret-pattern matches were synthetic rejection fixtures.

Full release QA, full PostgreSQL parity catalog, race and opt-in Windows test-cost were not run. This is task-local review, not release certification. Existing green suites do not execute the counterexamples above.

## RED evidence audit

Inspected retained `%TEMP%/opencode` logs:

- `phase16-finding1-fifthpass-core-red.txt` and `phase16-finding1-fifthpass-durable-red.txt`: behavioral independent-head coverage/ambiguity failures.
- `phase16-finding2-core-red.txt` and `phase16-finding2-durable-red.txt`: behavioral payload/input identity and exact older selected valuation failures.
- `phase16-finding3-core-red.txt` and `phase16-finding3-durable-red.txt`: behavioral allocation coverage failures; core also records an old-code coverage-slice panic after earlier failed assertions.

The prior report's caveat remains: execution-fact RED included a sensitivity/mutation check, and exact historical graph-validation RED was not reconstructed. Current relevant tests pass. New findings need their own narrow RED/GREEN evidence.

## Handoff

Remediate both Important findings, rerun affected core/SDK/store suites and both durable dialect paths, then obtain a fresh independent `/kiro-review`. Tasks 16/16.1–16.3 remain unchecked; Phase 17 remains blocked. Only this report was edited by the reviewer.
