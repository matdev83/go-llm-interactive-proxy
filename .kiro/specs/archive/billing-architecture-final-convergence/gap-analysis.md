# Brownfield Requirements Gap Analysis

## Dependency Gate

This analysis is against the architecture after PR #340. The predecessor Kiro spec is `billing-post-usage-correctness-hardening`, published as PR #346 and merged as spec-only commit `9bf9c66a09de50ab3dcad18f0a8a84c2c2d49ed9`. That merge is **not** evidence that the predecessor implementation exists.

`spec.json` therefore carries two distinct dependency facts: the known predecessor spec PR/merge SHA, and an intentionally empty `implementation_main_sha`. Before implementation of this convergence spec begins, Phase 0 MUST locate the commit on `main` that actually implements the predecessor, record that SHA in `spec.json` and the canonical baseline artifact, rerun the predecessor certification suite, and change dependency verification from `pending_phase_0` only after those checks pass. Until then `ready_for_implementation` remains false.

Any predecessor implementation changes to names/paths should update this gap file without weakening the requirements. Semantic drift triggers requirements/design revalidation.

## Gap Inventory

| Gap | Current state | Requirement impact | Target |
|---|---|---|---|
| G-01 | `RateCall` rebuilds legacy `TurnUsageRecord`/`LegUsageRecord` | 2.1-2.7 | Native current-record customer rating |
| G-02 | Legacy rating helpers still own customer leg selection | 2.2-2.6 | Move needed logic to native call rating, delete bridge |
| G-03 | `turn_usage_records` remains required schema | 3.1-3.8 | Safe forward retirement |
| G-04 | `leg_usage_records` remains required schema | 3.1-3.8 | Safe forward retirement |
| G-05 | `usage_record_processing` remains required schema | 3.2-3.8 | Delete retired processing path |
| G-06 | Legacy TUR append/processing/shadow files remain production code | 3.5, 10.5, 12.3 | Delete after consumer inventory |
| G-07 | `Account.ReservedNano` remains current domain | 4.1-4.8 | Remove from current types |
| G-08 | `AccountSnapshot.ReservedNano` and journal snapshot fields remain | 4.2-4.4 | Current snapshots no longer expose reserved state |
| G-09 | `JournalBookLegacyAuthorization` remains normal journal enum | 4.5-4.8 | Isolate historical decode; remove from current writers |
| G-10 | UsageAuthority supports `AmountUnitMoneyNano` | 5.1-5.8 | Quantity/request-only authority |
| G-11 | UsageAuthority reserve/settle/release contracts carry monetary fields | 5.2-5.8 | Delete money-specific fields/paths |
| G-12 | Money budget/spend-cap rules remain configuration/domain surface | 5.4-5.5 | Explicit migration error then remove |
| G-13 | Provider COGS locks customer account row | 6.1-6.7 | Independent journal ordering/idempotency |
| G-14 | Provider COGS shares customer account sequence | 6.4-6.6 | Database-recorded provider ordering independent of customer sequence |
| G-15 | Runtime attempts central usage append synchronously | 7.1-7.10 | One local durable spool append |
| G-16 | Retrying append wrappers add fallback outbox after direct failure | 7.3-7.10 | Delete layering after spool cutover |
| G-17 | Central `usage_append_outbox` is transport retry state | 7.10, 8.1-8.5 | Drain/reconcile then replace with local spool work state |
| G-18 | Runtimebundle creates multiple billing workers/wrappers through conditional branches | 8.1-8.8 | One clear composition path |
| G-19 | Previous LOC ratchet allows tiny reductions | 10.1-10.8 | Canonical reproducible baseline artifact + 10% + structural deletion |
| G-20 | Current architecture docs retain migration terminology | 10.8, 12.9 | One authoritative flow |
| G-21 | Generic migration wording permits proof-before-DROP races | 3.6, 11.1-11.6 | One dialect-locked migration critical section for proof/conversion/drop |
| G-22 | Local spool draft does not define crash-ack durability, bounded growth, or stale claim recovery | 7.1-7.9 | Explicit SQLite durability/capacity/claim contract |
| G-23 | Independent call/leg spool delivery could expose closure before legs | 7.4, 8.2 | Central complete-call claim remains gated on every frozen expected B-leg |
| G-24 | Current provider ordering design had multiple alternatives | 6.4-6.6 | One chosen `(recorded_at, transaction_id)` provider-order contract |
| G-25 | `BillingAuthoritative` boolean can preserve mode branching in final runtime surface | 8.6-8.8, 12.5 | All-or-none constructor invariant; no runtime billing mode boolean |

