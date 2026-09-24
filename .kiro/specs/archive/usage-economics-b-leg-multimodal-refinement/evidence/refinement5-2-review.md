# Refinement 5.2 review

Verdict: `PASS`

Independent Luna/Max `kiro-review` approved Task 5.2 against requirements 3.4
and 4.5 after atomic-outbox, trusted-authority, durable-processing, and
same-revision convergence remediation.

The review verified that late provider finalizer, B-leg-correlated statement,
and correction evidence attaches to the original closed B-leg through the
atomic observation/outbox seam. Plain observation sinks fail closed. Trusted
provider identity and lineage come from sealed durable evidence rather than
caller-supplied values. No path can reopen execution, allocate a replacement
B-leg, mutate terminal outcomes, or re-terminalize the call.

Stock composition tests exercise the durable relay and valuation workers,
provider COGS revisions and exact journal deltas, customer non-rebill,
restart/replay, expired-lease recovery, duplicate and changed-payload handling,
and concurrent distinct late evidence. Equal-revision heads compare canonical
persisted evidence sets: strict supersets advance, subsets remain stale, exact
sets replay idempotently, and incomparable sets fail closed transactionally.
The forced lower-hash superset ordering converges provider COGS from 125 to 132
exactly once while customer and lifecycle state remain unchanged.

Repeated focused and full affected-package tests, SQLite parity, vet,
formatting, whitespace/BOM, diff, and targeted architecture checks passed.
Direct PostgreSQL parity remains blocked by the pre-existing
`metering_components.value_present` int4/boolean mismatch; Task 5.2 changed no
schema or migration. Broad architecture ratchets and Windows race execution
also retain their documented repository/toolchain limitations and are not
claimed as passing.
