# APPROVED — Task 5.3 Cycle 2 final independent review after C2R7

Date: 2026-09-19
Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`
Branch: `feat/b-leg-usage-economics`
Review base: approved Cycle 1 commit `0120bdf8` plus the live uncommitted Cycle 2 tree

## Verdict

**APPROVED for Cycle 2 only. Cycle 3 may start.**

The C2R6 duplicate-key blocker is fixed. Top-level `b_leg_id` cardinality is
now checked before provider-head retention: SQLite uses `json_each(subject_json)`
and PostgreSQL deliberately uses `json_each(subject_json::json)` rather than
`jsonb`, preserving duplicate object members. Any count other than exactly one
poisons the whole call through a `SELECT DISTINCT call_id` anomaly result. The
marker is evaluated before head grouping, retained-fact selection, revision
state loads, and operation-snapshot loads.

No production, test, schema, migration, HTTP, task-status, branch, commit, PR, or
Kiro-state change was made by this review. The only review-owned write is this
evidence file; the other dirty files are the shared implementer's Cycle 2 tree.

## Scope and traceability

I inspected the live status/diff and all untracked Cycle 2 files before relying on
reports, the three Cycle 2 implementation evidence files, the prior review
evidence, approved requirements 2.2, 2.6, 3.1, 3.3, and 3.6, the Rolling
Economic Query design, Task 5.3, and the current report/provider/core and
writer-fence code.

The reviewed implementation remains within the approved Cycle 2 scope: report
readers/evaluators, provider revision/fence writer behavior and focused tests.
There are no schema, migration, HTTP, or unrelated writer semantic changes.

## C2R7 duplicate and malformed-subject proof

`providerSubjectKeyCountExpr` counts only top-level keys named `b_leg_id`:

- SQLite: `COUNT(*) FROM json_each(h.subject_json) WHERE key = 'b_leg_id'`.
- PostgreSQL: `COUNT(*) FROM json_each(h.subject_json::json) WHERE key =
  'b_leg_id'`. The `json` cast is intentional; `jsonb` would canonicalize away
  duplicate members.

`loadALegProviderHeadAnomaliesTx` requires this count to equal one in addition
to exact account/A-leg/call/B-leg enumeration. Its outer query is
`SELECT DISTINCT h.call_id`, so duplicate payload members and pseudo-identities
cannot fan out the anomaly result. The call poison is joined before
`groupALegProviderHeads`, `retainALegProviderFacts`, and all derived revision or
snapshot loads.

The new production-report tests exercise all required duplicate shapes on both
dialects: first valid/last foreign, first foreign/last valid, two different
enumerated B-legs, and the same B-leg twice. Every affected call is unknown with
no provider children or subtotal, while the neighboring call remains known.

Malformed and nested behavior is fail-closed:

- Invalid JSON fails the SQLite JSON function or PostgreSQL cast/query before a
  report can produce a known subtotal.
- A valid non-object, array, `null`, empty object, or nested-only
  `b_leg_id` has no exactly-one top-level key. SQLite `json_each` probes return
  zero matching keys for nested and array cases; PostgreSQL either returns zero
  for a top-level object without the key or rejects a non-object with a query
  error. Either path produces no known result.
- A top-level non-string/foreign value does not match the exact enumerated
  B-leg, so the same call-poison predicate applies.

The previous C2R5/C2R6 pseudo-leg failure is therefore closed without changing
Go decoding semantics: SQL no longer has to agree with first-versus-last
duplicate selection because duplicate cardinality itself is an anomaly.

## Provider authority and boundedness accepted

- Heads, posting fences, execution fences, work, and derived facts use the
  deterministic identity plus total per-(call, B-leg) `ROW_NUMBER` windows over
  the full candidate set before filtering. The cap+1 probe is retained and
  overflow fails closed with no partial subtotal.
- Unattributable or foreign B-leg evidence poisons the whole call; attributable
  malformed posting/execution lineage poisons only the exact enumerated leg.
  Work keys require the exact `call:b-leg` form and conservatively poison the
  call on mismatch. Marker queries are bounded to one row per call for
  pseudo-identity flooding; exact-leg rows can name only enumerated legs.
- The shared execution fence remains the latest-applied-child gate. Every
  applied child revision advances it transactionally; replay, stale, ignored,
  and CAS-loser paths do not. Its full owner envelope is checked against the
  present evaluated child, with distinct fence domains preserved.
- Provider-charge child heads are discovered and evaluated independently with
  per-child head/posting/execution proof, deterministic lineage ordering, and a
  checked atomic sum. Any pending, unknown, duplicate, mixed, over-cap,
  overflow, stray, or malformed payable child suppresses the entire aggregate.
- Zero snapshots bind writer-derived source, account/kind/fingerprint,
  integrity, revision/sequence, balances, and current operation/transaction.
  All payable zero children become explicit `known_zero`/`ZeroLegs`; no zero
  child is represented as a misleading known monetary child.
- Exact subject kind/ID and store/account/A-leg/call/B-leg ancestry, including
  provider-charge child identity, are validated. Current operation, original
  transaction, terminal monetary transaction, head/revision/hash/fingerprint,
  currency, and posting/execution fences agree before nonzero authority is
  known.
- Retail customer subtotal and provider COGS remain separate. Failed, retry,
  loser, and otherwise payable legs are not filtered by surfaced/winner
  selectors. Pass-through adjustments retain exact A-leg lineage and do not
  contaminate retail totals.

## Mechanical verification

Fresh commands run in the exact worktree:

| Check | Result |
| --- | --- |
| SQLite duplicate/order/two-enumerated/same-value tests plus prior poison, 50-child, same-charge, nine-child, and zero/mixed suites | PASS, `go test -count=1 ...`, 0.456s |
| Direct PostgreSQL duplicate/order/two-enumerated/same-value tests plus prior poison, 50-child, nine-child, zero, and same-charge suites with `LIP_REQUIRE_POSTGRES=1` | PASS, 27.743s; direct PostgreSQL reachable |
| SQLite `json_each` probes: different duplicate `2`, same-value duplicate `2`, nested `0`, array `0`, `null` `0`, scalar `0` | PASS |
| Shuffled repeated provider/core suites (`-count=3 -shuffle=on`) | PASS; core 0.592s, store 3.349s |
| SQLite DB parity | PASS, 16.009s |
| Direct PostgreSQL DB parity | PASS, 67.397s |
| Full `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | PASS; billing 0.668s, billingstore 44.611s, stdhttp packages PASS |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -d` over touched report/provider files | PASS; no output |
| `git diff --check` | PASS |
| Focused `go test -race` | UNAVAILABLE; Windows `cgo.exe` exited status 2 before package tests |

The C2R5 RED evidence remains credible: a real 50-child writer population is
reduced before Bun Scan to nine heads and nine posting fences, with one
execution fence and one work row for the flood leg on SQLite and direct
PostgreSQL. The current duplicate tests prove that SQL `json_each` cardinality
poisons before retention without changing that bound.

## Changed file and Cycle 3 gate

Only this review evidence file was written by this review:

`.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement5-3-cycle2-review.md`

**Cycle 3 may start.**

## Residual risks

- Race verification remains unavailable on this Windows cgo toolchain; all
  non-race focused, parity, full-package, build, vet, formatting, and diff
  checks passed.
- The exact-leg anomaly result is bounded by the enumerated usage-leg
  population rather than the retained cap, but pseudo/foreign identities are
  call-distinct and cannot widen it. The retained provider fact loads remain
  hard cap+1 bounded before Scan.
- Invalid JSON is surfaced as a read/query error rather than a DTO with an
  `unknown` row. This is fail-closed and cannot produce a known subtotal; any
  future requirement to return structured unknown rows for query corruption
  should be specified separately.
