# Phase 19.2 database, restart and lifecycle-race certification evidence

Task: `.kiro/specs/extensible-usage-economics-reconciliation/tasks.md` 19.2 —
certify database, restart and lifecycle races.
Branch: `feat/b-leg-usage-economics`. Baseline: `fe73f55b` plus the completed
19.1 files (preserved; see note at the end).
Boundary: tests: persistence and concurrency. Contracts: C6, Migration
Strategy, Testing Strategy.
Validation targets: `make test-db-parity`; supported-environment race evidence;
`make qa`.
Requirements in scope: 10.1, 10.3, 10.4, 10.5, 10.6, 11.4, 13.3, 14.4, 17.4,
17.5, 18.4, 18.6.

## 1. Environments

| Surface | Environment |
| --- | --- |
| Host | Windows 11, `go1.26.6 windows/amd64`, CGO gcc |
| Race (supported) | WSL2 Ubuntu 22.04.5, kernel `6.18.33.2-microsoft-standard-WSL2`, `go1.26.6 linux/amd64`, `CGO_ENABLED=1`, gcc 11.4.0 |
| PostgreSQL direct | Neon `ep-delicate-pond-asbd0kr1.c-4.eu-central-1.aws.neon.tech/neondb` (admin + runtime) |
| PostgreSQL transaction pooler | Neon `ep-delicate-pond-asbd0kr1-pooler.c-4.eu-central-1.aws.neon.tech/neondb` as runtime endpoint with `LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`; admin plane remains the direct endpoint |

Race runs use WSL with `TMPDIR`/`GOCACHE` on the native Linux filesystem
(`/root/.cache/lip-race`). The `runtimebundle` race suite runs as a non-root
Linux user (`lip`) because the host refuses to start the data plane as root.

## 2. Certification suite added

`internal/infra/billingstore/phase19_2_restart_lifecycle_race_certification_test.go`

Two integrated, file-backed tests close the one gap the inventory found: every
named 19.2 lifecycle had per-surface coverage, but no single test combined
pending/leased customer/provider/economic/adjustment work, loser-callback
races, duplicate/reordered callbacks, late evidence, cancellation, adjustment
races and cutover races with a same-file restart and same-A-leg resume while
asserting journals/balances/pins/heads/queue/exposure together and exactly
once.

1. `TestPhase192RestartLifecycleRaceExactlyOnce` — terminal/DONE for call-1 on
   `a-192`; pending economic work; pending provider cost; pending selected-cost
   head; 4-way concurrent settlement race (exactly one applies, three replay,
   one journal, balance debited once); 4-way concurrent provider-cost race
   (exactly one posts, three replay); 4-way worker-claim race (one winner,
   foreign-owner completion fences); 2-way distinct-correction race (one
   wins, one fences, head CAS); winner completes economic work (provider
   valuation head v1); 2-way cutover shadow transition race (one winner,
   marker version +1); **same-file/process restart** with a full snapshot
   equality assertion (journals/balance/selected head/valuation head/fence
   queue/legs/pins/marker); post-restart duplicate settlement/provider/economic
   replays are no-ops; stale/reordered correction is a typed stale zero-effect
   result with an unchanged snapshot; late correction (revision 3) applies then
   replays; canceled-context settlement adds no money; same-A-leg resume
   (call-2, fresh BillingCallID) settles independently and leaves the sealed
   call-1 fingerprint and leg set unchanged; final exactly-once journal count
   (6) and balance.
2. `TestPhase192RepeatedTerminalDoneSameALegResume` — six generations on one
   A-leg with one restart mid-sequence; each generation allocates a fresh
   BillingCallID, settles exactly once, and replays duplicate terminal/DONE
   callbacks idempotently; final balance and journal count prove one settlement
   per generation and the first sealed record is byte-stable.

Deterministic synchronization only: the existing ready/start-gate barrier
(`refinement82ReleaseBarrier`); no sleeps are used as correctness
synchronization. SQLite file-backed stores exercise close/reopen as a real
same-file process-restart analogue.

Commands and results (Windows host unless stated):

