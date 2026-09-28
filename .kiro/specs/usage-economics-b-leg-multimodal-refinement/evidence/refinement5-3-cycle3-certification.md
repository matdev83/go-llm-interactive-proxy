# Task 5.3 Cycle 3 certification — rolling A-leg as_of projections

Date: 2026-09-19
Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`
Branch: `feat/b-leg-usage-economics`
Base: approved Cycle 1 commit `0120bdf8` + approved Cycle 2 commit `f35f867c`
Scope: certification-only closure of Task 5.3 (`_Requirements: 2.2, 2.6, 3.1, 3.3, 3.6`).
This file is evidence, not approval.

## Ownership and production boundary

Only two new test files were created; no production, helper, schema,
migration, HTTP, task-status, branch, or Kiro-state change was made:

- `internal/infra/billingstore/reports_aleg_cycle3_certification_test.go`
  (SQLite: 3 tests, reuses existing helpers unmodified)
- `internal/infra/billingstore/reports_aleg_cycle3_certification_pg_test.go`
  (`//go:build integration`: 1 direct-PostgreSQL mirror test)

`git status` shows exactly these two untracked files; `git diff` is empty.
Stashes were not inspected, applied, or dropped.

## RED log (honest)

The first run of the new SQLite suite failed once, on the certifier's own
wrong expectation, not on production behavior:

- `TestALegReportCycle3LateCorrectionsAcrossPlanes` asserted provider
  totals `(45, known 1, pending 0, zero 1, unknown 0)`, but the report
  returned `pending 1`: the customer-only leg `b-a` (call A) never started
  provider work, so by design every attributable B-leg in scope carries
  explicit completeness and `b-a` stays `pending`. The expectation was
  corrected to `(45, 1, 1, 1, 0)` with a comment. Rerun: PASS.
- No production defect was demonstrated, so no production file was
  touched and no `BLOCKED_FOR_REMEDIATION` arose. The two other SQLite
  tests passed on first run (characterization of existing behavior).

## Certification matrix

