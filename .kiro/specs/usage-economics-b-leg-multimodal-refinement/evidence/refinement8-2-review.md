# Task 8.2 independent review

Review performed against the live `feat/b-leg-usage-economics` worktree at
`486c9caa` (`fix(journalstore): compare canonical outbox payloads`). The
upstream JSONB review is approved separately; its known `value_present`
int4/boolean parity mismatch is not attributed to Task 8.2.

## Review Verdict

- VERDICT: APPROVED
- TASK: 8.2
- BLOCKERS: none
- TESTS: Task-local focused, repeated, SQLite, and forced-live PostgreSQL suites pass; the broad runtimebundle package has intermittent existing-test failures under parallel load, and the repository-wide PostgreSQL parity sweep remains incomplete outside this task, both recorded below.
- FILES_WRITTEN: `.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement8-2-review.md` only
- RESIDUAL_RISKS: Windows provides no race-detector evidence; TTL/continuity-eviction and pre-EOF disconnect are not exercised (Host.Close is tested only as shutdown/reopen); the broad runtimebundle package has unrelated intermittent existing-test failures under parallel load; the full repository PostgreSQL parity target remains incomplete outside this task; the PG wrapper currently assumes a URL-form DSN for per-schema `search_path` (the helper's non-URL fallback is not used by this wrapper), while the forced current URL-form run passes.

## Prior blocker disposition

1. PostgreSQL restart handles: **remediated**. `refinement82_shared_revision_scenario_postgres_test.go:16-94` stores isolated schema identities and opens fresh Bun handles for every generation, while `:110-171` asserts the fresh reopen and continued writes. The forced live shared scenario and the dedicated reopen regression both pass with `LIP_REQUIRE_POSTGRES=1`.

2. Production preterminal checkpoint: **remediated**. `refinement82_preterminal_checkpoint_integration_test.go:182-342` uses a gated backend with the real `Executor`, B2BUA allocation, `ProviderEvidenceBuffer`, production sideband drain, durable journal/outbox, and stock workers. It observes revision 1 provider work and exact provider COGS while the backend is still gated, with no closure/customer settlement; releasing the gate then freezes the same B-leg through the terminal owner. No lifecycle or valuation append API is used to manufacture the terminal record.

3. Idle/disconnect/retirement proof: **remediated and narrowed**. `refinement82_resumable_session_integration_test.go:197-260` waits for provider head, provider amount, settlement, and an empty outbox before taking the no-write baseline. The repeated resume run passed ten shuffled executions. The test's `:450-452` wording explicitly limits `Host.Close` to shutdown/reopen and does not certify TTL eviction. The stream close is post-EOF; no pre-EOF disconnect or TTL behavior is claimed as covered.

4. All-payable loser overclaim: **removed**. `refinement82_revision_posting_integration_test.go:500-590` is explicitly named and documented as the non-payable, never-started loser case. It does not claim the all-payable loser policy, which remains outside this narrow Task 8.2 certification scope.

5. SQLite/PostgreSQL parity: **remediated for the task-local shared scenario**. The same `runRefinement82SharedRevisionScenario` function is called by the SQLite and PostgreSQL wrappers; SQLite parity and the forced live PostgreSQL scenario pass through revision, restart, replay, and barrier assertions. The repository-wide parity command still fails outside the task at an unrelated PostgreSQL `conversationview` timeout; the separate known `metering_components.value_present` mismatch is not used to reject this task.

The current PG run uses a URL-form DSN and passes. The wrapper ignores the
boolean returned by `postgresDSNWithSearchPath` at
`refinement82_shared_revision_scenario_postgres_test.go:32-33`, so a future
keyword-style DSN would need explicit `SET search_path` handling to preserve
the same isolation contract. This is a portability risk outside the forced
current run, not a Task 8.2 blocker in this environment. The reopen test's
comment at `:110-117` still contains the pre-fix “blocked on PG” historical
wording, but the current forced test and assertions are green.

## Lifecycle and accounting audit

- Same-A-leg continuation is exercised by `refinement82_resumable_session_integration_test.go:155-195` and `:285-338`: only continuity identity is supplied, production allocates each BillingCallID/B-leg, call 1 is closed once, call 2 uses the same A-leg with distinct IDs, and call 1's leg/closure remains immutable. No A-leg final marker is required.
- The preterminal path is a real production path, not local helper state: the gated stream blocks before terminal, `drainSidebandEvidence`/checkpoint workers produce durable provider economics, and only the stock terminal sink/worker path closes and settles the call. `refinement82_preterminal_checkpoint_integration_test.go:294-348` verifies provider-only money before closure, one customer settlement at closure, and the unchanged provider head at terminal.
- The same closed B-leg is used for late evidence. `refinement82_resumable_session_integration_test.go:355-438` verifies finalizer revision 2 and correction revision 3 through `LateEconomicAppender`, stable leg fingerprint/attempt count, chained provider work, one customer settlement, and replay stability. `refinement82_revision_posting_integration_test.go:296-439` verifies exact +125, +7, and +2 provider postings, reversal/correction linkage, restart persistence, customer 310 exactly once, and balance 9690.
- The store-only scenario is complementary rather than the sole lifecycle proof. `refinement82_revision_restart_concurrency_test.go:107-269` covers preterminal/terminal/postterminal fence and idempotency details; the runtimebundle tests cover Executor ownership and real worker money. The preterminal integration test's comment calls its late finalizer “rev3” (`:351`), but the actual finalizer is revision 2 (`:367`); the separate posting integration test exercises the actual revision-3 correction (`:382-390`). This is a documentation mismatch, not an uncovered required stage.
- Retry coverage is deliberately bounded to the non-payable loser. The test checks two recorded attempts, winner-only provider COGS, one customer settlement, and loser diagnostics; no all-payable policy is inferred.
- `refinement82_revision_restart_concurrency_test.go:392-407` uses ready/start barriers, indexed result collection, and no sleeps. The five barrier tests (`:422-799`) check exact replay, one claim winner/fence, strict-superset versus incomparable revision fencing, claim/correction interleave, out-of-order convergence, durable heads, work states, and valuation counts. Every goroutine result is collected and checked.

## Fresh mechanical evidence

All commands below were run after the current worktree was inspected. The
working tree had only the expected untracked Task 8.2 test/evidence files; no
tracked production diff was introduced by this review.

- `go test -count=1 -shuffle=on -timeout=240s ./internal/core/billing/ ./internal/infra/billingstore/ ./internal/infra/runtimebundle/ -run '^TestRefinement82'`: **PASS** (`billing` 0.896s, `billingstore` 18.464s, `runtimebundle` 10.310s).
- `go test -count=5 -shuffle=on -timeout=300s ./internal/infra/runtimebundle/ -run '^TestRefinement82RuntimePreterminalCheckpointAdvancesProvider$'`: **PASS** (31.990s).
- `go test -count=10 -shuffle=on -timeout=300s ./internal/infra/runtimebundle/ -run '^TestRefinement82RuntimeResumeKeepsTerminalOwnership$'`: **PASS** (64.291s).
- `go test -count=5 -shuffle=on -timeout=300s ./internal/infra/runtimebundle/ -run '^(TestRefinement82ProviderRevisionPostingsAdvancePerStage|TestRefinement82RetryLoserExcludedFromRetailWinnerPostsCOGS)$'`: **PASS** (34.946s).
- `go test -count=3 -shuffle=on -timeout=360s ./internal/infra/runtimebundle/ -run '^TestRefinement82'`: **PASS** (30.378s), confirming the complete Task 8.2 runtimebundle subset is stable under repeated shuffled execution.
- `go test -count=10 -shuffle=on -timeout=300s ./internal/infra/billingstore/ -run '^TestRefinement82Concurrent'`: **PASS** (83.754s).
- `go test -count=3 -shuffle=on -timeout=240s ./internal/infra/billingstore/ -run '^(TestRefinement82SharedRevisionScenarioSQLite|TestRefinement82SharedHarnessReopenYieldsFreshHandles)$'`: **PASS** (26.356s).
- `go test -count=1 -timeout=240s ./internal/core/runtime/... ./internal/core/billing/...`: **PASS**.
- `go test -count=1 -timeout=300s ./internal/infra/billingstore/ ./internal/infra/metering/journalstore/ ./internal/core/metering/...`: **PASS** (`billingstore` 71.930s; `journalstore` 7.285s; core metering packages pass).
- `$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration -run '^TestRefinement82SharedRevisionScenarioPostgresDirect$' -count=1 -timeout=240s ./internal/infra/billingstore/ -v`: **PASS** (test 16.59s; package 16.681s), including rev1/rev2/rev3/restart/rev4 and concurrency assertions through the shared function.
- `$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration -run '^TestRefinement82PostgresReopenWithoutOutbox$' -count=1 -timeout=180s ./internal/infra/billingstore/ -v`: **PASS** (test 11.91s; package 11.990s), with fresh generation handles and continued writes.
- `make test-db-parity-sqlite`: **PASS**; all ten SQLite components pass.
- `make GO_TEST_FLAGS="-count=1 -timeout=90s" test-db-parity`: **INCOMPLETE/FAIL outside Task 8.2** after SQLite and multiple PostgreSQL components; the bounded run timed out in existing `internal/infra/conversationview` `TestDBParity_PostgresDirect/TagCap4096`. The separate known `metering_components.value_present` int4/boolean mismatch is also baseline and untouched by this task.
- `go test -count=1 -timeout=360s ./internal/infra/runtimebundle/`: one fresh broad-package run **failed outside Task 8.2** in existing `TestRefinement4StockObservationToEconomicSettlement` (`refinement4_stock_economic_integration_test.go:185`, pending provider revision 2). The exact test passed alone (4.19s), and a whole-package rerun passed (37.238s). A subsequent `-count=3` broad-package run exposed a different existing `TestBackendResourcePoolCloseCanceledWaiterAndPendingBuilderNoLeak` goleak failure; that exact test also passed alone. The existing test sources are unchanged relative to `b77fec4d`; all Task 8.2 tests pass repeatedly. These are intermittent baseline/upstream package-load issues, not Task 8.2 failures.
- `make test-race`: **Windows canonical SKIP**: Go race evidence is unsupported on Windows; Linux CI remains required.
- `go vet ./internal/core/billing/... ./internal/core/runtime/... ./internal/infra/billingstore/... ./internal/infra/metering/journalstore/... ./internal/infra/runtimebundle/...`: **PASS**.
- `go vet -tags=integration ./internal/infra/billingstore/... ./internal/infra/metering/journalstore/...`: **PASS**.
- `go test -tags=integration -run '^$' -count=1 -timeout=120s ./internal/infra/billingstore/ ./internal/infra/metering/journalstore/`: **PASS** (tagged compile).
- `gofmt -l` over all seven Task 8.2 Go files: **CLEAN**; `git diff --check`: **PASS**.
- Placeholder scan over Task 8.2 Go files: **CLEAN**; the evidence-only matches are self-referential scan wording. Secret scan: **CLEAN**.

## Requirements and boundary result

Requirements 3.1-3.6 and 4.1-4.5 are covered by the production runtime
tests and the durable revision/store complements: accounting does not wait
for A-leg finality, terminal ownership closes one B-leg, continuation gets
new call/leg identities, provider economics can advance before closure, and
late evidence uses the same durable identity. Requirements 6.3 and 6.4 are
covered by the real Executor resume/preterminal tests plus the exact-money
revision/posting test. The runtime integration actions do not use synthetic
lifecycle rows, stream-time financial writes, or a report dependency; the
store-only companion intentionally creates explicit rows as a persistence and
fence fixture. No A-leg final marker, production source change, task status
change, or scope spill was introduced by Task 8.2 or this review.

The RED phase is **VERIFIED** as a mechanically recorded pre-change coverage
gap: the Task 8.2 tests did not exist before the certification work. It is not
being presented as a pre-change production failure.
