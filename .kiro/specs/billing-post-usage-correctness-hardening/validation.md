# Validation and Successor Baseline

Concise Phase 5 certification evidence for `billing-post-usage-correctness-hardening`.
`spec.json` intentionally remains in `tasks-generated` with approvals and `ready_for_implementation: true`; it is not archived because the successor spec owns TUR/LUR cleanup.

## Baseline for `billing-architecture-final-convergence`

- Main branch commit: `bdf6e5037c75a2586015cbf1ecac5207dadc3afe` (parent of this branch).
- Working branch: `feat/billing-post-usage-correctness-hardening` (certification tree at commit `cdbb5d3f`).
- Production shape the successor must preserve:
  - one `BillingCallID` per invocation; cheap credit screen -> route/quote -> atomic operational exposure admission -> billing-blind execution -> terminal leg/call records -> post-usage customer settlement (independent of provider-cost readiness).
  - positive persisted `attempt_seq` (v2 fingerprint) is the only legal customer-leg ordering source; `ExpectedBLegIDs` is a completeness set.
  - customer rating consumes only `CustomerRatingSnapshots` (pricing, policy, per-model cards); `OperatorRate` is provider-cost-only (`ProviderCostJoinResolver.ResolveProviderCost`).
  - runtime billing bookkeeping lives in private `billingCallState` on `preparedRequest` / `retryRecvStream`; `Executor` holds no billing-call registry.
  - legacy TUR/LUR rating bridge remains a temporary adapter and is a successor deletion target; nothing here deletes it.

## Requirement conformance summary (all green)

| Requirement | Outcome | Evidence |
| :--- | :--- | :--- |
| 1 Monetary authority preserved | PASS | hold-deletion + no-stream-money ratchets active; `go test ./internal/archtest` |
| 2 Actual B-leg attempt order | PASS | sequence contract/rating tests; sequence ratchets; `TestPostgresCallLegSequencePersistence` |
| 3 Pre-fix durable rows safe | PASS | legacy NULL/v1 replay SQLite+Postgres; pre-sequence schema migration test |
| 4 Correct backend/model customer pricing | PASS | model-card resolution through `CallRatingInput`; rating tests |
| 5 Customer/provider independence | PASS | resolver split; operator-freedom ratchets; `provider_cost_independence_test.go` |
| 6 Bounded runtime billing state | PASS | call-scoped state + stress/race tests; state-ownership ratchets |
| 7 Terminal usage/failure semantics | PASS | closure/finalization/parallel/post-output tests |
| 8 Correction proven before cleanup | PASS | Phase 0 RED tests, matrix, ratchets, this review |

## Verification commands run

- `go test ./internal/archtest/...` — pass (incl. new Phase 5.1 ratchets, per-file 500-line limit, docs contract).
- `go test ./internal/core/billing ./internal/core/runtime ./internal/infra/billingcompose ./internal/infra/billingadmission ./internal/infra/runtimebundle ./internal/infra/billingstore` — pass.
- `LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/billingstore` — pass (3 Postgres tests actually ran; skip cleanly when DSN/requirement absent).
- `go test -race ./internal/core/runtime -run 'Billing|Parallel|Close|CallState|Interleaved'` and `go test -race ./internal/infra/billingstore -run 'Concurrent|Sequence|Legacy'` — pass.
- `go vet` affected packages — pass.
- `make quality-checks` — pass. `make test-unit` — pass.
- `make test-race` on Windows is a documented skip (toolchain policy); targeted `-race` runs above provide evidence.

## Residual risks

- Sequences for legacy pre-fix rows remain unknown by design; order-dependent policies on such calls fail closed into reconcile-required (expected brownfield behavior).
- Postgres parity runs relied on a locally configured DSN; CI must keep `LIP_REQUIRE_POSTGRES=1` for the same coverage.
- Full `make test-race` only runs in Linux CI; Windows evidence is from targeted `-race` runs.
