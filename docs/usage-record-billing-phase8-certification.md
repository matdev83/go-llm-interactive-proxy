# Usage-record ledger billing — Phase 8 certification

Phase 8 deletes stream-time monetary machinery and ratchets the final ownership surface.

## Ownership

| Concern | Owner | Must not |
|---|---|---|
| Route plan + execute + non-money quota | `internal/core/runtime` | journal posting, stream price enrichment, token-ledger writes, stream-cost→Rated settle |
| Pessimistic authorize + TUR/LUR policy + rating + settlement commands | `internal/core/billing` | provider SDKs, lipapi, SQL/Bun, runtime imports |
| Bun journal / holds / snapshots / reconcile | `internal/infra/billingstore` | provider SDKs, stream handlers |
| Admission store adapter | `internal/infra/billingadmission` | stream-time calls |
| Journal/TUR reports HTTP | `internal/stdhttp/admin/billing` | reinterpret raw usage as money |
| Protocol/quota usage projection | `internal/core/tokenaccounting` | customer balance / journal input |

Final monetary flow:

```text
route plan
 -> pessimistic max-charge authorize
 -> execute (no financial mutation)
 -> sealed TUR/LUR handoff
 -> deterministic post-turn rating
 -> double-entry journal settlement
 -> journal/reports + reconcile/rebuild
```

## Cutover flag

`accounting.billing.authoritative: true` is a fail-closed composition gate, not a YAML store factory. Standard `lipstd` / public `lipruntime.Options` do not invent billing accounts or open `internal/infra/billingstore`. Authoritative Bun wiring (store, admission, identity, rating resolver, post-turn worker, journal-backed reports) exists only when those dependencies are injected on `runtimebundle.ProductionOptions` / `BuildHost`. Runtime also fails closed when the flag is set without `BillingAdmission` (`ErrAuthoritativeBillingRequired` at composition; `ErrBillingAdmissionDenied` at admit). Stream handlers never enrich money or write the legacy token ledger; usage-authority settle never rebuilds Rated/FinalCost from stream `CostNanoUnits`. Leftover `accounting.ledger.*` YAML is accepted without live sqlite/postgres paths and is never opened. Production `accounting.authority` YAML rejects monetary `budget` / `spend_cap` / `money_nano` rules.

## Residual-path inventory (dual financial truth must stay gone)

| Former dual path | Phase 8 status |
|---|---|
| `enrichUsageCost` / stream price enrichment | Deleted; symbol ratchet |
| Runtime `tokenaccounting/ledger` writes | Deleted; runtime import + symbol ratchet |
| Composition open of durable/memory token ledger | Retired; leftover `accounting.ledger.*` YAML is accepted without required paths and never opened |
| `EconomicsRater` / `rateMonetaryExposure` admit+settle | Deleted |
| Attempt settle `Rated` / Fact `Money` from stream cost | Removed in `attemptSettlementEvidence` |
| `attemptAuthorityCostAmount` / MoneyNano FinalUsage from stream cost | Returns empty / reserved estimate only |
| `BillingAuthoritative` without admission | Runtime gate returns `ErrBillingAdmissionDenied` |
| `mergeFinalizeBillingCost` | **Intentional** terminal TUR/LUR evidence assembly when connector `FinalizeBilling` has no money fields; not stream-time settlement |
| `accounting.EstimateCost` / `accounting.pricing` | Snapshot compile and shadow-characterization helpers; not stream enrichment |
| YAML `kind: budget\|spend_cap` / `unit: money_nano` | Rejected at production config validate; domain Budget/SpendCap types remain for unit tests |
| `ReclaimExpiredHolds` TTL reclaim | Intentional no-op (Req 15.6). Stale holds use `ReleaseStaleSafe` / unused-hold release, not automatic expiry |
| Unlimited TUR handoff retries | Unlimited **while the process is up** (never drop provider-accepted usage). `Host.Close` / PhaseQuiesce bounds the wait |

## Certification evidence (where to look)

| Requirement cluster | Evidence |
|---|---|
| Stream money deleted + protocol usage preserved | `authoritative_billing_cutover_test.go`, `executor_token_accounting_stream_test.go`, `executor_prepare_stages_test.go` |
| No stream-cost monetary authority settle | `attempt_hybrid_settle_test.go`, `TestAttemptAuthorityUsageAmountIgnoresStreamCostForMoney` |
| Architecture Fail-if / Req 1.8, 17.7–17.10 | `usage_record_billing_boundaries_test.go`, `import_rules.go`, `symbol_rules.go`, `phase8_billing_boundary_test.go` |
| Authoritative admission gate | `TestAuthoritativeBillingRequiresBillingAdmission` |
| Injection-only Bun composition | `production_options.go`, `ErrAuthoritativeBillingRequired`, `docs/legacy-options-migration.md` |
| Monetary authority YAML rejected | `TestValidateAccountingAuthorityRejectsMonetaryRules` |
| Retired ledger YAML without paths | `TestValidateTokenAccountingAcceptsRetiredLedgerWithoutPaths` |
| Close does not hang on wedged TUR handoff | `TestWaitBillingHandoffRetriesForCloseDoesNotBlockForever` |
| SQLite + PostgreSQL dialects | `internal/infra/billingstore/*_test.go`, `postgres_integration_test.go`, `postgres_authorization_concurrency_test.go`, `internal/infra/dbmigrate` ComponentBilling |
| Trial balance / reconcile_required / rebuild | `internal/infra/billingstore/reports*_test.go`, `reconcile*_test.go`, `settlement*_test.go` |
| B2BUA / parallel / cancel handoff | `billing_handoff_test.go`, `billing_leg_test.go` |
| Package ownership docs | `.kiro/steering/structure.md`, `docs/core-boundaries.md`, `README.md` (Usage-record billing cutover) |

## Explicit non-goals retained

- Payment gateway / invoicing / VAT / FX
- Live token-by-token debiting during streaming
- Reintroducing stream-time price enrichment or token-ledger monetary writes
- Using usageauthority Budget/SpendCap as a second customer-balance reducer beside the Bun journal
- Auto-composing `billingstore` inside `lipstd` without injected identity/pricing
- Automatic TTL reclaim of reserved holds
- Connector `FinalizeBilling` ABI money fields (terminal cost merge is the current evidence path)
