# Brownfield Requirements Gap Analysis

## Scope and Method

This analysis reviews the revised `usage-record-ledger-billing` requirements against repository `main` at `269b9e8df0e9ed476d962c2327e1794f4b74bb83` and against the initial draft spec in `.kiro/specs/cdr-billing-and-prepaid-admission/`.

Reviewed areas:

- runtime settlement/admission paths;
- B2BUA lineage (`ALegID` / `BLegID`);
- backend accounting evidence and `FinalizeBilling`;
- `internal/core/accounting`, `tokenaccounting`, `metering`, `controlplane`, and `usageauthority`;
- `internal/infra/usageauthority/authoritystore`;
- existing Bun database abstraction (`internal/infra/db`);
- existing Bun-backed/durable store patterns and migrations;
- terminal-work recovery paths;
- the previous usage-accounting convergence spec and initial CDR-first draft.

Classifications:

- **Preserve** — reusable mechanism already fits target.
- **Partial** — useful mechanism exists but ownership/semantics must change.
- **Missing** — required capability does not exist.
- **Duplicate** — overlapping authorities need deletion.
- **Constraint** — migration must preserve existing behavior.
- **Retire** — target explicitly removes the path.

Effort:

- **S** focused package/test work.
- **M** multi-package migration on existing seams.
- **L** durable schema/authority cutover across several packages.

## Assets Worth Preserving

### B2BUA lineage — Preserve

`pkg/lipapi/lineage.go` already records `ALegID`, `BLegID`, attempt sequence, backend, effective model, timestamps, and attempt outcome.

Disposition:

- retain canonical lineage;
- do not add financial fields to `lipapi`;
- correlate internal Turn/Leg Usage Records with these IDs.

### Side-effect-free route planning — Preserve

Routing produces eligible attempt groups before upstream execution.

Disposition:

- calculate pessimistic customer bound after this plan exists;
- do not make provider calls during estimation.

### Backend final billing evidence — Preserve/Partial

Backend plugin contracts already contain sideband accounting evidence and `FinalizeBilling`.

Disposition:

- converge to one final B-leg evidence result at termination;
- stop reconstructing financial truth from generic runtime stream-event arrays.

### Checked monetary arithmetic — Preserve

Current accounting/economics packages already contain exact integer money/rating helpers.

Disposition:

- reuse checked arithmetic and price snapshot concepts;
- remove stream-time cost mutation ownership.

### Atomic reservation techniques — Preserve/Partial

`authoritystore` already tracks `Consumed`, `Reserved`, deterministic reservation keys, and capacity checks inside atomic store mutation.

Disposition:

- reuse transactional ideas;
- simplify monetary semantics to Billing Account + Authorization Hold + Journal;
- do not preserve generalized monetary settlement through raw facts/exposure/lifecycle descriptors solely for compatibility.

### Bun database abstraction — Preserve

`internal/infra/db.NewBunDB` already supports SQLite and PostgreSQL dialects. Bun-backed stores/migrations and PostgreSQL harnesses already exist elsewhere.

Disposition:

- use existing infrastructure directly;
- no new ORM/DB abstraction.

### Terminal/terminal-work ownership — Preserve

Runtime already has a terminal boundary and durable terminal-work concepts.

Disposition:

- seal/persist Turn Usage Record there;
- do not create a second terminal authority.

## Gap Register