| Command | Result |
| --- | --- |
| `go test -count=1 -run TestPhase192 -v ./internal/infra/billingstore/` | PASS (2/2) |
| `go test -count=5 -run TestPhase192 ./internal/infra/billingstore/` | PASS |
| WSL race: `go test -race -tags=precommit,integration -count=1 -run TestPhase192 ./internal/infra/billingstore/` | PASS (9.429s) |
| WSL race stress: `-count=20 -parallel=16 -run '(TestPhase191\|TestPhase192\|TestSQLiteClaimCompleteCallsYieldsLargeIncompletePrefix\|TestPostJournalTransactionConcurrentDistinctReversalsRejectAllButOne\|TestSQLiteDeferProviderCostWorkAtomicUnderConcurrency\|TestSQLiteDeferUsageAppendAtomicUnderConcurrency\|TestF2BConcurrentEnqueueSerialized)'` with the billing/runtime race suites running concurrently | PASS (244.619s) |
| Full package, tags: `go test -count=1 -tags=precommit,integration ./internal/infra/billingstore/` | PASS (88.185s) |

Full lifecycle/race → requirement matrix: `phase19-2-lifecycle-race-matrix.tsv`.

## 3. Dual-dialect and pooler certification

Canonical gate `make test-db-parity` (`LIP_REQUIRE_POSTGRES=1`, DSN from
`LIP_TEST_POSTGRES_DSN`):

- SQLite: every registered component PASS.
- PostgreSQL direct: every component PASS including `metering-journal`
  after Phase19 R3 blocker-3 repair (see R3 note below). Prior RED was
  `metering_components.value_present` `int4` vs boolean plus `42804`
  boolean-vs-integer on writes.
- Billing component isolated: `go run ./internal/testkit/dbparity/cmd sqlite
  -component billing` PASS (16.169s); `... postgres-direct -component billing`
  PASS (83.071s).

Transaction-pooler topology (real Neon `-pooler` endpoint, attested):

| Package | Command | Result |
| --- | --- | --- |
| `internal/infra/concurrencyauthority/leasestore` | `go test -tags=integration -run Pooled ./...` | PASS 6/6 (23.461s) |
| `internal/infra/terminalwork/workstore` | same | PASS 3/3 (12.136s) |
| `internal/infra/usageauthority/authoritystore` | same | PASS 6/6 (32.328s) |
| `internal/infra/metering/journalstore` | same (with `search_path=public`, see R3 note) | PASS (all pooled contracts; R3 GREEN below) |

`billingstore` has no pooler-gated test surface in the canonical catalog
(`postgres-direct` only), so pooler evidence for the billing component is the
direct + non-session-pinned contracts above plus its own direct PG suites.

### Phase19 R3 blocker-3 correction (metering-journal direct/pooler)

Prior evidence incorrectly attributed all pooled failures to the direct
`int4`/boolean mismatch. Retained logs distinguish three causes:

1. Direct: `metering_components.value_present` `integer`/`int4` vs catalog
   `boolean`; writes fail `42804` boolean-vs-integer.
   RED: `%TEMP%/opencode/phase19-r3-metering-pg-red.txt`
   (canonical `postgres-direct -component metering-journal`).
2. Pooled: `verify postgres metering_components subject index: missing`.
   The shared Neon `public` schema lacked the five observation-projection
   indexes (`metering_facts_store_observation_revision_key` plus four
   `idx_metering_components_*`) because the pooled negative test dropped
   them with `DROP INDEX` and restored only a subset (`SELECT 1` for six
   names), and no forward migration recreated them.
3. Pooled negative contract: `DROP INDEX metering_facts_store_id_key`
   rejected `SQLSTATE 2BP01` (FK-backed
   `metering_components(store_id, observation_row_id) ->
   metering_facts(store_id, id)`); remaining six subtests reported
   `subject index: missing` instead of the dropped name because
   `VerifySchema` descriptions omitted index names and two V2 indexes
   (`store_observation_revision_key`, `components_observation`) had no
   PG check.

Fix (worktree `feat/b-leg-usage-economics`, no intent change outside
`internal/infra/metering/journalstore`, `internal/testkit` journal cleanup,
spec CHECK/defaults):

- Forward migration `20260917000000_metering_presence_boolean_repair`
  (idempotent): PG validates `0`/`1`/boolean text, `DROP DEFAULT`,
  `TYPE BOOLEAN USING (... IN ('1','true','t',...))`, `SET DEFAULT FALSE`
  for `value_present`/`money_present`; both dialects
  `CREATE ... IF NOT EXISTS` the five missing indexes. SQLite keeps
  `INTEGER` (dialect-native boolean). Registered in
  `RequiredMigrationNames`.
- `VerifySchema` PG now checks all 13 `V2BoundedIndexNames` with stable
  index-name descriptions (adds `store_observation_revision_key`,
  `components_observation`).
