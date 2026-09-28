# Task 5.3 Redesign Cycle 1 — final execution record (R13 consolidation)

This is the single authoritative execution record for Cycle 1 through
R15, rewritten at R13 and extended coherently at R14/R15 to replace
the accretion of per-turn sections.
Per-turn RED excerpts below are historical captured output reported
during that work order; they are not claims about the final tree.
Final-state results are in "Final GREEN" only. Cycle 1 replaces the
rejected 6.6k-line report proof engine (preserved read-only at
`stash@{0}` `backup/task5.3-proof-engine-rejected-after-redesign-20260918`;
salvaged concepts only: DTO/status/cursor/bounded-projection shapes and
rolling/pagination test ideas).

## 1. Final worktree and changed-file inventory

- Worktree:
  `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`,
  branch `feat/b-leg-usage-economics`, HEAD `0c032401`.
- Independent re-review inputs (read-only, never edited by
  implementation turns):
  `.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement5-3-cycle1-review.md`
  is mutable reviewer output: it was rewritten by each independent
  re-review (rejected verdicts with findings C1–C9, then F1/F2), so no
  frozen line count is recorded for it. Prior `wc -l` figures for the
  files below are superseded; every count here uses one explicit
  method: PowerShell `Get-Content -LiteralPath <file> |
  Measure-Object -Line`.

| Path | State | Lines | Role |
| --- | --- | --- | --- |
| `internal/core/billing/aleg_report.go` | new | 274 | DTO, query, cursor, `ALegReportReader` seam |
| `internal/core/billing/aleg_report_test.go` | new | 120 | 4 contract tests |
| `internal/core/billing/aleg_authority.go` | new (R12, R14) | 594 | Single pure customer-authority evaluator |
| `internal/core/billing/aleg_authority_test.go` | new (R12, R14, R15) | 539 | 21 authority table tests |
| `internal/infra/billingstore/reports_aleg.go` | new | 627 | Tx snapshot shell, pagination, DTO assembly |
| `internal/infra/billingstore/reports_aleg_customer_test.go` | new | 1802 | 49 `TestALegReport*` functions |
| `internal/infra/billingstore/reports_call_explanation.go` | modified | 249 | Private tx-aware `loadCallExplanationTx` extraction (R2) |
| `internal/infra/billingstore/reports_call_explanation_test.go` | modified | 143 | +1 missing-exposure regression (R2) |
| `internal/infra/billingstore/20260925000000_billing_aleg_report_scope.go` | new (R5) | 47 | 3 index-only DDL statements, both dialects |
| `internal/infra/billingstore/20260925000000_billing_aleg_report_scope_test.go` | new (R5) | 85 | Index-existence + EXPLAIN plan tests |
| `internal/infra/billingstore/20260812000000_billing_baseline.go` | modified (R5) | 332 | One-line migration registration |
| This file | rewritten (R13), extended (R14, R15) | — | Final-state record |

Tracked diff vs HEAD: 3 files, 136 insertions, 41 deletions. No HTTP,
writer, reconciliation, provider, schema-column, or spec/task-status
changes. No stash, commit, rebase, merge, or PR operations in any turn.

## 2. Scope and threat model

- One rollback-only read transaction defines `AsOf` for calls, legs,
  operation snapshots, and journals (PostgreSQL repeatable-read
  read-only; SQLite snapshot default). `AsOf` is assigned after
  `BeginTx` and is an output observation timestamp, never a
  caller-provided historical cutoff. No time travel, no finality flag.
- The read side is pure: no rating, settling, journal appends, balance
  changes, or lifecycle mutation. Read-only SQL scan of the report
  production files shows no `INSERT`/`UPDATE`/`DELETE`/`COMMIT`.
- Consistency of durable facts is validated under a trusted
  DB/writer boundary. Arbitrary coherent DBA forgery is out of scope:
  fingerprints prove content integrity, not writer provenance.
- Customer known economics require the exact canonical settlement
  marker (`CustomerSettlementSourceKey` + `customer_call_settlement`
  snapshot with recomputed current `snapshot:v1` integrity) plus the
  matching canonical journal, or a proven zero (unchanged
  snapshot AND no same-call settlement journal/chain). Everything
  else is pending (no proof yet) or unknown (conflicting/stray
  evidence with an issue). Unknown is never zero and never totals.
- Corrections net only through the exact same-scope immutable chain
  (account/book/currency/call/A-leg/empty-B-leg/operation plane,
  group binding, replacement-needs-reversal, raw-exact IDs).
  Arithmetic overflow fails the query (`ErrMoneyOverflow`).
