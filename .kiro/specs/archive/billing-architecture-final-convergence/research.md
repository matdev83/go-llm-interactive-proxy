# Research and Architecture Convergence Rationale

## Starting Point

This spec is based on the post-merge review of main `811c6aa1e442ba15206ebcd9506d945c70911d7b` and is chronologically dependent on `billing-post-usage-correctness-hardening`.

PR #340 made the most important conceptual correction: operational exposure replaced monetary authorization holds. However the reported production billing surface moved only from 10,786 to 10,583 lines, approximately a 1.9% reduction. The review found that the implementation is better partitioned but still contains multiple historical representations and authority concepts.

This second spec exists specifically because combining the correctness fixes and deletion work into one implementation would make review harder and risk hiding wrong-charge regressions.

## R1 — New Records Still Adapt Back to Legacy Records

`CallUsageRecord` and `CallLegUsageRecord` are the new durable model, but `RateCall` rebuilds `TurnUsageRecord`/`LegUsageRecord` and calls the legacy rating algorithm.

This bridge already caused two defects found in the predecessor review:

- real B-leg sequence was lost and reconstructed positionally;
- model-specific customer pricing was lost while adapting.

Once predecessor correctness is implemented, retaining the bridge has negative value.

## R2 — Legacy Tables Remain in the Current Schema Contract

`billingstore.VerifySchema` still requires legacy `turn_usage_records`, `leg_usage_records`, and `usage_record_processing` tables, indexes and triggers even though the current call/leg path uses `usage_call_records`/`usage_leg_records`.

That makes migration history part of the live operational model instead of history.

## R3 — Reserved Balance Remains in Current Types

`billing.Account` and `AccountSnapshot` still expose `ReservedNano` even though READY accounts require it to be zero and current spendable math no longer subtracts it.

This is dead conceptual weight and encourages future code to reuse the wrong abstraction.

## R4 — Legacy Authorization Book Is Still a Current Journal Enum

The journal type still accepts the historical authorization book for decode/report compatibility. Current call-path writers no longer use it, but exposing it through normal types makes “what books are live?” less obvious.

Historical read compatibility should be isolated from current writer contracts.

## R5 — UsageAuthority Remains Money-Capable

The generic UsageAuthority domain still defines `AmountUnitMoneyNano` and reserve/settle/release structures with Spend, FinalCost and EstimatedCost.

Runtime stock billing currently sends empty monetary spend, so it is not a second customer-balance debit path today. Nevertheless it remains a complete second money-reservation architecture that maintainers must reason about and that could be reactivated accidentally.

The simplest convergence is to keep UsageAuthority for request/token quota and concurrency policy only.

## R6 — Provider COGS Shares Customer Account Serialization

`ApplyProviderCost` currently locks the customer billing account even though provider COGS does not mutate customer credit. This is inherited from journal account-sequence allocation.

Operator-cost backlog can therefore contend with call admission for no financial reason.

## R7 — Terminal Handoff Uses Direct Central Append Plus Fallback Outbox

Runtime terminal paths call central billing appenders synchronously with a large timeout. Wrappers then enqueue a central durable outbox only after direct append failure.

This is two transport modes in one hot path:

```text
try central current table
 -> on failure write central retry table
```

A simpler telecom-style ownership boundary is:

```text
append once to process-local durable spool
 -> terminal path ends
 -> process worker delivers to central current table at least once
```

This also eliminates the executor's dependency on central billing database latency during terminalization.

## R8 — Simplification Metric Was Too Weak

The previous architecture ratchet required only a net reduction from 10,786 lines. The final implementation passed with 10,583.

For an architecture whose purpose was significant simplification, `< baseline` is too weak. The successor needs:

- a wider counted surface;
- explicit deletion targets;
- a material percentage reduction;
- anti-gaming rules for code movement.

## Why 10% Is Chosen

The 10% threshold applies to the fresh post-predecessor production baseline, not the historical 10,786 number. It is deliberately paired with structural deletion gates.

Ten percent is large enough to reject another “rename/add adapters” result, yet small enough not to force unsafe deletion solely to hit a metric. Structural deletions remain the primary invariant.

## Target End-State

Runtime:

```text
cheap credit gate
 -> exposure admission
 -> execute
 -> call-scoped usage bookkeeping
 -> local durable terminal spool
```

Post-usage:

```text
spool flusher -> central call/leg usage
                  |-> customer worker -> native rating -> customer journal + exposure close
                  `-> provider worker -> provider COGS
```

Policy:

```text
UsageAuthority -> requests/tokens/concurrency only
Billing        -> all financial money authority
```

Persistence:

```text
current account/exposure/journal/operation/call-leg usage tables
historical migrations retained as history
legacy operational tables dropped after safety checks
```

## Non-Goals

This is not a new financial platform. No generic event sourcing, ledger DSL, workflow engine, Kafka, distributed saga framework, or provider-specific accounting code is justified.
