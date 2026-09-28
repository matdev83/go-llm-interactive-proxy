# Refinement 8.2 execution: resumable-session and incremental-revision certification

Status: `READY_FOR_REVIEW_REFINEMENT_8_2` (A-E complete; the former
sub-pass E PG block is resolved — the upstream journalstore JSONB fix is
approved and committed at `486c9caa`, and the forced live-PG shared scenario
now PASSES with the same assertions as SQLite. Task 8.2 is ready for
independent review, not complete/approved yet.)

## Scope

This record certifies Kiro Task 8.2 (`Run resumable-session and
incremental-revision certification`) for spec
`usage-economics-b-leg-multimodal-refinement`, requirements 6.3, 6.4, 3.1,
3.2, 3.3, 3.4, 4.1, 4.2, 4.3, 4.4, 4.5.

Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`,
branch `feat/b-leg-usage-economics`, HEAD `486c9caa`
(`fix(journalstore): compare canonical outbox payloads` — the approved
upstream JSONB fix; the stashed Task 8.2 certification files are restored
on top of it).

Out of scope (untouched): Task 5.3 blocker, named stashes (never
inspected/applied/dropped), A-leg report code, task checkboxes/spec status,
commits/staging, production code, migrations, public APIs. Task 8.1 committed
tests/evidence preserved as-is.

This is a certification/test task. No production `*.go` file was changed by
this task itself; the upstream production fix it was blocked on landed
separately as approved commit `486c9caa` (canonical fingerprint/outbox
validation, reviewed and committed by the owning track).

Sub-pass A (review remediation, lifecycle finding only): the initial
submission's lifecycle certification manually constructed BillingCallIDs,
B-legs, and call/leg rows and called `AppendCallLegUsage`/`AppendCallUsage`
directly, bypassing `Executor` terminal ownership and `TerminalUsageSink`
(review finding 1). Those two simulation files were REMOVED and replaced with
one stock-host integration test through the real production seams (details in
`Sub-pass A` below).

Sub-pass B (review remediation, posting finding only): the revision
certification stopped at valuation heads and never asserted provider/customer
postings, journals, balances, or worker completion (review finding 2). Two
stock-host money tests were ADDED through the real relay/worker/posting path
with literal fixture amounts (details in `Sub-pass B` below).

Sub-pass C (review remediation, concurrency + parity findings only): the
remaining concurrency case launched goroutines with a bare WaitGroup and
wrote valuation results serially, and no shared SQLite/PostgreSQL scenario
existed (review findings 3-4). The serial case was REPLACED with five
barrier-gated tests over the real claim/fence/result/append APIs, and the
durable revision/restart/concurrency assertions were EXTRACTED into one
shared scenario with a SQLite wrapper (default) and a PostgreSQL-direct
wrapper (integration tag, explicit SKIP without DSN). Details in `Sub-pass
C` below. No production change in any pass.

## Requirement-to-test matrix

Prior state: resume and late-evidence vectors were proven in isolation
(5.1 runtime allocator + billingstore lifecycle/settlement/TTL; 5.2 durable
late-evidence store + stock runtime integration; 4.1 pre-terminal
checkpoints; 4.2 revision worker; 4.3 settlement policy) but no executable
test combined same-A-leg sequential BillingCallIDs with pre-terminal,
terminal, and post-terminal checkpoints across a restart plus concurrent
late revisions asserting zero duplicate economics and zero A-leg finality
dependency. RED inventory at clean HEAD confirmed zero `TestRefinement82`
coverage in all three packages.

Certification files (all test-only; production seams reused unchanged):

- `internal/infra/runtimebundle/refinement82_resumable_session_integration_test.go`
  (package `runtimebundle_test`, sub-pass A, [D] quiescence-hardened):
  full lifecycle through the stock host — real `Executor.Execute`/B2BUA
  attempt allocation, production-allocated BillingCallID/B-leg, usage via
  the real `TerminalUsageSink` (`billing.DurableStore` wired in
  `ComposeBilling`), same-A-leg resume via production session continuation,
  post-terminal finalizer/correction via `LateEconomicAppender` + stock
  relay/workers, host shutdown via `Host.Close` (shutdown/reopen, NOT TTL
  retirement). The test never calls
  `AppendCallLegUsage`/`AppendCallUsage`/`AppendEconomicRevisionResult`.
- `internal/infra/runtimebundle/refinement82_preterminal_checkpoint_integration_test.go`
  (package `runtimebundle_test`, [D]): REAL preterminal checkpoint proof — a
  gated backend stream holds a live B-leg open while its provider V2 usage
  travels ProviderEvidenceBuffer -> `drainSidebandEvidence` ->
  `queueEconomicCheckpoint` -> first-flush-immediate journal+outbox append ->
  stock relay/workers (rev1: no closure, provider head/posting exist,
  customer untouched); gate release terminalizes the SAME B-leg
  (rev2: closure frozen, head replay-stable, retail settles once); then a
  post-terminal finalizer advances the head with a chained delta while
  lifecycle stays closed (rev3). No direct mutation APIs in the integration
  action; store readers only inspect results.
- `internal/infra/runtimebundle/refinement82_revision_posting_integration_test.go`
  (package `runtimebundle_test`, sub-pass B, [D] narrowed): stage-by-stage money proof
  through the stock relay/valuation/provider-cost/settlement workers —
  rev1 posts exactly +125 COGS with a 125/rev1 cost head, retail unposted
  pre-claim while provider advanced, exactly one 310 customer settlement at
  closure (balance 9690), rev2 posts only the +7 delta chained, host restart
  preserves cursors/fences/journals/balances, rev3 posts only the +2 delta
  chained, exact replay creates no duplicate; plus a stock failover call
  proving loser-excluded retail (one 310) with winner-only COGS (one 125
  on the winner B-leg; never-started loser recorded with zero payable
  charge and no journal — no claim about provider-payable losers, which
  belong to retail-selector scope).
- `internal/core/billing/refinement82_resumable_revision_policy_test.go`
  (package `billing`, unchanged in sub-pass A): domain key/fence/queue-isolation
  certification, reusing `NewObservationEconomicWorkBuilder`,
  `CompareEconomicEvidenceSets`, `CallLegUsageKey`, `Seal`/`CheckCallLegUsageReplay`,
  and the existing `bridgeTestObservation`/`bridgeHash` fixtures.
- `internal/infra/billingstore/refinement82_revision_restart_concurrency_test.go`
  (package `billingstore`): durable pre-terminal/terminal/correction serial
  proof (kept) plus [C] five barrier-gated concurrency tests replacing the
  serial WaitGroup-only case: 16-way exact-replay burst (single identity,
  all-nil, one head version, one valuation), 8-way worker-claim race
  (exactly one lease winner, foreign complete fails closed, single
  completion, no re-acquire), established-head superset-vs-incomparable race
  (deterministic nil + fence, winner head, winner-only row, stale subset
  stable), claim-plus-correction interleave (one lease holder, both results
  nil, convergent rev-3 head), out-of-order rev2/rev3 race (both nil,
  convergent rev-3 head).
- `internal/infra/billingstore/refinement82_shared_revision_scenario_test.go`
  (package `billingstore`, [C]): dialect-portable shared scenario
  (rev1/2/3 + replays + restart readback + rev4 correction + replay burst +
  claim race + fence race, all via public readers, no hand-rolled SQL/Tx)
  plus the default SQLite wrapper on file-backed stores.
- `internal/infra/billingstore/refinement82_shared_revision_scenario_postgres_test.go`
  (`//go:build integration`, package `billingstore`, [C]): PostgreSQL-direct
  wrapper running the SAME scenario on isolated schemas via existing
  helpers; compiles always, SKIPs explicitly without a DSN, and now PASSES
  on live PG with a DSN (see closeout below). No dbparity
  catalog change (billing + metering-journal already registered).