- Negative test restores all 13 indexes; `store_id_key` drops/recreates
  through the owning FK constraint to preserve integrity.
- Catalog spec adds `DefaultPostgres: false` for booleans and
  dialect-agnostic CHECK fragments (`measure`, `pending`; PG normalizes
  `IN` to `= ANY`).
- Journal cleanup deletes `components`/`supersessions`/`outbox` before
  `facts` (FK-safe; previously masked because inserts failed on `int4`).

Topology (no secrets): admin direct
`ep-delicate-pond-asbd0kr1.c-4.eu-central-1.aws.neon.tech/neondb`
(`search_path=public`), runtime pooler
`ep-delicate-pond-asbd0kr1-pooler.c-4.eu-central-1.aws.neon.tech/neondb`
(`search_path=public`, same Neon DB),
`LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1`,
`LIP_REQUIRE_POSTGRES_POOLER=1`, `LIP_REQUIRE_POSTGRES=1`.
Explicit `search_path=public` avoids pgbouncer session `search_path`
leakage from isolated-schema tests (fresh pooler connections otherwise
inherit a dropped `journal_r3_*` schema and report
`relation "metering_facts" does not exist`).

GREEN (same worktree, dirty Phase19 preserved):

- `go run ./internal/testkit/dbparity/cmd postgres-direct -component metering-journal` PASS (2.7s).
- `go test -tags=integration -run 'TestDBParity_PostgresDirect|TestPhase34_Postgres_DirectPooled_CorrectionAggregateIdentical|TestPhase34_PostgresPooled_VerifySchemaFailsWhenV2IndexMissing|TestPostgresPooled_AppendReplayConflictCorrection|TestPostgresPooled_DMLAfterAdminClose|TestPostgresPooled_AppendRejectsSameIdentityDifferentContent|TestPostgresPooled_ConcurrentSameKeyAppendIsIdempotent|TestPostgresPooled_VerifySchemaFailsWhenRequiredIndexMissing' -count=1 ./internal/infra/metering/journalstore` PASS (33.2s, all 13 negative subtests PASS including FK-backed `store_id_key`).
- `go test -tags=integration -count=1 ./internal/infra/metering/journalstore` PASS (111.1s).
- `go test -run 'TestDBParity_SQLite|TestPhase19R3_SQLite|TestPhase19R3_PresenceBooleanDDL' -count=1 ./internal/infra/metering/journalstore` PASS; `go run ./internal/testkit/dbparity/cmd sqlite` PASS all components.
- Upgrade: `TestPhase19R3_SQLite_PresenceRepairIdempotent` PASS;
  `TestPhase19R3_Postgres_PresenceUpgradeFromInt4` (isolated schema,
  old `int4` `0`/`1` with present/absent, indexes, canonical
  `component_key_hash` linkage) PASS (7.0s).
- `go vet`, `gofmt -l`, `git diff --check` clean.

## 4. Race evidence (supported non-Windows environment)

All commands run in WSL2 (Linux 6.18, Go 1.26.6, CGO=1) from the same worktree
mounted at `/mnt/c` with Linux-native `TMPDIR`/`GOCACHE`.

| Scope | Command | Result |
| --- | --- | --- |
| Focused 19.2 suite | `-race -count=1 -run TestPhase192 ./internal/infra/billingstore/` | PASS 9.429s |
| Billing core | `-race ./internal/core/billing/` | PASS 4.049s |
| Metering journal | `-race ./internal/infra/metering/journalstore/` | PASS 16.277s |
| Terminal work | `-race ./internal/infra/terminalwork/workstore/` | PASS 1.606s |
| Runtime + billing infra | `-race ./internal/core/runtime/... ./internal/infra/billingcompose/ ./internal/infra/billingadmission/ ./internal/infra/metering/journalstore/ ./internal/infra/terminalwork/workstore/` (×2 repeats) | PASS (core/runtime 68.518s; journalstore 39.790s; others <2s) |
| Full billingstore package | `-race -tags=precommit,integration -count=1 -timeout=45m ./internal/infra/billingstore/` (final run, concurrent with runtimebundle race) | PASS 1134.694s |
| Host composition (`internal/infra/runtimebundle`) | non-root Linux user, `-race -tags=precommit,integration -timeout=30m ./internal/infra/runtimebundle/` | PASS 157.868s |

No `WARNING: DATA RACE` report was emitted in any run. The failures observed
before the fixes below were assertion/lock errors, not detector reports.

## 5. Fixes found and applied (RED → GREEN)

RED evidence: `phase19-2-red.txt`.