| # | Matrix item | New proof (executed this cycle) | Inherited proof (re-executed, not duplicated) |
| --- | --- | --- | --- |
| 1 | One A-leg, sequential BillingCallIDs across DONE/terminal closure; distinct IDs + new B-legs; earlier facts immutable; later projection includes both exactly once; no finality marker | `TestALegReportCycle3RollingTwoCallsExactlyOnce` (SQLite) + `TestPostgresCycle3RollingReadOnlyCertification` (PG): distinct IDs asserted; call-1 charge/opkey/status identical before/after call 2; `CallCount` 2, retail 20+30; limit-1 cursor walk unions 2 calls + 2 legs exactly once in deterministic repeat order; idle re-query renders identical logical snapshot | `TestALegReportSnapshotStableAndRolling` (resumed call appears naturally) |
| 2 | Retirement/retention/idle creates zero new usage, charge, adjustment, journal, head, fence, snapshot; row-count/hash equality at same logical snapshot | `TestALegReportCycle3RetirementAndReadOnlyNoWrites`: 12-table row counts + canonical DTO identical across baseline, 3 repeats, and final query; writer-silence between queries IS the retirement/idle transition (no billing retirement writer exists; retention is storage-only) | — (no prior retirement test existed; this was a real gap) |
| 3 | Late post-terminal updates without reopening execution: customer revision, pass-through +/-, provider +/-/zero + multi-child; earlier snapshot stable; latest changes exactly once | `TestALegReportCycle3LateCorrectionsAcrossPlanes`: 20→12 customer netting with `usage_call_records`/`usage_leg_records` counts unchanged; pass-through -20 then +10 validated; provider charge-a 30→25 + charge-b 20 = 45 with recorded-zero sibling; pre-correction DTO copy retains 20-view; limit-1 walk unions 3 calls + 3 legs exactly once; PG mirror covers provider 50→40 correction with usage counts unchanged | `TestALegReportPassThroughLateRevisionVisible`, `TestALegReportProviderCorrectionAdvancesCurrent`, `TestALegReportProviderChildCorrections`, `TestALegReportCanonicalCorrectionNets` |
| 4 | Bounded pagination/chunking: exactly-once deterministic order; cursor prevents duplicates/omissions under appended evidence; over-cap fails closed, never partial | `cycle3UnionPages` helper (double-walk order identity + exactly-once union) exercised in all 4 new tests, including after late appended evidence (items 1, 3) | `TestALegReportBoundedFactLoading`, `TestALegReportBoundedIndependentCursors`, `TestALegReportExhaustedLegStreamExactOnce`, `TestALegProviderHeadLoaderBounded`, `TestALegReportProviderLoaderLegBoundFifty` (+ PG `TestPostgresProviderLoaderLegBoundFifty`, `TestPostgresProviderNineChildrenFanout`, `TestPostgresProviderOverCapRotationUnknown`) |
| 5 | Adversarial authority stays pending/unknown/excluded, never known | (mapped only) full fail-closed behavior re-verified by re-running the suites below | Customer suite: stray/foreign/conflicting markers, competing replacements, reversal cycles, malformed/padded/cross-scope/cross-currency/cross-A-leg chains, duplicate settlements, snapshot overflow. Pass-through suite: foreign heads, amount mismatch, stale/competing revisions, unrelated legacy. Provider suites (both dialects): empty/foreign/malformed subjects and fences, cross-call lineage, duplicate subject keys (incl. same-value), pending-first-zero, mixed/duplicate/overflow children, gate-owner drift. |
| 6 | Report path read-only: identical DTO on repeats; DB-level write counters for all touched billing tables | `TestALegReportCycle3RetirementAndReadOnlyNoWrites` (3 repeats, 12 tables) + PG mirror (3 repeats, 12 tables). DTO identity is modulo `AsOf`, which is an output observation timestamp (`time.Now` at query time): canonical JSON deletes `AsOf` and compares all other fields byte-for-byte. Implementation note: `QueryALegReport` runs in a rollback-only transaction (`reports_aleg.go:79-93`; `ReadOnly` on PG). | `report_integrity_test.go` / `review_fix_test.go` read-only assertions |
| 7 | DTO totals/known-zero/unknown reconciliation; deterministic lineage; provider/pass-through never leak into retail | `TestALegReportCycle3LateCorrectionsAcrossPlanes`: retail 72 = 12 + 60 exactly; provider 45 = 25 + 20 child sum with charge-ordered children; `ZeroLegs` 1 via recorded-zero sibling; `PendingCalls` 1 (provider-only call); `UnknownCalls/Legs` 0; adjustments validated and excluded from retail | `TestALegReportProviderTwoChildrenKnown` (sum/lineage), `TestALegReportProviderOneZeroChildKnown`, `TestALegReportProviderMixedZeroNonzeroKnown`, `TestALegReportPassThroughRevisionsKnown` (retail carries settlement only) |
| 8 | SQLite + live direct PostgreSQL dual-dialect parity | `TestPostgresCycle3RollingReadOnlyCertification` PASS (17.1s); `TestPostgresProvider*` suite PASS (33.6s); billingstore `TestDBParity_PostgresDirect` PASS (66.5s); SQLite parity PASS | Cycle 2 PG duplicate/malformed/poison suites re-executed via the runs below |

## Requirements mapping (2.2, 2.6, 3.1, 3.3, 3.6)

- 2.2 (BillingCallID groups one invocation; totals are B-leg projections):
  items 1, 7 — per-call summaries sum to retail; no call-level meter.
- 2.6 (A-leg totals are rolling projections, never one-time final
  settlement): items 1, 2, 6 — no finality field exists on
  `billing.ALegReport`; idle re-queries are stable; resumed calls appear.
- 3.1 (no A-leg completion marker required to recognize economics):
  items 1, 3 — reports serve settled economics with the A-leg open.
- 3.3 (resumed invocation gets its own BillingCallID/B-legs; priors
  immutable): item 1 — distinct IDs, immutable call-1 lineage, union count.
- 3.6 (retention/retirement creates no usage or cost): item 2.

