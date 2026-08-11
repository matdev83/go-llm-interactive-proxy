# Design Validation Review

## Review Method

The revised design was validated as a brownfield architecture change against:

- root `AGENTS.md` and `.kiro/AGENTS.md`;
- project structure/testing steering;
- repository `main` at `269b9e8df0e9ed476d962c2327e1794f4b74bb83`;
- B2BUA lineage in `pkg/lipapi/lineage.go`;
- current runtime accounting/authority lifecycle;
- backend accounting evidence and connector `FinalizeBilling`;
- `usageauthority` reserve/settle stores;
- existing Bun DB abstraction and Bun-backed durable stores;
- current metering/token-accounting/control-plane paths;
- every acceptance criterion in revised `requirements.md`;
- every gap in `gap-analysis.md`.

Any unresolved accounting-integrity, B2BUA-attribution, concurrency, database, recovery, or runtime-boundary issue returned NO-GO.

## Round 1 — Financial Journal Completeness

**Decision: NO-GO**

### Issue 1: Initial CDR-first design had no classical double-entry truth

The first draft made a mutable balance/reservation store authoritative.

Risk:

- one-sided mutation can be difficult to detect;
- account state cannot be independently rebuilt from financial history;
- corrections may overwrite history.

Resolution:

- add durable `journal_transactions` + `journal_entries`;
- require >=1 debit and >=1 credit;
- require exact debit=credit;
- posted entries immutable;
- corrections reverse/repost;
- account state becomes a materialized projection.

### Issue 2: Authorization hold semantics were financially ambiguous

A pessimistic hold is not earned revenue.

Resolution:

- use the same journal engine with a separate `authorization` book;
- hold: debit customer reserved exposure, credit authorization contra;
- close/release with opposite entries;
- financial book remains revenue/cost/funding;
- both books balance independently.

### Issue 3: "Two ledgers" could create duplicated sources of truth

Literal debit and credit databases would introduce coordination risk.

Resolution:

- one journal;
- each financial operation is one balanced transaction with two or more entries;
- multi-entry allocations are allowed.

## Round 2 — Account and Credit Semantics

**Decision: NO-GO**

### Issue 1: Prepaid/postpaid balance sign was not explicit

Resolution:

```text
Balance = credits - debits on customer financial account
CreditFloor = 0 prepaid, -CreditLimit postpaid
Spendable = Balance - CreditFloor - Reserved
```

This gives:

- prepaid +250 -> usage down to 0, never negative;
- postpaid 0 with limit 100 -> usage down to -100, never below.

### Issue 2: Credit-limit mutation was being pulled toward financial posting

A limit change moves no money.

Resolution:

- keep credit limit in durable account policy/master data;
- append account-policy audit events;
- do not invent debit/credit postings for policy changes;
- reject or explicitly block unsafe limit reductions.

### Issue 3: Holds and materialized account state needed cross-process proof

Resolution:

- Bun transaction with row lock/version CAS;
- hold journal + `reserved_nano` update in the same transaction;
- strict mode requires durable SQLite/PostgreSQL backing;
- concurrency tests use real store semantics.

## Round 3 — B2BUA Accounting

**Decision: NO-GO**

### Issue 1: Logical turn and provider cost have different accounting granularity

One A-leg can create multiple B-legs, each with different provider/model/rate and real operator cost.

Resolution:

- rename evidence aggregate to `TurnUsageRecord`;
- one `LegUsageRecord` per B-leg;
- customer settlement boundary = A-leg/turn;
- operator cost boundary = B-leg;
- customer charging policy explicitly selects leg/logical components;
- journal entries retain A-leg/B-leg/model/rate references.

### Issue 2: Failed/losing legs must remain financially visible

Resolution:

- all provider-billable B-legs can produce COGS/payable postings;
- surfaced status does not erase operator cost;
- customer revenue remains policy-controlled.

### Issue 3: Session should not become financial transaction scope

Resolution:

- multiple A-legs in one user session are aggregated only in read models;
- no session-wide mutable financial accumulator.

## Round 4 — Recovery and Auditability

**Decision: NO-GO**

### Issue 1: Before/after snapshots risked becoming a second truth

Resolution:

- journal entries remain authoritative;
- snapshots are redundant diagnostic evidence;
- reconciliation validates snapshots against replay;
- rebuild ignores snapshot values as source input except for integrity checking.

### Issue 2: Materialized balance needed a deterministic rebuild contract

Resolution:

```text
Balance = customer credits - customer debits
Reserved = reserved-exposure debits - credits
Available = Balance - CreditFloor - Reserved
```