1. **Shared-cache SQLite fixture omitted the production transaction posture.**
   `newSQLiteTestStore` used `mode=memory&cache=shared` without
   `busy_timeout`/`_txlock=immediate`, unlike every file-backed fixture and
   the production DSN in this package. Under concurrent load this produced
   `SQLITE_LOCKED` "database is deadlocked (6)" errors that exhausted retries
   (`TestPostJournalTransactionConcurrentDistinctReversalsRejectAllButOne`, two
   independent concurrent full-suite runs), and left worker polls unable to
   converge (`TestF2BConcurrentEnqueueSerialized` pending=2).
   Fix: add `_pragma=busy_timeout(5000)&_txlock=immediate` (store_test.go).
2. **Two concurrent Defer write paths were not retry-wrapped.**
   `DeferProviderCostWork` and `DeferUsageAppend` opened transactions directly
   without the package's `withAccountTx` contention retry used by every other
   account write path; with immediate transactions the shared-cache tests then
   surfaced `database table is locked (262)`.
   Fix: route both through `withAccountTxErr` (40×3ms), body extracted to
   attempt functions. RED: `ph192-final-race-billingstore.log` (two Defer
   tests). GREEN: full-package race PASS and the 20-repeat stress PASS.
3. **Claim-prefix scan starved behind a >page incomplete prefix (production
   scheduler bug, not a test-timing issue).**
   `TestSQLiteClaimCompleteCallsYieldsLargeIncompletePrefix` assumed the race
   scan would start within `completeCallIncompleteYield` (1s) of the previous
   scan's deferrals. Under the race detector the 256 bounded candidate
   transactions exceed 1s, so the prefix became eligible again and the scan
   could never reach the complete call (isolated RED, 7.96s).
   Correction (Phase 19 R2 blocker 2, supersedes the earlier hold-horizon
   test edit, which manufactured scheduler state with a direct SQL rewrite
   of production `next_claim_at` deadlines and is removed): production
   `ClaimCompleteCalls`/`listCompleteCallCandidates` now scan eligible rows
   in fair `(next_claim_at, sealed_at, call_id)` order with a matching
   keyset cursor (existing index, no schema change, no extended deadline),
   so each durably deferred incomplete row sorts behind never-deferred rows
   on later invocations; a deterministic manual claim clock
   (`DurableStore.claimNowFunc`) advances past the 1s window with no sleeps
   and no SQL mutation. RED:
   `%TEMP%/opencode/phase19-r2-claim-prefix-red.txt`
   (`TestPhase19R2ClaimPrefixFairProgressDeterministic` fails pre-fix in
   0.24s). GREEN: PASS under full-package race and repeat stress, including
   concurrent-claim and restart controls in
   `phase19_r2_claim_prefix_test.go`.
4. **Codex connector was not buildable/testable on Linux.**
   `connectors/codex/internal/codex/account_windows.go` and its `_windows_test.go`
   had a GOOS-suffixed filename with no build tag, so the package was excluded
   from Linux builds (`undefined: AccountWindowSnapshot`), which blocked the
   runtimebundle race suite and Linux connector builds. Renamed to
   `account_window_snapshots.go` / `account_window_snapshots_test.go` (content
   unchanged) and added the linter-required `t.Parallel()` to the six
   pre-existing tests that the rename surfaced in that module (2 files).
5. **Ad-hoc goroutine allowlist missing two owned billing workers.**
   The guard flagged `internal/core/billing/economic_revision_worker.go` and
   `internal/infra/runtimebundle/observation_economic_bridge.go`; both are
   process/generation-owned workers whose `Start` arms cancel/done and whose
   `Stop` cancels and joins. Added justified entries to
   `scripts/check-adhoc-goroutines.ps1` and `.sh`. Guard PASS afterwards
   (37 allowed files).
6. **Phase 19.1 file was not gofumpt-clean** (formatting only). Applied
   `gofumpt -w` to
   `internal/infra/billingstore/phase19_1_lifecycle_certification_test.go`;
   no semantic change, all Phase 19.1 tests still pass. Content hash changed
   from `8f09966b…` to `86f77221…` (formatting only).

## 6. Requirement evidence highlights

Full mapping in the TSV. Representative integrated proofs:

- 10.1 idempotent replay/identity conflict: 19.2 settlement/provider/economic
  replays plus `TestCallLegUsageReplayIdenticalIsNoopAndConflictIsIntegrityError`,
  `TestAccountingCutoverExactReplayIsIdempotent`.