## Commands and results (all in the exact worktree, branch `feat/b-leg-usage-economics`)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestALegReportCycle3' ./internal/infra/billingstore/` | PASS (3/3, 0.17s) |
| `go test -count=5 -shuffle=on -run 'TestALegReportCycle3' ./internal/infra/billingstore/` | PASS |
| `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | PASS (billing 0.66s, billingstore 42.56s, stdhttp all PASS) |
| `$env:LIP_REQUIRE_POSTGRES="1"; go test -tags=integration -count=1 -run 'TestPostgresCycle3RollingReadOnlyCertification' ./internal/infra/billingstore/` | PASS (17.1s, direct PostgreSQL reachable) |
| `$env:LIP_REQUIRE_POSTGRES="1"; go test -tags=integration -count=1 -run 'TestPostgresProvider' ./internal/infra/billingstore/` | PASS (33.6s) |
| `make test-db-parity-sqlite` | PASS (billingstore 16.0s + all components) |
| `$env:LIP_REQUIRE_POSTGRES="1"; go test -tags=integration -count=1 -run 'TestDBParity_PostgresDirect' ./internal/infra/billingstore/` | PASS (66.5s) |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -d` over the two new test files | no output |
| `git diff --check` | PASS |
| `go test -race -count=1 -run 'TestALegReportCycle3RollingTwoCallsExactlyOnce' ./internal/infra/billingstore/` | UNAVAILABLE: Windows `cgo.exe` exit status 2 before package tests (same platform gate as Cycles 1-2) |

## Skipped / failed checks (stated plainly)

- Race: unavailable on this Windows cgo toolchain; fails at build, not at test.
- Full `make test-db-parity-postgres-direct`: FAILS in
  `internal/infra/metering/journalstore` —
  `TestDBParity_PostgresDirect/MigrationAndSchemaParity`:
  `metering_components.value_present` type mismatch (`int4` vs boolean).
  This package is outside Task 5.3 ownership and untouched by this change
  (diff is two new billingstore test files only), so the failure is
  pre-existing relative to this certification. Billingstore-scoped
  PostgreSQL parity (above) passes.

## Residual risks

- `AsOf` equality is logical (modulo observation timestamp), not byte
  identity: two queries can never share a timestamp. Any future
  historical-`as_of` requirement would need a new query contract.
- The retirement proof is writer-silence by construction (no billing
  retirement writer exists). If a future lifecycle feature adds an
  A-leg-retirement hook that touches billing tables, this test still
  guards the report path but a new hook-specific test would be needed.
- Race verification remains unavailable on this toolchain.
- The out-of-scope metering journalstore PG parity mismatch needs a
  separate owner and may gate release-wide `make test-db-parity`.

---

# C3R1 addendum (2026-09-19) — SQLite lifecycle/pagination/immutability/read-only repairs

Reviewer blockers 1-4 closed by test-only changes to
`internal/infra/billingstore/reports_aleg_cycle3_certification_test.go`
(rewritten + extended; no production file touched; the PostgreSQL mirror
file is untouched — PG equivalence is deferred to the next follow-up).
`TestALegReportCycle3RollingTwoCallsExactlyOnce` was replaced by the
stronger `TestALegReportCycle3RealLifecycleFullDTOImmutable`;
`TestALegReportCycle3RetirementAndReadOnlyNoWrites` was rewritten on
content hashes; `TestALegReportCycle3LateCorrectionsAcrossPlanes` gained
an AsOf observation assertion. This file claims evidence only, not
approval.

## Blocker 1 — old token vs appended evidence

`TestALegReportCycle3OldTokenContinuationAfterAppend`: real page 1
(limit 1) with its minted opaque token is retained; a third
BillingCallID/B-leg/charge is appended through the production terminal
path; the OLD token is resumed to exhaustion; then a fresh projection is
walked. Proven: token stays valid; original calls/legs each exactly once
across old pages (no duplicate/omission); appended call at most once in
the continuation (live tail, never duplicated); resumed pages carry LIVE
scope-wide totals (`CallCount` 3, retail 90); every resumed page mints a
fresh non-decreasing AsOf; the fresh walk yields all three exactly once
with originals in identical relative order to the pre-append walk.