Add:

- account reconciliation;
- first-mismatch diagnostics;
- exclusive rebuild of materialized state;
- no mutation of posted journal.

### Issue 3: Crash boundary between charge, hold release, and account row

Resolution:

One Bun transaction atomically owns:

- customer financial posting;
- authorization hold closure;
- materialized account mutation;
- point-in-time snapshots;
- customer settlement marker.

Any failure rolls back the whole customer mutation.

## Final SOLID Review

### Single Responsibility — PASS

- runtime executes;
- adapters finalize B-leg evidence;
- calculator rates immutable usage records;
- billing application orchestrates authorization/settlement;
- Bun store owns transaction mechanics;
- journal owns financial history;
- reporting reads journal/processed records.

### Open/Closed — PASS

- new model/provider rates are data/snapshot inputs;
- B2BUA supports N B-legs without runtime accounting branches;
- customer charging policy extends in billing, not stream handlers.

### Liskov Substitution — PASS

SQLite/PostgreSQL store implementations must satisfy the same atomicity, idempotency, balance, and reconstruction contract.

### Interface Segregation — PASS

No generic financial service locator. Store/query interfaces may be split by actual consumers.

### Dependency Inversion — PASS

Core billing depends on narrow value/store/snapshot contracts; Bun/provider SDK details stay at driven edges.

## Hexagonal Review

**Decision: PASS**

- financial domain does not import DB/provider SDKs;
- Bun adapter does not leak handles into core;
- runtime calls only authorization + terminal usage-record handoff;
- reporting is read-side;
- no provider-specific financial branching enters core.

## Accounting Integrity Review

**Decision: PASS**

Required invariants are explicit:

1. each journal transaction contains debit and credit entries;
2. per transaction debits equal credits;
3. posted entries immutable;
4. financial and authorization books balance independently;
5. prepaid balance cannot go below 0;
6. postpaid balance cannot go below `-CreditLimit`;
7. accepted holds cannot make spendable negative;
8. replay is idempotent;
9. journal replay reconstructs materialized monetary state;
10. corrections are additive reversal/replacement.

## B2BUA Review

**Decision: PASS**

- TUR explicitly carries A-leg;
- LUR explicitly carries B-leg;
- multiple/parallel/failover legs supported;
- per-leg provider/model/rate cost supported;
- customer and operator accounting separated;
- session aggregation is not a write boundary.

## Database Review

**Decision: PASS**

- existing `internal/infra/db` Bun abstraction reused;
- Bun/SQL remains infrastructure-only;
- durable strict billing supports SQLite/PostgreSQL contract semantics;
- migrations use repository conventions;
- no memory fallback for strict mode.

## Recovery Review

**Decision: PASS**

A verified journal history plus durable account policy/master data can reconstruct:

- signed balance;
- reserved exposure;
- available/spendable capacity.

Point-in-time snapshots make diagnosis faster but do not replace replay.

## Simplicity / Overengineering Review

**Decision: PASS**

New essential concepts are bounded:

1. Billing Account
2. Authorization Hold
3. Turn Usage Record
4. Leg Usage Record
5. Billing Result
6. Journal Transaction
7. Journal Entry

Explicitly rejected:

- separate debit and credit databases;
- generic ERP/chart-of-account framework;
- Kafka/CQRS/event sourcing;
- workflow engine;
- stream-time token debiting;
- arbitrary billing DSL;
- DI container;
- session-scanning concurrency controller.

The added complexity is financial integrity, not execution orchestration.

## Requirements Traceability Review

Every final acceptance criterion is assigned to at least one TDD task in `tasks.md`.

Implementation sequencing keeps:

- characterization before ownership change;
- schema/journal tests before production posting;
- authorization contract tests before cutover;
- shadow usage-record calculation before authoritative settlement;
- rebuild certification before old financial path deletion.

## Final Assessment

**Decision: GO FOR DESIGN READINESS**

The revised design is materially stronger than the initial CDR draft while preserving the central simplification: no financial instrumentation during LLM streaming.

The target is now:

```text
route
 -> pessimistic authorization
 -> execute
 -> Turn/Leg Usage Record
 -> deterministic rating
 -> double-entry journal settlement
 -> reconstructible account state
```

Implementation remains gated by Kiro approvals.

## Implementation Gate

Before implementation maintainers must:

1. approve requirements;
2. approve design;
3. approve tasks;
4. set `ready_for_implementation` to `true` in `spec.json`.

Implementation begins with RED characterization/store/journal contract tests.
