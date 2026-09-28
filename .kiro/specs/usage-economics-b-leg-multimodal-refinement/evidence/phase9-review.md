# Phase 9 review

- Verdict: APPROVED after focused blocker decomposition and bounded delegated re-review.
- Scope: parent tasks 9.1–9.5 and refinement task 2.3.
- Base: `4a98f977` (accepted Phase 8 checkpoint).

## Accepted behavior

- Existing tariff sources resolve to immutable, replayable rule snapshots with authoritative rule kinds, exact arithmetic and deterministic qualifier/tier selection.
- Linear, fixed, block, minimum, all-units, graduated and explicit conversion behavior validates contradictory material and applies line/call/period rounding at the declared boundary.
- Expected, provider-quantity and provider-reported valuations remain independent, preserve canonical input and snapshot identity, and survive durable replay without cross-basis fallback.
- Multimodal input/output components and native units rate independently, including asymmetric direction-specific prices and unsupported-rate behavior.
- Replay, supersession and correction reduction are deterministic, source- and component-local, fail closed on unusable predecessors, and preserve legacy empty-hash rows.
- Operator COGS consumes the effective charge graph: superseded coverage is audit-only, correction taint remains local, aggregate/component overlap requires explicit surcharge semantics, supersession is charge-cardinality independent, and Subject/Correlation lineage is normalized.
- Retained charge items retain their own pending coverage diagnostics through partial replacement, producing a known subtotal that remains incomplete and non-payable.

## Delegated review evidence

- The final five focused blocker families passed repeated shuffled tests together.
- The retained-charge partial-replacement aggregate and COGS regressions passed deterministically.
- Affected billing, aggregate, replay, metering, economics, journalstore and billingstore package tests passed.
- `go vet`, SQLite database parity, targeted architecture/QA checks, `gofmt -l`, `git diff --check`, placeholder scan and precise secret scan passed.
- Phase 9 RED/GREEN commands and results are recorded in `phase9-execution.md`.

## Non-blocking limitations

- Windows race builds remain unavailable because `cgo.exe` exits with status 2.
- Direct PostgreSQL validation requires `LIP_TEST_POSTGRES_DSN`; SQLite parity passed and PostgreSQL-specific migration coverage remains environment-gated.
- The known unrelated billing composition failover assertion remains `1003` versus `1000`.
- Existing broad architecture/import ratchets remain outside the Phase 9 delta.

No Phase 9 requirement-blocking finding remains.