REMOVED in sub-pass A (manual-lifecycle bypass, review finding 1):

- `internal/core/runtime/refinement82_resume_certification_test.go`
  (allocated B-legs from `b2bua.NewMemoryStore`, invented BillingCallIDs,
  sealed records itself; never invoked `Executor`/terminal owner/sink).
- `internal/infra/billingstore/refinement82_resumable_revision_certification_test.go`
  (called historical `AppendCall*` persistence APIs directly instead of the
  runtime `TerminalUsageSink` handoff).

| Required case | Certifying test(s) | What is asserted |
|---|---|---|
| 1. One A-leg, call1 (BillingCallID 1, DONE/terminal), later call2 (new BillingCallID/B-legs); no A-leg finality prerequisite; call1 immutable | `TestRefinement82RuntimeResumeKeepsTerminalOwnership` (runtimebundle, sub-pass A) + `TestRefinement82BillingCallIDIsInvocationBoundary` (billing, domain keys) | call1 executes via real `Executor` (only A-leg continuity supplied; allocator creates BillingCallID/B-leg); DONE via drain-to-EOF; call1 settles with no A-leg final marker; call2 resumes SAME A-leg via production `ResumeToken`/`ALegID` continuation with distinct allocator-created BillingCallID/B-leg; call1 closure fingerprint + leg fingerprint unchanged; B2BUA attempts 1 -> 2 |
| 2. Pre-terminal authoritative checkpoint durable, advances provider/COGS accrual under revision/fence rules | [D] `TestRefinement82RuntimePreterminalCheckpointAdvancesProvider`: gated live B-leg emits V2 usage through the real drain/queue/first-flush/outbox seam; asserts zero closures, head rev1 + one +125 journal on the still-open B-leg, zero customer settlements, opening balance intact | prior store-API rev-1 delivery superseded; [B] terminal-evidence worker proof retained as complement |
| 3. Terminal checkpoint finalizes current revision without duplicates | [B] same test, rev-2 stage (finalizer via real sideband + stock workers) | only the +7 delta posts with reversal/correction linkage; cost head 132/rev2; customer settlement and balance byte-stable |
| 4. Post-terminal correction references SAME closed B-leg, fenced delta, no reopen, no replacement B-leg | [B] same test, rev-3 stage across host restart (rebuilt host + workers) | only the +2 delta posts chained; cost head 134/rev3; one fingerprint-stable leg; customer/balance stable; exact replay stable |
| 5. Restart between checkpoints continues same identities/revisions | [C] shared scenario restart on both dialects (same assertions): close + reopen same identity → heads/legs/observations/outbox/valuations byte-stable → post-restart rev-4 correction advances to head v4 | file-backed SQLite verified; PG verified live (forced run PASS, see closeout) |
| 6. Concurrent late revisions: exactly-once winner, idempotent replay, stale/conflicting classified, no double entries | [C] five barrier-gated tests (serial WaitGroup-only case deleted): 16-way replay burst (single identity, all nil, one head version/row); 8-way claim race (one winner, fail-closed completion); established-head fence race (nil + fence, winner head, winner-only row, subset stable); claim/correction interleave (one holder, convergent rev3); out-of-order race (convergent rev3) | every error indexed/collected; no assumed winner except where the contract guarantees it (superset advance, lease exclusivity) |
| 7. Idle/disconnect/host-shutdown create no economic effect; later resumed call works | Sub-pass A (+[D] quiescence): DONE (drain EOF) + disconnect (stream Close) + idle quiescence + host shutdown (`Host.Close`) zero-write snapshots inside `TestRefinement82RuntimeResumeKeepsTerminalOwnership`; resumed call works (case 1, same test, before shutdown) | idle: B-leg obs count, legs, closures, journal tx count, balance/version, outbox empty, provider+customer work IDs all stable after head/posting/settlement quiescence waits; shutdown: same snapshots stable across `Host.Close` + durable reopen. TTL/eviction retirement remains UNCOVERED (not certified): in-memory continuity has no clock control; no production change made for it |
| 8. Call closure local: expected B-legs freeze per BillingCallID | Sub-pass A: call1 `ExpectedBLegIDs == [leg1]` asserted from the real terminal handoff; call2 closure holds only its new B-leg; call1 rows fingerprint-stable after call2 | production terminal owner froze both sets; no test-side sealing involved |
| 9. Retail waits for stable call-level selection; provider advances per revision; neither waits for A-leg finality | [B] timing wedge + `TestRefinement82RetryLoserExcludedFromRetailWinnerPostsCOGS` ([D] narrowed; stock failover bad→good): 2 lifecycle legs (never-started + winner), exactly one +125 COGS journal on the winner B-leg (never-started loser recorded with zero payable charge and no journal), exactly one TurnID-linked 310 customer settlement, balance 9690 | pre-drain zero everything; pre-claim zero retail while provider advanced (when window observable); no provisional debits (store rejects non-appended closures); queue isolation in billing domain test. No all-payable-loser claim: provider-payable failed/loser scope belongs to retail-selector work, not Task 8.2 |
| 10. SQLite + PostgreSQL parity with same assertions where harness supports | [E, closed out] `make test-db-parity-sqlite` PASS; shared scenario SQLite PASS; PG reopen-without-outbox PASS on live PG; forced live-PG full shared scenario now PASSES with the same assertions after approved upstream fix `486c9caa` (historical pre-fix RED: PG full scenario FAILED at the first outbox append with the production JSONB collision, pre-restart, identically before/after the harness fix); PG-direct SKIP wrapper retained for no-DSN environments; no new DB infrastructure, no catalog change, no schema/migration delta by this task | full `make test-db-parity` not completable locally; known `value_present` baseline untouched by either change (journalstore PG-direct parity still reports the pre-existing `metering_components.value_present` int4-vs-boolean mismatch; repo-wide gate not run to completion here) |