- Pass-through rides a distinct adjustment plane (financial-ledger
  side signed); any pass-through evidence defers the call to pending
  and the production-writer join is Cycle 2. Provider COGS and
  exclusions stay pending in Cycle 1, never known zero.

## 3. TDD chronology (historical RED → GREEN)

Initial Cycle 1. RED (historical): core contract tests failed to build
(`ALegReportQuery` undefined); infra suite failed on missing
`QueryALegReport`. Two genuine behavioral REDs were fixed, not
weakened: the journal `SourceKey` is the settlement source key while
the marker `SourceKey` is the call ID, and a balanced pair nets to
zero so adjustment amounts sign by financial-ledger side. One
test-assertion bug was fixed honestly: call order is
`(sealed_at, call_id)`, asserted by repeat-traversal equality.
GREEN: DTO/cursor seam, tx snapshot shell, marker/canonical authority,
independent call/leg streams.

R1 status semantics (review C1/C2-era behavior). RED (historical):
`go test -count=1 ./internal/infra/billingstore/ -run
'TestALegReportZeroMarkerWithAdjustmentPending|
TestALegReportPositiveMarkerWithAdjustmentPending|
TestALegReportProviderNeverStartedStaysPending'` →
`expected: "pending", actual: "known"` (twice) and
`expected: "pending", actual: "known_zero"`. GREEN: any pass-through
evidence defers to pending with `customer_adjustment_pending`;
every leg reports `ALegProviderPending`; `ZeroLegs` stays zero.

R2 CallExplanation absence precedence (C6). RED (historical):
`TestSQLiteCallExplanationMissingExposureWithMalformedClosureIsNotFound`
→ `missing exposure with malformed closure = billingstore: decode
call usage: invalid character 'o' in literal null (expecting 'u'),
want ErrReportNotFound`. (The mandated `-run 'TestCallExplanation'`
pattern honestly matched nothing: existing names carry a `TestSQLite`
prefix; coverage used
`TestCallExplanation|TestSQLiteCallExplanation|TestSQLiteQueryOpenExposures`,
4/4 PASS.) GREEN: private loader gained `requireExposure`; public
`CallExplanation` passes true and keeps its `HasExposure` mapping;
snapshot mode unchanged.

R3 leg cursor exact-once (C4). RED (historical):
`TestALegReportExhaustedLegStreamExactOnce` →
`"[bc_...b-1 bc_...b-1]" should have 1 item(s), but has 2`;
`TestALegReportUnknownLegCursorRejected` →
`Expected error with "billing: invalid report query" in chain but got
nil`. GREEN: exhausted leg position carries forward as the validated
terminal; non-empty leg positions validate against the scope
(`ErrReportInvalid`). No cursor format change.

R4 scope-anchored authority (C3/C7). RED (historical): 7/7 FAIL, each
`expected: "unknown", actual: "known"` (foreign-A-leg canonicals,
wrong-plane pairs, duplicate markers, wrapped deltas, unanchored
chains, B-legged canonicals); the snapshot-mismatch guard passed
before and after. GREEN: `alegScope` threading, exact writer pair,
`reflect.DeepEqual` snapshot agreement, checked deltas,
deterministic two-pass chain evaluation, raw-exact IDs.
(Historical size note: 709 → 772 lines, superseded by R12.)

R5 bounded chunked reader + indexes (C5). RED (historical):
`TestALegReportBoundedFactLoading` → `"10" is not less than or equal
to "3"` (IN-list bound) and `"1" is not greater than or equal to
"3"` (single scope-wide marker query). GREEN: `alegReportScopeChunkSize`
chunked streaming for IDs/markers/journals; keyset call/leg pages;
page-union presence/closures; two aggregate counts; migration
`20260925000000` with the three indexes below plus plan tests.

R6 correction-only anchor (C1). RED (historical):
`TestALegReportCorrectionOnlyPositiveUnknown|
TestALegReportCorrectionOnlyNegativeUnknown` → both
`expected: "unknown", actual: "known"`. GREEN: linked claims evaluate
only with the exact canonical journal present, else
`customer_correction_unresolved`.

R7 operation plane (C2). RED (historical): 5/5 FAIL, each
`expected: "unknown", actual: "known"` (cross-operation chains,
canonical correction metadata, empty mode, reversed sequence,
backward version). GREEN: centralized plane predicate
(`customer_marker_invalid` for marker semantics;
`customer_journal_mismatch` for canonical root impurity) and
marker-kind-anchored correction links.