| ID | Severity | Class | Effort | Finding | Required disposition |
|---|---:|---|---:|---|---|
| G-01 | P0 | Missing | L | No durable double-entry financial journal owns customer monetary truth. | Add journal transactions/entries with debit=credit invariant. |
| G-02 | P0 | Missing | M | No explicit prepaid vs postpaid billing-account model with individual credit limits. | Add account mode, signed balance, credit floor, and spendable formula. |
| G-03 | P0 | Missing | M | Materialized balance cannot be rebuilt from a classical immutable journal. | Make journal replay reconstruct balance/reserved/available. |
| G-04 | P0 | Missing | M | Authorization holds are operational state but not represented as balanced accounting evidence. | Add separate balanced authorization journal book. |
| G-05 | P0 | Missing | M | Customer-affecting operations lack durable before/after balance/reserved/available snapshots. | Persist point-in-time snapshots inside the same DB transaction. |
| G-06 | P0 | Partial | M | Existing B2BUA lineage exists, but financial models do not make every billable B-leg first-class. | Add Turn Usage Record with per-B-leg Leg Usage Records and operator cost. |
| G-07 | P0 | Partial | M | Different B-leg models/rates can exist but current runtime accounting tends to merge usage. | Bind provider/model/rate evidence per B-leg and rate separately post-turn. |
| G-08 | P0 | Duplicate | L | Financial interpretation is split among runtime, tokenaccounting, metering, usageauthority, and controlplane. | Converge financial authority on usage record -> calculator -> journal. |
| G-09 | P0 | Retire | M | `StreamUsage.Reconstruct`/raw usage merges can influence settlement. | Remove from financial settlement path. |
| G-10 | P0 | Retire | M | Runtime per-event cost enrichment mutates stream usage for economics. | Remove financial ownership from execution path. |
| G-11 | P0 | Duplicate | M | Runtime economic dedupe and final adapter evidence both attempt to prevent double counting. | Use B-leg/usage-record/journal source identity; remove runtime economic dedupe. |
| G-12 | P0 | Partial | L | Current reservations are atomic but not backed by a reconstructible double-entry journal. | Post hold/release authorization transactions atomically with materialized reserved state. |
| G-13 | P0 | Missing | M | No trial-balance invariant exists for billing data. | Add per-transaction and range debit=credit validation. |
| G-14 | P0 | Missing | M | Posted monetary corrections are not specified as reversal/replacement. | Make journal entries immutable and corrections additive. |
| G-15 | P0 | Missing | L | No admin operation rebuilds account state from durable financial history. | Add reconciliation/rebuild service and store contract tests. |
| G-16 | P0 | Missing | M | No authoritative provider-cost journal exists per B-leg. | Post inference COGS/provider payable entries per billable B-leg. |
| G-17 | P0 | Missing | M | Customer revenue cannot be naturally itemized across multiple chargeable B-legs/models. | Allow multi-entry balanced customer charge transaction. |
| G-18 | P0 | Partial | M | Existing money authority mixes financial and non-financial quota concepts. | Move prepaid/postpaid financial authority into billing bounded context; retain unrelated quota rules separately. |
| G-19 | P0 | Constraint | L | Strict concurrency must work across processes and both durable DB dialects. | Store-level lock/CAS and SQLite/PostgreSQL contract tests. |
| G-20 | P0 | Missing | M | Credit-limit changes have no target audit model. | Add append-only account-policy events; do not fake financial entries. |
| G-21 | P1 | Partial | S/M | Existing control-plane reporting separates perspectives but may derive from raw metering/usage. | Move authoritative financial reports to journal + processed usage records. |
| G-22 | P1 | Retire | M | Legacy token ledger may still receive direct runtime writes. | Inventory consumers; project one-way or delete. |
| G-23 | P1 | Constraint | M | Client-visible usage must remain compatible during billing rewrite. | Keep wire projection independent of financial authority. |
| G-24 | P1 | Constraint | S | Provider SDK types must stay outside core billing. | Normalize final evidence at adapter boundary. |
| G-25 | P1 | Missing | M | First draft uses telecom term CDR. | Rename target model to Turn/Leg Usage Record. |
| G-26 | P1 | Missing | M | Funding/payment accounting is needed to create/reduce customer balances but payment gateway is out of scope. | Add narrow trusted posting commands only. |
| G-27 | P1 | Missing | M | Reconciliation needs first-failure diagnostics. | Validate stored pre/post snapshots during replay. |
| G-28 | P1 | Constraint | M | Materialized account state is required for low-latency authorization. | Keep it transactionally updated but explicitly rebuildable. |
| G-29 | P1 | Constraint | S | Credit limit is not a financial transaction. | Audit policy changes separately; no fake debit/credit. |
| G-30 | P1 | Missing | M | Initial draft lacks an explicit safe state after invariant/reconciliation failure. | Add blocked/reconcile-required state and fail closed. |

## Requirements Review Round 1 — Initial CDR Draft

The initial draft correctly separated execution from post-turn billing and selected atomic pessimistic holds for concurrency.

**Decision: GO for execution simplification, NO-GO for financial-system completeness.**

Missing:

- double-entry journal;
- rebuildable account state;
- prepaid/postpaid exact semantics;
- before/after balance snapshots;
- first-class B2BUA per-leg accounting.

## Requirements Review Round 2 — Double-Entry Additions

The first revised requirements added a classical financial ledger but initially treated authorization holds ambiguously.

**Decision: NO-GO.**

### R2-A: A hold cannot be posted as revenue

A pessimistic authorization is contingent exposure, not an earned charge.

Remediation:

- add a separate `authorization` journal book using the same transaction/entry schema;
- financial book remains posted revenue/cost/funding;
- both books independently balance;
- materialized `Reserved` is reconstructible from authorization exposure entries.

### R2-B: "Two ledgers" was too literal

Classical double-entry is better represented as one journal transaction with two or more account entries rather than two independent databases/ledgers that must stay synchronized.

Remediation:

- one durable journal schema;
- each transaction has >=1 debit and >=1 credit;
- debits == credits;
- multi-entry transaction allowed.

### R2-C: Postpaid sign convention was unclear

Remediation:

```text
Balance = customer credits - customer debits
prepaid floor = 0
postpaid floor = -CreditLimit
Spendable = Balance - floor - Reserved
```

This exactly produces the requested examples.

## Requirements Review Round 3 — B2BUA and Recovery

The revised requirements were checked against current lineage and DB architecture.

**Decision: NO-GO before two clarifications.**

### R3-A: Customer and operator posting scopes differ

One A-leg can have multiple B-legs using different models/rates.

Remediation:

- Turn Usage Record per A-leg;
- Leg Usage Record per B-leg;
- customer charge normally applied once per A-leg according to charging policy;
- operator cost posted per provider-billable B-leg;
- customer revenue entries can retain B-leg attribution for multi-leg policy.

### R3-B: Rebuild must not depend on snapshots

Point-in-time snapshots improve debugging but are redundant.

Remediation:

- journal entries + durable account master/policy reconstruct monetary state;
- snapshots are validation/debug evidence;
- reconciliation compares snapshots and materialized row to journal replay;
- rebuild never edits journal.

## Requirements Quality Gate

**Decision: PASS**

The final requirements now define:

- simple live path;
- domain-neutral usage-record terminology;
- prepaid/postpaid semantics;
- pessimistic concurrent holds;
- double-entry financial and authorization books;
- Bun durability;
- B2BUA per-leg attribution;
- atomic settlement;
- replay/rebuild;
- transparency snapshots;
- deletion of stream-time financial authority.

No implementation is authorized until normal Kiro approvals are set.
