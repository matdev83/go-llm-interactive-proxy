# Parent task 17.3 final S1 re-review

## Current Review Verdict

- VERDICT: APPROVED
- TASK: 17.3 — durable epoch, worker fencing, and in-flight ownership.
- AUTHORIZATION: Task 17.3 may be checked complete and task 17.4 may begin.
- BLOCKING FINDINGS: None remaining in the reviewed task scope.

This verdict supersedes the rejected review sections retained below as historical evidence. Reviewed the actual current diff/status against `e60d02fb`, including untracked implementation/tests, and directly inspected the S1 transition and worker integration rather than accepting the remediation summary. Only this report was edited. No source, test, checklist, commit, or branch mutation was performed. Applied `kiro-review`, Go audit, testing, concurrency, error-handling, and architecture review guidance.

### S1 disposition and regression assessment

The prior reachable S1 counterexamples are repaired. `economic_revision_queue_state.go:157` now performs evidence-to-monetary escalation under the caller's marker lock and a queue-row lock, increments the delivery fence, revokes the evidence lease, and reopens completed/failed evidence delivery. Already-monetary delivery is not reopened or incremented again. Complete/Retry retain owner-and-fence predicates, so the old evidence worker cannot retire the new monetary obligation. An expired processing row with its revoked lease remains reclaimable; it is not stranded by retaining the processing status.

`internal/core/billing/economic_revision_worker.go:273` consumes the authoritative monetary token immediately after atomic claim, before result recovery, valuation processing, and provider posting. Thus a pre-upgrade evidence-only list snapshot no longer suppresses the posting authorized by the later claim. Immutable evidence/result identity is unchanged. The recovered completed evidence valuation is used for the monetary delivery; shadow execution itself still writes no money.

Executed `TestS1ASequentialUpgradeAfterEvidenceCompletePostsOnce`, `TestS1BOldEvidenceLeaseCannotRetireUpgradedGeneration`, `TestS1CStaleListClaimUsesAuthoritativeMonetary`, and `TestS1DRepeatedUpgradesIdempotent`, including ten repetitions alongside the evidence-only control. These cover completed evidence then live upgrade; a held evidence lease followed by upgrade and rejected old Complete/Retry; a stale list envelope followed by authoritative claim; and repeats before/after monetary completion. Assertions include one payable, queue retirement, completed monetary pin, zero pending economic drain count, and no second posting. S1C uses a read-only stale-list decorator over the real durable claim/posting/state implementation; it does not manufacture queue or pin completion. Combined with the integrated coordinator lifecycle tests, this closes the previously demonstrated drain-stranding path.

Previously repaired F1-F9 and R1-R4 remain acceptable: marker serialization and activation checks, complete open/queued drain inventory, real admitted V2 terminal sink/spool and worker paths, absence of phantom historical adjustment pins, immutable per-revision ownership, renewable marker/lease authority, atomic legacy promotion, required production claim tokens, authoritative economic relay ownership, deterministic terminal no-money revision dispositions, and real integrated restart/drain certification. Repeated regression suites passed. No new reachable dual-posting or permanent-drain counterexample was established. The approved 17.2 shadow no-post boundary remains intact. Task 17.4 rollback/stale-binary policy is not required by this approval.

### Fresh verification