Properties covered: BillingCallID is the invocation boundary; A-leg is
open-ended continuity; B-leg execution terminalizes exactly once while
economic evidence remains appendable; revision/fence/idempotency keys prevent
duplicates across replay, restart, and concurrency; no stream-time
rating/journal writes (all appends go through durable journal/store/work
seams); unknown/partial evidence stays incomplete (supersession and fence
rules reject unclassifiable branches rather than coercing); provider/COGS and
customer retail timing/selection remain separate (queue isolation + call-level
settlement).

CORRECTED OVERCLAIM (sub-pass A): the initial submission stated "real
durable writers/readers and authoritative service seams throughout, no local
simulations". That was wrong for the lifecycle vectors: two files manually
constructed IDs/records and called historical persistence APIs, bypassing
`Executor` terminal ownership and `TerminalUsageSink`. Those files are
deleted; lifecycle cases 1/7/8 are now proven only through the stock-host
integration test above, which never calls terminal append APIs directly. [B]
The worker posting/balance proof is now certified at stock level by the two
sub-pass B money tests (literal 125/7/2/134 COGS chain, one 310 settlement,
9690 balance, replay/restart stability). The revision/restart/concurrency
store file itself still stops at valuation/head/outbox assertions and keeps
serial writer schedules — deterministic barriers deferred to sub-pass C per
review finding 3.

## RED / GREEN

RED (before additions, clean HEAD `b77fec4d`):

```text
go test ./internal/core/billing/ ./internal/infra/billingstore/ ./internal/core/runtime/ -run 'TestRefinement82' -count=1
# ok billing [no tests to run] -> zero certification coverage
# ok billingstore [no tests to run] -> zero certification coverage
# ok runtime [no tests to run] -> zero certification coverage
```

GREEN (initial submission; lifecycle rows since REPLACED by sub-pass A —
see `Sub-pass A` section; no production edit):

```text
go test ./internal/core/billing/ -run 'TestRefinement82' -count=1 -v
# PASS: 3 tests (kept file)
go test ./internal/infra/billingstore/ -run 'TestRefinement82' -count=1 -v
# PASS: 5 tests at the time (2 resume tests since REMOVED with their file;
# revision/restart/concurrency tests kept for B/C)
```

## Sub-pass A: real runtime terminal ownership (review finding 1)

Production path traced with CodeGraph and reused unchanged
(`Executor.Execute` in `internal/core/runtime/executor.go:116`, B2BUA
attempt/session allocation, `billing.TerminalUsageSink` in
`internal/core/billing/append.go:17`, `TerminalUsageSink: store` wiring in
stock `ComposeBilling`, `ClaimCompleteCall` host-loop stand-in,
`LateEconomicAppender` + stock observation relay/workers from Task 5.2):

- `executor.Execute` -> route plan -> attempt open/B-leg allocation ->
  `ProviderEvidenceBuffer` drain -> atomic `MeteringObservationSink` ->
  durable journal -> terminal owner seals `CallLegUsageRecord`/
  `CallUsageRecord` via `TerminalUsageSink.AppendLeg`/`AppendCall` ->
  exposure closure -> `ClaimCompleteCall` settlement.
- Late evidence: `LateEconomicAppender` (closed-leg reader + atomic sink) ->
  observation outbox -> stock relay -> provider/customer revision workers ->
  valuation heads / provider-cost heads / journals.
- `Host.Close` is the real lifecycle retirement/shutdown API; the host owns
  the store handles, so post-close reads reopen the same files via
  production constructors.

