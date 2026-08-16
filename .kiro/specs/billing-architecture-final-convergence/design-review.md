# Brownfield Design Validation

## Result

**GO FOR DESIGN READINESS — IMPLEMENTATION BLOCKED UNTIL PREDECESSOR IMPLEMENTATION COMPLETES**

The proposed end-state is materially simpler and resolves the residual architecture identified in the post-merge review. The design is intentionally more deletion-oriented than the predecessor refactor.

The predecessor relationship is now machine-readable rather than prose-only: `spec.json` records `billing-post-usage-correctness-hardening`, spec PR #346, its spec-only merge SHA `9bf9c66a09de50ab3dcad18f0a8a84c2c2d49ed9`, required state `implemented_on_main`, an empty future `implementation_main_sha`, and `verification_status=pending_phase_0`. That empty implementation SHA is deliberate: PR #346 merged the **spec**, not the implementation. Phase 0 must fill and verify the implementation SHA before this spec becomes implementation-ready.

## Validation Checks

### Core vs edge ownership — PASS

Native billing policy remains in `internal/core/billing`. The local spool is a driven persistence adapter. Runtime retains only admission and terminal handoff.

### Canonical model neutrality — PASS

No provider protocol or `pkg/lipapi` contract change is required.

### Streaming-first/no retry after output — PASS

The local durable spool strengthens separation: terminal path performs local durability only; central post-usage work cannot alter stream retry semantics.

### No provider SDK leak — PASS

Provider `FinalizeBilling` remains at the backend edge. The spool transports already-normalized billing records.

### Single monetary authority — PASS after required UsageAuthority cut

The design explicitly removes money units/reservations from UsageAuthority rather than relying on runtime callers to leave `Spend` empty.

### Customer leg-selection semantics — PASS, NOW EXPLICIT

The predecessor's evidence filter is authoritative: “all potential” means all **provider-accepted billable B-legs with customer evidence**, not every planned/never-started/no-evidence candidate. Evidence acceptance is applied before charge-scope selection. This preserves predecessor behavior and avoids accidentally turning missing evidence into a charge. Differential tests must cover all-potential, surfaced, interrupted and sequence-unknown cases.

### Provider COGS ordering — PASS, ONE CONTRACT SELECTED

The earlier design listed multiple possible provider-order mechanisms. That ambiguity is removed. Customer-affecting journal rows retain non-null per-account `account_sequence`. Provider COGS uses `account_sequence=NULL` and deterministic database-recorded `(recorded_at, transaction_id)` order. `recorded_at` is immutable and assigned at insert by the database; globally unique `transaction_id` is the tie-break. Required indexes, validators/report ordering and SQLite/PostgreSQL migration behavior are part of the design. No new global sequence table/counter is introduced.

### Final runtime composition — PASS, MODE BOOLEAN REMOVED

`BillingAuthoritative` does not belong in the converged runtime contract. The final production invariant is all-or-none construction: either all billing ports are absent (stock non-billing host), or cheap credit gate + exposure admission + terminal usage sink + identity bundle are all supplied. Partial wiring fails construction/NewExecutor validation. There is no runtime boolean capable of selecting legacy/current monetary paths.

### Persistence reuse — PASS

The local spool reuses Bun/internal DB infrastructure and SQLite/WAL. No second ORM or generic messaging framework is introduced.

### Concern: local spool adds a datastore — ACCEPTED WITH HARD BOUNDS

A local SQLite spool is additional persistence, but it replaces three live concepts: synchronous central terminal append, retrying appender wrappers, and central fallback outbox/worker. Its role is narrowly transport durability and it contains no billing policy.

External review identified that “durable local SQLite” was underspecified. The design now makes the production contract explicit: Bun/`internal/infra/db`, one stable process-local SQLite/WAL file, `synchronous=FULL`, success only after commit acknowledgement, restrictive file placement/permissions, bounded row/payload/database/disk capacity, processed-row retention, health metrics, and restart recovery of stale `delivering` rows. Alternative injected spools remain test seams in this spec. This preserves simplicity while making durability testable.

### Concern: independent call/leg spool appends — MITIGATED BY CENTRAL COMPLETENESS GATE

Atomic call+all-leg append was considered but rejected because B-legs terminalize independently and forcing a runtime aggregation transaction would recreate the billing mini-framework this spec is deleting. Instead, delivery order remains arbitrary and the current central `ClaimCompleteCall` invariant is normative: the call closure freezes exact expected B-leg IDs and customer rating cannot claim the call until every expected leg has one valid sealed row. A call delivered before its legs therefore remains pending rather than partially rated.

### Concern: 10% LOC target could drive unsafe deletion — MITIGATED AND MADE REPRODUCIBLE

The LOC gate is secondary to structural correctness/deletion requirements and is measured after the predecessor implementation. Migrations/history are excluded only when explicitly classified as historical-only, and code movement is followed semantically. Final review may be NO-GO even if 10% is met.