R8 row-anchored lineage (C3). RED (historical): 3/3 FAIL (foreign-A-leg
leg satisfied presence; two payload-drift fixtures succeeded instead
of failing with `ErrReportInvalid`). GREEN: scope-bound presence
join; `checkALegClosureLineage` / `checkALegLegLineage` replay
row-anchored expected records through `CheckCallUsageReplay` /
`CheckCallLegUsageReplay`; single decode-and-validate leg map.

R9 foreign call cursor (C4). RED (historical): 2/2 FAIL,
`Expected error with "billing: invalid report query" in chain but got
nil`. GREEN: `sealed_at` lookup resolves with exact
`call_id + account_id + a_leg_id` predicates.

R10 checked negation (C6). RED (historical):
`TestALegReportAdjustmentOverflowPending` → `Should not be:
-9223372036854775808` (raw unary minus exposed wrapped MinInt64).
GREEN: checked `ReportDifference(currency, 0, pairNet)` with
presence-gated pending (overflow contributes lineage only).

R11A presence/issues bounds (C5). RED (historical): 2/2 FAIL
(8-leg settled call resolved known; page-2 calls lost issue
context). GREEN: window-capped presence (`customer_leg_fanout`,
`MissingBLegIDs` suppressed over cap) and during-collection bounded
issue retention preserving page-call context with a single
`customer_issues_truncated` marker. Suite-hardening (also R11B):
package-var override tests dropped `t.Parallel()` (C8) — parallel
tests sharing the process observed the override window.

R11B journal/entry fanout + book gate (C5). RED (historical): 3/3 FAIL
(unbounded journals resolved stray; six-entry claim netted known 40;
nonfinancial settlement-shaped journal invisible to the book-filtered
loader). GREEN: per-call/per-transaction window-capped loaders
(`customer_journal_fanout` / `customer_entry_fanout`, never partially
netted) and the financial-book gate (`customer_book_conflict`).

R12 single core evaluator (C7). RED (historical): new table tests
failed to build (`undefined: ALegAuthorityScope`, `undefined:
ALegMarker`, `undefined: ALegSettlementKind`, `undefined:
EvaluateALegCallAuthority`); then 5/14 failed on a test-fixture bug
(fixtures set Before 100 / After 120, but the writer contract
decreases prepaid balances, so the delta was negative — fixtures
fixed to Before 120 / After 100, no production change); then `go vet`
failed on the one direct unit caller (`reports_aleg_customer_test.go:
undefined: classifyALegCall`), retargeted to the core evaluator with
assertions unchanged. GREEN: pure `EvaluateALegCallAuthority`
carrying all R1–R10 semantics; shell reduced to adapter + assembly;
`reports_aleg.go` 1102 → 654 lines with zero references to the
deleted engine.

Shared-worktree note (consolidated, was repeated R6–R11B): the
`TestALegReportBogusMarkerIntegrityUnknown` expectation naming
`customer_journal_mismatch` where the fixture plants literal
`"bogus-integrity"` was corrected identically to
`customer_marker_integrity` after external reverts between turns
(eighth occurrence at R14 RED time, same one-line repair);
status/subtotal assertions were always correct. Final tree passes
without further change.

R14 canonical-rooted correction graph + exact shape (review F1, the
only remaining blocker; all other C1–C9 checks and mechanical gates
passed). RED (historical): 6 new core tests and 4 new integrated
tests failed on the unfixed evaluator, each
`expected: "unknown", actual: "known"` —
`TestEvaluateALegCallAuthorityUnrelatedComponentUnresolved`,
`CompetingReplacementsUnresolved`, `ReversalCycleUnresolved`,
`DualLinkClaimUnresolved`, `DuplicateLinksDeterministic`,
`MalformedCorrectionShapeUnresolved`, and integrated
`TestALegReportUnrelatedCorrectionComponentUnknown` (same diff at
`reports_aleg_customer_test.go:374`), `CompetingReplacementsUnknown`,
`ReversalCycleUnknown`, `MalformedCorrectionShapeUnknown` (each also
leaking a known retail subtotal). The unrelated-component reversal
moved the canonical subtotal even though its chain was not rooted at
the canonical journal; competing replacements both netted; the cycle
netted; dual-linked and duplicate-linked claims netted; clearing-account
and extra-entry corrections contributed customer entries.
GREEN (core-only, no shell/schema/writer change):
`admitALegCorrections` builds the deterministic graph for the
selected marker plane over candidates ordered by
(account_sequence, transaction_id): phase A proves each claim locally
(exactly one raw-exact link kind — a claim carrying both `ReversalOf`
and `CorrectsTransactionID`, even to the same target, is ambiguous —
plus the existing scope/plane/fingerprint/group checks and the new
exact ledger shape); phase B rejects duplicate or competing claims on
one target (at most one reversal and one replacement per target);
phase C admits only claims transitively reachable from the
correction-free canonical root, keeping replacement-needs-reversal.
The shape rule (`checkALegCorrectionShape`) allows exactly the
canonical writer pair ledgers in either balanced orientation
(`customer_financial_account` debit plus `usage_revenue` credit, or
its reversal mirror, order-independent), derived from the trusted
settlement writer (`call_settlement.go` writer pair) and the generic
journal writer path (`prepareCorrection` constrains only duplicate
reversals, so cardinality and shape stay report-side fail-closed
rules). Any violation returns `customer_correction_unresolved` with
no partial netting; only admitted claims net, in deterministic order.
Essential fixture correction: `TestALegReportCanonicalCorrectionNets`
planted both link kinds on its reversal claim, which F1 now defines
as ambiguous — narrowed to the single-`ReversalOf` trusted reversal
shape (name, charge-12 assertions, and intent unchanged; still nets
under both old and new policy). Writer behavior and the DBA threat
scope are untouched.