The test reuses the existing Task 5.2 integration harness verbatim
(`openRefinement52ConcurrentBillingStore`, `seedBillingHostLoopCatalog`,
`ComposeBilling`, `writeRefinement52MeteredConfig`, `BuildHost`,
`hostActiveExecutor`, `injectRefinement52AuthenticUsageBackend`,
`drainBillingHostLoopStream`, `waitBillingHostLoopCall`,
`waitRefinement4StockOutboxDrained`, `refinement52RuntimeProviderAuthority`,
`refinement52RuntimeObservation`, `refinement52ExpectedProviderWork`,
`waitRefinement52StockHeadExact`); the only new helper is
`waitRefinement82SettledCalls` (two-record settled-call waiter in the same
polling idiom). Mocks observe nothing and replace nothing: the backend
fixture only supplies provider usage bytes through `ProviderEvidenceBuffer`;
all IDs, B-legs, closures, heads, and journals are production-created.

RED (sub-pass A):

```text
go test ./internal/infra/runtimebundle/ -run 'TestRefinement82RuntimeResumeKeepsTerminalOwnership' -count=1
# FAIL: refinement82_resumable_session_integration_test.go:459:
#   billingstore: list call-leg usage: sql: database is closed
```

Genuine lifecycle gap, not a test typo: `Host.Close` owns and closes the
store handles, so post-retirement assertions cannot reuse live handles. The
minimal test-side fix reopens the same durable files through production
constructors (`openRefinement52ConcurrentBillingStore`,
`openRefinement52RuntimeJournal`), which additionally proves the identities
survive restart. No production defect; no production edit.

GREEN (sub-pass A):

```text
go test ./internal/infra/runtimebundle/ -run 'TestRefinement82RuntimeResumeKeepsTerminalOwnership' -count=1 -v
# PASS (5.26s)
go test ./internal/infra/runtimebundle/ -run 'TestRefinement82RuntimeResumeKeepsTerminalOwnership' -count=3
# ok (15.793s)
```

What the GREEN run proves through production seams: call1 `BillingCallID`
and B-leg created by the allocator (test supplies only A-leg continuity);
provider authority observation present in the terminal leg row (reached
`TerminalUsageSink` via production drain); `ExpectedBLegIDs == [leg]`;
B2BUA attempts 1; DONE/idle/disconnect zero-write snapshots stable;
call2 same-`ALegID` with distinct allocator-created `BillingCallID`/B-leg
and call1 fingerprints stable; B2BUA attempts 2; finalizer rev 2 and
correction rev 3 (exact supersession) advance the provider head rev 2 -> 3
with the late identities present in the work inputs; legs still 1 row with
identical fingerprint/`BLegID`/`AttemptSeq`; attempts still 2 (no
replacement B-leg, no reopen); exact replay leaves head
fingerprint/version/fence unchanged; `Host.Close` host shutdown leaves legs
(both calls), closures, journal transactions, balance/version, journal
observations, and provider head unchanged across durable reopen.

[D] Quiescence hardening: the zero-write snapshots now run only after exact
durable quiescence (provider rev-1 head + 125 current amount + closed
exposure + one customer settlement + drained outbox), because claiming
settles asynchronously and snapshotting earlier races the settlement
journal/balance writes (reviewer-observed line-246 flake; mechanism
confirmed, fixed test-side, stability re-proven `-count=10`).

## Sub-pass D: real preterminal checkpoint + quiescence + narrowed claims

D1 — production preterminal path traced (no production change):
`drainSidebandEvidence` runs in the Recv loop -> `consumeBackendUsageEvidenceForAttempt`
drains `ProviderEvidenceBuffer` -> `rememberEconomicEvidenceOnce` ->
`queueEconomicCheckpoint` -> `flushEconomicCheckpoints(force=false)` fires
immediately on first drain (`checkpointLastFlush` zero) ->
`batchSink.AppendObservations` == stock `NewObservationSinkWithOutbox`
(wired by `configureObservationEconomicBridge`, replacing the plain sink) ->
journal row + outbox row -> stock relay/workers. `TestRefinement82RuntimePreterminalCheckpointAdvancesProvider`
holds a gated backend stream open (prefix events, terminal blocked) and
proves, in order: rev1 checkpoint durable with zero closures, provider head
rev1 + one +125 journal on the still-open B-leg, zero customer settlements,
opening balance intact; gate release terminalizes the SAME B-leg (closure
frozen, head fingerprint/version replay-stable, retail settles exactly once
to 310/9690); post-terminal finalizer advances the head with a chained
delta while the leg row stays fingerprint-stable. No direct mutation APIs in
the integration action.

RED (sub-pass D): (1) the reviewer-observed line-246 idle flake (settlement
journal/balance landing after the claim-side snapshot); reproduced by
mechanism, fixed by quiescence waits, re-proven `-count=10`. (2) First D1
run timed out finding the checkpoint: `ListObservations{StoreID}` alone is
`query too broad` — fixed with the test-controlled `ProviderAccountKey`
scope. (3) Second D1 run proved the terminal rating path is genuine:
without in-stream V1 token evidence the call is unrateable and exposure
never closes (worker retries with backoff); the gated prefix now carries
the token usage event (V1 fallback, no accounting identity) while the V2
charge observation stays the sole provider-charge source. No production
defect in any RED; no production edit.

GREEN (sub-pass D):

```text
go test ./internal/infra/runtimebundle/ -run 'TestRefinement82RuntimePreterminalCheckpointAdvancesProvider' -count=1 -v
# PASS (4.21s)
go test ./internal/infra/runtimebundle/ -run 'TestRefinement82RuntimePreterminalCheckpointAdvancesProvider' -count=5
# ok (22.292s)
go test ./internal/infra/runtimebundle/ -run 'TestRefinement82RuntimeResumeKeepsTerminalOwnership' -count=10
# ok (51.646s)
```

D2 — quiescence fix described above; no assertion weakened (same zero-write
comparisons, strictly later snapshot point).

D3 — narrowed claims: `Host.Close` is documented as host shutdown/reopen
everywhere (not TTL retirement; TTL/eviction remains uncovered, not
certified); the retry test is renamed to
`TestRefinement82RetryLoserExcludedFromRetailWinnerPostsCOGS` with the
all-payable-loser claim removed (never-started loser proves nothing about
provider-payable losers; that scope is not Task 8.2); no Task 4.3
certification borrowing remains in tests or evidence.