External review correctly identified that the prior metric description was not reproducible enough. Phase 0 now has one canonical artifact contract:

```text
internal/archtest/testdata/architecture/billing_final_convergence_baseline.json
```

The artifact is created only after the predecessor implementation's final main SHA exists. It records that SHA, `physical-go-lines-v1`, denominator, included roots/files/declarations, exclusions, seed symbols, versioned AST symbol-following rules and deletion targets. The counting algorithm and deterministic fixed-point declaration following are defined in `design.md` Section 12. Final certification must reproduce the baseline denominator from the pinned SHA before comparing final LOC. No future SHA/value is fabricated in this spec PR.

### Concern: removing money UsageAuthority could break users — MITIGATED

The change is explicit and fail-closed. Legacy money rule configuration receives a migration-required startup error. The spec does not silently remove enforcement.

### Concern: historical authorization journal rows — MITIGATED

Historical rows may remain readable through isolated persistence/decode code. Current writer types no longer accept that book.

### Concern: destructive table retirement — MITIGATED WITH CRITICAL-SECTION RULE

Forward migrations must prove no unresolved financial work remains and fail rather than guess/drop. External review correctly identified a check-then-drop race: a writer could create work after a successful proof if proof and DDL were separate.

The design now requires application writer quiescence plus a single database migration-critical section. PostgreSQL takes transaction-scoped migration serialization and `ACCESS EXCLUSIVE` table locks before proof/conversion/re-proof/drop; SQLite performs proof/conversion/re-proof/drop on one connection under `BEGIN IMMEDIATE` or stronger supported writer exclusion. Concurrency tests must prove new legacy work cannot commit between proof and destructive DDL.

The same preserve-or-block gate applies to the old central `usage_append_outbox`: retryable rows are delivered idempotently before deletion; conflicts/malformed/unprovable work block retirement and require reconciliation.

## Changes Applied After Design Validation

1. Provider COGS design selects deterministic `(recorded_at, transaction_id)` ordering instead of leaving multiple alternatives or using the customer account lock.
2. Terminal spool explicitly replaces the old direct+fallback stack and is required to remove more concepts than it adds.
3. Destructive table retirement includes unresolved-work guards, writer quiescence, dialect-specific critical-section locking and fresh/upgraded schema convergence.
4. UsageAuthority money removal includes configuration migration failure semantics.
5. LOC reduction is paired with explicit deletion targets, a canonical reproducible Phase-0 baseline artifact and deterministic anti-code-movement rules.
6. Implementation order moves native rating before legacy domain deletion and spool cutover before outbox deletion.
7. Terminal spool durability pins commit/return ordering, SQLite durability posture, stable record key/fingerprint semantics, bounded capacity/retention/health and stale-claim recovery.
8. Independent call/leg delivery is explicitly completion-gated at central `ClaimCompleteCall` rather than reintroducing runtime aggregation.
9. Destructive outbox/TUR/LUR migrations require preserve-or-resolve, unresolved-state blocking and post-migration reconciliation.
10. Final certification is required to run through one checked-in fail-fast Make target with explicit race-test platform conditional.
11. “All potential” customer charging is defined as all provider-accepted billable evidence legs; evidence filtering precedes charge scope.
12. Final `BillingRuntime` removes the `BillingAuthoritative` mode boolean and uses an all-or-none construction invariant.
13. Predecessor dependency metadata now distinguishes the merged Spec 1 SHA from the still-future implementation SHA and keeps readiness blocked until Phase-0 verification.

## Dependency Revalidation

Immediately before implementation, rerun this validation against the final `main` SHA that actually implements `billing-post-usage-correctness-hardening`.

Spec 1 identity already pinned:

```text
feature: billing-post-usage-correctness-hardening
spec PR: #346
spec merge SHA: 9bf9c66a09de50ab3dcad18f0a8a84c2c2d49ed9
implementation main SHA: <must be recorded by Phase 0>
```

Phase 0 must update `spec.json.dependencies[0].implementation_main_sha`, set dependency verification to a verified state only after predecessor certification succeeds, and record the same implementation SHA in the canonical LOC baseline artifact.

Specifically verify:

- `CallLegUsageRecord` sequence/presence naming and semantic fingerprint version;
- call-scoped runtime state final shape;
- customer snapshot resolver names;
- customer all-potential evidence semantics;
- central `ClaimCompleteCall` completeness behavior;
- migration head and actual Bun transaction semantics;
- current journal constraints before making `account_sequence` nullable for provider COGS;
- process state-directory conventions for the local spool;
- production baseline paths and canonical baseline-artifact scanner inventory.

Only names/baselines may be adjusted without reopening requirements. Any semantic difference requires requirements/design review.

## Final Quality Gate

The design meets the requested simplification objective on paper:

```text
one current usage model
one monetary authority
one terminal durability mechanism
one customer worker
one provider worker
```

It is suitable for implementation after approval and **verified predecessor implementation completion**, subject to the hardened durability/migration/baseline contracts above.