- 10.3 correction appends without mutation:
  `TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates`,
  `TestCorrectionRecoverySQLiteLateProviderCorrectionAfterClosureDoesNotRebillCustomer`,
  `TestQueryEconomicDetailContinuationInvalidatesOnLateCorrections`, 19.2 late
  correction + replay.
- 10.4 terminal/DONE checkpoint, fresh BillingCallID on resume:
  `TestExecuteResumeSameALegAllocatesDistinctBillingCallIDs`,
  `TestRefinementContinuationAfterDoneUsesFreshCallState`,
  `TestRefinement82RuntimeResumeKeepsTerminalOwnership`,
  `TestRefinement43CustomerRetailSettlesEachResumedBillingCallIndependently`,
  19.2 resume tests.
- 10.5 durability failure keeps recoverable intent and never retries upstream:
  `TestRecovery174ForwardRecoveryDrainsExactlyOnceAcrossRestart`,
  `TestF1GreenCrashReopenPreservesActivation`,
  `TestRefinement43ProviderCostRevisionRollsBackAfterCrashAndRetries`,
  `TestPhase172ShadowV2CanceledContextFailsClosed`, 19.2 canceled settlement.
- 10.6 restart rebuilds identical state:
  `TestRefinement82RestartContinuesIdentitiesWithoutLossOrDuplicates`,
  `TestPhase11AllocationPersistsAcrossRestart`,
  `TestReconciliationRetentionRestartAndLegacyIsolation`,
  `TestCutoverIntegratedCrashRestartNoDuplicates`, 19.2 snapshot equality.
- 11.4 isolation/uniqueness/pagination:
  `TestAccountingCutoverStoreIsolation`,
  `TestDurableStoreCustomerUnitLedgerConcurrentVersionFencePreventsDoubleSpend`,
  `TestPostJournalTransactionConcurrentSameSourceKeyIsIdempotent`,
  `TestQueryEconomicDetailCursorTraversalExactlyOnce`, 19.2 pins snapshot.
- 13.3 idempotent balanced adjustment:
  `TestSelectedCostAdjustmentSQLiteInitialAndDownwardCorrection`,
  `TestPhase5OperatorCOGSAppliesChargeCorrectionOnce`,
  `TestCorrectionRecoveryCertifyCorrectedAggregateStatementProducesOneIdempotentDelta`,
  19.2 adjustment race + stale + late correction.
- 14.4 atomic dedupe of settlement/adjustment:
  `TestB2b1ConcurrentV1V2ContendersSingleWinner`,
  `TestB2b2ConcurrentV1V2ContendersSingleWinner`,
  `TestB2b3ConcurrentOwnersSingleWinner`,
  `TestB2b4ConcurrentSingleWinner`, 19.2 loser-callback races.
- 17.4 cutover fences one posting authority:
  `TestAccountingCutoverTwoConcurrentContenders`,
  `TestCutoverIntegratedConcurrentV1V2SingleWinner`,
  `TestF5F7_ConcurrentV1V2AfterActive`,
  `TestCutoverIntegratedActivationRacesMonetaryFamilies`, 19.2 cutover race.
- 17.5 rollback/recovery preserves postings:
  `TestRecovery174V2PostingsBlockStaleBinary`,
  `TestRecovery174ForwardRecoveryDrainsExactlyOnceAcrossRestart`,
  `TestRecovery174PendingProviderEvidencePreserved`,
  `TestRecovery174RetentionPrunePreservesLinkageAndRecovery`.
- 18.4 dual-dialect + pooler + crash: section 3 plus the fault-injection crash
  suites (`TestB2b*CrashAtBoundariesIsAtomic`,
  `TestSelectedCostAdjustmentSQLiteFaultInjectionRollsBackEachWriteBoundary`,
  `TestCutoverIntegratedCrashRestartNoDuplicates`).
- 18.6 quality/parity/wide QA/race: sections 3–4 and section 7.

## 7. QA disposition

- `make qa` → fails at `quality-checks-fast` **only** on the root-module lint
  gate. Proof of baseline ownership: `golangci-lint run --new-from-rev=HEAD`
  reports **0 issues** for the 19.2 diff in the root module and
  `connectors/codex`; the full lint output is entirely in files not modified by
  19.2 (250+ pre-existing findings). Everything else in quality-checks-fast
  passes: generated feature planes, Go formatting, module check, ad-hoc
  goroutines, regex hot-path, archtest, and `PASS: connectors/codex`.
