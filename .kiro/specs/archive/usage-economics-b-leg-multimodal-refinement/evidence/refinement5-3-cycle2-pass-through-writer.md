# Task 5.3 Cycle 2A1 — production pass-through A-leg retention

Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`,
branch `feat/b-leg-usage-economics`, base HEAD `0120bdf8` (Cycle 1 customer
authority approved and committed; clean tree at slice start).

## Bug

`costPassThroughHeadRow` carries `ALegID` (`bun:"a_leg_id"`) and
`persistCostPassThroughHeadInTx` INSERTs `call.ALegID`, but
`costPassThroughHeadSelect` omitted the `a_leg_id` column. Bun maps the raw
SELECT by column name, so every `loadCostPassThroughHead` returned `ALegID:
""`. `applyCostPassThroughRevisionAttempt` then emitted the immutable
adjustment journal with `ALegID: row.ALegID` — empty-A-leg lineage on a
production-written head.

## TDD

RED (`TestSQLiteCostPassThroughRevisionRetainsALegLineage`, new file
`internal/infra/billingstore/cost_pass_through_aleg_retention_test.go`):
head created through the real production settlement path
(`AppendCallUsage` → `AdmitExposure` → `ApplyCallBillingResult` with a
provisional pass-through state, posted 60 of safe-bound 100), then the actual
`DurableStore.ApplyCostPassThroughRevision`. No seeded journal/head state.
Pre-fix failure:

```text
--- FAIL: TestSQLiteCostPassThroughRevisionRetainsALegLineage (0.04s)
    cost_pass_through_aleg_retention_test.go:133: positive adjustment a_leg_id = "", want "a-leg-pass-through-retention" (loaded head lost A-leg identity)
```

The direct durable-column read in the same test confirmed the writer had
persisted `a_leg_id` correctly — the loss was purely the reader.

Fix (one line, `internal/infra/billingstore/cost_pass_through_store.go`):
added `a_leg_id` to `costPassThroughHeadSelect` in the exact scan-column
position matching `costPassThroughHeadRow` (after
`settlement_operation_key`, before `original_transaction_id`). No migration:
the column already exists in both dialects and INSERT already writes it.

## Coverage (GREEN)

The test advances one head through both signed orientations:

- Positive revision 2, amount 80 (delta +20 debit): asserts exact
  account/call/A-leg, operation/source key
  (`CostPassThroughAdjustmentSourceKey`), currency USD, previous/current/delta
  60/80/+20, status final, provider-cost echo, two-leg journal entries
  (debit `customer_financial_account` / credit
  `customer_adjustment_clearing`, 20), `CorrectionGroupID` equal to the
  production settlement's original transaction ID, operation-snapshot linkage,
  and head `posted 80 / revision 2 / version 2 / fence 2` with A-leg intact.
- Negative correction revision 3, amount 70 (delta −10 credit): same identity
  assertions, mirrored entries (debit clearing / credit financial, 10), head
  `70 / 3 / 3 / 3`.
- Balance proves signed orientation exactly: 1000 − 60 − 20 + 10 = 930.
- Idempotent replay of revision 3 returns `Replayed` without application: still
  exactly 2 adjustment journals (all A-legged) and head unchanged at
  `70 / 3 / 3 / 3`.
- Existing positive path (`cost_pass_through_phase10_test.go`, 3 tests)
  unchanged and green; no old-journal backfill or mutation; no report/core
  DTO/migration/schema/HTTP/provider changes.

## Verification (all exit 0)

| Command | Result |
| --- | --- |
| `go test -count=1 -run '^TestSQLiteCostPassThroughRevisionRetainsALegLineage$' ./internal/infra/billingstore/` | RED before fix (above), PASS after |
| `go test -count=1 -run 'CostPassThrough' ./internal/infra/billingstore/` | PASS |
| `go test -count=5 -shuffle=on -run 'CostPassThrough' ./internal/infra/billingstore/` | PASS |
| `go test -count=1 ./internal/core/billing/ ./internal/infra/billingstore/` | PASS (billingstore 30.7s) |
| `go test -count=1 -run '^TestDBParity_SQLite$' ./internal/infra/billingstore/` | PASS |
| `go test -tags=integration -count=1 -run '^TestDBParity_PostgresDirect$' ./internal/infra/billingstore/` (`LIP_REQUIRE_POSTGRES=1`) | PASS, direct PostgreSQL available (74.2s) |
| `go vet ./internal/infra/billingstore/ ./internal/core/billing/` | clean |
| `gofmt -d` on both touched Go files | no output |
| `git diff --check` | clean |

`go test -race` not run: unavailable on the Windows cgo toolchain (same
standing platform gate as Cycle 1).

## Residual risk

Adjustment journals written before this fix keep empty `a_leg_id` lineage —
immutable history is not backfilled by this slice (per work order). Report
integration (Cycle 1 defers any pass-through evidence to pending) is a later
slice; this slice only repairs/proves the production reader/writer path.
