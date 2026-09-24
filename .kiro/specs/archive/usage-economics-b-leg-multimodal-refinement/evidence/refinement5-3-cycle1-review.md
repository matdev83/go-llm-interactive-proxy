# Task 5.3 Cycle 1 — independent R15 approval re-review

Date: 2026-09-18  
Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`

## Verdict

**APPROVED for Cycle 1.** Cycle 2 may start. This approval is for the
rolling A-leg report slice only; it is not approval of all Task 5.3.

R15 closes both prior review blockers. The positive replacement-chain tests
are valid and discriminating, the R14 negative graph cases remain present and
green, and the execution inventory now matches the live tree.

## R15 findings verification

### Positive core authority chain

`internal/core/billing/aleg_authority_test.go:545-576`,
`TestEvaluateALegCallAuthorityValidReplacementChainKnown`, constructs:

- canonical customer settlement: 20;
- full mirror `ReversalOf` claim: 20;
- same-scope `CorrectsTransactionID`-only replacement: 12;
- exact customer/revenue pair, same account/A-leg/currency/group, sealed
  fingerprints, and no B-leg.

It passes journals in replacement/canonical/reversal order, then evaluates a
second permutation. Assertions require known status, `Known == true`, exact
USD 12 charge, canonical operation key, and empty issues. Both evaluations
are required to be identical. The full reversal followed by replacement is a
valid writer-supported correction shape; the durable writer's actual
reversal/replacement contract remains covered independently by
`internal/infra/billingstore/journal_store_test.go:91-145`.

### Positive integrated report chain

`internal/infra/billingstore/reports_aleg_customer_test.go:467-499`,
`TestALegReportValidReplacementChainKnown`, uses a production-writer-created
canonical call/marker, then plants the exact valid correction rows with the
replacement inserted before the lower-sequence reversal. The report loader
orders by durable account sequence, so this exercises the report's
input-order-independent graph path. Assertions require known status, known
USD 12 charge, exact customer settlement operation key and CallID, no
adjustments/issues, subtotal 12, one settled call, and zero unknown calls.
The raw planter is fixture setup only; its fields match the writer contract,
while the writer itself is covered by the existing positive contract test.

Both new tests passed immediately against unchanged R14 production, and the
execution record honestly labels them characterization passes rather than
manufactured RED/GREEN work.

### R14 negative coverage retained

All prior graph discriminators remain in the tree and passed:

- core: `UnrelatedComponentUnresolved`,
  `CompetingReplacementsUnresolved`, `ReversalCycleUnresolved`,
  `DualLinkClaimUnresolved`, `DuplicateLinksDeterministic`, and
  `MalformedCorrectionShapeUnresolved`;
- integrated: `UnrelatedCorrectionComponentUnknown`,
  `CompetingReplacementsUnknown`, `ReversalCycleUnknown`, and
  `MalformedCorrectionShapeUnknown`.

The implementation still sorts correction candidates, enforces one link kind,
per-target cardinality, canonical-root reachability, replacement-needs-
reversal, exact writer-pair shape, and no partial netting. No R14 production
source was changed for R15.

## Inventory and production-boundary verification

I independently used the evidence's stated method,
`Get-Content -LiteralPath <file> | Measure-Object -Line`. Every claimed live
count matches:

| File | Evidence/live lines |
| --- | ---: |
| `internal/core/billing/aleg_report.go` | 274 |
| `internal/core/billing/aleg_report_test.go` | 120 |
| `internal/core/billing/aleg_authority.go` | 594 |
| `internal/core/billing/aleg_authority_test.go` | 539 |
| `internal/infra/billingstore/reports_aleg.go` | 627 |
| `internal/infra/billingstore/reports_aleg_customer_test.go` | 1802 |
| `internal/infra/billingstore/reports_call_explanation.go` | 249 |
| `internal/infra/billingstore/reports_call_explanation_test.go` | 143 |
| `internal/infra/billingstore/20260925000000_billing_aleg_report_scope.go` | 47 |
| `internal/infra/billingstore/20260925000000_billing_aleg_report_scope_test.go` | 85 |
| `internal/infra/billingstore/20260812000000_billing_baseline.go` | 332 |

The current status/diff still has the same three tracked Cycle 1 production
modifications and expected untracked Cycle 1 files. The R15 test files were
written after the production files (their timestamps are later), and no
production file appears in the R15 test/evidence-only delta. This is
consistent with the execution record's pre/post-production SHA256 claim; the
record does not print the hash values, so this review does not invent a hash
comparison that is not present in the evidence.

## Mechanical validation

| Command | Result |
| --- | --- |
| `git status --short`, `git diff --stat`, `git diff --no-ext-diff`, `git ls-files --others --exclude-standard` | exit 0; inventory inspected first |
| `go test -count=1 -run '^TestEvaluateALegCallAuthorityValidReplacementChainKnown$' ./internal/core/billing/` | exit 0 |
| `go test -count=1 -run '^TestALegReportValidReplacementChainKnown$' ./internal/infra/billingstore/` | exit 0 |
| `go test -count=1 -run 'TestEvaluateALegCallAuthority' ./internal/core/billing/` | exit 0; 21 tests |
| `go test -count=1 -run 'TestALegReport' ./internal/infra/billingstore/` | exit 0; full focused set |
| `go test -count=5 -shuffle=on -run 'TestEvaluateALegCallAuthority' ./internal/core/billing/` | exit 0 |
| `go test -count=5 -shuffle=on -run 'TestALegReport' ./internal/infra/billingstore/` | exit 0 |
| `go test -count=1 ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | exit 0 |
| `go test -count=1 -run '^(TestSQLiteBillingSchema\|TestALegReportScope\|TestDBParity)' ./internal/infra/billingstore/` | exit 0 |
| `go test -count=1 -run '^TestDBParity_SQLite$' ./internal/infra/billingstore/` | exit 0 |
| `LIP_REQUIRE_POSTGRES=1 go test -tags=integration -count=1 -run '^TestDBParity_PostgresDirect$' ./internal/infra/billingstore/` | exit 0; direct PostgreSQL available (67.819s) |
| writer correction boundary tests | exit 0 |
| `go test -count=1 -run '^TestSQLiteCallExplanation' ./internal/infra/billingstore/` | exit 0 |
| `go build ./...` | exit 0 |
| `go vet ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/stdhttp/...` | exit 0 |
| `gofmt -d` over all touched Go files; `git diff --check` | exit 0; no output |
| high-signal credential-pattern scan over touched files | exit 0; no matches |
| `go test -race -count=1 -run 'TestEvaluateALegCallAuthority\|TestALegReport' ...` | exit 1 at Windows cgo build; unavailable platform gate |

## Residual risks

- Race testing remains unavailable because the Windows cgo toolchain exits
  before package tests run.
- Historical RED excerpts have no machine-captured log artifact; the
  execution record explicitly labels them historical and records the R15
  characterization-pass limitation.
- Duplicate transaction IDs supplied directly to the pure evaluator would be
  outside the report loader's primary-keyed SQL input boundary and remain
  outside the agreed arbitrary-DBA-forgery threat model.
- Provider COGS and pass-through economics remain pending/unresolved by
  design in Cycle 1; their writer join belongs to Cycle 2.

## Files written

Only this file was updated:
`.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement5-3-cycle1-review.md`.

No production code, tests, migrations, task status, commits, branches, or PR
state were modified by this review.