R15 positive replacement-chain coverage + inventory refresh (review
F1/F2; test/evidence-only, production frozen). The R14 narrowing had
left no positive `CorrectsTransactionID` chain in the suite. Two
characterization tests were added FIRST and run against the unchanged
R14 implementation — both passed immediately, so no behavioral RED
existed and no production edit was made (nor manufactured):
`TestEvaluateALegCallAuthorityValidReplacementChainKnown` (canonical
20, full mirror reversal, `CorrectsTransactionID`-only replacement
re-asserting 12, journals in scrambled input order with a second
permuted evaluation asserting identical verdict/issues → known 12,
`20 − 20 + 12`) and
`TestALegReportValidReplacementChainKnown` (same chain planted in
deliberately non-account-sequence insertion order → known 12,
subtotal 12, op-key lineage, empty adjustments, zero issues). Every
R14 unresolved test is unchanged. Production-file SHA256 hashes
before and after are identical. F2 inventory staleness is resolved
by §1: one explicit `Get-Content | Measure-Object -Line` method for
all counts, and the review artifact recorded as mutable reviewer
output with no frozen count.

## 4. Final GREEN (fresh, final tree, R15 turn; test-only changes since R14)

All commands exit 0 (`ok`), run from the exact post-test tree:

| Command | Result |
| --- | --- |
| `go test -count=1 -run 'TestEvaluateALegCallAuthorityValidReplacementChainKnown' ./internal/core/billing/` + `go test -count=1 -run 'TestALegReportValidReplacementChainKnown' ./internal/infra/billingstore/` | PASS, both new tests (characterization-pass, first run) |
| `go test -count=1 -run 'TestEvaluateALegCallAuthority' ./internal/core/billing/` | PASS, 21/21 |
| `go test -count=5 -shuffle=on -run 'TestEvaluateALegCallAuthority' ./internal/core/billing/` | PASS |
| `go test -count=1 -run 'TestALegReport' ./internal/infra/billingstore/` | PASS, full focused set |
| `go test -count=5 -shuffle=on -run 'TestALegReport' ./internal/infra/billingstore/` | PASS |
| `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | PASS, full packages, no FAIL lines |
| `go test -count=1 -run '^(TestSQLiteBillingSchema\|TestALegReportScope\|TestDBParity)' ./internal/infra/billingstore/` | PASS |
| `go test -count=1 -run '^TestDBParity_SQLite$' ./internal/infra/billingstore/` | PASS |
| `go test -tags=integration -count=1 -run '^TestDBParity_PostgresDirect$' ./internal/infra/billingstore/` (with `LIP_REQUIRE_POSTGRES=1`) | PASS, direct PostgreSQL available (72.317s this tree) |
| `go test -count=1 -run 'TestPostJournalTransactionCorrection\|TestPostJournalTransactionRejectsSecondReversal\|TestPostJournalTransactionConcurrentDistinctReversals' ./internal/infra/billingstore/` | PASS, writer boundary unchanged |
| `go test -count=1 -run '^TestSQLiteCallExplanation' ./internal/infra/billingstore/` | PASS |
| `go build ./...` | clean |
| `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | clean |
| `gofmt -d` on all touched Go files | no output |
| `git diff --check` | clean (also rerun after this evidence edit) |

Skipped/unavailable: `go test -race` unavailable on Windows (cgo
toolchain; also recorded in the independent review). Counted test
inventory: 49 `TestALegReport*` functions, 21 authority, 4
report-contract, 2 scope-index/plan, plus the CallExplanation
regression set.

## 5. Final architecture