## Requirements Changes After Gap Analysis

The requirements were strengthened in these areas:

### Safe destructive migration

A first draft simply required dropping old tables. Brownfield analysis showed that unresolved legacy processing rows could represent monetary work. Requirement 11 requires explicit pre-drop checks and blocks destructive migration on unresolved state. The design further makes the proof, conversion/recheck and destructive DDL one migration-critical section: application writers are quiesced first; PostgreSQL uses transaction-scoped migration serialization plus `ACCESS EXCLUSIVE` locks on retiring tables; SQLite uses one connection with `BEGIN IMMEDIATE` (or stronger supported writer exclusion). Concurrency tests must prove a legacy writer cannot commit new work between proof and `DROP`.

The same preserve-or-block rule applies to `usage_append_outbox`: pending/deferred rows must be idempotently delivered into current usage storage before removal; conflict/malformed/unprovable rows block retirement and require reconciliation.

### Fresh schema convergence

Keeping every historical table in fresh installs merely to drop it in a later migration perpetuates operational clutter. Requirement 3.8 permits safe migration consolidation where the repository's migration policy allows it, while preserving historical migration files for upgrades.

### UsageAuthority migration posture

Deleting money units without handling existing configuration would silently disable policy. Requirement 5.5 requires an explicit startup migration error for retired money rules.

### Provider COGS sequencing

Simply removing the customer lock is insufficient if journal uniqueness still relies on per-account sequence. Requirement 6 explicitly separates provider ordering from customer credit serialization. Design validation now chooses one contract rather than leaving alternatives: customer-affecting transactions keep non-null `account_sequence`; provider COGS uses `account_sequence=NULL` and deterministic `(recorded_at, transaction_id)` ordering, with database-assigned immutable `recorded_at`, unique `transaction_id` tie-break, explicit indexes, validators and cross-dialect migration rules.

### Durable spool ownership and production contract

An in-memory queue would reduce latency but weaken crash durability. Requirement 7 therefore uses one process-local durable spool and restart replay. For this spec, the authoritative production implementation is deliberately **not pluggable**: Bun/`internal/infra/db` plus a stable process-local SQLite/WAL file is the supported production contract. Injected alternatives remain test seams unless a future spec defines an explicit conformance contract.

The detailed design now makes Requirement 7 enforceable: SQLite uses WAL + `synchronous=FULL`; append success occurs only after the local transaction commits; the file lives in durable process state with restrictive permissions; record identity/fingerprint comes from the sealed current billing records; the spool has explicit record/byte/disk bounds, processed retention and health metrics; and rows left `delivering` by a crash are reclaimed on restart. The central complete-call claim, not spool delivery order, remains the correctness barrier and must require every frozen expected B-leg before customer rating.

### Runtime composition mode removal

The final runtime no longer carries a `BillingAuthoritative` feature/mode boolean. Production construction is all-or-none: either billing ports are all absent (stock non-billing host) or the cheap gate, exposure admission, terminal sink and identity bundle are all present and validated by construction/NewExecutor. Partial billing wiring fails construction. This keeps “billing enabled” from becoming a second runtime mode selector or legacy/current switch.

### Non-gameable simplification

A percentage alone can be gamed by moving code. Requirement 10 combines a wider measured surface, symbol-following, explicit deletion targets and a 10% threshold. Phase 0 must materialize the canonical artifact at `internal/archtest/testdata/architecture/billing_final_convergence_baseline.json` after the predecessor implementation lands. That artifact pins the exact implementation main SHA, physical-LOC counting method, denominator, included roots/files/declarations, exclusions, seed symbols and versioned deterministic AST fixed-point following rules. The final ratchet must reproduce the denominator from the pinned SHA before comparing the final tree.

## Accepted Brownfield Retention

The following may remain after completion:

- immutable historical migration source files;
- narrowly scoped legacy decode structs inside migration/recovery code where required to read old rows;
- posted financial journal rows from historical books, if retained for audit;
- metering money observations as telemetry only;
- non-money UsageAuthority request/token/concurrency functionality.

They must not remain normal production authority paths.

## Quality Gate

**GO FOR DESIGN READINESS, CONDITIONAL ON PREDECESSOR IMPLEMENTATION.**

The predecessor spec PR is already pinned, but the implementation SHA is intentionally unresolved. The design must be revalidated against that final implementation SHA before work starts. The Phase-0 LOC artifact is intentionally created only after that SHA exists; inventing a future baseline in this spec PR would violate the chronological dependency.
