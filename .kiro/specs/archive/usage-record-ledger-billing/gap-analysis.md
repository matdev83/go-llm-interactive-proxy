# Brownfield Requirements Gap Analysis

## Scope and Method

This analysis compares the target usage-record/double-entry billing architecture with current Go-LIP financial paths on `main`. It covers runtime stream accounting, token accounting, metering/control-plane reporting, `usageauthority`, B2BUA lineage, backend/connector billing evidence, terminal ownership, and existing Bun SQLite/PostgreSQL infrastructure.

Classifications:

- **Missing** — target capability does not exist.
- **Partial** — reusable machinery exists but ownership/semantics are incomplete.
- **Duplicate** — multiple authorities implement the same economic interpretation.
- **Constraint** — current compatibility or runtime invariant must be preserved.
- **Delete** — migration-era machinery should disappear after cutover.

## Assets Worth Preserving

1. **B2BUA lineage:** existing A-leg/B-leg identities and attempt outcomes already express the correct billing topology.
2. **Adapter evidence:** backend adapters/connectors already normalize provider usage/cost and expose sideband/final billing evidence.
3. **Checked money/rating helpers:** pure arithmetic and rating concepts are reusable outside stream execution.
4. **Atomic authority-store mechanics:** current reserve/settle stores demonstrate same-account atomic reservation patterns, though the generalized monetary lifecycle is too broad for the target.
5. **Bun DB infrastructure:** `internal/infra/db` already supports Bun over SQLite/PostgreSQL and should be reused.
6. **Terminal ownership:** the existing terminal/terminal-work boundary is the correct place to seal durable turn evidence.
7. **Architecture tests:** current forbidden-import/symbol/budget patterns can ratchet the final boundary after deletion.

## Core Brownfield Problem

Today usage observations can participate in multiple interpretations while a request is live: stream usage reconstruction, runtime aggregation, pricing enrichment, authority settlement, metering facts, token ledgers, and control-plane projection. These paths have different identity, presence, dedupe, correction, and aggregation semantics.

The target removes this entire class of fragility by making execution financially passive after admission. The authoritative path becomes:

```text
route plan
 -> pessimistic max customer charge
 -> atomic authorization hold
 -> execute with no financial mutation
 -> immutable TUR/LUR evidence
 -> exact-snapshot deterministic rating
 -> double-entry journal settlement
 -> journal-backed account/report/reconciliation
```

## Gap Register

| ID | Severity | Class | Current finding | Required disposition |
|---|---:|---|---|---|
| G-01 | P0 | Duplicate/Delete | Runtime reconstructs/merges usage for multiple consumers and settlement. | Billing shall consume sealed TUR/LUR evidence, not stream arrays. |
| G-02 | P0 | Duplicate/Delete | Runtime enriches usage events with cost/rating. | Move rating entirely to post-turn calculation. |
| G-03 | P0 | Duplicate/Delete | Economic dedupe and remembered customer/operator totals live in retry stream state. | Delete after durable TUR/LUR identity is authoritative. |
| G-04 | P0 | Partial | Adapter evidence exists but may surface repeated cumulative samples. | Adapter/finalizer must expose one final B-leg evidence result. |
| G-05 | P0 | Partial | Existing `usageauthority` mixes money with generalized token/request lifecycle facts/exposure. | Narrow money billing to account authorization + post-turn settlement; preserve non-money rules separately. |
| G-06 | P0 | Missing | No domain-neutral immutable A-leg usage record containing every B-leg. | Add TUR/LUR contracts. |
| G-07 | P0 | Missing | No explicit prepaid/postpaid signed-balance model with individual postpaid credit floor. | Add account mode, balance convention, floor and spendable formula. |
| G-08 | P0 | Partial | Existing reservation arithmetic exists, but target customer maximum-charge semantics are not the single admission contract. | Add deterministic pessimistic max-charge estimator + atomic hold. |
| G-09 | P0 | Missing | No classical reconstructible double-entry customer/provider financial journal is authoritative. | Add one balanced immutable journal engine. |
| G-10 | P0 | Missing | Holds are not modeled as an independently balanced authorization book distinct from revenue. | Add authorization book using same journal engine. |
| G-11 | P0 | Missing | Materialized account state is not explicitly rebuildable from financial history. | Journal replay + account-policy history must reconstruct balance/reserved/spendable. |
| G-12 | P0 | Missing | No point-in-time before/after account snapshot is required for every customer-affecting operation. | Persist diagnostic snapshots in the same DB transaction. |
| G-13 | P0 | Missing | Provider costs are not defined as one posting source per B-leg/model/rate. | Post COGS/payable per provider-billable LUR. |
| G-14 | P0 | Constraint | One A-leg can create sequential/parallel B-legs with different providers/models. | Customer settlement remains A-leg/TUR scoped; operator cost remains B-leg/LUR scoped. |
| G-15 | P0 | Missing | No deterministic post-turn processing state/retry model is dedicated to sealed records. | Add small durable processing table; no event platform. |
| G-16 | P0 | Constraint | Strict credit safety must hold across concurrent sessions/processes. | Store-atomic pessimistic holds; no session scan/concurrency=1 heuristic. |
| G-17 | P1 | Partial | Metering/token ledgers provide audit data but have incompatible billing semantics. | Keep only as one-way telemetry/read projections or delete unused paths. |
| G-18 | P0 | Missing | Financial reporting can bypass the exact charging result. | Reports read journal + processed TUR/LURs. |
| G-19 | P0 | Constraint | Client-visible usage events remain protocol behavior. | Preserve wire usage separately from financial truth. |
| G-20 | P0 | Constraint | No retry/failover after client output must remain unchanged. | Billing cannot trigger retry/failover. |
| G-21 | P0 | Missing | Existing trusted top-up/payment/adjustment operations do not share one explicit narrow double-entry command contract. | Add closed trusted financial operations; no arbitrary posting API. |
| G-22 | P0 | Delete | Direct runtime token/economic ledger writes preserve a second financial path. | Retire after journal cutover. |
| G-23 | P0 | Missing | No trial-balance/rebuild certification exists for customer balances. | Add journal balance, replay and materialized-state reconciliation. |
| G-24 | P0 | Missing | Strict billing could be tempted to use memory fallback during DB failure. | Require durable Bun backing; fail closed. |
| G-25 | P1 | Partial | Current architecture docs still describe tokenaccounting/metering as core financial ownership. | Update package/architecture docs after cutover. |