- Snapshot: `QueryALegReport` normalizes the query, opens one
  rollback-only tx (PG repeatable-read read-only), resolves the
  account/currency, validates cursor positions against the scope, and
  loads counts, call/leg pages, page-union presence/closures, and
  chunked per-call facts — all through that tx. `AsOf` is assigned
  post-begin.
- Bounded constants (all verified in-tree): scope chunk 200,
  presence legs/call 100, journals/call 16, entries/transaction 8,
  retained issues 128. Over-cap calls resolve unknown with a fanout
  issue, never partially netted; over-cap presence suppresses
  `MissingBLegIDs` instead of claiming completeness.
- Indexes (migration `20260925000000`, both dialects,
  `IF NOT EXISTS`, no columns/constraints/data):
  `idx_usage_call_records_account_aleg_sealed(account_id, a_leg_id,
  sealed_at, call_id)`,
  `idx_billing_journal_account_turn(account_id, turn_id)`,
  `idx_usage_leg_records_aleg_call_bleg(a_leg_id, call_id, b_leg_id)`.
  Plan tests lock covering-index seeks with no temp sort for the
  call scope/order/keyset, journal chunk, and leg join keyset; the
  marker chunk was already fully index-safe, so no fourth index.
- Authority: `internal/core/billing/aleg_authority.go` owns every
  financial decision (kinds, 14 issue codes, marker/canonical/chain/
  stray/adjustment rules). Corrections additionally pass the
  canonical-rooted deterministic graph (`admitALegCorrections`:
  single link kind, per-target cardinality, transitive reachability
  from the correction-free root with replacement-needs-reversal) and
  the exact writer-pair ledger shape (`checkALegCorrectionShape`)
  before any netting. The shell owns SQL, `alegCoreMarkers`
  adaptation, bounds, cursor, totals, and DTO assembly, and calls the
  evaluator once per call. No second proof engine remains.
- Cursor: opaque scope-bound token over durable `(call)` and
  `(call, b-leg)` identities; ordering never uses timestamps.
  `sealed_at` resolves server-side only. Partial, one-sided,
  cross-scope, oversize, and non-canonical tokens are rejected with
  `ErrReportInvalid`; exhausted legs carry forward as the validated
  terminal for exact-once traversal. Totals are scope-wide and stable
  on every page.
- Status: customer known (proven charge, including proven zero) /
  pending (no proof yet, pass-through present, provider-side always)
  / unknown (conflict/stray with issue). No final flag; resumed
  calls appear naturally in later snapshots.

## 6. Mapping and residuals

- Task 5.3 Cycle 1 scope (rolling `as_of` projections) is covered by:
  tx snapshot (§5), customer authority (chronology R1/R2/R4/R6/R7/R10,
  final §5), lineage/cursor (R3/R8/R9), bounded reader (R5/R11A/R11B),
  single evaluator (R12), canonical-rooted graph and exact shape
  (R14), positive replacement-chain coverage and measured inventory
  (R15). Review findings map as: C1→R6, C2→R7,
  C3→R4+R8, C4→R3+R9, C5→R5+R11A+R11B, C6→R2+R10, C7→R12,
  C8→R11A/R11B parallelism fix, C9→R13 rewrite, F1→R14+R15, F2→R15.
- Explicit residuals: Windows cgo race unavailable; coherent
  all-row forgery out of scope by threat model;
  provider known-zero vocabulary (`ALegProviderKnownZero`,
  `ZeroLegs`) remains in the DTO but Cycle 1 always emits pending
  with zero counts (advisory: remove/deprecate in the smallest
  contract follow-up before provider economics); pass-through
  production-writer join belongs to Cycle 2.
- Stale statements removed/reconciled by this rewrite: the "no
  migration/schema/test change" scope claims; the "CallExplanation
  test untouched" claim; the in-memory scope-slice pagination
  description; scope-wide fact-set loading and residual-risk text;
  all obsolete file/test counts; the R4 size narrative superseded by
  R12; per-turn shared-worktree file inventories; and the seven
  duplicated collateral tails (one consolidated note in §3).

## 7. Evidence limitations

RED excerpts are transcribed from per-turn captured output as
reported at the time; no machine-captured RED log artifact exists,
and they must not be re-run against the final tree as expectations
of failure. The R15 positive replacement tests are honestly recorded
as characterization-pass on first run (no behavioral RED existed, so
none was manufactured). GREEN results above are the fresh final-tree
runs from the R15 turn. R15 changed only the two owned test files
(one appended test each) and this record; production-file SHA256
hashes before and after are identical, so no code change is claimed
beyond tests and evidence.