## Sub-pass B: real posting/worker/journal/balance proof (review finding 2)

Production path traced through the stock harness seams (posting/fence
contracts as implemented, not borrowed as external proof):
observation outbox -> stock relay (`observation_economic_bridge.go`) ->
valuation worker -> provider-cost revision worker (posting fence
`(store_id, account_id, call_id, lineage_key)`, execution fence on the plain
B-leg key, per-head CAS) -> `provider_call_cogs` journals
(debit `inference_provider_cogs` / credit `provider_payable_clearing`,
signed `current - previous` deltas, `ReversalOf`+`CorrectsTransactionID`
chain) and `billing_provider_cost_heads` (no customer-account lock, no
balance mutation); customer plane via `ClaimCompleteCall` ->
`ApplyCallBillingResult` (rejects non-appended closures; no provisional
debits) -> one `customer_call_settlement` (debit `customer_financial_account`
/ credit `usage_revenue`) at stable call closure. Literal fixture tariffs
(from `billing_host_loop_test.go` constants, never from production output):
opening 10000, customer 310 = 100 input + 200 output + 10 fixed, operator
125 = 50 input + 75 output, finalizer charge 7, correction charge 9
(delta +2), USD throughout.

New file `refinement82_revision_posting_integration_test.go` reuses the
sub-pass A harness verbatim (file-backed stores, `ComposeBilling` with
`TerminalUsageSink: store`, `BuildHost`, authentic V2 usage backend,
`LateEconomicAppender`, stock relay/workers, existing wait/read helpers);
the only new helper is the shared setup plus small literal-assertion
helpers. No fake posting calculator exists anywhere in the file.

RED (sub-pass B): prior `TestRefinement82*` files contained zero
`provider_call_cogs` / `customer_call_settlement` / amount assertions
(grep-verified: `JournalTransactions`/`GetAccount` were used only for
count/equality absence-of-write checks) — the posting proof genuinely did
not exist. Test-first execution then exposed three real sequencing truths,
all fixed test-side with no production change:

1. Pre-output loser legs DO carry local diagnostic boundary observations
   (`Origin:local`, no `Charges`, no provider charge identity) — the
   assertion was narrowed to the economically meaningful property (no
   payable charges on the loser).
2. The provider-cost journal for the failover winner needed the production
   worker-completion wait (`waitBillingHostLoopProviderCost`); asserting
   immediately after closure races the worker.
3. Valuation-head advancement does not imply posting completion: rev2/rev3
   journal assertions must wait on `waitRefinement4StockProviderCurrentAmount`
   (132/134), not just the valuation head.
4. Repeat runs proved host-loop settlement is autonomous, not test-gated:
   claiming while the exposure is still open risks wedging the settlement
   wait, and a background settle can precede the test claim. The test now
   follows the harness protocol exactly (claim only after exposure closes
   via `waitBillingHostLoopCall`) and observes the retail-timing rule
   without racing the worker: pre-drain zero closures/journals/settlements
   (no speculative posting, deterministic), provider rev-1 head durable
   pre-claim with zero customer settlements whenever the exposure is still
   open (ordering window, logged either way), then exactly one TurnID-linked
   310 settlement per sealed closure (structural proof).

GREEN (sub-pass B, historical commands shown with the pre-[D]-rename test name):

```text
go test ./internal/infra/runtimebundle/ -run 'TestRefinement82ProviderRevisionPostingsAdvancePerStage|TestRefinement82RetryLoserExcludedFromRetailIncludedInCOGS' -count=1 -v
# PASS both (4.91s, 3.81s)
go test ./internal/infra/runtimebundle/ -run 'TestRefinement82ProviderRevisionPostingsAdvancePerStage|TestRefinement82RetryLoserExcludedFromRetailIncludedInCOGS|TestRefinement82RuntimeResumeKeepsTerminalOwnership' -count=5
# ok (30.929s)
```

What the GREEN runs prove through production readers after every stage:
rev1 = one +125 COGS journal (winner B-leg, USD, exact ledgers) + cost head
125/rev1 + zero customer settlements pre-claim + one 310 settlement at
closure + balance 9690; rev2 = only the +7 delta journal chained by
reversal/correction IDs + cost head 132/rev2 + customer settlement and
balance byte-stable; restart = journals/heads/balances/fences stable across
host rebuild; rev3 = only the +2 delta chained + cost head 134/rev3 +
lifecycle still one fingerprint-stable leg + customer/balance stable; exact
replay = journal IDs/fingerprints, heads, balances, exposures, customer
settlement all stable. Failover call = 2 lifecycle legs (never-started +
winner), exactly one +125 COGS journal on the winner B-leg, exactly one 310
customer settlement, balance 9690. The general all-payable
failed/retry/loser inclusion rule through the real revision worker and fence
remains certified at domain/store level by Task 4.3
(`refinement4-3-operator-provider-cost.md`); sub-pass B cites it rather than
rebuilding a payable-loser provider.

## Sub-pass C: deterministic concurrency + shared dialect proof (review findings 3-4)

C1 — barrier tests. `refinement82ReleaseBarrier` (ready channel per
goroutine, single start gate closed after all N readies, indexed error
collection, `WaitGroup` join, no sleeps) drives every concurrent case over
the real production APIs: `LateEconomicAppender`, `AppendEconomicRevisionResult`,
`Claim`/`CompleteEconomicRevisionWork`, `GetEconomicValuationHead`,
`ListValuations`, journal append/readback. No production algorithm is
duplicated in helpers. Outcome sets (barrier suite `-count=10` stable):

- 16-way exact-replay burst: 16 nil, 1 observation, 1 outbox row, then
  8-way identical result race: 8 nil (writer serialization + idempotent
  replay probe), head v1, 1 valuation row.