- `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... ./internal/infra/billingadmission/... -count=1 -timeout=15m`: PASS. Core 0.407s, store 188.939s, runtimebundle 59.732s, admission 0.012s. This fresh combined run is green; the earlier runtimebundle failure retained below was not reproduced.
- `go test ./internal/infra/billingstore ./internal/infra/runtimebundle -run 'S1|R[1-4]|CutoverIntegrated|F1Postgres' -count=3 -timeout=15m`: PASS, store 81.152s and runtimebundle 51.163s. Includes composed sink/relay, token rejection, revision drain/crash and integrated lifecycle regressions. Untagged execution is not PostgreSQL proof.
- `go test ./internal/infra/billingstore -run 'TestS1|TestF2BNoPostingAdapterRemainsEvidence' -count=10 -timeout=5m`: PASS, 1.807s.
- Configured PostgreSQL with `LIP_REQUIRE_POSTGRES=1`: `go test -tags=integration ./internal/infra/billingstore -run '(F[1-8]|R[1-4]|S1|CutoverIntegrated|AccountingCutover|PostingOwnership).*Postgres|TestDBParity_PostgresDirect' -count=1 -timeout=15m`: PASS, 131.219s. Covers available PostgreSQL billing parity, marker/pin, F-series and integrated lifecycle/activation race cases. The four new S1 schedules are SQLite tests, not PostgreSQL twins; the regex does not manufacture additional PG coverage.
- `go run ./internal/testkit/dbparity/cmd sqlite`: PASS, all ten catalog components.
- `go vet` across affected billing/core/store/runtimebundle/admission packages: PASS, independently executed with exit 0.
- Changed tracked/untracked Go `gofmt -l`: empty. `git diff --check`: clean. Scoped placeholder/private-key/API-key/password-pattern scan: no matches (the scan's exit 1 means no matches, not a vet failure).
- Seven scoped architecture tests covering settlement input, customer-balance authority, stream monetary boundary, 17.2 no-writer shadow, production mode-selector absence, public economics DAG, and binding import closure: PASS, 3.753s.

Limits: no Windows race-detector claim, no full repository QA/test-cost run, and no assertion that the unrelated full-catalog PostgreSQL metering type mismatch or known broader lipapi architecture issue is repaired. Billing-component PostgreSQL parity was freshly verified independently. The authorized 1000-Go-file ceiling is applied; size alone is not a finding. No schema migration was needed for S1 because the existing delivery fence and queue lifecycle columns represent this transition.

---

# Superseded: parent task 17.3 focused re-review

## Review Verdict

- VERDICT: REJECTED
- TASK: 17.3
- AUTHORIZATION: Do not check 17.3 or begin 17.4 on its completion.
- SUMMARY: R1, R3, and the missing-token/atomic-claim parts of R4 are repaired, but an evidence-to-monetary delivery upgrade can still silently lose its payable or strand its new pin.

This newest section supersedes both reviews below. The complete feature diff/status against `e60d02fb` and current source/tests were inspected before relying on any remediation claim. Ownership remained report-only. No source/tests/checklist/Git changes were made. The 1000-Go-file allowance is not a blocker, and task 17.4 rollback policy is not imposed.

### One remaining correctness blocker: S1 — Important — monetary upgrade does not transition delivery lifecycle/lease authority

Exact locations:

- `internal/infra/billingstore/economic_revision_queue_state.go:147`: `upgradeEconomicStateToMonetaryTx`.
- `economic_revision_queue_state.go:163`: upgrade changes only `provider_posting` and `posting_owner`; it neither reopens completed delivery nor invalidates an evidence-only lease.
- `economic_revision_queue_state.go:297`: completion checks the existing lease owner/fence, not the delivery intent under which that lease was granted.
- `economic_revision_queue_state.go:440`: the new atomic claimer withholds completed rows before examining upgraded intent.
- `internal/infra/billingstore/economic_revision_store.go:375`: pending reader excludes completed rows.
- `internal/core/billing/economic_revision_worker.go:250` and `:386`: worker retains the work envelope obtained before claim and skips provider posting when that envelope says evidence-only, even if the atomic claim has just acquired monetary authority.

Reachable sequential counterexample (no malformed input or administrative SQL):

1. In shadow, append a valid provider revision using the evidence-only path.
2. Let its evidence-only worker process the valuation successfully. The queue row becomes `completed`; no payable is written. This behavior is itself covered by the existing no-post worker tests.
3. Deliver the equivalent live monetary work through `AppendProviderPostingEconomicRevisionWork` (V1 while shadow, or appropriately authorized V2 after activation).
4. The accepted replay upgrade changes the row to monetary but leaves `status=completed`. All future pending reads and claims omit it. The already persisted valuation is never recovered for monetary posting, and no ownership pin proves a completed payable. The upgrade returns success despite permanently dropping the requested posting.

Two concurrent variants expose the same lifecycle error:

- Pause an evidence-only worker after its lease is acquired, upgrade the work to monetary, then resume it. Upgrade leaves its lease fence valid. That worker legitimately skips money under its old evidence-only envelope and successfully completes the now-monetary row. The payable is lost.
- Pause between pending-list read and the new atomic claim, then upgrade. Claim sees current monetary intent and creates a monetary pin/token, but it returns no authoritative work envelope. The worker still has the old evidence-only item, skips posting at line 386, and completes the queue. The new pin stays `pinned` with no remaining pending job, blocking drain indefinitely.

The current `TestR2ShadowUpgradeBecomesMonetaryAndPostsOnce` upgrades before any worker list/claim/completion. It proves the repaired payload/state overlay for that ordering, not these ordinary later-delivery or concurrent orderings. New R4 token validation does not fix this because the evidence-only early return occurs before monetary-token validation.

Minimal remediation: make evidence-to-monetary escalation an atomic delivery-generation transition under the existing marker/queue locks. Requeue or otherwise schedule the monetary obligation even if evidence delivery is already complete; revoke/fence an old evidence-only lease so it cannot retire the new obligation. Return/consume the authoritative claimed intent rather than continuing with a stale pre-claim envelope. Preserve the immutable evidence/valuation identity and no-post shadow semantics. Add three tests matching the sequential and two barrier schedules above, checking payable count, queue completion, pin disposition, and coordinator drain readiness together.

Anchors: requirements 10.6 (durable replay/restart outcome), 14.4 (atomic posting transition), 17.4 (one durable posting authority), task 17.3 in-flight classification/drain, and preservation of the approved 17.2 no-post boundary. This is forward delivery/cutover correctness, not future rollback work.

### Re-evaluation of earlier findings

| Area | Current result |
| --- | --- |
| F1 serialization | Marker-locked ordinary monetary transactions and transaction-local activation checks remain present; PostgreSQL marker/cutover tests pass. Upgrade authorization is now rechecked inside its retry transaction. |
| F2 admitted/open work inventory | Open exposures and monetary economic work remain counted and classified. S1 is a remaining delivery-state interaction, not a missing table scan. |
| F3 / R1 fresh V2 terminal pipeline | Repaired in production sink: admitted durable V2 ownership is consumed by ordinary AppendCall/AppendLeg. Executed composed admission/sink/worker and spool-delivery regressions, not just direct WithOwner APIs. |
| F4 phantom adjustment pins | Historical heads are still not classified as pending adjustments. Synchronous adjustments retain marker serialization. |
| F5 provider revision ownership | Distinct immutable revision pins and active V1 fencing remain present. |
| F6 / R4 renewable claims | Customer/provider claims now issue validated tokens within marker-locked transactions; economic claims also bind work/lease owner/fence. Posting checks current lease state. Missing/partially cleared tokens fail closed in production workers. S1 concerns upgraded work intent across a lease, not the previous nil-token fallback. |
| F7 legacy handoff | Pin completion remains inside the handoff/promotion monetary transaction. |
| F8 / R4 mandatory metadata | Dropped customer/provider/economic token and malformed customer token regressions execute real workers and assert zero monetary effects. The former empty-operation-key fallback is removed. |
| R2 economic owner/overlay | Production relay now resolves the admitted owner; explicit V2 with empty payload owner posts; pending shadow-to-live upgrade overlays monetary intent correctly. S1 remains when upgrade happens after evidence delivery starts/completes. |
| R3 terminal no-money outcomes | Stale/subset/tie-break/excluded outcomes now persist deterministic no-money disposition and complete pins; exact exclusion crash/reopen replay succeeds without extra journals. |
| F9 certification | Real same-file SQLite lifecycle and production drain remain; direct and spool V2 sink regressions now close the earlier test substitution. Repeated existing concurrency/crash suites pass. They do not include S1 schedules. |

### Fresh mechanical results

- Executed R1-R4 counterexample/regression tests, including composed fresh V2 terminal sink/spool, production observation relay, explicit V2 delivery owner, pending shadow upgrade, stale/excluded revision drain and crash replay, dropped tokens, malformed tokens, first-acquisition tokens, and lease renewal. `go test ./internal/infra/billingstore ./internal/infra/runtimebundle -run 'R[1-4]|CutoverIntegrated|F1Postgres' -count=3 -timeout=15m` passed: store 65.596s, runtimebundle 35.073s. The untagged command does not execute integration-only PostgreSQL tests.
- Independently reran the evidence-only worker control, pending shadow-upgrade test, dropped economic-token test, stale revision drain, and excluded revision crash replay together: PASS, 1.671s.
- Broad affected command: `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... ./internal/infra/billingadmission/... -count=1 -timeout=15m`. Core PASS 0.377s; billingstore PASS 198.460s; admission PASS 0.015s. Runtimebundle reported FAIL in this combined run; its detailed diagnostic was truncated by output capture and a standalone rerun is recorded below. Do not call this combined command green.
- Standalone `go test ./internal/infra/runtimebundle/... -count=1 -timeout=5m`: PASS, 38.182s. The earlier combined-run failure was not reproduced or attributed; it is retained as a verification limitation, not asserted to be S1 or an additional proven task blocker.
- Configured PostgreSQL, `LIP_REQUIRE_POSTGRES=1`: `go test -tags=integration ./internal/infra/billingstore -run '(F[1-8]|R[1-4]|CutoverIntegrated|AccountingCutover|PostingOwnership).*Postgres|TestDBParity_PostgresDirect' -count=1 -timeout=15m`: PASS, 123.253s. This includes billing component parity, cutover/marker/pin and available claim/revision integration tests; it does not imply every new SQLite R-test has a PostgreSQL twin.
- `go run ./internal/testkit/dbparity/cmd sqlite`: PASS, all ten catalog components.
- Affected billing/store/runtimebundle/admission `go vet`: PASS, independently rerun with exit 0.
- Changed tracked/untracked Go `gofmt -l` and `git diff --check`: CLEAN.
- Changed-code TODO/TBD/FIXME/HACK/XXX and obvious private-key/credential-marker scan: CLEAN; rg exit 1 means no matches.
- Seven scoped billing/runtime/public-DAG/no-post-shadow architecture tests: PASS, 3.062s. The previously established unrelated lipapi import-closure failure was not used as a rejection reason.
- No Windows race, Linux race, whole `make qa`, opt-in test-cost, full PostgreSQL catalog, or transaction-pooler restart certification is claimed. Full PostgreSQL's previously observed unrelated metering integer/boolean mismatch is not this task's blocker; current billing component parity was verified separately.

S1 is a direct source-traced composition of reachable supported APIs and worker ordering. The existing regressions above were executed; no new S1 test file was written because reviewer ownership permits editing only this report. Its absence from current tests is explicitly identified rather than presenting those green runs as proof of the missing ordering.

---

## Previous re-review — superseded by the newest section above

# Parent task 17.3 independent re-review

## Current verdict: REJECTED

Task 17.3 must remain unchecked; task 17.4 is not authorized to begin on the basis of completed 17.3. This section supersedes the initial review retained below. Several initial defects are genuinely repaired, but the actual runtime terminal path, economic delivery intent, terminal revision outcomes, and mandatory claim contract still contain blocking failures.

Scope: complete current feature worktree relative to `e60d02fb`, including untracked implementation/tests, on `feat/b-leg-usage-economics`. First action was Git status/diff plus direct source inspection. Source, checklist, and Git state were not changed. Only this report is reviewer-owned. The user-authorized 1000-Go-file allowance is respected; neither file count nor LOC is a rejection reason. Requirements 10.6, 14.4, 17.4, 18.4 and design Migration Strategy step 6 remain the acceptance anchors. No task 17.4 rollback/stale-binary policy is imposed.

### Current blocking findings

#### R1 — Critical: production terminal handoff still uses V1-only persistence after V2 admission

Evidence:

- `internal/infra/billingadmission/adapter.go:151` selects V2 admission from the active marker.
- The accepted composed `TerminalUsageSink` exposes `AppendCall`/`AppendLeg`, not the new WithOwner methods (`internal/core/billing/append.go:17`).
- `internal/infra/billingstore/20260829000000_billing_retire_usage_append_outbox.go:403` and `:407` implement those methods by calling the old `AppendCallUsage`/`AppendCallLegUsage` paths.
- Those paths reject fresh work in active state (`call_usage_store.go:90`, `call_leg_usage_store.go:93`); their F2A exception is for draining V1, not active V2.
- No production caller of `AppendCallUsageWithOwner` or `AppendCallLegUsageWithOwner` exists outside their definitions. The supposedly runtime-level test calls them directly at `internal/infra/runtimebundle/cutover_v2_pipeline_f3_test.go:196` and `:212`, bypassing `prod.BillingTerminalUsageSink`.

Concrete sequence: legally activate an empty/drained store; compose with the durable store as the terminal sink, as the current runtime test does; call the composed admission adapter, which successfully creates a V2 exposure/pin; deliver terminal records through that same composition's `BillingTerminalUsageSink.AppendCall/AppendLeg`. Both fresh terminal records are fenced as V1. The call remains open and cannot reach the successfully tested manual WithOwner settlement path.

Remediation: route the real terminal transport/durable delivery path using the admitted durable owner, without reclassifying a call from a fresh global marker read. Change the integration test to invoke the composed terminal sink, including the deployed spool/receiver path where applicable, then run the real workers. Anchor: task 17.3 enabling usable V2 admissions, Migration Strategy step 6, requirements 10.6/17.4.

#### R2 — Important: economic posting ownership/intent is stored but not consistently consumed

Evidence: `internal/infra/runtimebundle/observation_economic_bridge.go:233`; `internal/infra/billingstore/economic_revision_store.go:143`, `:213`, `:356`; `economic_revision_queue_state.go:130`, `:365`; `internal/core/billing/economic_revision_worker.go:372`.

There are three concrete failures in the same delivery-intent boundary:

1. The production observation relay always calls `AppendProviderPostingEconomicRevisionWork(..., PostingOwnerV1)`. After legal activation, a normal provider observation for a fresh V2 call is therefore rejected by the new monetary enqueue gate. No current-marker/admitted-owner selection exists on this production path.
2. Even a caller using the explicit V2 API correctly can fail: pass ordinary normalized monetary work with its default empty `PostingOwner` and explicit API argument `PostingOwnerV2`. The store persists V2 in mutable delivery state, but persists the unchanged empty-owner immutable payload. `ListPendingEconomicRevisionWork` selects only the payload, never overlays the delivery state. First acquisition has no pin, so the cutover claimer returns nil metadata; the worker derives default V1 from the payload and is fenced in active. The passing F2B V2 test sets V2 in BOTH places (`cutover_economic_f2b_test.go:338`, `:343`), hiding this split source of truth.
3. The advertised evidence-to-monetary upgrade changes only `provider_posting`/`posting_owner`. A work item originally stored by `ShadowV2Capture` has `EvidenceOnly=true` in its immutable payload. On equivalent live delivery, the state can be upgraded to monetary while the payload stays evidence-only. The worker still skips posting from that payload and can complete the job without its intended payable. If classification runs first, it reports a non-monetary payload in monetary state and drain cannot converge. The upgrade retry transaction also locks the marker but does not revalidate the owner/state gate after the first transaction was rolled back, so a shadow-to-active transition between those transactions can create V1 monetary state after activation.

Remediation: establish one authoritative delivery-intent read/claim contract and consume its owner/evidence flag in production. Recheck authorization inside every transaction that changes monetary intent. Cover relay-driven fresh V2 work, explicit V2 owner with an otherwise legacy-compatible payload, and shadow/evidence-to-live replay of the same immutable work, including a transition between retry transactions. Anchors: task 17.3, requirements 10.6/17.4, preservation of 17.2 no-post shadow.

#### R3 — Important: stale and excluded provider revisions strand pins or fail exact crash replay

Evidence: `internal/infra/billingstore/provider_cost_revision_store.go:378`, `:582`, `:689`, `:739`, `:746`, `:822`; `cutover_economic_f2b_store.go:87`; `internal/core/billing/economic_revision_worker.go:334`, `:369`.

Per-revision immutable pins correctly replace the former mutable lineage completion, but terminal no-money outcomes do not consistently retire those pins.

- Keep revision 1 pending/retryable while revision 2 for the same head has already posted (ordinary out-of-order delivery or retry). Begin draining: classification creates the revision-1 pin. The worker later submits revision 1, `ApplyProviderCostRevision` returns `Stale` at the lower-revision branch without completing that pin, and the worker treats the nil error as success and completes the economic queue item. Activation remains blocked by a `pinned` row with no pending worker. Replaying the stale input returns the same result without completing it. Candidate-subset/tie-break loser branches have the same pattern.
- For an exact ignored/excluded revision, the first attempt completes the revision pin through `checkRevisionNewPin`/`completeRevisionPin` and commits. If the process fails before `CompleteEconomicRevisionWork`, replay reaches the same exclusion branch and calls `checkRevisionNewPin` again. That helper rejects the already completed pin instead of recognizing its exact immutable no-money outcome. The durable job retries permanently despite its already completed exclusion. This includes the B-leg/provider-charge execution-authority exclusion branch.

Remediation: give every successful terminal revision result, including stale/superseded/ignored/nonpayable outcomes, a deterministic durable pin disposition and exact replay path. Do not fabricate a journal for a no-money result. Add a real worker drain test with an older queued revision behind a newer head and a crash between exclusion commit and queue completion. Anchors: requirements 10.6/14.4 and task 17.3 drain/restart completion.

#### R4 — Important: production WithCutover claims are still post-claim lookups with an unvalidated missing-token bypass

Evidence: `internal/infra/billingstore/call_usage_store.go:657`; `call_leg_usage_store.go:453`; `economic_revision_queue_state.go:365`; `internal/core/billing/call_post_usage_worker.go:219`; `call_provider_cost_worker.go:203`; `economic_revision_worker.go:398`.

The new contracts/documentation promise atomic claim plus current-marker authority, but implementations call the old claim/list method, let it finish, and then fetch metadata separately. The economic lease is already committed when its token is fetched. Neither the token nor the posting input carries and validates the economic lease fence.

More directly, production workers accept a missing or malformed token as first acquisition: customer/provider only test whether `OperationKey` is empty, skipping the rest of validation in that case; economic processing accepts nil cutover and re-enters the optional legacy metadata lookup/fallback. A decorator that forwards valid claimed work but drops the token (or clears only `OperationKey` on an otherwise stale/malformed token) is accepted by runtime composition and causes a preactive durable monetary posting with nil/default authority. This is the precise mandatory-port bypass F8 required eliminating, now in the new production constructors. The wrapper tests exercise interface presence/source strings and malformed OLD WithClaim lookups, not missing tokens in an actual WithCutover-composed worker.

The durable marker's posting lock independently protects several cross-version races; this finding does not claim those protections disappeared. It does mean the advertised lease/claim fencing proof is absent and a metadata failure is indistinguishable from an authorized first acquisition.

Remediation: issue explicit, validated current authority as part of the durable claim transaction, including first acquisition. Do not use an empty/nil token to mean authorized. Carry any required lease identity through posting and validate it before effects. Exercise real runtime composition with wrappers dropping tokens, partially malformed tokens, a marker transition between claim and return, and reclaim of an expired lease. Anchors: task 17.3 workers waking after lease/epoch change, requirement 17.4.

### Disposition of all nine initial findings

| Initial finding | Re-review result |
| --- | --- |
| F1 marker/posting serialization | Original direct-adjustment PostgreSQL race repaired: shared marker row lock and transaction-local activation counts are real. Repeated PG barrier passes. The separate monetary-intent retry transaction still needs the R2 authorization correction. |
| F2 incomplete in-flight inventory | Open exposures and monetary economic work are now counted/classified; admitted V1 terminal handoff during draining works in focused tests. R2/R3 expose remaining economic inventory/drain inconsistencies. |
| F3 unusable fresh V2 pipeline | Version-aware APIs and automatic admission exist, but R1 proves the real terminal sink still does not use them; R2 covers provider-economic production delivery. Not closed. |
| F4 phantom historical adjustment pins | Repaired: the historical-head scan is removed; synchronous adjustments serialize with the marker and no fictitious adjustment pin is invented. Focused history/activation tests pass. |
| F5 completed V1 pin authorizing new revisions | Original bypass repaired using distinct immutable revision operation keys and active-state fencing. R3 is a new terminal/replay gap introduced/exposed by the per-revision pin lifecycle. |
| F6 pin epoch versus renewable authority | Stable ownership now yields current-marker tokens; tested forward renewal works without rewriting pin ownership. Atomic claim/lease binding remains incomplete under R4. |
| F7 legacy handoff atomicity | Original payable handoff/promotion defect repaired: helper now completes the revision pin before its transaction commits, with rollback hooks and focused reopen tests. |
| F8 fail-closed mandatory metadata | Old WithClaim lookup errors now propagate, but production moved to WithCutover and retains the missing-token bypass in R4. Not closed. |
| F9 genuine integrated certification | Material improvement: new SQLite full lifecycle really closes/reopens the same file, drains through workers, and uses coordinator activation without manufactured completion. New PG lifecycle and races pass. However V2 terminal delivery is manually substituted (R1), and R2-R4 are not covered. PG "reopen" at `cutover_integrated_certification_postgres_test.go:183` reuses the same still-open `bunDB`, so it is not connection/process restart evidence. Old misleading crash-test comments remain but are not a separate correctness rejection. |

### Fresh re-review mechanical evidence

All tests ran against the current worktree; no new source/test probes were written because review ownership allows editing only this report. R1-R4 are directly traced reachable API/worker executions, not claims of newly executed red tests. Existing counterexample/regression suites below were rerun independently.

| Check | Fresh result |
| --- | --- |
| `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... ./internal/infra/billingadmission/... -count=1 -timeout=15m` | PASS: billing 0.386s, billingstore 176.396s, runtimebundle 53.380s, admission 0.012s |
| `go test ./internal/infra/billingstore -run 'Test(F[1-8]\|CutoverIntegrated)' -count=3 -timeout=15m` | PASS, 86.871s; includes new and old integrated suites |
| `LIP_REQUIRE_POSTGRES=1`, `go test -tags=integration ./internal/infra/billingstore -run '(F1\|F2\|F3\|F4\|F5\|F6\|F7\|F8\|CutoverIntegrated\|AccountingCutover\|PostingOwnership).*Postgres' -count=1 -timeout=15m` | PASS on configured PostgreSQL, 52.741s |
| Configured PG `TestF1PostgresActivationSerializedWithDirectAdjustment` and `TestCutoverIntegratedPostgresActivationRacesMonetaryWhenConfigured`, count 3 | PASS, 46.723s |
| `go run ./internal/testkit/dbparity/cmd sqlite` | PASS, all ten catalog components |
| `LIP_REQUIRE_POSTGRES=1`, `go test -tags=integration ./internal/infra/billingstore -run '^TestDBParity_PostgresDirect$' -count=1 -timeout=10m` | PASS, billing component parity, 78.959s |
| `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... ./internal/infra/billingadmission/...` | PASS, exit 0 |
| `gofmt -l` on all changed tracked/untracked Go files; `git diff --check` | CLEAN, no output |
| Changed-file TODO/TBD/FIXME/HACK/XXX and obvious credential/private-key marker scan | CLEAN; rg exit 1 means no matches, not a failed check |
| Eight previously selected billing architecture tests | Same known `TestBillingCoreStaysProviderAndPersistenceFree` lipapi import-closure failure; not attributed to this task |
| Remaining seven selected billing/runtime/public-DAG/shadow architecture tests rerun alone | PASS, 2.812s |

The full PostgreSQL catalog was not rerun in this re-review: current billing component parity and all SQLite components were verified separately as authorized. The prior full-catalog metering-journal int4/boolean mismatch remains previously observed, not newly verified here, and is not a rejection reason. Windows race execution was not performed; no Linux/race or transaction-pooler restart proof is claimed. Full `make qa`, full default suite, and opt-in test-cost were not run.

Boundary review: the admission, worker, and runtime composition changes are necessary consumers of the task's billingstore/claim authority, not rejected merely for crossing package paths. The defects above concern missing or contradictory consumption of that authority. Existing no-post shadow architecture tests remain green; R2 requires preserving that separation when upgrading durable delivery intent.

RED evidence was re-read from `%TEMP%/opencode/phase17-3-f1-red-pg.txt`, `phase17-3-f2a-red.txt`, `phase17-3-f2b-red.txt`, `phase17-3-f3-red.txt`, `phase17-3-f4-red.txt`, `phase17-3-f5-f7-red.txt`, and `phase17-3-f6-f8-red.txt`. F1/F2/F4/F5/F7/F6/F8 artifacts contain relevant behavioral failures. F3's artifact is missing-symbol compilation failure for the new APIs, not a failing real terminal-sink lifecycle. No F9-named RED artifact was found in that directory; the original C artifact remains the constructor-symbol RED described below. These limitations reinforce the need for the specific production-path regressions, but the rejection is based on R1-R4, not log naming or cosmetic test style.

Summary: the original lock, phantom-head, revision-owner, and handoff repairs are substantial and verified, but task 17.3 still cannot be certified until real terminal delivery, authoritative economic intent, terminal revision disposition, and mandatory claim tokens work end-to-end.

---

## Initial review retained for provenance — superseded by the re-review above

# Parent task 17.3 independent review

## Verdict: REJECTED

Task 17.3 must remain unchecked. Task 17.4 must not begin on the assumption that the 17.3 cutover boundary is certified.

Scope: baseline `e60d02fb` plus the current uncommitted 17.3 A/B1/B2a/B2b1-B2b4/C implementation in branch `feat/b-leg-usage-economics`. This review covers durable markers, posting pins, coordinator inventory, customer/provider/adjustment monetary paths, worker claim propagation, runtime composition, migrations, and certification. Only this report was changed. No implementation, test, status, or Git mutation was performed.

Acceptance anchors: task 17.3; requirements 10.6 (durable replay/restart), 14.4 (atomic monetary deduplication), 17.4 (durable version boundary and writer fencing), and 18.4 (dialect/restart/cutover proof); design Migration Strategy step 6. The approved 17.1 historical-reader and 17.2 no-post-shadow boundaries are preserved. No rollback mechanism or stale-binary policy from task 17.4 is required by this review.

The findings below are concrete source-traced execution schedules, not newly executed adversarial probes. Existing tests were independently rerun as recorded below. Their green results do not exercise or disprove these schedules.

## Blocking findings

### F1 — Critical: PostgreSQL activation is not serialized with monetary transactions

Locations: `internal/infra/billingstore/accounting_cutover_store.go:74`; `cutover_coordinator_store.go:247`; `trusted_operations.go:255`; marker reads throughout the posting fences.

`loadAccountingCutover` uses an ordinary SELECT. Reading it inside a transaction does not lock the marker. `ActivateCutoverV2` checks drain status outside the later marker-transition transaction. Marker CAS protects competing marker updates, not transactions that already read the old marker. Neither schema supplies a trigger that closes this gap.

Reachable PostgreSQL schedule:

1. In shadow, start a valid V1 direct adjustment on a funded account. Pause at `b2b4-before-effects`, after its marker check and uncommitted pin insertion.
2. Another connection begins draining and activates. Its reads cannot see that uncommitted pin; no customer/provider queue row represents this synchronous command.
3. Resume the first transaction. It posts money and commits the V1 pin after `v2_active`, without rechecking or contending on the marker.

The same missing serialization boundary affects admissions and fresh work append. Account/head locks do not help because activation does not acquire them. This violates the stop/drain-before-enable contract, even if per-operation uniqueness independently prevents some duplicate journals.

Required correction: a shared transactional serialization boundary between activation and every authorized admission/posting path, including absent-marker initialization. Drain verification and activation must be safe against concurrent work, not merely consecutive calls. Add a deterministic two-connection PostgreSQL barrier test using an in-flight monetary transaction and verify that activation waits or the stale transaction fails without effects. Anchor: task 17.3, requirement 17.4, Migration Strategy step 6.

### F2 — Critical: the drain inventory omits admitted live calls and the economic revision queue

Locations: `cutover_coordinator_store.go:347`, `:355`, `:363`; `exposure_store.go:86`; `call_usage_store.go:75`; `call_leg_usage_store.go:72`; `economic_revision_store.go:48`; `economic_job_queue_store.go:26`; `economic_revision_queue_state.go:114`.

The coordinator counts nonprocessed call closures, legacy provider work, and existing pins. It does not enumerate already admitted/open exposures or monetary work in `billing_economic_work` and its lease state.

Two normal counterexamples:

- Admit a V1 call, then begin draining while the upstream stream is still running and before terminal closure/leg evidence exists. There is no counted call row or posting pin. Activation can report ready. Later `AppendCallUsage` and `AppendCallLeg` reject the terminal evidence because their unconditional new-V1 gate rejects both draining and active. This loses the normal settlement handoff for a call that was already admitted. If a closure exists but its final leg does not, the same gate can leave drain permanently incomplete.
- Queue or lease a payable economic provider revision before its first head/pin exists. This is production `EconomicRevisionWorker` work, but it is not a legacy `provider_cost_work` row. Classification and activation overlook it; its V1 posting later cannot obtain a new pin. The economic queue's claim path itself has no cutover gate.

Required correction: inventory actual pre-boundary admissions and all monetary worker queues, classify their immutable owner, allow classified terminal handoff, and reject only genuinely new V1 work. Do not indiscriminately treat every evidence-only economic job as payable work. Prove both schedules through the production workers. Anchors: task 17.3, requirements 10.6/17.4, Migration Strategy step 6.

### F3 — Critical: legal activation provides no usable V2 admission/settlement pipeline

Locations: `exposure_store.go:86`; `call_usage_store.go:75`; `call_leg_usage_store.go:72`; `cutover_coordinator_store.go:772`; `customer_settlement_fence_b2b1_extra_test.go:22`.

After `v2_active`, the production exposure admission and usage append APIs still unconditionally execute `cutoverGateForNewV1Tx`. They have no authoritative version-aware alternative. The ordinary customer/provider claim paths also stop returning work in active state. `ApplyCallBillingResult` still needs the durable exposure and closure that these APIs will not create.

Thus the empty-store legal sequence shadow -> begin drain -> activate succeeds as a marker transition, but the next fresh customer call cannot enter the billing pipeline. The positive V2 settlement test preloads V1 exposure/usage and forces marker transitions directly rather than proving legal activation followed by fresh V2 admission. Posting-time `IsV2NewWorkAuthorized` checks are useful but do not supply the missing admission path; the standalone `CheckV2NewWorkAuthorized` helper is not wired into such a path.

Required correction: consume durable V2 authority in the actual admission/evidence/claim path and prove a fresh V2 call after coordinator-authorized activation, without preloading work or bypassing drain checks. This is the explicit task 17.3 requirement to enable V2 admissions, not a request to implement task 17.4.

### F4 — High: ordinary historical provider heads create phantom unfinished adjustments

Locations: `cutover_coordinator_store.go:561`, `:711`; `financial_adjustment_b2a_b2b3_test.go`; `cutover_integrated_certification_test.go:424`.

`classifyAdjustmentHeads` scans every provider-cost head for the store, without a pending adjustment command or live worker predicate. `coordinatorInsertAdjustmentPin` creates a `financial_adjustment` pin in `pinned` state. A completed provider revision ordinarily has a completed `provider_charge` pin and a head, not a pending selected-cost adjustment. Classification invents an additional unfinished adjustment for this historical head.

With all real queues processed, that pin still blocks activation. Neither the ordinary customer/provider worker nor a background adjustment drainer completes it. Reclassification only finds the same pin. The bounded OFFSET scan also restarts over an unchanged head inventory, so repeated calls do not provide a durable progress cursor for heads beyond its total scan bound.

The integrated test masks the primary defect: it ignores a provider posting error caused by the wrong CallID, generically completes every remaining pin with transaction ID `tx-c3c-drain`, and directly marks queue rows processed in SQL. That is not a production drain/completion path.

Required correction: distinguish genuinely in-flight adjustment operations from historical head projections. Historical completion backfill must reference actual durable economic outcomes; do not fabricate an unfinished operation or a completion transaction. Prove activation after ordinary completed provider history with no pending adjustment and without administrative pin completion. Anchors: task 17.3, requirements 10.6/14.4/17.4.

### F5 — High: a completed V1 provider pin authorizes a new monetary revision after activation

Locations: `provider_cost_revision_store.go:338`, `:391`, `:804`, `:875`.

For an existing completed pin, `checkRevisionNewPin` uses acquire-replay authorization, but completion authorization is checked only when the pin is still `pinned`. A completed V1 pin remains replay-readable in active state. A direct revision input with default V1 owner and nil claim therefore passes this check for a new higher revision on that lineage. The normal revision path can post a delta and overwrite the pin's completion outcome after `v2_active`.

This is not an exact historical replay. Completed historical V1 pins are expected to remain present after successful cutover, so the bypass does not require corrupt state. Selected-cost and cost-pass-through paths contain additional active-state checks that this provider path lacks.

Required correction: separate immutable replay authorization from authorization to produce a new revision/adjustment outcome. Reject new V1 monetary outcomes in active state regardless of whether a completed lineage pin exists. If pins are lineage projections whose completion advances, explicitly preserve and validate the identity of each immutable logical revision rather than treating completed-pin replay permission as permission to write. Test a changed-amount, changed-evidence revision against a completed V1 pin after activation. Anchors: task 17.3, requirements 14.4/17.4.

### F6 — High: old pin epochs cannot be safely reclaimed across normal forward transitions

Locations: `cutover_coordinator_store.go:98`, `:529`; `provider_cost_revision_store.go:338`; `internal/core/billing/economic_revision_worker.go:330`.

Claim metadata is derived from the pin's acquisition epoch. Existing pins are preserved during classification. New provider revision posting requires the claim epoch to equal both the current marker epoch and the existing pin epoch.

Example: a completed V1 provider pin is created in epoch 1. Entering shadow advances the marker to epoch 2. A legitimate later V1 correction obtains epoch 1 metadata from that pin and is fenced forever; no fresh claim can satisfy both equalities. Likewise, a legitimately acquired incomplete pin before entry into draining cannot be completed by a newly awakened worker: old metadata fails the marker check, while new metadata fails the pin check. Draining requires nonnil metadata, so legacy auto-acquisition cannot rescue it.

Required correction: separate stable posting ownership from renewable worker/lease authority. Support safe current-epoch reclaim of previously classified V1 work without changing its historical owner, and exercise real workers across shadow and draining transitions. This is forward cutover liveness, not rollback. Anchors: task 17.3, requirements 10.6/17.4.

### F7 — High: legacy-to-revision handoff commits money without completing its pin

Locations: `provider_cost_revision_store.go:660`, `:693`, `:959`.

Both handoff branches call `checkRevisionNewPin` and then return directly through `applyProviderCostRevisionAfterLegacyFence`. That helper posts the delta, updates heads/snapshots/fences, and commits, but never calls `completeRevisionPin` or an equivalent pin completion/update.

For an actual upgrade containing legacy money but no new ownership pin, the transaction can acquire a pin, post the revision delta, and commit with the pin still `pinned`. The worker then successfully completes its economic job; there is no guaranteed retry to repair the ownership row. A crash after commit produces the same durable state. If an existing completed pin was used, its completion outcome remains stale despite the newly committed revision.

Existing economic fences may deduplicate a replay of that revision; this finding does not claim they necessarily duplicate its journal. The defect is that promised atomic monetary outcome plus ownership completion is absent, and drain can remain blocked with no pending worker.

Required correction: complete/update the ownership outcome in the same handoff transaction before commit, covering both legacy handoff and promotion branches. Prove upgrade from pre-pin legacy rows and a crash/reopen immediately after the committed handoff. Anchors: requirements 10.6/14.4 and task 17.3.

### F8 — High: required production claim metadata remains best-effort

Locations: `internal/core/billing/call_post_usage_worker.go:178`; `call_provider_cost_worker.go:223`; `economic_revision_worker.go:330`; `internal/infra/runtimebundle/cutover_wrapper_certification_test.go`.

The WithClaim constructors require an interface, but metadata read and validation failures are swallowed. Customer/provider workers return empty claim inputs; the economic worker continues with nil claim and default owner. This includes storage/cancellation/malformed-metadata errors, not only a deliberate no-existing-pin result. Durable posting paths still support preactive auto-pin behavior with nil claims, and the completed-provider-pin active bypass in F5 makes the distinction particularly important.

A durable decorator that implements the method but returns an error is accepted by composition and can silently take the fallback path. The wrapper certification's interface assertions, all-nil constructor failures, and source-string checks do not exercise this case. Metadata is also looked up at posting time rather than being a mandatory token returned by the original lease claim.

Required correction: define and propagate mandatory production claim authority, distinguish authorized first acquisition from metadata failure, and fail closed on operational or validation errors. Test an actual composed durable wrapper returning an error/malformed token and an actual leased worker across an epoch transition. Explicit in-memory test constructors may remain separate. Anchors: task 17.3, requirement 17.4.

### F9 — High: integrated certification does not establish the claimed production transition

Locations: `cutover_integrated_certification_test.go:424`, `:445`, `:450`, `:469`, `:531`, `:609`; `cutover_integrated_certification_postgres_test.go`; `customer_settlement_fence_b2b1_extra_test.go:22`.

Beyond the manufactured drain in F4:

- The positive V2 customer test directly advances the marker over pending preloaded work.
- The integrated stale-worker case mutates metadata in draining rather than waking a genuinely leased worker after active transition.
- The integrated concurrency test races ordinary V1-active customer/provider posting, not activation against all three monetary families.
- The integrated crash test closes one store, creates a different temporary database, and later reconstructs stores over the still-open second database. That is not a same-file close/reopen at each cutover crash boundary. Some focused B2b tests do provide useful reopen coverage, but not this integrated lifecycle.
- The integrated PostgreSQL test does not prove the advertised simultaneous provider/adjustment drain, activation, crash, or epoch races.

Required correction: certification must drive real production admission, claims, posting, queue completion, and coordinator transitions; assert journal/balance/pin outcomes together. Do not directly mark unfinished jobs processed or invent completion transaction IDs. Add deterministic PostgreSQL transition races and genuine same-database reopen boundaries. Anchor: task 17.3 and requirement 18.4.

## Fresh verification

All commands ran in the named feature worktree. No broad clean/reset or source change was used.

| Check | Result |
| --- | --- |
| `go test ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... -count=1 -timeout=15m` | PASS; billing 0.389s, billingstore 170.434s, runtimebundle 53.353s |
| Store tests matching `Test(AccountingCutover\|PostingOwnership\|Cutover\|CustomerSettlementFence\|ProviderChargeFence\|FinancialAdjustment)`, count 5, timeout 10m | PASS, 78.076s |
| Core/store/runtime tests matching `(B2b[1234].*(Concurrent\|Crash\|Stale\|Lease\|Reopen\|Claim)\|CutoverIntegrated\|Cutover.*Concurrent\|PostingOwnership.*Concurrent)`, count 5 | PASS; store 19.447s; runtime reported no matching tests, not wrapper proof |
| Configured PostgreSQL, `LIP_REQUIRE_POSTGRES=1`, integration-tagged marker/pin/coordinator/integrated/fence tests | PASS, 34.329s |
| Configured PostgreSQL integration tests matching `(AccountingCutover\|PostingOwnership\|CutoverCoordinator\|CutoverIntegrated\|B2b[1234]).*Postgres` | PASS, 17.060s |
| `go run ./internal/testkit/dbparity/cmd sqlite` | PASS, all ten catalog components |
| `LIP_REQUIRE_POSTGRES=1; go run ./internal/testkit/dbparity/cmd postgres-direct` | FAIL at metering-journal: `metering_components.value_present` integer/int4 versus expected boolean; earlier billing component passed. Same unrelated parity defect documented in the prior phase review; whole catalog is not green |
| `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...` | PASS |
| Scoped billing architecture tests | FAIL only in `TestBillingCoreStaysProviderAndPersistenceFree`, reporting the lipapi import-closure violation already recorded by the prior review |
| Remaining seven selected billing/runtime/public-DAG/shadow architecture tests, rerun excluding that known failing test | PASS, 2.724s |
| `gofmt -l` on changed tracked and untracked Go files; `git diff --check` | PASS, no formatting/whitespace findings |
| Changed-code placeholder and obvious credential-marker scan | No matches; this is not a comprehensive security audit |

The scoped architecture set was `TestBillingCoreStaysProviderAndPersistenceFree`, `TestBillingSettlementRejectsRawUsageAndMeteringInputs`, `TestNoSecondAuthoritativeCustomerBalanceReducerOutsideBillingStore`, `TestRuntimeBillingBoundaryHasNoStreamMonetarySettlement`, `TestPhase172ShadowV2HasNoMonetaryWriters`, `TestBillingRuntimeHasNoModeSelectorInProductionComposition`, `TestDualPlaneEconomicsPublicPackageDAG`, and `TestBillingBindingImportClosureIsPublicAndNeutral`.

Windows race execution was not performed; repository policy skips its normal Windows race target. No Linux race certification is claimed. Full `make test`, `make qa`, and opt-in test-cost were not run. Configured PostgreSQL tests are real backend executions, but their green sequential cases do not establish the missing transaction schedules. The 100-Go-file ratchet was not used as a blocker; this review is about correctness, not the user-authorized file-count allowance.

## TDD and certification evidence assessment

Inspected `%TEMP%/opencode/phase17-3*.txt` evidence. A, B1, and B2a RED logs establish missing-symbol/interface failures. B2b RED logs contain meaningful behavioral failures for missing fences, stale authorization, pins, and fault boundaries. The C RED artifact contains a hypothesis manifest but its executable failure is the undefined `NewCallPostUsageWorkerWithClaim` symbol; it is not a red demonstration of the claimed integrated crash/cutover behavior. C GREEN output and this review's broad reruns are genuine green test execution, subject to the assertion deficiencies above.

Positive boundaries retained: explicit durable marker/pin schemas, scoped marker identities, canonical error families, significant SQLite/PostgreSQL focused coverage, atomic pin/effect work in several ordinary posting paths, and unchanged no-post shadow architecture checks. Direct-adjustment empty-call identities and versioned key preimages were inspected; no additional blocker is asserted there. Funding/payment/policy provisioning is not reclassified as usage settlement merely to widen scope. Existing immutable revision/journal records mean that advancing a head projection is not, by itself, proof of deleted monetary history; F5 and F7 identify the concrete authorization/atomicity defects instead.

Re-review should start from real lifecycle counterexamples F1-F8 and replace the manufactured integrated completion steps. Passing the current suite alone is insufficient to accept task 17.3.