Honest RED (scratch controls, since deleted): a literal frozen-snapshot
demand (`CallCount == 2` on old-token resume) FAILS — actual is 3.
Cursors are keyset positions over live data, not frozen snapshots:
totals use an unfiltered scope-wide `COUNT(*)` (reports_aleg.go:122-125),
both streams re-evaluate keyset `WHERE` clauses per query
(reports_aleg.go:146/163), and each query runs in its own rollback-only
read transaction (reports_aleg.go:79-93). No requirement demands frozen
cross-query snapshots; Requirement 3.3 demands resumed calls appear,
which live continuation satisfies. The certification therefore asserts
the implemented semantic (exactly-once originals + live tail), not the
frozen one. This is not a production defect: no BLOCKED state arose.

## Blocker 2 — real lifecycle, full-DTO immutability

`TestALegReportCycle3RealLifecycleFullDTOImmutable` drives closure
through the runtime-owned `billing.TerminalUsageSink` interface
(`internal/core/billing/append.go:17-20`) with the store bound as the
sink — the same binding production uses
(`billing_compose_test.go:193`; reached at request-terminal via
`billing_admission.go:426` for calls and `billing_leg.go:538` for legs)
— with `TurnOutcomeCompleted` / `LegOutcomeWinner` DONE/terminal
outcomes, then `AdmitExposure` + `ApplyCallBillingResult`. The A-leg
idles with no billing writer invoked: the sole production retirement
observer (`compile_generation.go:208-218`) ends only prompt-cache and
conversation-view state, so writer-silence IS the faithful
retirement/idle transition (no retirement writer was invented). Resume
allocates via `billing.NewBillingCallID` with a new B-leg through the
same sink path. The complete canonical call-1 DTO (Calls entry plus all
its contributions, every lineage field) is byte-identical before/after;
retail 60 → 85; provider totals hold.

## Blocker 3 — content hashes, all tables, work state

`cycle3SnapshotHashes` captures SHA-256 over canonical full-row dumps
(`SELECT * ORDER BY rowid`, existence-verified via `sqlite_master`) for
all 13 report-read tables — the 12 previous ones plus
`billing_economic_revision_work_state`, which the report reads at
reports_aleg.go:1609 (revision-pending probe; table list cross-checked
against every `FROM`/`JOIN` in reports_aleg.go). The rewritten
read-only/retirement test asserts hash identity across baseline, three
repeats, and final query: zero new charge/head/fence/snapshot/journal or
work-state mutation. Hash sensitivity was proven RED-capable by scratch
control (since deleted): a same-count `UPDATE ... last_error` on
`provider_cost_work` changed that table's hash (PASS), so hidden
same-count mutations cannot pass silently.

## Blocker 4 — AsOf reconciliation

`cycle3BusinessJSON` compares the full business DTO; `AsOf` is excluded
only as a documented output observation clock
(`asOf := time.Now().UTC()`, reports_aleg.go:93; core: "an output
observation timestamp for the returned snapshot, not a
caller-provided historical cutoff"). `cycle3RequireAsOfObserved`
separately asserts non-zero AsOf on every snapshot and a non-decreasing
observation clock across page-1 → resumed pages → fresh projections and
across idle repeats (applied in all four SQLite tests). No snapshot
identity is deleted: cursors, totals, lineage, and counts are all inside
the compared business DTO.

## C3R1 verification (branch `feat/b-leg-usage-economics`)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestALegReportCycle3' ./internal/infra/billingstore/` | PASS (4/4) |
| `go test -count=5 -shuffle=on -run 'TestALegReportCycle3' ./internal/infra/billingstore/` | PASS |
| `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | PASS (billing 1.5s, billingstore 45.8s, stdhttp all PASS) |
| `make test-db-parity-sqlite` | PASS |
| `go build ./...` / `go vet ./...` / `gofmt -l` / `git diff --check` | PASS / PASS / clean / PASS |

Residual: race still unavailable (Windows cgo); PG equivalence and
customer production correction explicitly deferred to the next focused
follow-up; the metering journalstore PG parity mismatch (out of scope,
pre-existing) is unchanged.

---

# C3R2 addendum (2026-09-19) — production customer correction/replay proof (SQLite)

Reviewer advisory closed by test-only changes to
`TestALegReportCycle3LateCorrectionsAcrossPlanes` in
`internal/infra/billingstore/reports_aleg_cycle3_certification_test.go`
(no production file touched; the PostgreSQL mirror is untouched — PG
equivalence stays deferred). The raw `plantALegSettlementJournal`
customer reversal was replaced with the repository's real correction
writer. This file claims evidence only, not approval.

## Production APIs

- Initial settlement: `AdmitExposure` + `ApplyCallBillingResult`
  (via `seedALegCustomerCall`; charge 20, `ExpectedBLegIDs ["b-a"]`).
- Late correction: `DurableStore.postJournalTransaction`
  (`internal/infra/billingstore/journal_store.go:77`) with
  `ReversalOf` = canonical settlement journal ID. The writer validates
  input, enforces correction linkage (`prepareCorrection`: same-scope
  target, single posted reversal per canonical, replacement-requires-
  reversal, group derivation), allocates the account sequence, seals the
  semantic fingerprint, and replays idempotently by source key. The test
  leaves `CorrectionGroupID` empty so the writer derives the canonical
  group, then asserts `ReversalOf` and `CorrectionGroupID` both equal the
  canonical source key.
- Note: `ApplyCallBillingResult` itself is one-shot per call (same
  fingerprint replays as `Replayed: true`; different charge conflicts at
  `call_settlement.go:127-137), so post-settlement customer corrections
  flow through the journal correction writer — the same writer whose
  linkage contract `TestPostJournalTransactionCorrectionLinksAreAuditable`
  covers. No fence, head, snapshot, revision, or idempotency path is
  bypassed: all are exercised by the writer on every attempt below.

