## Review verdict

- VERDICT: APPROVED
- TASK: parent-phase-14 (Tasks 14.1-14.3)
- BASE: ae2bed69
- REVIEW DATE: 2026-09-20
- SCOPE: Final independent cumulative re-review of the live Phase 14 remediation diff. No source, task-checkbox, or spec-status changes were made by this reviewer.

## Mechanical verification

- PASS: `go test -count=1 -run 'Test(RichQuote|EvaluateSettle|.*RouteTariff.*|.*RouteBinding.*|.*Overrun.*|.*Breach.*|.*Qualifier.*)' ./internal/core/billing/...` (`internal/core/billing`, 0.018s).
- PASS: `go test -count=1 -run 'Test(AdapterRichQuote|AdapterStrictQuote|AdapterLegacyQuote|.*Capability.*|.*Route.*Binding.*|.*Rich.*Quote.*)' ./internal/infra/billingadmission/...` (0.012s).
- PASS: `go test -count=1 -run 'Test(SQLiteApplyCallBillingResultOverrun|SQLiteSettlement.*Route|.*RouteTariff.*|.*Breach.*|.*Overrun.*|.*SupplierBacklog.*)' ./internal/infra/billingstore/...` (0.401s).
- PASS: `go test -count=1 ./internal/core/billing/... ./internal/core/runtime/... ./internal/infra/billingadmission/... ./internal/infra/billingstore/...`; core/billing 0.360s, core/runtime and failclosed 6.831s combined, billingadmission 0.014s, billingstore 67.317s.
- PASS: `make test-db-parity-sqlite`; billingstore and all registered SQLite components passed (billingstore 15.976s).
- PARTIAL/ENVIRONMENTAL: `make test-db-parity-postgres-direct`; the live PostgreSQL billingstore suite passed (78.849s) and the concurrency lease suite passed (1.275s). The full command then stopped only at the known unrelated metering baseline: `metering_components.value_present` got PostgreSQL `int4`, expected boolean, at `internal/infra/metering/journalstore/dbparity_postgres_test.go:44`.
- PASS: `go test -count=1 -run '^TestPhase14' ./internal/archtest/...` (internal/archtest 0.271s; changesurface packages had no tests).
- PASS: `go vet ./internal/core/billing/... ./internal/core/runtime/... ./internal/infra/billingadmission/... ./internal/infra/billingstore/...`.
- PASS: `gofmt -l` over all changed Go files produced no output; `git diff --check` passed; changed-file marker/secret scans found no matches.
- ENVIRONMENTAL: `go test -race -count=1 -run 'Test(EvaluateSettle|SQLiteApplyCallBillingResultOverrun|SQLiteApplyCallBillingResultFailedCall|SQLiteSupplierBacklog)' ./internal/core/billing/... ./internal/infra/billingstore/...` could not build the Windows `runtime/cgo` tool (`cgo.exe: exit status 2`); this is the documented Windows race limitation and not a Phase 14 blocker.

## Requirement and task mapping

- Task 14.1 / Requirements 7.1, 7.3, 7.4, 14.1, 14.2, 14.5 / C3: `EstimateRichCustomerCharge` derives the rich offer from canonical frozen policy/tariff snapshots, applies fixed/unit/block/minimum/conditional/tier/resource semantics and settlement rounding, and requires finite enforceable bounds. Missing or non-enforceable work bounds, missing capabilities, unsupported currency/period semantics, and unbounded uncertainty fail closed. Successful rich quotes emit normalized route bindings containing route key, tariff reference, and canonical content hash; ordering/deduplication and semantic-fingerprint inclusion are deterministic. The rich adapter no longer supplies a scalar ceiling or external qualifier overlay; legacy scalar behavior remains unchanged. Conditional quote/terminal-rating parity and same-version qualifier-content drift rejection passed.
- Task 14.2 / Requirements 10.5, 14.1-14.4 / C6: settlement compares the full actual charge with the admitted bound, posts full actual cost on overrun, records durable `Breached`/`OverrunNano`, and retains account-floor/reconciliation behavior. Route attestation is checked before monetary writes; journal, balance, exposure, claim, operation snapshot, and dedupe transitions remain atomic. Exact replay after exposure closure, conflicting actuals, downward/non-overrun, cancellation, failure, and insufficient-spendable cases passed.
- Task 14.3 / Requirements 5.4, 8.6, 14.1, 14.5, 14.6, 18.3 / C6-C7: strict capability rejection occurs before provider open or exposure admission; legacy/observation-only behavior remains unaffected; supplier backlog work does not take customer balance locks; Phase 14 architecture tests preserve one cheap screen, one atomic monetary authority, and no stream-time rating/journal path or second authority.

## Cumulative remediation disposition

- Finding 1 (runtime compile): RESOLVED. `wire_billing_composition_test.go` uses field-aware `reflect.DeepEqual` for `CallExposure`; the required affected package suite passes.
- Finding 2 (route-binding bypass): RESOLVED. `CheckSettledRouteTariffs` permits only the empty-admitted/empty-used legacy pair; empty/non-empty and non-empty/empty mismatches reject even at zero charge. Rich zero/no-usage rating emits an explicit matching binding, and no-charge repair derives deterministic executed-leg attestations. Mismatch tests assert no money, exposure, or claim mutation; exact replay remains idempotent.
- Finding 3 (qualifier overlay): RESOLVED. Successful rich semantics use only canonical frozen tariff `EffectiveQualifiers`, which are covered by the tariff content hash. The external rich qualifier seam is absent; conditional quote/settlement parity and same-version qualifier-content drift rejection passed. Legacy scalar qualification behavior is unchanged.
- Original blocker 1 (route tariff version/content not carried through settlement): RESOLVED. Binding is normalized through quote, the single `AdmitExposure` transition and fingerprint, durable dual-dialect persistence/immutability guards, reopen, terminal attestation, and pre-write settlement verification. SQLite parity and live PostgreSQL billingstore checks passed.
- Original blocker 2 (unenforceable rich scalar ceiling): RESOLVED. `RichQuoteInput` has no rich ceiling fallback; missing/non-enforceable duration/tool/work bounds and missing capabilities fail closed, while the legacy scalar/token ceiling path is preserved.

## Authorization

APPROVED. Tasks 14/14.1-14.3 are fully met for this independent review. The parent is authorized to check Tasks 14, 14.1-14.3 and proceed to Phase 15.