## External Review Gaps (CodeRabbit)

CodeRabbit posted ten actionable comments after the initial double-entry design. They exposed real integrity gaps and reopened requirements/design validation.

| ID | Severity | Finding | Remediation now required |
|---|---:|---|---|
| G-31 | P0 | TUR/LUR financial replay identity was ambiguous. | TUR = account+stable turn/A-leg; LUR/provider cost adds `BLegID`; persist semantic fingerprints. |
| G-32 | P0 | Authorization checked only non-negative spendable rather than whether the requested hold fits. | Require `SpendableBefore >= MaxCustomerCharge` and prove post-hold spendable >= 0. |
| G-33 | P0 | Correction replacement lacked explicit link to corrected transaction. | `ReversalOf` + `CorrectsTransactionID` + `CorrectionGroupID` with referential checks. |
| G-34 | P0 | Replay ordering relied on `RecordedAt`. | Allocate durable monotonic account sequence atomically and replay by it. |
| G-35 | P0 | Final rating could accept a different pricing/rate snapshot identity. | Exact TUR/authorization/customer snapshot and LUR/operator-rate identity checks before rating. |
| G-36 | P0 | Immutable TUR payload was conflated with mutable worker status. | Separate `usage_record_processing` mutable table. |
| G-37 | P0 | Idempotency source-key uniqueness did not prove semantic equality. | Versioned canonical semantic fingerprint + compare-before-no-op. |
| G-38 | P1 | Store contract omitted payment/adjustment/explicit release operations required by the design. | Add narrow trusted commands with reason/idempotency semantics. |
| G-39 | P0 | Unrateable provider-billable B-leg could silently disappear from COGS. | Explicit `unreconciled_cost`; no zero/omission/processed state. |
| G-40 | P0 | `reconcile_required` blocking/re-enable behavior was design prose rather than acceptance behavior. | Testable account-state transition, fail-closed authorization, explicit verified clear path. |

## Requirements Review

### Round 1 — CDR-style simplification

**NO-GO:** post-turn billing was directionally correct but did not provide classical double-entry financial history or reconstructible account state.

Remediation: introduce TUR/LUR terminology, financial + authorization journal books, prepaid/postpaid account floors, Bun-backed journal and recovery.

### Round 2 — Financial semantics

**NO-GO:** authorization holds risked being conflated with revenue and prepaid/postpaid sign convention was implicit.

Remediation: separate authorization book; define `Balance = credits - debits`, prepaid floor 0, postpaid floor `-CreditLimit`, and `Spendable = Balance - Floor - Reserved`.

### Round 3 — B2BUA attribution

**NO-GO:** logical turn settlement and provider cost had different granularities.

Remediation: TUR/A-leg customer settlement, LUR/B-leg provider COGS, per-leg model/provider/rate references, session as read aggregation only.

### Round 4 — Recovery

**NO-GO:** materialized state and snapshots could become unrecoverable parallel truths.

Remediation: journal replay is authoritative reconstruction; snapshots are validation evidence; exclusive rebuild repairs only materialized state.

### Round 5 — CodeRabbit integrity review

**NO-GO until G-31–G-40 were incorporated.**

Post-remediation requirements now make durable identity/fingerprints, exact authorization amount, correction links, monotonic replay sequence, snapshot identity binding, immutable-vs-processing separation, trusted store commands, unreconciled provider cost, and reconcile-required behavior explicit.

## Requirements Quality Gate

**Decision: PASS**

The requirements now describe a small runtime seam and a rigorous durable financial boundary. The financial journal adds necessary accounting integrity without reintroducing live-stream instrumentation or generic enterprise-accounting infrastructure.