- 8-way claim race: exactly one lease winner (nonzero fence), 7 held-out;
  foreign-owner complete is claim-lost; winner completes once; second
  complete is claim-lost; post-completion claims all held-out.
- Established-head superset-vs-incomparable race: superset nil, fence error,
  head v2 with superset hash, 2 valuation rows (base + superset; fenced
  branch rolled back), stale subset stable.
- Claim-plus-correction interleave: one lease holder, both result appends
  nil, convergent rev-3 head, completed rev-2 lease.
- Out-of-order rev2/rev3 race: both nil, convergent rev-3 head either way.

RED (sub-pass C): grep proved the prior file had no start barrier, no claim
race, and no PG wrapper. Test-first runs then corrected two wrong
expectations with no production change: (1) concurrent identical result
appends do NOT conflict — all return nil via writer serialization plus the
idempotent-replay probe (single head version, single valuation row), so the
{1 + 7 conflicts} hypothesis was replaced by the observed mechanism;
(2) `ListValuations` with only `StoreID` fails as `metering: query too
broad` — the portable form scopes `SubjectKind`+`SubjectID`.

C2 — shared scenario. `runRefinement82SharedRevisionScenario` runs
rev1/2/3 + exact replays (heads v1/2/3, 3 valuations, 3 observations, 2
outbox rows, sealed leg stable), restart (close + reopen same identity:
heads/legs/observations/outbox/valuations byte-stable), post-restart rev-4
correction (head v4, 4 valuations, 3 outbox rows, leg stable), 8-way replay
burst (stable), 8-way claim race (one winner, completed), and the
superset-vs-fence race on a fresh subject (winner head, 6 valuations total,
leg stable) — all through public readers with no hand-rolled SQL or
transactions, so the stores own their (dialect-specific) isolation.
Transaction isolation matches production by construction: the scenario never
opens its own transaction.

- SQLite wrapper (`TestRefinement82SharedRevisionScenarioSQLite`, default):
  file-backed stores, real migrations → PASS (1.86s).
- PostgreSQL-direct wrapper
  (`TestRefinement82SharedRevisionScenarioPostgresDirect`,
  `//go:build integration`): `testkit.SkipUnlessPostgres` →
  `SKIP: set LIP_REQUIRE_POSTGRES=1 and LIP_TEST_POSTGRES_DSN...` (0.08s,
  no DSN in this environment). Same assertions, isolated schemas, existing
  helpers only; the forced live-PG run now PASSES (see closeout). No catalog churn:
  `billing` and `metering-journal` were already dbparity-registered.
- Worker-money boundary (honest): full runtimebundle workers cannot be
  parametrized across dialects without new infrastructure, so the shared
  store scenario is paired with sub-pass B's real SQLite worker money proof
  (125/7/2/134 chain, one 310 settlement, 9690 balance); the PG wrapper
  covers durable revision/fence/idempotency/restart/concurrency only.

TDD record from the initial submission (kept files; honest): the first full
billingstore run exposed one test-design defect, not a production defect —
the incomparable fence probe used a revision-2 branch against a revision-3
head and was classified stale (no-op) instead of fenced. Narrowing the
conflicting observation to revision 3 (same-revision branch, the fenced case
per the approved model) produced the specified `ErrEconomicRevisionFence`
with no partial valuation. No production contract defect was exposed, so no
production edit was made and no NEEDS_CONTEXT stop was needed.

Repetition (initial submission, historical; the serial concurrency case was
replaced in sub-pass C and runtime entries superseded by sub-pass A
`-count=3` integration repeat; no race-detector claim):

```text
go test ./internal/infra/billingstore/ -run 'TestRefinement82ConcurrentLateRevisionsSerializeExactlyOnce|TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates' -count=5
# ok (14.470s) — for the record; the first test no longer exists
go test ./internal/core/billing/ ./internal/core/runtime/ -run 'TestRefinement82' -count=10
# ok billing (0.507s), ok runtime (0.094s) — runtime file since removed per finding 1
```

## Sub-pass E: fresh-handle restart factory + PG wall isolation (review finding: closed-handle reuse)

Closeout: the production wall below is RESOLVED — the owning track's fix is
approved and committed at `486c9caa`, and the forced live-PG shared scenario
now PASSES (see closeout block after GREEN). The pre-fix RED transcripts are
kept as historical evidence and labeled accordingly.

RED (sub-pass E, historical pre-fix firsthand forced run, live Neon DSN):

```text
$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration -run '^TestRefinement82SharedRevisionScenarioPostgresDirect$' -count=1 -timeout=110s ./internal/infra/billingstore/
# FAIL (11.14s): finalizer append: metering: late economic append rejected:
#   durable observation append: metering/journalstore: fact identity collision:
#   observation outbox identity="refinement52-shared-final" revision=2
```

Two defects were entangled here, separated by evidence:

1. Task-local HARNESS bug (FIXED in this pass): the PG wrapper stored two
   `*bun.DB` handles, called `prev.Close()`, and rebuilt stores on those
   SAME handles — but both `DurableStore.Close()` implementations close the
   underlying Bun handle (`billingstore/store.go:364-369`,
   `journalstore/durable.go:432-438`). Any post-restart operation on a
   reused handle is use-after-close. Fixed by refactoring both harnesses to
   an explicit factory contract: every generation opens FRESH handles
   (SQLite: new file handles; PG: new pools on the same isolated schemas
   via existing helpers + reconstructed search_path DSNs, pinged before
   use), and each session Close releases exactly its own generation. Open
   counting (`Opens`, +2 per generation) plus readiness pings plus real
   post-reopen reads/writes prove freshness — never pointer equality alone.
   The SQLite harness already opened fresh handles; it was refactored to
   the same counted factory shape, and
   `TestRefinement82SharedHarnessReopenYieldsFreshHandles` guards the
   contract (opens 2→4, pings, leg/observation readback, fresh write).

