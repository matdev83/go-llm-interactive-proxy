# Refinement 4 observation-to-economic-work bridge evidence

## Scope and architecture

Refinement 4.1 observations now have a production-owned trigger seam into the
existing refinement 4.2 `EconomicRevisionWork` queues. The metering journal
owns `metering_observation_economic_outbox`; the observation row and its
canonical outbox payload are inserted in one local transaction. The
runtimebundle process owner starts one bounded relay, leases pending rows, and
releases claims on orderly shutdown. A crash leaves a pending or expired
processing row for the next relay owner.

The billing adapter remains the existing `AppendEconomicRevisionWork` port. It
normalizes and validates the work, including the 4.2 queue/head/revision/input
hash identity fence; exact replays are no-ops and changed immutable payloads
remain conflicts. A successful billing append followed by a relay crash is
therefore safe to replay. The relay only loads durable B-leg observations,
builds provider/customer work, appends queue markers, and acknowledges the
outbox. It does not rate, reconcile, lock balances, settle, post money, or
call provider code.

The stock builder creates independent provider-reported P and customer-policy
R work. It canonicalizes and replay-deduplicates observations before hashing,
retains provider-account/customer scope in the head identity, uses the latest
evidence revision, and includes both terminal and late correction evidence in
the durable set. The customer factory captures cloned catalog tariff/policy
snapshots at composition time. Runtime composition only replaces the durable
sink with the outbox-backed sink when a durable journal and billing work
appender are both present; disabled, memory, and unrelated injected recorders
remain no-op/non-economic paths.

## TDD evidence

The RED cases covered the missing production seam: builder output was absent,
the durable outbox relation was absent, and stock composition required manual
queue seeding. GREEN implementation added the smallest domain builder,
journal outbox, leased relay, and composition wiring.

Focused GREEN coverage includes:

- provider/customer queue separation and provider-account scope isolation;
- deterministic identity under reordered input, exact replay, and a new
  correction revision/input hash;
- one atomic observation plus outbox append and rollback after an injected
  post-outbox fault;
- bounded outbox backpressure that rolls the observation back with its trigger;
- relay append failure, lease expiry, restart/reclaim, and idempotent billing
  retry;
- real stock `BuildHost` + `ComposeBilling` composition, with preterminal
  observation automatically producing both queues without manual seeding;
- disabled metering behavior and no receive-path rating or monetary call;
- SQLite logical schema registration for the outbox table, unique identity
  fence, and pending index.

## Verification

Passed:

```text
go test ./internal/core/billing ./internal/infra/metering/journalstore ./internal/infra/runtimebundle ./internal/infra/billingstore -run 'TestObservationEconomicWorkBuilder|TestObservationOutbox|TestObservationEconomicRelay|TestStockCompositionObservationEconomicBridgeQueuesWithoutManualSeeding|TestRefinement42' -count=1
go vet ./internal/core/billing ./internal/infra/metering/journalstore ./internal/infra/runtimebundle ./internal/infra/billingstore
make test-db-parity-sqlite
git diff --check
```

The focused bridge tests and vet passed. After the phase-level remediation, the
full stock bridge and billing host-loop tests also pass. The earlier malformed
import diagnostic came from an ignored top-level ` internal` duplicate fixture;
the duplicate file was removed after byte-for-byte verification.

The focused race attempt was also made, but the Windows Go 1.26.6 toolchain's
`cgo.exe` exited with status 2 before package tests built; no race result is
claimed.

The repository PostgreSQL direct parity gate was attempted with
`make test-db-parity-postgres-direct`. Billingstore, concurrency, continuity,
conversation, and control-plane components reached the configured database;
the gate stopped at the existing metering-journal schema mismatch:
`metering_components.value_present` is remote PostgreSQL `int4` while the
logical schema requires boolean. No broad PostgreSQL pass is claimed for this
bridge until that shared schema is repaired or an isolated upgradeable schema
is supplied.

## Residual risks

- Existing observations written through the historical non-outbox sink are not
  proactively backfilled; a subsequent exact append repairs its missing trigger
  row. A dedicated historical backfill can be added if deployments require
  processing already-retained rows without replaying them.
- Relay delivery is intentionally at-least-once across separate metering and
  billing databases. The 4.2 billing identity fence is the duplicate barrier;
  operators should monitor pending/processing rows and retry errors.
- The bounded evidence envelope fails closed above
  `economics.MaxRatingObservations` rather than silently truncating economic
  inputs.

## Full stock-composition proof

The review follow-up is covered by
`internal/infra/runtimebundle/refinement4_stock_economic_integration_test.go::TestRefinement4StockObservationToEconomicSettlement`.
It builds a real host from `BuildHost` and `ComposeBilling`, appends only a
preterminal V2 observation through the production atomic observation sink, and
waits with bounded diagnostic polls for the process-owned relay and both
economic workers. The test observes provider P valuation and the authoritative
provider COGS journal while the BillingCallID exposure is still open, then
appends the durable leg/call terminal records and observes one selected-leg
customer settlement. It also proves exact terminal replay is idempotent and a
late provider charge correction creates a new provider identity, a `-3` delta,
and auditable reversal/correction links without advancing the frozen customer
R head or reopening the call.

The test never appends an economic queue marker directly. Read-only queue
counts prove provider/customer isolation (two provider revisions, one
quantity-backed customer revision), and the persisted provider head retains
the B-leg/A-leg lineage without an A-leg retirement callback.

Fresh verification:

```text
go test ./internal/infra/runtimebundle -run 'TestRefinement4StockObservationToEconomicSettlement|TestStockCompositionObservationEconomicBridgeQueuesWithoutManualSeeding|TestObservationEconomicRelay' -count=5 -v
go test ./internal/infra/runtimebundle -run '^TestBillingHostLoop' -count=1 -v
go test ./internal/core/billing ./internal/infra/metering/journalstore ./internal/infra/billingstore ./internal/infra/runtimebundle -run 'TestObservationEconomicWorkBuilder|TestObservationOutbox|TestObservationEconomicRelay|TestStockCompositionObservationEconomicBridgeQueuesWithoutManualSeeding|TestRefinement4StockObservationToEconomicSettlement|TestRefinement42|TestRefinement43' -count=1
make test-db-parity-sqlite
go vet ./internal/core/billing ./internal/infra/metering/journalstore ./internal/infra/runtimebundle ./internal/infra/billingstore
git diff --check
```

All listed commands passed. The focused stock test passed three initial
consecutive runs and then five repeated bridge runs. PostgreSQL shared-schema
parity remains uncertified because the configured remote still exposes the
pre-existing `metering_components.value_present` `int4`/logical-boolean
mismatch; the existing isolated billing migration probe is the available
PostgreSQL evidence. Windows race builds remain unavailable because Go
1.26.6's `cgo.exe` exits before test execution.
