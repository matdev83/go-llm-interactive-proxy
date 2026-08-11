# Research Notes

## Purpose

This research supports the revised `usage-record-ledger-billing` architecture. It replaces the initial telecom-flavored CDR draft with a domain-neutral Usage Record model and adds a durable double-entry journal, prepaid/postpaid account semantics, journal-based rebuild, point-in-time balance snapshots, and explicit B2BUA A-leg/B-leg accounting.

Repository baseline reviewed: `main` at `269b9e8df0e9ed476d962c2327e1794f4b74bb83`.

## Executive Findings

1. The simple post-turn architecture remains correct: financial settlement does not need stream-time instrumentation.
2. The first draft was too weak as a financial system because it made a materialized balance/reservation store authoritative without a classical reconstructible journal.
3. Go-LIP already has the database primitives needed for a durable ledger; introducing another DB stack would be wasteful.
4. The correct double-entry model is one journal transaction with two or more entries, not two independent ledgers.
5. Authorization holds should be auditable but should not recognize revenue; a separate authorization book solves this cleanly.
6. Prepaid and postpaid can share one signed-balance convention: customer credits minus customer debits.
7. B2BUA lineage already gives the right correlation vocabulary: A-leg for customer turn scope, B-leg for provider attempt/cost scope.
8. The journal can reconstruct balance and held exposure; the account row can therefore be treated as a materialized operational projection.
9. `CDR` is unnecessary terminology. `TurnUsageRecord` / `LegUsageRecord` better describes the domain.

## External Accounting Patterns

### Double-entry transactions

Modern Treasury's ledger documentation describes a ledger transaction as two or more entries with debit/credit direction and requires equal total debits and credits. That maps directly to the proposed `journal_transactions` + `journal_entries` model.

References:

- https://docs.moderntreasury.com/ledgers/docs/guide-to-debits-and-credits
- https://docs.moderntreasury.com/ledgers/docs/ledger-transactions-overview
- https://docs.moderntreasury.com/ledgers/docs/guide-to-ledger-objects

Design consequence:

```text
journal transaction
  -> debit entry(ies)
  -> credit entry(ies)

require Σ debit == Σ credit
```

A transaction may have more than two entries. This matters when one customer charge should be allocated to several B-leg/model revenue components while changing the customer balance once.

### Immutable corrections

Modern Treasury documents immutable posted/archived ledger transactions; corrections are naturally modeled as new transactions rather than destructive changes.

Reference:

- https://docs.moderntreasury.com/ledgers/docs/transaction-status-and-balances

Design consequence:

- never edit posted journal entries;
- reverse an incorrect transaction;
- post a corrected replacement;
- retain linkage.

### Available balance and pending/held exposure

Modern Treasury distinguishes posted, pending, and available balances and supports balance/version locking to prevent double-spend races.

References:

- https://docs.moderntreasury.com/ledgers/docs/ledger-accounts-overview
- https://docs.moderntreasury.com/ledgers/docs/lock-on-account-balance-or-version
- https://docs.moderntreasury.com/ledgers/docs/ledgers-guarantees

Design consequence:

Go-LIP can keep the operational concept of an authorization hold while protecting it with database locking/version semantics. We do not need to copy Modern Treasury's exact API/status model; the important properties are atomicity, balanced journal evidence, and safe available-balance computation.

### Immutable credit ledgers

Stripe documents usage-based billing credits as backed by an immutable append-only ledger, and Stripe's revenue-recognition system uses a double-entry ledger for debits/credits.

References:

- https://docs.stripe.com/billing/subscriptions/usage-based/billing-credits
- https://stripe.com/blog/introducing-credits-for-usage-based-billing
- https://docs.stripe.com/revenue-recognition/methodology

Design consequence:

Customer credit changes should have durable transaction-level evidence rather than relying only on a mutable balance column.

### Balance-change reconciliation

Adyen's balance-platform accounting reports track balance-changing financial activities and are intended to support balance calculation/reconciliation.

References:

- https://docs.adyen.com/platforms/quickstart-guide/reporting/
- https://docs.adyen.com/marketplaces/reports-and-fees/balance-platform-accounting-report

Design consequence:

The ability to recompute balances from transaction history is an expected property of serious monetary systems and should be an explicit Go-LIP test/operational capability.

## Repository Assets

### Existing Bun abstraction

`internal/infra/db/bun.go` already exposes `NewBunDB(*sql.DB, Dialect)` for:

- SQLite (`sqlitedialect`);
- PostgreSQL (`pgdialect`).

`internal/infra/db/open.go` already owns PostgreSQL opening, pool application, Bun wrapping, and secret-safe failures.

Therefore the billing design should consume the existing DB infrastructure rather than create a billing-specific connection abstraction.

### Existing migration/store patterns

The repository already contains Bun-backed/durable patterns in:

- `internal/core/continuity/bunstore/`;
- `internal/core/securesession/adapters/bunstore/`;
- `internal/infra/metering/journalstore/`;
- `internal/infra/terminalwork/workstore/`;
- `internal/infra/dbmigrate/`;
- PostgreSQL test harness under `internal/testkit/`.

The archived `bun-database-abstraction` spec explicitly states that Bun/SQL handles belong inside concrete store adapters and should not cross core ports. The billing store should follow the same rule.

### Existing B2BUA lineage

`pkg/lipapi/lineage.go` contains:

```go
type AttemptRecord struct {
    BLegID         string
    ALegID         string
    Seq            int
    BackendID      string
    EffectiveModel string
    StartedAt      time.Time
    FinishedAt     time.Time
    Outcome        AttemptOutcome
    Reason         string
}
```

This is almost exactly the lineage needed by billing. Financial data must not be added to `lipapi.AttemptRecord`; instead the internal `LegUsageRecord` can correlate with these IDs.

### Existing reserve semantics

`internal/infra/usageauthority/authoritystore/store_reserve.go` already demonstrates:

- atomic clone/apply store mutation;
- `remaining = limit - consumed - reserved`;
- deterministic reservation identity;
- idempotent replay by source key;
- capacity denial before mutation.

These concepts are reusable, but the financial target should be simplified around billing accounts/holds/journal rather than preserve generalized rule descriptors and streaming settlement inputs.

### Existing settlement semantics

`authoritystore/store_settle.go` demonstrates atomic reserve-to-consumed conversion and idempotent source-key handling. The financial design can reuse checked transactional ideas while replacing the semantic input with a deterministic post-turn Billing Result.

## Why the Initial CDR Draft Was Still Incomplete

### Gap A: materialized balance was too authoritative

The initial draft treated `balance + reserved` storage as the main monetary truth. That is adequate for simple quota enforcement but weak for a serious billing system.

Required correction:

- durable journal transactions/entries;
- immutable financial history;
- account row becomes materialized state;
- journal replay can rebuild state.

### Gap B: no classical debit/credit invariant

Without a balanced transaction invariant, a single bug can write one side of a financial operation without a mathematically obvious inconsistency.

Required correction:

```text
for every journal transaction:
    sum(debits) == sum(credits)
```

### Gap C: reservations were not represented in a ledger

A hold changes available credit and is financially important for debugging concurrent prepaid/postpaid behavior.

However, recognizing it as revenue would be wrong.

Required correction:

- balanced authorization book;
- reserved-exposure debit;
- authorization-contra credit;
- reverse when hold closes;
- financial balance unchanged.

### Gap D: prepaid/postpaid semantics were underspecified

The initial draft loosely referred to "prepaid balance or credit".

Required correction:

```text
prepaid:
    floor = 0

postpaid:
    floor = -CreditLimit

Spendable = Balance - floor - Reserved
```

This exactly supports the requested examples.

### Gap E: B2BUA cost attribution needed to be first-class

A single user A-leg may cause:

- failed provider B-leg;
- retry B-leg on another backend/model;
- parallel B-legs;
- winning surfaced B-leg.