2. journalstore PRODUCTION PG defect (RESOLVED upstream — was blocking at
   the time; this test-only pass made no production edits): the failure above
   fires on the FIRST outbox append on a FRESH isolated schema, before any
   restart — so it cannot be the closed-handle bug. Root cause, verified in
   code: migration `20260916000000_observation_economic_outbox.go` declares
   `payload_json TEXT` on SQLite but `JSONB` on PG; PG normalizes JSONB key
   order/whitespace on write, so the verify-compare in
   `observation_outbox.go:141` (`inserted.PayloadJSON != string(payload)`)
   false-collided on EVERY first V2-outbox append on PG. The owning track
   fixed it by comparing canonical fingerprints/hashes (already stored
   exactly) instead of JSON bytes — approved commit `486c9caa`
   (`fix(journalstore): compare canonical outbox payloads`). This was
   distinct from both the harness bug and the known `value_present`
   parity-checker mismatch.

GREEN (sub-pass E, harness scope):

```text
go test ./internal/infra/billingstore/ -run 'TestRefinement82SharedHarnessReopenYieldsFreshHandles|TestRefinement82SharedRevisionScenarioSQLite' -count=1 -v
# PASS both (2.61s, 2.86s)
$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration -run '^TestRefinement82PostgresReopenWithoutOutbox$' -count=1 -timeout=110s ./internal/infra/billingstore/ -v
# PASS (10.87s) on live PG: fresh-handle factory, pings, leg+observation
# readback and fresh writes across restart — restart modeling proven on PG
# with outbox-independent paths only
$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration -run '^TestRefinement82SharedRevisionScenarioPostgresDirect$' -count=1 -timeout=110s ./internal/infra/billingstore/ -v
# HISTORICAL pre-fix FAIL (11.21s) at the SAME pre-restart finalizer step
# with the SAME collision — harness fix orthogonal and complete at the time;
# the remaining failure was the production JSONB wall above
```

Closeout (fresh parent verification, upstream fix `486c9caa` committed,
certification files restored):

```text
$env:LIP_REQUIRE_POSTGRES='1'; go test -tags=integration -run '^TestRefinement82SharedRevisionScenarioPostgresDirect$' -count=1 -timeout=180s ./internal/infra/billingstore/ -v
# PASS, TestRefinement82SharedRevisionScenarioPostgresDirect 14.65s, package 14.742s
```

So the task-local closed-handle failure IS eliminated (fresh handles,
pings, post-reopen ops proven on both dialects), AND the full PG scenario
now executes green on the same assertions as SQLite. No assertions were
weakened and no restart phase was skipped to get here. Task 8.2 is ready
for independent review, not complete/approved yet.

## Validation gates (sub-pass A/B/C/D/E deltas marked [A]/[B]/[C]/[D]/[E])

- [E] Opener regression + shared SQLite `-count=1` -> PASS both.
  Forced live-PG reopen-without-outbox -> PASS (10.87s). Forced live-PG
  shared scenario -> PASS after approved upstream fix `486c9caa`
  (`TestRefinement82SharedRevisionScenarioPostgresDirect` 14.65s, package
  14.742s; pre-fix historical FAIL at the pre-restart finalizer with the
  production JSONB collision, identical before/after the harness fix).
- [E] Full billingstore `TestRefinement82* -count=1` -> PASS (9 tests).
  Relevant trees (`billing`, runtimebundle `TestRefinement82*`,
  `billingcompose`, `billingadmission`, `journalstore`) -> PASS.
  `make test-db-parity-sqlite` -> PASS. `make test-race` -> Windows SKIP.
  `go vet` (untagged + `-tags=integration`) / `gofmt -l` / TODO-secret
  scans clean.

- [D] Preterminal test `-count=1` -> PASS (4.21s); `-count=5` -> ok
  (22.292s). Resume test (quiescence-hardened) `-count=10` -> ok (51.646s).
- [D] No direct mutation APIs in integration actions (grep-verified: no
  `AppendEconomicWork`/`AppendValuation`/`AppendProviderCost`/call-leg
  append/terminal-store writes in the three runtimebundle files; only
  readers, `LateEconomicAppender`, executor, and the claim worker stand-in).
- [C] Barrier suite: `TestRefinement82Concurrent* -count=1` -> PASS (5
  tests); `-count=10` -> ok (71.9s). No race-detector claim on Windows.
- [C] Shared scenario SQLite `-count=1` -> PASS (1.86s). PG-direct tagged
  wrapper -> compiles (`go vet -tags=integration` clean), SKIPs explicitly
  without DSN (0.08s).
- [C] Full billingstore `TestRefinement82* -count=1` -> PASS (8 tests).
  Relevant trees (`billing`, `billingcompose`, `billingadmission`,
  `journalstore`) -> PASS. runtimebundle `TestRefinement82*` -> PASS.
- [B] Focused money tests:
  `go test ./internal/infra/runtimebundle/ -run 'TestRefinement82ProviderRevisionPostingsAdvancePerStage|TestRefinement82RetryLoserExcludedFromRetailWinnerPostsCOGS' -count=1 -v`
  -> PASS both ([D] rename applied; re-verified below). Repeat with sub-pass A test `-count=5` -> ok (30.929s).
- [B] Full relevant packages:
  `go test -count=1 ./internal/core/billing/ ./internal/infra/billingcompose/ ./internal/infra/billingadmission/`
  -> PASS (all three ok).
  `go test -count=1 ./internal/infra/billingstore/` -> PASS (35.332s).
  `go test -count=1 ./internal/infra/runtimebundle/` -> PASS (32.859s,
  whole package including pre-existing integration tests).
- [B] `make test-db-parity-sqlite` -> PASS (exit 0; all 10 components ok).
  No new dbparity registration, no schema/migration delta.