## Proven

- Initial 20 then writer-posted reversal of 8 updates the rolling report
  20 -> 12 exactly once (call A known, `CustomerChargeKnown`).
- Replay of the identical correction returns the same identity and
  sequence with zero durable content-hash change (13-table
  `cycle3SnapshotHashes` equality).
- Stale correction (same source key, amount 9) fails with
  `ErrIdentityConflict` and zero hash change (CAS-loser analog at the
  source-key identity).
- Second reversal of the same canonical (race loser, new source key)
  fails with `ErrCorrectionInvalid` ("already has a posted reversal")
  and zero hash change.
- The report business DTO is byte-identical across replay/stale/loser
  attempts with non-decreasing AsOf; usage execution row counts are
  unchanged (no reopened execution).
- Selected B-leg lineage/policy/operation anchors are production-real:
  `ExpectedBLegIDs ["b-a"]`, no missing legs, `CustomerOperationKey`
  equals the production settlement source (`sourceA +
  ":customer_call_settlement"`).
- Pass-through (-20/+10 validated) and provider (45 = 25+20 checked
  child sum, recorded-zero sibling) planes in the same test are
  unchanged and correct: retail 72 = 12+60, provider 45, adjustments
  excluded from retail; the pre-correction DTO copy still carries the
  20-charge view.