- `make qa-tests` → failures are baseline-owned or environment artifacts:
  - `internal/archtest` budget/ratchet tests
    (`TestCriticalFileBudgets`, `TestPackageTreeBudgets*`,
    `TestLineComplexityBudgets`, `TestShrinkage_*`,
    `TestBillingFinalConvergenceLOCRatchetActive`, …): stale after Phases 5–18
    growth (already recorded as baseline-owned in the Phase 19.1 evidence at
    `fe73f55b`).
  - `internal/qa`: `TestPhase8ProducerCensusHasExplicitDisposition` (Task 18.1
    disposition text), `TestQAFastPreflight_TestCost_ArchtestGoListCallsUseDedicatedCache`
    (Phase-18 archtest file), `TestQAFastPreflight_ArchitectureBudgets`
    (ratchet drift), `TestQAFastPreflight_KiroLifecycle` (pre-existing
    `.kiro/specs/usage-economics-b-leg-usage-economics/` has no `spec.json`;
    archived `database-dialect-parity-enforcement/tasks.md` has unchecked
    tasks).
  - `tools/changesize TestRun_StagedRejectsOverLimit`: artifact of the
    `LIP_ALLOW_LARGE_CHANGE=1` environment override set for the run; the test
    reads `os.Getenv` and therefore passed with the variable unset
    (`go test -count=1 ./tools/changesize/` PASS).
  - `internal/infra/billingstore` repo-wide failures
    (`TestRefinement82ConcurrentExactReplaysShareOneIdentity`,
    `TestF1SQLiteActivationSerializedWithDirectAdjustment`,
    `TestF1GreenSelectedCostBlocksActivation`) are load-sensitive 5s
    `busy_timeout` expirations in tests with their own pre-existing DSNs and
    code paths (not the changed helper or Defer paths). All three PASS in
    isolation (3.3s) and the full tagged billingstore package PASSes solo
    (88.185s).
- `make vuln` → PASS: 0 vulnerabilities (1 uncalled in a required module).
- `make backend-plugin-release-gates-static` → PASS.
- `make test-openresponses-compliance-static` → PASS.
- `make lint` (all modules) → same root-module pre-existing debt as above.

## 8. Residual limitations

- PostgreSQL pooler evidence is available for the four repository packages
  with pooled contracts; the billing component itself has no pooler-gated
  surface in the canonical catalog.
- `metering-journal` direct PostgreSQL parity and pooled suites are GREEN
  after Phase19 R3 blocker-3 repair (section 3); no residual drift remains
  in this worktree.
- Root-module golangci-lint debt is pre-existing and not repaired in this task.
- Windows-authoritative test-cost evidence belongs to 19.3 and was not run.

## 9. Verification hygiene

- `gofmt -l` on all changed/new Go files: clean.
- `gofumpt -l` on the new 19.2 test and the reformatted 19.1 test: clean.
- `go vet` on changed packages: clean.
- `git diff --check`: clean.

## 10. Changed files

- `internal/infra/billingstore/phase19_2_restart_lifecycle_race_certification_test.go` (new)
- `internal/infra/billingstore/store_test.go` (fixture DSN parity)
- `internal/infra/billingstore/provider_cost_work_store.go` (Defer retry)
- `internal/infra/billingstore/20260829000000_billing_retire_usage_append_outbox.go` (Defer retry)
- `internal/infra/billingstore/call_usage_store_test.go` (claim-prefix determinism; R2: SQL horizon rewrite removed, manual-clock fair-progress)
- `internal/infra/billingstore/call_usage_store.go` (R2: fair next_claim_at scan order + keyset cursor, claim clock)
- `internal/infra/billingstore/store.go` (R2: claimNowFunc manual clock hook)
- `internal/infra/billingstore/phase19_r2_claim_prefix_test.go` (R2 new: deterministic RED/GREEN, concurrency, restart)
- `internal/infra/billingstore/phase19_1_lifecycle_certification_test.go` (gofumpt only)
- `connectors/codex/internal/codex/account_window_snapshots.go` (renamed from `account_windows.go`)
- `connectors/codex/internal/codex/account_window_snapshots_test.go` (renamed; +4 `t.Parallel()`)
- `connectors/codex/internal/codex/usage_aggregation_test.go` (+2 `t.Parallel()`)
- `scripts/check-adhoc-goroutines.ps1`, `scripts/check-adhoc-goroutines.sh` (allowlist entries)
- Evidence: this file, `phase19-2-red.txt`, `phase19-2-lifecycle-race-matrix.tsv`