- [A] Focused integration:
  `go test ./internal/infra/runtimebundle/ -run 'TestRefinement82RuntimeResumeKeepsTerminalOwnership' -count=1 -v`
  -> PASS (5.26s). Repeat `-count=3` -> ok (15.793s).
- [A] Full runtime tree: `go test -count=1 ./internal/core/runtime/...`
  -> PASS (runtime, runtime/failclosed).
- [A] Relevant domain/store packages:
  `go test -count=1 ./internal/core/billing/ ./internal/core/metering/ ./internal/infra/metering/journalstore/`
  -> PASS (all three ok).
- Full requested trees (initial submission, still valid for kept files):
  `go test -count=1 ./internal/core/runtime/... ./internal/core/billing/...`
  -> PASS (runtime, runtime/failclosed, billing).
- Relevant stores (initial submission, kept file):
  `go test -count=1 ./internal/infra/billingstore/ ./internal/infra/metering/journalstore/ ./internal/core/metering/...`
  -> PASS (all 9 packages ok).
- `make test-db-parity-sqlite` -> PASS (exit 0; billingstore,
  journalstore, and all 8 other components ok). No new dbparity registration:
  both touched store components were already parity-registered and no
  schema/migration file changed.
- `make test-db-parity` (full) -> not completable here: exceeded the 120s
  tool timeout without output. `make test-db-parity-postgres-direct` ->
  same (110s timeout, no output). Recorded as environment limitation, not
  a task-local result; the known PG baseline remains the pre-existing
  `metering_components.value_present` int4-vs-boolean mismatch documented
  in 5.2/8.1 evidence (the journalstore PG-direct parity check still
  reports it), which neither this task nor the upstream JSONB fix touches.
  The forced live-PG shared scenario above is the executed PG proof for
  Task 8.2 scope.
- `make test-race` -> canonical Windows skip:
  `SKIP: Go race evidence is unsupported on Windows; Linux CI remains
  mandatory.` No race-detector coverage is claimed; the concurrency vectors
  above were instead repeated (`-count=5` store concurrency/revision,
  `-count=10` domain/runtime) and passed.
- `go vet` clean (untagged + `-tags=integration`) and `gofmt -l` clean on
  all new files ([C] files included).
- Hygiene ([C]): `git status --short` shows exactly five new untracked test
  files plus this evidence file; tracked production diff is empty.
  Placeholder/secret scan over the [C] files: no TODO/FIXME/placeholder/secret
  patterns. `refinement8-2-review.md`, stashes, tasks.md, spec status
  untouched; nothing staged/committed.
- [C] `make test-race` -> canonical Windows SKIP (no local race-detector
  evidence; Linux CI mandatory). The barrier suite substitutes overlap
  scheduling, not detector coverage.

## Skips (honest, sub-pass A deltas marked [A])

- [A] In-memory continuity TTL-eviction is not reachable through the stock
  host (no clock control; continuity store is `memory`); retirement is
  certified via the real `Host.Close` shutdown API with before/after durable
  snapshots. No production change was made to expose TTL.
- [A] No Task 5.3 report-surface assertions: the integration reads only leg/
  closure/exposure/outbox/journal/head/account rows, never A-leg rolling
  reports. [B] adds `AccountReport`-filtered journal reads
  (`provider_call_cogs` / `customer_call_settlement`scoped journal listings
  via existing helpers) — per-account financial facts, not A-leg reports.
- [B] Deterministic concurrent writer schedules are addressed in sub-pass C
  above (barrier suite + shared scenario + PG SKIP wrapper); the forced
  live-PG shared scenario has now been executed green (see closeout).
  Race-detector runs remain a Linux CI gate.
- PostgreSQL live run: the forced PG-direct shared scenario now PASSES on
  live PG (closeout block above); the PG wrapper still SKIPs explicitly
  without a DSN. File-backed SQLite reopen through production
  constructors/migrations remains the default restart harness.
- No race-detector run: Windows toolchain skip is canonical; repeated
  `-count` execution is the local concurrency evidence.
- No A-leg report assertions: Task 5.3 owns the report boundary and is
  deliberately blocked; this task asserts only that economics never require
  A-leg finality and host shutdown creates no economics. TTL/eviction
  retirement is uncovered, not certified.

## Files changed (closeout state — ready for independent review, PG wall resolved upstream)

- Added: `internal/infra/runtimebundle/refinement82_preterminal_checkpoint_integration_test.go`
  (sub-pass D: gated-stream REAL preterminal checkpoint proof)
- Modified: `internal/infra/runtimebundle/refinement82_resumable_session_integration_test.go`
  (sub-pass D: deterministic quiescence; shutdown language narrowed)
- Modified: `internal/infra/runtimebundle/refinement82_revision_posting_integration_test.go`
  (sub-pass B/D: money proof; retry test renamed, all-payable claim removed)
- Modified: `internal/infra/billingstore/refinement82_shared_revision_scenario_test.go`
  (sub-pass E: counted fresh-handle factory + opener regression test)
- Modified: `internal/infra/billingstore/refinement82_shared_revision_scenario_postgres_test.go`
  (sub-pass E: fresh-handle factory per generation + PG reopen-without-outbox test)
- Kept: `internal/infra/billingstore/refinement82_revision_restart_concurrency_test.go`
  (sub-pass C barrier suite),
  `internal/core/billing/refinement82_resumable_revision_policy_test.go`
- Removed (sub-pass A): `internal/core/runtime/refinement82_resume_certification_test.go`,
  `internal/infra/billingstore/refinement82_resumable_revision_certification_test.go`
  (manual-lifecycle bypass)
- Updated: `.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement8-2-execution.md`
  (this file; sub-pass E deltas marked [E])
- Modified production code by this task: none (no checkboxes, no spec status, no commits). Production change since: approved upstream commit `486c9caa` (`fix(journalstore): compare canonical outbox payloads`) by the owning track.