## C3R2 verification (branch `feat/b-leg-usage-economics`)

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestALegReportCycle3LateCorrectionsAcrossPlanes'` | PASS |
| `go test -count=5 -shuffle=on -run 'TestALegReportCycle3'` | PASS |
| `go test -count=1 -run 'TestPostJournalTransaction\|TestSQLiteCallSettlement\|TestApplyCallBilling\|TestCallSettlement'` | PASS (16.1s) |
| `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | PASS (billingstore 40.4s, all stdhttp PASS) |
| `make test-db-parity-sqlite` | PASS |
| `go build ./...` / `go vet ./...` / `gofmt -l` / `git diff --check` | PASS / PASS / clean / PASS |

RED note: the rewritten correction block passed on first run
(characterization of existing writer+report behavior: the writer-sealed
reversal carries the same canonical fields the report evaluates, so it
nets identically to the retired planter shape). No production defect was
demonstrated; no production edit made; no BLOCKED state arose. The
replay/stale/loser assertions are discriminating (each asserts the
specific writer error plus hash equality — a silent-mutation writer
would fail them).

Residual: PG mirror of the writer-posted customer correction is deferred
to the next task; race still unavailable (Windows cgo).

---

# C3R3 addendum (2026-09-19) — direct PostgreSQL equivalence (SQLite↔PG)

Reviewer blocker closed by replacing the narrow
`TestPostgresCycle3RollingReadOnlyCertification` with one integrated
`TestPostgresCycle3FullEquivalenceCertification` in
`internal/infra/billingstore/reports_aleg_cycle3_certification_pg_test.go`
(test/evidence only; no production file touched). The old narrow test is
removed as fully superseded; shared fixtures and assertions are reused
from the untagged companion files (same package under the integration
tag) — only content hashing has a PostgreSQL variant
(`cycle3PGSnapshotHashes`: `row_to_json` dumps in sorted order plus
`information_schema` existence checks, instead of `sqlite_master` /
`rowid`). This file claims evidence only, not approval.

## SQLite↔PG assertion mapping (materially equivalent facts on live PG)

| SQLite Cycle 3 contract | PG equivalence assertion |
| --- | --- |
| Old-token continuation: token valid, originals exactly once, appended call at most once, live totals, fresh AsOf (`OldTokenContinuationAfterAppend`) | Same: token over 2 settled calls; call C appended with provider economics; resume walk yields A/B exactly once, C at most once; resumed pages live (`CallCount` 3, retail 80); fresh walk all exactly once |
| Real lifecycle + full-DTO immutability via `billing.TerminalUsageSink` (`RealLifecycleFullDTOImmutable`) | Same: `sink.AppendLeg/AppendCall` closure, `AdmitExposure` + `ApplyCallBillingResult` settlement, `NewBillingCallID` resume; complete call-A DTO byte-identical after appended evidence |
| Idle/retirement read-only with 13-table content hashes incl. `billing_economic_revision_work_state` (`RetirementAndReadOnlyNoWrites`) | Same: `cycle3PGSnapshotHashes` over all 13 tables; business DTO + hashes identical across 3 idle repeats with non-decreasing AsOf |
| Writer-posted customer reversal 20→12, replay/replay-identity, stale `ErrIdentityConflict`, loser `ErrCorrectionInvalid`, zero hash mutation, lineage anchors (`LateCorrectionsAcrossPlanes` C3R2) | Same: `postJournalTransaction` reversal with writer-derived group; replay same ID/sequence; stale and loser errors; hash equality after each; usage counts unchanged; `ExpectedBLegIDs ["b-a"]`, production operation key; retail 72, provider (45,1,1,1,0); earlier 20-view copy stable |
| Pass-through +/− (−20/+10 validated) and provider multi-child (30→25, +20 = 45, recorded-zero sibling) with no plane leakage | Same totals and lineage order (`charge-a` before `charge-b`), adjustments excluded from retail |

## C3R3 verification (branch `feat/b-leg-usage-economics`, direct PostgreSQL live)

| Command | Result |
| --- | --- |
| `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -run TestPostgresCycle3FullEquivalenceCertification` | PASS (27.3s, first run) |
| Same, `-count=5` | PASS (125.6s) |
| `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -run 'TestPostgresProvider'` (50→9 bound, poison suites) | PASS (33.5s) |
| `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -run TestDBParity_PostgresDirect ./internal/infra/billingstore/` | PASS (66.2s) |
| SQLite Cycle3 `go test -count=5 -shuffle=on -run TestALegReportCycle3` | PASS |
| `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | PASS (billingstore 40.1s) |
| `make test-db-parity-sqlite` | PASS |
| `go build ./...` / `go vet ./...` / `go vet -tags=integration ./internal/infra/billingstore/` / `gofmt -l` / `git diff --check` | PASS / PASS / PASS / clean / PASS |
| `make test-db-parity-postgres-direct` (full repo) | billingstore PASS; only failure remains the pre-existing `metering/journalstore` `value_present int4 vs boolean` mismatch (unchanged from C3R1, not fixed per scope) |

RED note: the integrated PG test passed on first run (characterization:
same dialect-branched report/writer code certified in Cycle 2, exercised
through the same production writers as SQLite). No defect demonstrated;
no production edit; no BLOCKED state. Assertions are discriminating
(specific errors, exact totals/sequences, hash equality after each
mutation attempt).

Residual: race unavailable (Windows cgo); SQLite↔PG parity for the new
contracts now rests on the integrated PG test plus the retained PG
poison/bound suites; full-repo PG parity still gated only by the
pre-existing out-of-scope metering mismatch.