All can create operator cost even when the customer is charged once.

Required correction:

- TUR = A-leg scope;
- LUR = B-leg scope;
- operator cost per B-leg;
- customer policy separately decides which legs/components are revenue;
- journal entries retain A-leg/B-leg/model/rate references.

### Gap F: transparency snapshots were missing

Journal replay is correct but can be cumbersome during incident debugging.

Required correction:

Every user-affecting operation records:

- balance before/after;
- reserved before/after;
- available before/after;
- mode, floor/limit, currency;
- state version before/after.

These snapshots are redundant evidence, not a second source of truth.

## Terminology Review

### Rejected: CDR

Pros:

- immediately evokes post-event rating;
- familiar from telecom.

Cons:

- telecom-specific;
- "call" is not the Go-LIP business object;
- risks cargo-culting PBX vocabulary into LLM code.

### Considered: Billing Record

Problem: implies already-rated/financial output, while this record is evidence input to rating.

### Considered: Usage Detail Record

Reasonable, but "detail" adds little and the acronym UDR is not meaningful inside the project.

### Selected: Turn Usage Record / Leg Usage Record

Why:

- describes exactly what is stored;
- maps A-leg turn and B-leg attempts;
- does not imply financial posting;
- intuitive in code and docs;
- supports future non-telecom consumers.

## Accounting Model

### Signed customer balance

Use:

```text
Balance = credits - debits on customer's financial ledger account
```

This produces both desired user-facing models.

#### Prepaid

Top-up 250:

```text
Dr Cash/Clearing             250
Cr Customer Prepaid Account  250

signed balance = +250
```

Usage charge 40:

```text
Dr Customer Prepaid Account   40
Cr Usage Revenue              40

signed balance = +210
```

#### Postpaid

Usage charge 40:

```text
Dr Customer A/R               40
Cr Usage Revenue              40

signed balance = -40
```

Payment 40:

```text
Dr Cash/Clearing              40
Cr Customer A/R               40

signed balance = 0
```

The posting direction can therefore be uniform even though the account classifications differ.

### Provider cost

For each billable B-leg:

```text
Dr Inference COGS
Cr Provider Payable/Clearing
```

This is separate from customer revenue and enables direct gross-margin reconciliation.

## Authorization Book

Recommended posting for hold 50:

```text
Dr Customer Reserved Exposure 50
Cr Authorization Contra       50
```

Closure:

```text
Dr Authorization Contra       50
Cr Customer Reserved Exposure 50
```

Advantages:

- every hold/release is balanced;
- active reserved exposure can be replayed;
- no fake revenue;
- financial and authorization trial balances are separately testable.

## Recovery Model

Authoritative/reconstructible state:

```text
Balance =
    customer financial credits - customer financial debits

Reserved =
    reserved exposure debits - reserved exposure credits

Available =
    Balance - CreditFloor - Reserved
```

Materialized account state exists for performance/atomic admission only.

Required recovery tooling:

1. replay one account;
2. validate every journal transaction;
3. validate stored pre/post snapshots;
4. compare replay result to materialized account row;
5. optionally rebuild materialized row under exclusive lock;
6. never modify posted journal entries.

## Simplicity Check

The revised design remains intentionally small. It adds financial rigor through a few durable concepts:

- Billing Account;
- Authorization Hold;
- Turn Usage Record;
- Leg Usage Record;
- Journal Transaction;
- Journal Entry;
- Billing Result.

It does **not** add:

- Kafka;
- generic event sourcing;
- generic ledger scripting;
- arbitrary chart-of-account configuration;
- a DI container;
- a workflow engine;
- stream-time financial callbacks.

The complexity increase is in durable accounting correctness, not runtime orchestration.

## Research Conclusion

The new target should be understood as:

```text
execution produces usage records
billing rates usage records
accounting posts balanced journal transactions
journal reconstructs monetary state
```

This is simpler than the current runtime instrumentation while materially stronger than the initial CDR draft as a financial system.
